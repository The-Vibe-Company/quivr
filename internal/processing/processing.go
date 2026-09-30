// Package processing runs the baseline and enrichment of Record Versions
// through the pinned ingestion plugin, under engine ownership.
package processing

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

type Indexer interface {
	Index(context.Context, string, content.Version, content.Segmentation) error
}

// Service runs the baseline (segments, searchable by keyword) and the
// enrichment (vectors) of each Version through the pinned ingestion plugin.
// The engine segments and embeds nothing itself.
type Service struct {
	Content    content.Service
	Retrieval  Indexer
	Enrichment EnrichmentIndexer
	// Observer receives processing outcomes for metrics; nil disables them.
	Observer Observer
	// Normalizer invokes the pinned external normalizer for routed Blobs
	// before publication; nil normalizes nothing.
	Normalizer Normalizer
	// Plugin segments and embeds through the pinned ingestion plugin.
	Plugin *PluginDeriver
	// Routing resolves a Corpus's generation.
	Routing GenerationRouter
	// LegacySpace is the E5 space the engine served itself before the
	// core.ingest plugin (THE-777). A Corpus still routed to a generation it
	// serves gets its new Versions segmented by the pinned plugin, searchable
	// by keyword, while their vectors wait for the Corpus's rebuild.
	LegacySpace string
}

// GenerationRouter resolves the generation PostgreSQL routes a Corpus to.
type GenerationRouter interface {
	Generation(ctx context.Context, org, corpusID string) (content.Generation, error)
}

// ErrSpaceUnowned reports a Corpus whose routed generation is served by a
// space the pinned ingestion plugin does not own, such as the space of a
// plugin no longer pinned. Its Versions wait, rather than being segmented
// another way, until the owner is pinned again or the Corpus is rebuilt; a
// Corpus served by LegacySpace waits for its rebuild for vectors only.
var ErrSpaceUnowned = errors.New("served space has no pinned owner")

// ErrPluginDeadline reports a plugin call the engine ended at its deadline:
// the plugin answered nothing within the timeout_ms it declares (at most
// SegmentAndEmbedTimeoutCap).
var ErrPluginDeadline = errors.New("plugin call reached its deadline")

// Engine bounds on the enrichment of one Version through the ingestion plugin.
const (
	// SegmentAndEmbedTimeoutCap bounds one segment_and_embed call, whatever
	// timeout_ms the plugin declares (the manifest schema's maximum).
	SegmentAndEmbedTimeoutCap = 5 * time.Minute
	// EnrichmentTimeoutBudget is how many calls for one Version may end at
	// their deadline before its enrichment stops as blocked
	// (content.CodeEnrichmentTimeout). An unavailable plugin never counts.
	EnrichmentTimeoutBudget = 3
)

// route is how a Version's Corpus is served.
type route struct {
	corpusID   string
	generation content.Generation
	// legacy: the routed generation is served by LegacySpace.
	legacy bool
}

// route resolves a Version's routed generation: served by one of the pinned
// plugin's spaces, or by LegacySpace; ErrSpaceUnowned otherwise.
func (s Service) route(ctx context.Context, org string, v content.Version) (route, error) {
	if s.Plugin == nil || s.Routing == nil {
		return route{}, ErrSpaceUnowned
	}
	r, err := s.Content.Record(ctx, corpus.Scope{Organization: org, Actions: []string{"content:read"}, Corpora: []string{"*"}}, v.RecordID)
	if err != nil {
		return route{}, err
	}
	g, err := s.Routing.Generation(ctx, org, r.Source.CorpusID)
	if err != nil {
		return route{}, err
	}
	out := route{corpusID: r.Source.CorpusID, generation: g}
	switch {
	case s.Plugin.Owns(ctx, g.SpaceID):
		return out, nil
	case s.LegacySpace != "" && g.SpaceID == s.LegacySpace:
		out.legacy = true
		return out, nil
	}
	return out, ErrSpaceUnowned
}

// gone is the diagnostic that stops work pinned to a plan after cause
// (PluginDeriver.gone), or nil to keep retrying.
func (s Service) gone(ctx context.Context, cause error) (*content.Diagnostic, error) {
	if s.Plugin == nil {
		return nil, nil
	}
	return s.Plugin.Gone(ctx, cause)
}

// Observer is told each processing outcome (bounded stage and outcome names,
// the failure code and how long the stage ran), when a Receipt's Version
// became searchable and when an enrichment stage that ran for `ran` gave it
// its vectors.
type Observer interface {
	Outcome(org, stage, outcome, code string, d time.Duration)
	Searchable(ctx context.Context, org, receiptID string)
	Enriched(ctx context.Context, org, receiptID string, ran time.Duration)
}

// outcome reports one stage outcome to the Observer and a correlated log line.
func (s Service) outcome(org, stage, outcome, receiptID string, v content.Version, started time.Time, code string) {
	if s.Observer != nil {
		s.Observer.Outcome(org, stage, outcome, code, time.Since(started))
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
	var result content.Segmentation
	rt, err := s.route(ctx, org, v)
	if err == nil {
		// The segments alone, so the Version is searchable by keyword
		// whatever the embedding backend's state; enrichment adds vectors.
		result, err = s.Plugin.Segment(ctx, org, rt.corpusID, v, rt.generation)
	}
	if errors.Is(err, content.ErrIngestionRefused) {
		slog.Warn("ingestion plugin refused a version", "component", "worker", "version_id", v.ID, "error", err.Error())
		s.outcome(org, "baseline", "blocked", receiptID, v, started, "ingestion_refused")
		return s.Content.BaselineProgress(ctx, org, v.ID, "blocked", "ingestion_refused", true)
	}
	if err != nil {
		// Work pinned to a plan whose ingestion plugin left the active plan
		// and stays unreachable is quarantined, never moved to another one.
		reason, goneErr := s.gone(ctx, err)
		if goneErr != nil {
			err = goneErr
		} else if reason != nil {
			slog.Warn("pinned ingestion plugin unreachable; quarantining the version", "component", "worker", "version_id", v.ID, "plan", reason.Plan, "plugin", reason.Plugin, "plugin_version", reason.PluginVersion, "error", err.Error())
			s.outcome(org, "baseline", "blocked", receiptID, v, started, reason.Code)
			return s.Content.QuarantineVersion(ctx, org, v.ID, *reason)
		}
	}
	if err == nil {
		err = s.Retrieval.Index(ctx, org, v, result)
	}
	if err != nil {
		s.outcome(org, "baseline", "retrying", receiptID, v, started, "baseline_unavailable")
		_ = s.Content.BaselineProgress(ctx, org, v.ID, "retrying", "baseline_unavailable", false)
		return errors.New("baseline processing unavailable")
	}
	s.outcome(org, "baseline", "succeeded", receiptID, v, started, "")
	if s.Observer != nil {
		s.Observer.Searchable(ctx, org, receiptID)
	}
	return nil
}
