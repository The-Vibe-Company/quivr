package retrieval

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/processing"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"golang.org/x/sync/errgroup"
)

// RebuildTarget is a running rebuild Operation and its logical target generation.
type RebuildTarget struct {
	Operation  operations.Operation
	Generation content.Generation
}

// RebuildCandidate is a current eligible Version the target does not yet cover.
// VectorsRequired means the currently routed generation serves its vectors, so
// the target must reuse the stored artifacts rather than degrade to lexical-only.
type RebuildCandidate struct {
	RecordID, VersionID string
	// SourceNamespace is the Record's Source Namespace, projected for filtering.
	SourceNamespace string
	VectorsRequired bool
}

// RebuildStore owns the canonical PostgreSQL side of a rebuild: Operation
// state, target coverage and the atomic route cutover.
type RebuildStore interface {
	// BeginRebuild moves a queued Operation to running and returns its target.
	BeginRebuild(ctx context.Context, org, operationID string) (RebuildTarget, error)
	// RebuildCandidates lists current eligible Versions of the Corpus missing
	// target projection or vector coverage, in stable order.
	RebuildCandidates(ctx context.Context, org, operationID string, limit int) ([]RebuildCandidate, error)
	// CheckpointRebuild persists the last version of a successfully joined page.
	// Call only after checking progress; failed pages leave the cursor unchanged.
	CheckpointRebuild(ctx context.Context, org, operationID, after string) error
	// CoverRebuild records target coverage for a verified segmentation and its
	// reused artifacts. It returns false when the Version is no longer eligible.
	CoverRebuild(ctx context.Context, org, operationID string, seg content.Segmentation, artifacts []content.Embedding) (bool, error)
	// ActivateRebuild atomically validates coverage, installs the Corpus route
	// and records success. It returns false while coverage gaps remain and
	// operations.ErrNotRunning when the Operation may no longer take effect.
	ActivateRebuild(ctx context.Context, org, operationID string) (bool, error)
	// FailRebuild records a terminal failure for a queued or running Operation.
	FailRebuild(ctx context.Context, org, operationID string, failure operations.Error) error
	// QuarantineRebuild atomically holds a terminal per-Version failure and
	// records bounded diagnostics. Cancellation fences it like coverage.
	QuarantineRebuild(ctx context.Context, org, operationID, versionID string, reason content.Diagnostic) error
}

// RebuildContent reads canonical Versions.
type RebuildContent interface {
	TrustedVersion(ctx context.Context, org, corpusID, recordID, versionID string) (content.Version, error)
}

// SpaceDeriver derives a Version's segmentation and its vectors through the
// pinned ingestion plugin. It reuses the stored segmentation and artifacts
// and calls the plugin only for what is missing; a terminal refusal is
// content.ErrIngestionRefused.
type SpaceDeriver interface {
	Owns(ctx context.Context, space string) bool
	// Segment returns the Version's segmentation alone.
	Segment(ctx context.Context, org, corpusID string, v content.Version, g content.Generation) (content.Segmentation, error)
	// Derive returns the segmentation and its vectors in the generation's
	// spaces the plugin owns.
	Derive(ctx context.Context, org, corpusID string, v content.Version, g content.Generation) (content.Segmentation, []content.EmbeddingData, error)
	// Gone reports, for a failed call, the diagnostic that stops work pinned
	// to a plan whose ingestion plugin left the active plan and could not be
	// reached, or could no longer serve the work, for the budget; nil keeps
	// retrying.
	Gone(ctx context.Context, cause error) (*content.Diagnostic, error)
}

// GenerationRouter resolves the generation a Corpus is routed to.
type GenerationRouter interface {
	Generation(ctx context.Context, org, corpusID string) (content.Generation, error)
}

// rebuildBatch bounds the work of one step so progress is durable and
// heartbeats stay frequent.
const rebuildBatch = 25

// Rebuild concurrency bounds Version work inside one activity. Plugin/provider
// limits remain authoritative; activity slots are configured independently.
const (
	DefaultRebuildConcurrency = 8
	MaxRebuildConcurrency     = 32
)

// Cancellation settles a stopped worker's cancellation request.
type Cancellation interface {
	ConfirmCancel(context.Context, string, string) error
}

// Rebuilder reconstructs a Corpus projection from canonical text and stored
// vectors, then activates it through canonical PostgreSQL routing.
type Rebuilder struct {
	// Concurrency bounds Versions in flight per Step; zero selects 8.
	// The supported range is 1–32; 1 restores serial coverage.
	Concurrency  int
	Cancellation Cancellation
	Store        RebuildStore
	Content      RebuildContent
	Projection   Projection
	// Plugin segments and embeds through the pinned ingestion plugin, which
	// must own the target's served space.
	Plugin SpaceDeriver
	// Routing resolves the generation a Corpus is routed to during the rebuild.
	Routing GenerationRouter
}

// terminal is a deterministic failure that retrying cannot repair.
type terminal struct {
	failure operations.Error
	reason  *content.Diagnostic
}

func (t terminal) Error() string { return t.failure.Code }

// Step performs one bounded unit of rebuild work and reports whether the
// Operation needs no further steps. Transient failures return an error so the
// caller retries with backoff; the Operation stays running.
func (r Rebuilder) Step(ctx context.Context, org, operationID string) (bool, error) {
	concurrency := r.Concurrency
	if concurrency == 0 {
		concurrency = DefaultRebuildConcurrency
	}
	if concurrency < 1 || concurrency > MaxRebuildConcurrency {
		return false, fmt.Errorf("rebuild concurrency must be between 1 and %d", MaxRebuildConcurrency)
	}
	target, err := r.Store.BeginRebuild(ctx, org, operationID)
	if err != nil {
		return false, err
	}
	if target.Operation.State != operations.StateRunning {
		return r.stop(ctx, org, operationID)
	}
	candidates, err := r.Store.RebuildCandidates(ctx, org, operationID, max(rebuildBatch, r.Concurrency))
	if err != nil {
		return false, err
	}
	group, work := errgroup.WithContext(ctx)
	group.SetLimit(concurrency)
	// Each goroutine owns its outcome slot; read only after the join.
	outcomes := make([]error, len(candidates))
	for i, c := range candidates {
		if work.Err() != nil {
			break
		}
		group.Go(func() error {
			// Go may unblock after another candidate canceled the group.
			if err := work.Err(); err != nil {
				return err
			}
			outcomes[i] = r.cover(work, org, target, c)
			var item terminal
			if errors.As(outcomes[i], &item) {
				reason := content.Diagnostic{Code: item.failure.Code, Message: item.failure.Message}
				if item.reason != nil {
					reason = *item.reason
					reason.Code = item.failure.Code
				}
				// An item failure does not cancel healthy siblings. Use the
				// parent context if another sibling encountered an outage.
				outcomes[i] = r.Store.QuarantineRebuild(ctx, org, operationID, c.VersionID, reason)
			}
			return outcomes[i]
		})
	}
	// Join every effect before settling, comparing progress, or retrying. The
	// store fences canonical effects and idempotently counts committed coverage.
	err = group.Wait()
	// The first transient error cancels work, but cannot hide a cancellation
	// or deterministic failure another candidate already observed.
	for _, outcome := range outcomes {
		if errors.Is(outcome, operations.ErrNotRunning) {
			return r.stop(ctx, org, operationID)
		}
	}
	if err != nil {
		return false, err
	}

	if len(candidates) > 0 {
		remaining, err := r.Store.RebuildCandidates(ctx, org, operationID, max(rebuildBatch, r.Concurrency))
		if err != nil {
			return false, err
		}
		if slices.Equal(candidates, remaining) {
			return false, errors.New("rebuild coverage did not advance; retry after storage or routing recovers")
		}
		if err := r.Store.CheckpointRebuild(ctx, org, operationID, candidates[len(candidates)-1].VersionID); err != nil {
			if errors.Is(err, operations.ErrNotRunning) {
				return r.stop(ctx, org, operationID)
			}
			return false, err
		}
		return false, nil
	}
	activated, err := r.Store.ActivateRebuild(ctx, org, operationID)
	if errors.Is(err, operations.ErrNotRunning) {
		return r.stop(ctx, org, operationID)
	}
	return activated, err
}

// stop ends work on an Operation that left the running state. Every canonical
// effect is fenced on running, so a pending cancellation is now safe to settle.
func (r Rebuilder) stop(ctx context.Context, org, operationID string) (bool, error) {
	if err := r.Cancellation.ConfirmCancel(ctx, org, operationID); err != nil {
		return false, err
	}
	return true, nil
}

func (r Rebuilder) cover(ctx context.Context, org string, target RebuildTarget, c RebuildCandidate) error {
	return workqueue.Track(ctx, org, "operation", target.Operation.ID, c.VersionID, func(ctx context.Context) error { return r.coverDocument(ctx, org, target, c) })
}

func (r Rebuilder) coverDocument(ctx context.Context, org string, target RebuildTarget, c RebuildCandidate) error {
	corpusID := target.Operation.CorpusID
	v, err := r.Content.TrustedVersion(ctx, org, corpusID, c.RecordID, c.VersionID)
	if errors.Is(err, corpus.ErrNotFound) {
		// Hydration can also miss canonical metadata for a still-eligible
		// Version. Only ignore it if a fresh listing confirms it left the
		// batch; otherwise the stable head would be selected forever.
		candidates, listErr := r.Store.RebuildCandidates(ctx, org, target.Operation.ID, max(rebuildBatch, r.Concurrency))
		if listErr != nil {
			return listErr
		}
		for _, candidate := range candidates {
			if candidate.VersionID == c.VersionID {
				return terminal{failure: operations.Error{Code: "canonical_content_unavailable", Message: "an eligible rebuild Version cannot be read from canonical content"}}
			}
		}
		return nil
	}
	if errors.Is(err, content.ErrArtifactMissing) || errors.Is(err, content.ErrArtifactCorrupt) {
		return terminal{failure: operations.Error{Code: "canonical_content_unavailable", Message: "canonical content artifact is missing or failed verification"}}
	}
	if err != nil {
		return err
	}
	if r.Plugin == nil || !r.Plugin.Owns(ctx, target.Generation.SpaceID) {
		return processing.ErrSpaceUnowned
	}
	req := processing.DerivationRequest{CorpusID: corpusID, Version: v, Target: target.Generation, Kind: processing.Vectors}
	if !c.VectorsRequired {
		routed, routeErr := r.Routing.Generation(ctx, org, corpusID)
		if routeErr != nil {
			return routeErr
		}
		// A generation can already carry the next owner's space for
		// evaluation. Only preserve lexical-only readiness when the served
		// ingestion owner is unchanged, not merely when its space is carried.
		currentOwner, targetOwner := "", ""
		if routed.IngestionRouting != nil {
			currentOwner = routed.IngestionRouting.For(v.SourceMediaType)
		}
		if target.Generation.IngestionRouting != nil {
			targetOwner = target.Generation.IngestionRouting.For(v.SourceMediaType)
		}
		// Before source routing was recorded, the primary served space
		// identifies the owner. A carried evaluation owner's space must not
		// be mistaken for that primary space.
		legacyOwner := routed.IngestionRouting == nil && routed.ServedFor(targetOwner) == routed.SpaceID
		if currentOwner == targetOwner || legacyOwner {
			req.Current = &routed
		}
	}
	counter, _ := r.Content.(processing.DeadlineCounter)
	out := processing.Derive(ctx, org, r.Plugin, counter, req)
	if out.Terminal != nil {
		code := out.Terminal.Code
		if code == "derivation_conflict" {
			code = "segmentation_mismatch"
		}
		return terminal{failure: operations.Error{Code: code, Message: out.Terminal.Message}, reason: out.Terminal}
	}
	if out.Retry != nil {
		return out.Retry
	}
	seg, data := out.Segmentation, out.Data
	if err = r.Projection.Publish(ctx, target.Generation, org, corpusID, c.SourceNamespace, v, seg); err != nil {
		return err
	}
	artifacts := make([]content.Embedding, 0, len(data))
	if len(data) > 0 {
		if err = r.Projection.PublishEmbeddings(ctx, target.Generation, org, data); err != nil {
			return err
		}
		for _, d := range data {
			artifacts = append(artifacts, d.Artifact)
		}
	}
	_, err = r.Store.CoverRebuild(ctx, org, target.Operation.ID, seg, artifacts)
	if errors.Is(err, content.ErrConflict) || errors.Is(err, content.ErrInvalid) {
		return terminal{failure: operations.Error{Code: "segmentation_mismatch", Message: "reconstructed segmentation differs from the durable artifact"}}
	}
	return err
}
