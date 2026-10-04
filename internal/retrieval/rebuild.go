package retrieval

import (
	"context"
	"errors"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/processing"
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
	// CoverRebuild records target coverage for a verified segmentation and its
	// reused artifacts. It returns false when the Version is no longer eligible.
	CoverRebuild(ctx context.Context, org, operationID string, seg content.Segmentation, artifacts []content.Embedding) (bool, error)
	// ActivateRebuild atomically validates coverage, installs the Corpus route
	// and records success. It returns false while coverage gaps remain and
	// operations.ErrNotRunning when the Operation may no longer take effect.
	ActivateRebuild(ctx context.Context, org, operationID string) (bool, error)
	// FailRebuild records a terminal failure for a queued or running Operation.
	FailRebuild(ctx context.Context, org, operationID string, failure operations.Error) error
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

// Cancellation settles a stopped worker's cancellation request.
type Cancellation interface {
	ConfirmCancel(context.Context, string, string) error
}

// Rebuilder reconstructs a Corpus projection from canonical text and stored
// vectors, then activates it through canonical PostgreSQL routing.
type Rebuilder struct {
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
type terminal struct{ failure operations.Error }

func (t terminal) Error() string { return t.failure.Code }

// Step performs one bounded unit of rebuild work and reports whether the
// Operation needs no further steps. Transient failures return an error so the
// caller retries with backoff; the Operation stays running.
func (r Rebuilder) Step(ctx context.Context, org, operationID string) (bool, error) {
	target, err := r.Store.BeginRebuild(ctx, org, operationID)
	if err != nil {
		return false, err
	}
	if target.Operation.State != operations.StateRunning {
		return r.stop(ctx, org, operationID)
	}
	candidates, err := r.Store.RebuildCandidates(ctx, org, operationID, rebuildBatch)
	if err != nil {
		return false, err
	}
	for _, c := range candidates {
		err = r.cover(ctx, org, target, c)
		var failure terminal
		if errors.As(err, &failure) {
			// A failure never overrides an earlier cancellation request.
			if err = r.Store.FailRebuild(ctx, org, operationID, failure.failure); err != nil {
				return false, err
			}
			return r.stop(ctx, org, operationID)
		}
		if errors.Is(err, operations.ErrNotRunning) {
			return r.stop(ctx, org, operationID)
		}
		if err != nil {
			return false, err
		}
	}
	if len(candidates) > 0 {
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
	corpusID := target.Operation.CorpusID
	v, err := r.Content.TrustedVersion(ctx, org, corpusID, c.RecordID, c.VersionID)
	if errors.Is(err, corpus.ErrNotFound) {
		return nil // Withdrawn meanwhile; the next candidate listing excludes it.
	}
	if errors.Is(err, content.ErrArtifactMissing) || errors.Is(err, content.ErrArtifactCorrupt) {
		return terminal{failure: operations.Error{Code: "canonical_content_unavailable", Message: "canonical content artifact is missing or failed verification"}}
	}
	if err != nil {
		return err
	}
	if r.Plugin == nil || !r.Plugin.Owns(ctx, target.Generation.SpaceID) {
		return terminal{failure: operations.Error{Code: "unsupported_vector_space", Message: "the pinned ingestion plugin does not own the target generation's vector space"}}
	}
	req := processing.DerivationRequest{CorpusID: corpusID, Version: v, Target: target.Generation, Kind: processing.Vectors}
	if !c.VectorsRequired {
		routed, routeErr := r.Routing.Generation(ctx, org, corpusID)
		if routeErr != nil {
			return routeErr
		}
		req.Current = &routed
	}
	counter, _ := r.Content.(processing.DeadlineCounter)
	out := processing.Derive(ctx, org, r.Plugin, counter, req)
	if out.Terminal != nil {
		code := out.Terminal.Code
		if code == "derivation_conflict" {
			code = "segmentation_mismatch"
		}
		return terminal{failure: operations.Error{Code: code, Message: out.Terminal.Message}}
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
