// Package processing executes validated local contributions under engine ownership.
package processing

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

var ErrUnsupported = errors.New("segmentation_limit")

type Input struct {
	Organization string
	Version      content.Version
}

// Processor is the shared contribution interface. Local execution needs no network.
type Processor interface {
	Process(context.Context, Input) (content.Segmentation, error)
}

type Indexer interface {
	Index(context.Context, string, content.Version, content.Segmentation) error
}
type Service struct {
	Content    content.Service
	Processor  Processor
	Retrieval  Indexer
	Embedder   Embedder
	Enrichment EnrichmentIndexer
	// Observer receives processing outcomes for metrics; nil disables them.
	Observer Observer
	// Normalizer invokes the pinned external normalizer for routed Blobs
	// before publication; nil normalizes nothing.
	Normalizer Normalizer
	// Plugin segments and embeds the Versions of Corpora whose routed
	// generation a pinned ingestion plugin serves; the others keep the
	// built-in Processor and Embedder. Nil when no ingestion plugin is pinned.
	Plugin *PluginDeriver
	// Routing resolves a Corpus's generation; needed with Plugin.
	Routing GenerationRouter
}

// GenerationRouter resolves the generation PostgreSQL routes a Corpus to.
type GenerationRouter interface {
	Generation(ctx context.Context, org, corpusID string) (content.Generation, error)
}

// ErrSpaceUnowned reports a Corpus whose routed generation is served by a
// space neither the built-in embedder nor the pinned ingestion plugin owns,
// such as the space of a plugin no longer pinned. Its Versions wait, rather
// than being segmented another way, until the owner is pinned again or the
// Corpus is rebuilt.
var ErrSpaceUnowned = errors.New("served space has no pinned owner")

// pluginRoute reports whether a Version goes through the pinned ingestion
// plugin: its Corpus's routed generation is served by one of the plugin's
// spaces. It is ErrSpaceUnowned when no pinned owner serves that space.
func (s Service) pluginRoute(ctx context.Context, org string, v content.Version) (bool, string, content.Generation, error) {
	if s.Routing == nil {
		return false, "", content.Generation{}, nil
	}
	r, err := s.Content.Record(ctx, corpus.Scope{Organization: org, Actions: []string{"content:read"}, Corpora: []string{"*"}}, v.RecordID)
	if err != nil {
		return false, "", content.Generation{}, err
	}
	g, err := s.Routing.Generation(ctx, org, r.Source.CorpusID)
	if err != nil {
		return false, "", g, err
	}
	if s.Plugin != nil && s.Plugin.Owns(g.SpaceID) {
		return true, r.Source.CorpusID, g, nil
	}
	if s.Embedder != nil && g.SpaceID != s.Embedder.Space().ID {
		return false, r.Source.CorpusID, g, ErrSpaceUnowned
	}
	return false, r.Source.CorpusID, g, nil
}

// Observer is told each processing outcome (bounded stage and outcome names)
// and when a Receipt's Version became searchable.
type Observer interface {
	Outcome(stage, outcome string)
	Searchable(ctx context.Context, org, receiptID string)
}

// outcome reports one stage outcome to the Observer and a correlated log line.
func (s Service) outcome(stage, outcome, receiptID string, v content.Version, started time.Time, code string) {
	if s.Observer != nil {
		s.Observer.Outcome(stage, outcome)
	}
	level := slog.LevelInfo
	if outcome != "succeeded" {
		level = slog.LevelWarn
	}
	slog.Log(context.Background(), level, "processing outcome", "component", "worker", "stage", stage, "outcome", outcome, "code", code,
		"receipt_id", receiptID, "record_id", v.RecordID, "version_id", v.ID, "duration_ms", time.Since(started).Milliseconds())
}

// Normalizer runs external normalization for one accepted receipt.
type Normalizer interface {
	Normalize(ctx context.Context, org, receiptID string) error
}

// Normalize runs external normalization before publication. Content that is
// not a routed Blob needs none.
func (s Service) Normalize(ctx context.Context, org, receiptID string) error {
	if s.Normalizer == nil {
		return nil
	}
	return s.Normalizer.Normalize(ctx, org, receiptID)
}

func (s Service) Run(ctx context.Context, org, receiptID string) error {
	if err := s.Content.Materialize(ctx, org, receiptID); err != nil {
		return err
	}
	v, err := s.Content.ProcessingVersion(ctx, org, receiptID)
	if err != nil || v.ID == "" {
		return err
	}
	if v.Availability.State == "retrieval_ready" || v.Availability.State == "quarantined" {
		return nil
	}
	if err = s.Content.BaselineProgress(ctx, org, v.ID, "running", "", false); err != nil {
		return err
	}
	started := time.Now()
	plugin, corpusID, g, err := s.pluginRoute(ctx, org, v)
	var result content.Segmentation
	switch {
	case err != nil:
	case plugin:
		// The plugin's segmentation and vectors are stored together; the
		// enrichment phase attaches the vectors from the stored artifacts.
		result, _, err = s.Plugin.Derive(ctx, org, corpusID, v, g)
		if errors.Is(err, content.ErrIngestionRefused) {
			slog.Warn("ingestion plugin refused a version", "component", "worker", "version_id", v.ID, "error", err.Error())
			s.outcome("baseline", "blocked", receiptID, v, started, "ingestion_refused")
			return s.Content.BaselineProgress(ctx, org, v.ID, "blocked", "ingestion_refused", true)
		}
	default:
		result, err = s.Processor.Process(ctx, Input{Organization: org, Version: v})
		if errors.Is(err, ErrUnsupported) {
			s.outcome("baseline", "blocked", receiptID, v, started, "segmentation_limit")
			return s.Content.BaselineProgress(ctx, org, v.ID, "blocked", "segmentation_limit", true)
		}
		if err == nil {
			err = s.Content.SaveSegmentation(ctx, org, v, result)
		}
	}
	if err == nil {
		err = s.Retrieval.Index(ctx, org, v, result)
	}
	if err != nil {
		s.outcome("baseline", "retrying", receiptID, v, started, "baseline_unavailable")
		_ = s.Content.BaselineProgress(ctx, org, v.ID, "retrying", "baseline_unavailable", false)
		return errors.New("baseline processing unavailable")
	}
	s.outcome("baseline", "succeeded", receiptID, v, started, "")
	if s.Observer != nil {
		s.Observer.Searchable(ctx, org, receiptID)
	}
	return nil
}
