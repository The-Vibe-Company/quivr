package processing

import (
	"context"
	"errors"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

// DerivationKind selects segments alone, target vectors, or vectors for an
// existing segmentation. Every caller uses the same pinned-owner step.
type DerivationKind int

const (
	Segments DerivationKind = iota
	Vectors
	FillVectors
)

type DerivationRequest struct {
	CorpusID string
	Version  content.Version
	Target   content.Generation
	Kind     DerivationKind
	// Current lets a rebuild preserve lexical-only availability when its owner
	// keeps the same served space. Nil requires vectors for a Vectors request.
	Current      *content.Generation
	Segmentation content.Segmentation
	Spaces       []string
	AllowLegacy  bool
}

type DerivationDriver interface {
	Gone(context.Context, error) (*content.Diagnostic, error)
}

type DeadlineCounter interface {
	CountEnrichmentTimeout(context.Context, string, string) (int, error)
}

type DerivationResult struct {
	Segmentation content.Segmentation
	Data         []content.EmbeddingData
	// Terminal is a refusal, incompatible derivation, exhausted deadline budget,
	// or stopped pinned owner. Retry is a transient failure; both nil is success.
	Terminal *content.Diagnostic
	Retry    error
	Cause    error
}

type boundDriver interface {
	Bound(context.Context, string, []string) DerivationDriver
	ServedSpace(content.Generation) string
	Serves(context.Context, content.Version, content.Generation) error
}

type segmentDriver interface {
	Segment(context.Context, string, string, content.Version, content.Generation) (content.Segmentation, error)
}
type vectorDriver interface {
	Derive(context.Context, string, string, content.Version, content.Generation) (content.Segmentation, []content.EmbeddingData, error)
}
type fillDriver interface {
	Fill(context.Context, string, string, content.Version, content.Segmentation, []string) ([]content.EmbeddingData, error)
}

// Derive binds the route once, invokes that owner, and classifies its outcome.
// Outages never consume the three-deadline vector budget. Baseline segmentation
// retains its retry policy; enrichment and rebuild share the durable per-Version
// vector deadline counter. Backfill retains its skip-on-deadline policy. Callers own publication and task effects.
func Derive(ctx context.Context, org string, plugin DerivationDriver, counter DeadlineCounter, req DerivationRequest) DerivationResult {
	out := DerivationResult{}
	if plugin == nil {
		out.Retry = ErrSpaceUnowned
		return out
	}
	if driver, ok := plugin.(boundDriver); ok {
		var spaces []string
		if req.Kind == FillVectors {
			spaces = req.Spaces
		}
		plugin = driver.Bound(ctx, req.Version.SourceMediaType, spaces)
	}
	kind := req.Kind
	var err error
	if driver, ok := plugin.(boundDriver); ok && kind != FillVectors && !req.AllowLegacy {
		err = driver.Serves(ctx, req.Version, req.Target)
	}
	if err == nil && kind == Vectors && req.Current != nil {
		served := func(g content.Generation) string {
			if d, ok := plugin.(boundDriver); ok {
				return d.ServedSpace(g)
			}
			return g.SpaceID
		}
		if served(*req.Current) == served(req.Target) {
			kind = Segments
		}
	}
	if err == nil {
		switch kind {
		case Segments:
			if driver, ok := plugin.(segmentDriver); ok {
				out.Segmentation, err = driver.Segment(ctx, org, req.CorpusID, req.Version, req.Target)
			} else {
				err = content.ErrInvalid
			}
		case Vectors:
			if driver, ok := plugin.(vectorDriver); ok {
				out.Segmentation, out.Data, err = driver.Derive(ctx, org, req.CorpusID, req.Version, req.Target)
			} else {
				err = content.ErrInvalid
			}
		case FillVectors:
			out.Segmentation = req.Segmentation
			if driver, ok := plugin.(fillDriver); ok {
				out.Data, err = driver.Fill(ctx, org, req.CorpusID, req.Version, req.Segmentation, req.Spaces)
			} else {
				err = content.ErrInvalid
			}
		default:
			err = content.ErrInvalid
		}
	}
	out.Cause = err
	if err == nil {
		return out
	}
	terminal := func(code, message string) { out.Terminal = &content.Diagnostic{Code: code, Message: message} }
	switch {
	case errors.Is(err, content.ErrIngestionRefused):
		reason := content.RefusalReason(err)
		out.Terminal = &reason
	case errors.Is(err, ErrSegmentsDiffer):
		terminal("segmentation_differs", "the ingestion plugin cuts different segments; rebuild the Corpus")
	case errors.Is(err, content.ErrConflict):
		terminal("derivation_conflict", "the derived artifact differs from its stored value")
	case errors.Is(err, content.ErrArtifactCorrupt):
		terminal("artifact_unavailable", "a stored vector failed verification; rebuild the Corpus")
	default:
		reason, goneErr := plugin.Gone(ctx, err)
		if goneErr != nil {
			out.Retry = goneErr
			return out
		}
		if reason != nil {
			out.Terminal = reason
			return out
		}
		if kind == FillVectors && errors.Is(err, ErrPluginDeadline) {
			terminal("plugin_deadline", "the plugin did not finish this backfill Version within its deadline")
			return out
		}
		if kind == Vectors && errors.Is(err, ErrPluginDeadline) {
			if counter == nil {
				out.Retry = errors.New("vector deadline counter unavailable")
				return out
			}
			n, countErr := counter.CountEnrichmentTimeout(ctx, org, req.Version.ID)
			if countErr != nil {
				out.Retry = countErr
				return out
			}
			if n >= EnrichmentTimeoutBudget {
				terminal(content.CodeEnrichmentTimeout, "the ingestion plugin exceeded its deadline three times for this Version")
				return out
			}
		}
		out.Retry = err
	}
	return out
}
