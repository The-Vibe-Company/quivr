package retrieval

import (
	"context"
	"errors"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
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
	// ConfirmCancel settles a cancel_requested Operation as canceled once this
	// worker stops; it leaves every other state unchanged.
	ConfirmCancel(ctx context.Context, org, operationID string) error
}

// RebuildContent reads canonical Versions and verified durable Embedding Artifacts.
type RebuildContent interface {
	Version(ctx context.Context, scope corpus.Scope, recordID, versionID string) (content.Version, error)
	LoadEmbedding(ctx context.Context, org, derivation string) (content.Embedding, []float32, error)
}

// ArtifactIdentity names the pinned vector space and producer used to derive
// Embedding Artifact identities. It deliberately cannot run inference.
type ArtifactIdentity interface {
	Space() content.VectorSpace
	Producer() string
}

// Segmenter re-derives a Version's segmentation locally from canonical text.
type Segmenter func(ctx context.Context, org string, v content.Version) (content.Segmentation, error)

// SpaceDeriver derives a Version's segmentation and its vectors in the
// spaces of a generation whose served space a pinned ingestion plugin owns.
// It reuses the stored segmentation and artifacts and calls the plugin only
// for what is missing; a terminal refusal is content.ErrIngestionRefused.
type SpaceDeriver interface {
	Owns(space string) bool
	Derive(ctx context.Context, org, corpusID string, v content.Version, g content.Generation) (content.Segmentation, []content.EmbeddingData, error)
}

// rebuildBatch bounds the work of one step so progress is durable and
// heartbeats stay frequent.
const rebuildBatch = 25

// Rebuilder reconstructs a Corpus projection from canonical text and stored
// vectors, then activates it through canonical PostgreSQL routing.
type Rebuilder struct {
	Store      RebuildStore
	Content    RebuildContent
	Projection Projection
	Segment    Segmenter
	Artifacts  ArtifactIdentity
	// Plugin derives targets served by a pinned ingestion plugin's space; nil
	// when none is pinned.
	Plugin SpaceDeriver
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
	if err := r.Store.ConfirmCancel(ctx, org, operationID); err != nil {
		return false, err
	}
	return true, nil
}

func (r Rebuilder) cover(ctx context.Context, org string, target RebuildTarget, c RebuildCandidate) error {
	corpusID := target.Operation.CorpusID
	v, err := r.Content.Version(ctx, corpus.Scope{Organization: org, Actions: []string{"content:read"}, Corpora: []string{corpusID}}, c.RecordID, c.VersionID)
	if errors.Is(err, corpus.ErrNotFound) {
		return nil // Withdrawn meanwhile; the next candidate listing excludes it.
	}
	if errors.Is(err, content.ErrArtifactMissing) || errors.Is(err, content.ErrArtifactCorrupt) {
		return terminal{failure: operations.Error{Code: "canonical_content_unavailable", Message: "canonical content artifact is missing or failed verification"}}
	}
	if err != nil {
		return err
	}
	var seg content.Segmentation
	var data []content.EmbeddingData
	if r.Plugin != nil && r.Plugin.Owns(target.Generation.SpaceID) {
		seg, data, err = r.Plugin.Derive(ctx, org, corpusID, v, target.Generation)
		switch {
		case errors.Is(err, content.ErrIngestionRefused):
			return terminal{failure: operations.Error{Code: "ingestion_refused", Message: "the ingestion plugin refuses a Version of the Corpus; its log names why"}}
		case errors.Is(err, content.ErrConflict):
			return terminal{failure: operations.Error{Code: "segmentation_mismatch", Message: "the stored segmentation differs from canonical text"}}
		case err != nil:
			return err
		}
	} else {
		seg, err = r.Segment(ctx, org, v)
		if errors.Is(err, content.ErrInvalid) || errors.Is(err, content.ErrConflict) {
			return terminal{failure: operations.Error{Code: "segmentation_mismatch", Message: "segmentation cannot be reconstructed from canonical text"}}
		}
		if err != nil {
			return err
		}
		data, err = r.vectors(ctx, org, corpusID, v, seg, target.Generation, c.VectorsRequired)
		if err != nil {
			return err
		}
	}
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

// vectors loads every segment's verified stored vector. It never calls
// inference: when the routed generation serves vectors for this Version, a
// missing or corrupt artifact fails the rebuild; otherwise the Version stays
// lexical-only until its own enrichment completes.
func (r Rebuilder) vectors(ctx context.Context, org, corpusID string, v content.Version, seg content.Segmentation, g content.Generation, required bool) ([]content.EmbeddingData, error) {
	space := r.Artifacts.Space()
	if space.ID != g.SpaceID {
		return nil, terminal{failure: operations.Error{Code: "unsupported_vector_space", Message: "target generation vector space is not the pinned space"}}
	}
	data := make([]content.EmbeddingData, 0, len(seg.Segments))
	for _, p := range seg.Segments {
		input := content.EmbeddingInput(org, corpusID, v, seg, p, space, r.Artifacts.Producer())
		artifact, vector, err := r.Content.LoadEmbedding(ctx, org, input.DerivationID)
		switch {
		case err == nil && artifact.SpaceID == g.SpaceID:
			data = append(data, content.EmbeddingData{Artifact: artifact, Vector: vector})
			continue
		case err == nil, errors.Is(err, content.ErrConflict), errors.Is(err, content.ErrInvalid), errors.Is(err, content.ErrArtifactCorrupt):
			if required {
				return nil, terminal{failure: operations.Error{Code: "embedding_artifact_corrupt", Message: "stored embedding artifact failed verification"}}
			}
		case errors.Is(err, corpus.ErrNotFound), errors.Is(err, content.ErrArtifactMissing):
			if required {
				return nil, terminal{failure: operations.Error{Code: "embedding_artifact_unavailable", Message: "stored embedding artifact is missing"}}
			}
		default:
			return nil, err
		}
		return nil, nil
	}
	return data, nil
}
