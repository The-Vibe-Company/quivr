// Package processing runs the baseline and enrichment of Record Versions
// through the pinned ingestion plugin, under engine ownership.
package processing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/logging"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
)

type Indexer interface {
	Index(context.Context, string, content.Version, content.Segmentation) error
}

// Service runs the baseline (segments, searchable by keyword) and the
// enrichment (vectors) of each Version through the pinned ingestion plugin.
// The engine segments and embeds nothing itself.
type Service struct {
	Evaluation *Evaluator
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
// plugin no longer pinned. A same-owner recipe switch retains lexical
// baseline and settles enrichment until a rebuild supplies current vectors.
var ErrSpaceUnowned = errors.New("served space has no pinned owner")

// ErrPluginDeadline reports a plugin call the engine ended at its deadline:
// the plugin answered nothing within the timeout_ms it declares (at most
// SegmentAndEmbedTimeoutCap).
var ErrPluginDeadline = plugins.ErrCallDeadline

// Engine bounds on the enrichment of one Version through the ingestion plugin.
const (
	// SegmentAndEmbedTimeoutCap bounds one segment_and_embed call, whatever
	// timeout_ms the plugin declares (the manifest schema's maximum).
	SegmentAndEmbedTimeoutCap = plugins.SegmentAndEmbedTimeoutCap
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
	// recipeMismatch permits lexical publication by the same ingestion owner
	// while the routed generation still serves a retired model.
	recipeMismatch bool
}

// route resolves a Version's routed generation: served by one of the pinned
// plugin's spaces, or by LegacySpace; ErrSpaceUnowned otherwise.
func (s Service) route(ctx context.Context, org string, v content.Version) (route, error) {
	if s.Plugin == nil || s.Routing == nil {
		return route{}, ErrSpaceUnowned
	}
	r, g, err := VersionRoute(ctx, org, v, s.Content, s.Routing)
	if err != nil {
		return route{}, err
	}
	// Work admitted before a source cutover continues deriving with its own
	// owner. Publication separately checks the live serving route.
	if w, ok := plugins.WorkOf(ctx); ok && w.Kind == plugins.WorkIngestion {
		driver := s.Plugin.forVersion(ctx, v)
		if driver.Plugin != nil && g.ServedFor(content.PluginOfRecipe(driver.descriptor.Recipe)) == "" {
			spaces := driver.descriptor.Spaces
			if len(spaces) > 0 && g.Carries(spaces[0]) {
				g.Spaces = append([]content.GenerationSpace(nil), g.Spaces...)
				for i := range g.Spaces {
					if g.Spaces[i].ID == spaces[0] {
						g.Spaces[i].Role = content.SpaceServed
					}
				}
			}
		}
	}
	driver := s.Plugin.forVersion(ctx, v)
	mismatch := false
	if driver.Plugin != nil {
		owner := content.PluginOfRecipe(driver.descriptor.Recipe)
		space := g.ServedFor(owner)
		sameOwner := g.IngestionRouting != nil && g.IngestionRouting.For(v.SourceMediaType) == owner
		if g.IngestionRouting == nil {
			for _, sp := range g.Spaces {
				if sp.OwnerPluginID == owner && sp.Role == content.SpaceServed {
					sameOwner = true
				}
			}
		}
		mismatch = owner != "" && sameOwner && space != "" && !driver.owns(space)
	}
	return route{corpusID: r.Source.CorpusID, generation: g, legacy: s.LegacySpace != "" && g.SpaceID == s.LegacySpace, recipeMismatch: mismatch}, nil
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
func (s Service) outcome(ctx context.Context, org, stage, outcome, receiptID string, v content.Version, started time.Time, code string, details ...any) {
	if s.Observer != nil {
		s.Observer.Outcome(org, stage, outcome, code, time.Since(started))
	}
	level := slog.LevelInfo
	if outcome != "succeeded" {
		level = slog.LevelWarn
	}
	attrs := []any{"component", "worker", "stage", stage, "outcome", outcome, "code", code,
		"receipt_id", receiptID, "record_id", v.RecordID, "version_id", v.ID, "duration_ms", time.Since(started).Milliseconds()}
	slog.Log(ctx, level, "processing outcome", append(attrs, details...)...)
}

// Baseline diagnostics expose operational metadata, never dependency messages:
// plugin responses and database errors may contain submitted content or secrets.
var baselinePluginCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,99}$`)
var baselineSQLState = regexp.MustCompile(`^[0-9A-Z]{5}$`)

func baselineFailure(err error) []any {
	kind := logging.Diagnostic("dependency_error")
	var details []any
	var pluginErr *plugins.PluginError
	var databaseErr interface{ SQLState() string }
	var networkErr net.Error
	switch {
	case errors.As(err, &pluginErr):
		kind = "plugin_error"
		if baselinePluginCode.MatchString(pluginErr.Code) {
			details = append(details, "plugin_code", pluginErr.Code)
		}
		if pluginErr.Status >= 100 && pluginErr.Status <= 599 {
			details = append(details, "plugin_http_status", pluginErr.Status)
		}
		details = append(details, "plugin_retryable", pluginErr.Retryable)
	case errors.Is(err, plugins.ErrCallDeadline):
		kind = "plugin_deadline"
	case errors.Is(err, plugins.ErrUnavailable):
		kind = "plugin_unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		kind = "context_deadline"
	case errors.Is(err, context.Canceled):
		kind = "context_canceled"
	case errors.Is(err, ErrSpaceUnowned):
		kind = "space_unowned"
	case errors.As(err, &databaseErr):
		kind = "database_error"
		if state := databaseErr.SQLState(); baselineSQLState.MatchString(state) {
			details = append(details, "sqlstate", state)
		}
	case errors.As(err, &networkErr):
		kind = "network_error"
		details = append(details, "network_timeout", networkErr.Timeout())
	}
	return append(details, "failure_kind", kind)
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
	if handled, err := s.runPrepared(ctx, org, receiptID); handled {
		return err
	}
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
	step := "route"
	rt, err := s.route(ctx, org, v)
	var result content.Segmentation
	if err == nil {
		step = "derive"
		out := Derive(ctx, org, s.Plugin, s.Content, DerivationRequest{CorpusID: rt.corpusID, Version: v, Target: rt.generation, Kind: Segments, AllowLegacy: rt.legacy || rt.recipeMismatch})
		result, err = out.Segmentation, out.Retry
		if out.Terminal != nil {
			s.outcome(ctx, org, "baseline", "blocked", receiptID, v, started, out.Terminal.Code)
			return s.Content.QuarantineVersion(ctx, org, v.ID, *out.Terminal)
		}
	}
	if err == nil {
		step = "index"
		err = s.Retrieval.Index(ctx, org, v, result)
	}
	if err != nil {
		details := append([]any{"failure_step", logging.Diagnostic(step)}, baselineFailure(err)...)
		s.outcome(ctx, org, "baseline", "retrying", receiptID, v, started, "baseline_unavailable", details...)
		_ = s.Content.BaselineProgress(ctx, org, v.ID, "retrying", "baseline_unavailable", false)
		return fmt.Errorf("baseline processing unavailable (%s): %w", step, err)
	}
	s.outcome(ctx, org, "baseline", "succeeded", receiptID, v, started, "")
	if s.Observer != nil {
		s.Observer.Searchable(ctx, org, receiptID)
	}
	return nil
}
