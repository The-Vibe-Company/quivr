package backfill

import (
	"context"
	"errors"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/processing"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"golang.org/x/sync/errgroup"
)

// Target is a backfill Operation and the Corpus's routed generation, which
// carries the target spaces once the backfill has started.
type Target struct {
	Operation  operations.Operation
	Generation content.Generation
}

// Candidate is a current Version in scope whose selected owner's projection
// misses a vector in a target space. Recipe names the owner's stored
// segmentation when one exists; Independent requests an owner's independent
// cuts, including when that projection is absent.
type Candidate struct {
	RecordID, VersionID        string
	Namespace, SourceMediaType string
	Recipe, OwnerPluginID      string
	Independent                bool
}

// RunStore keeps a backfill's state. Every effect is fenced on the Operation
// running, so a pause or a cancellation stops it at the next Version.
type RunStore interface {
	// BeginBackfill moves a queued backfill to running and records the plan
	// its first step is pinned to. It returns the Operation, in whatever
	// state it is, with the Corpus's routed generation.
	BeginBackfill(ctx context.Context, org, id, plan string) (Target, error)
	// CarryBackfillSpaces adds the target spaces to the Corpus's routed
	// generation, so live enrichment fills them for new Versions, counts the
	// scope the first time, and returns that generation. It is
	// operations.ErrNotRunning once the backfill is paused or stopped.
	CarryBackfillSpaces(ctx context.Context, org, id string) (content.Generation, error)
	// BackfillCandidates lists, after the checkpoint and in Version id order,
	// the Versions in scope the generation serves with a vector in its
	// served space for every segment and without one in a target space.
	BackfillCandidates(ctx context.Context, org, id string, g content.Generation, limit int) ([]Candidate, error)
	// CoveredEmbeddings lists the artifacts the generation holds for the
	// segments of seg, in every space.
	CoveredEmbeddings(ctx context.Context, org, generationID string, seg content.Segmentation) ([]content.Embedding, error)
	// CoverBackfill records the target vectors of one Version and moves the
	// checkpoint past it, with no change event. It is
	// operations.ErrNotRunning once the backfill is paused or stopped.
	CoverBackfill(ctx context.Context, org, id string, g content.Generation, versionID string, artifacts []content.Embedding) error
	// CoverBackfillEvaluation records an independent owner's projection,
	// vectors, and checkpoint atomically. It is
	// operations.ErrNotRunning once the backfill is paused or stopped.
	CoverBackfillEvaluation(ctx context.Context, org, id string, g content.Generation, versionID string, seg content.Segmentation, artifacts []content.Embedding) error
	// SkipBackfill moves the checkpoint past a Version it cannot fill and
	// counts why; it is operations.ErrNotRunning like CoverBackfill.
	SkipBackfill(ctx context.Context, org, id, versionID, code string) error
	// CompleteBackfill records success once no candidate remains.
	CompleteBackfill(ctx context.Context, org, id, generationID string) error
	FailBackfill(ctx context.Context, org, id string, failure operations.Error) error
}

// Content reads canonical Versions, their segmentations and their stored
// vectors.
type Content interface {
	TrustedVersion(ctx context.Context, org, corpusID, recordID, versionID string) (content.Version, error)
	PluginSegmentationOf(ctx context.Context, org string, v content.Version, recipe string) (content.Segmentation, error)
	LoadEmbedding(ctx context.Context, org, derivation string) (content.Embedding, []float32, error)
}

// Deriver derives vectors through the ingestion plugin of the plan the work
// is pinned to (processing.PluginDeriver).
type Deriver interface {
	Owns(ctx context.Context, space string) bool
	Fill(ctx context.Context, org, corpusID string, v content.Version, seg content.Segmentation, spaces []string) ([]content.EmbeddingData, error)
	FillIndependent(ctx context.Context, org, corpusID string, v content.Version, spaces []string) (content.Segmentation, []content.EmbeddingData, error)
	Gone(ctx context.Context, cause error) (*content.Diagnostic, error)
}

// Projection attaches vectors to the generation's projected segments.
type Projection interface {
	Publish(ctx context.Context, g content.Generation, org, corpusID, namespace string, v content.Version, seg content.Segmentation) error
	PublishEmbeddings(ctx context.Context, g content.Generation, org string, data []content.EmbeddingData) error
}

// Pinned names the plan the work ctx carries is pinned to and that plan's
// ingestion registration.
type Pinned interface {
	Ingestion(ctx context.Context, registrationID string) (plan, registration string, err error)
}

// Steps records effective time per completed Version from batch wall time
// (observability), which the next dry runs read as throughput.
type Steps interface {
	Step(org, step string, d time.Duration, errorCode string)
}

// StepName is the observability step a backfilled Version is counted under.
const StepName = "backfill"

// maxBatch bounds one step, so its heartbeats stay frequent and a pause
// takes effect quickly.
const maxBatch = 25

// Skip codes, counted on the Operation.
const (
	SkipSegmentationDiffers = "segmentation_differs"
	SkipIngestionRefused    = "ingestion_refused"
	SkipUnavailable         = "version_unavailable"
	SkipArtifactUnavailable = "artifact_unavailable"
	SkipPluginDeadline      = "plugin_deadline"
)

// Cancellation settles a stopped worker's cancellation request.
type Cancellation interface {
	ConfirmCancel(context.Context, string, string) error
}

// Backfiller runs backfill Operations one bounded step at a time.
type Backfiller struct {
	Cancellation Cancellation
	Store        RunStore
	Content      Content
	Plugin       Deriver
	Projection   Projection
	Pinned       Pinned
	Steps        Steps
	Settings     Settings
}

// Progress says whether the Operation needs no further step, and otherwise
// how long to wait before the next one: the pace of the deployment's rate,
// or the poll interval while paused.
type Progress struct {
	Done bool
	Wait time.Duration
}

// terminal is a failure no retry can repair; it fails the Operation.
type terminal struct{ failure operations.Error }

func (t terminal) Error() string { return t.failure.Code }

// Step fills at most one batch of Versions. Transient failures return an
// error so the caller retries with backoff; the Operation stays running.
func (b Backfiller) Step(ctx context.Context, org, id string) (Progress, error) {
	settings := b.Settings.WithDefaults()
	plan, registration, err := b.Pinned.Ingestion(ctx, "")
	if err != nil {
		return Progress{}, err
	}
	t, err := b.Store.BeginBackfill(ctx, org, id, plan)
	if err != nil {
		return Progress{}, err
	}
	op := t.Operation
	if op.Backfill != nil {
		_, registration, err = b.Pinned.Ingestion(ctx, op.Backfill.RegistrationID)
		if err != nil {
			return Progress{}, err
		}
	}
	switch op.State {
	case operations.StatePaused:
		return Progress{Wait: settings.Poll}, nil
	case operations.StateCancelRequested:
		return Progress{Done: true}, b.Cancellation.ConfirmCancel(ctx, org, id)
	case operations.StateRunning:
	default:
		return Progress{Done: true}, nil
	}
	if failure := b.check(ctx, t, registration); failure != nil {
		return b.fail(ctx, org, id, *failure)
	}
	if t.Generation, err = b.Store.CarryBackfillSpaces(ctx, org, id); errors.Is(err, operations.ErrNotRunning) {
		return Progress{}, nil
	} else if err != nil {
		return Progress{}, err
	}
	batch := min(max(1, int(settings.Rate)), maxBatch)
	candidates, err := b.Store.BackfillCandidates(ctx, org, id, t.Generation, batch)
	if err != nil {
		return Progress{}, err
	}
	if len(candidates) == 0 {
		err := b.Store.CompleteBackfill(ctx, org, id, t.Generation.ID)
		if errors.Is(err, operations.ErrNotRunning) {
			return Progress{}, nil
		}
		return Progress{Done: true}, err
	}
	started := time.Now()
	group, work := errgroup.WithContext(ctx)
	group.SetLimit(settings.Concurrency)
	completed := 0
	previous := make(chan struct{})
	close(previous)
	for _, c := range candidates {
		if work.Err() != nil {
			break
		}
		turn, next := previous, make(chan struct{})
		previous = next
		group.Go(func() error {
			if err := work.Err(); err != nil {
				return err
			}
			commit, err := b.prepare(work, org, t, c)
			if err != nil {
				return err
			}
			// Preparation may finish out of order, but the checkpoint is a
			// high-water mark. Commit only after every preceding Version.
			select {
			case <-work.Done():
				return work.Err()
			case <-turn:
			}
			if err := commit(work); err != nil {
				return err
			}
			// The turn chain serializes this counter as well as checkpoints.
			completed++
			close(next)
			return nil
		})
	}
	// Join all effects before retrying, failing or returning a pause. The
	// store's running-state fences and ordered checkpoints remain final.
	err = group.Wait()
	if b.Steps != nil && completed > 0 {
		// A duration per concurrent task counts ordered waiting repeatedly.
		// Batch wall time / completions is the effective throughput dry
		// runs need, including a successfully committed prefix on failure.
		perVersion := time.Since(started) / time.Duration(completed)
		for i := 0; i < completed; i++ {
			b.Steps.Step(org, StepName, perVersion, "")
		}
	}
	var failure terminal
	switch {
	case errors.As(err, &failure):
		return b.fail(ctx, org, id, failure.failure)
	case errors.Is(err, operations.ErrNotRunning):
		// Paused or stopped meanwhile: the next step reads which.
		return Progress{}, nil
	case err != nil:
		return Progress{}, err
	}
	// Pace the next batch so the backfill never exceeds the rate.
	wait := time.Duration(float64(len(candidates))/settings.Rate*float64(time.Second)) - time.Since(started)
	return Progress{Wait: max(wait, 0)}, nil
}

// check refuses to go on when the plan the backfill is pinned to no longer
// names its registration, or the routed generation cannot take the spaces.
func (b Backfiller) check(ctx context.Context, t Target, registration string) *operations.Error {
	spec := t.Operation.Backfill
	switch {
	case spec == nil:
		return &operations.Error{Code: "backfill_missing", Message: "the Operation has no backfill"}
	case registration != spec.RegistrationID:
		return &operations.Error{Code: "plan_changed", Message: "the Pipeline Plan this backfill is pinned to names another ingestion plugin than the one it was accepted for; request a new backfill"}
	case !t.Generation.SpacesProjected:
		return &operations.Error{Code: ErrRebuildRequired.Error(), Message: "the Corpus is routed to a generation built before named vector spaces; rebuild it first"}
	}
	for _, space := range spec.Spaces {
		if !b.Plugin.Owns(ctx, space) {
			return &operations.Error{Code: "unsupported_vector_space", Message: "the pinned ingestion plugin does not own vector space " + space}
		}
	}
	return nil
}

func (b Backfiller) fail(ctx context.Context, org, id string, failure operations.Error) (Progress, error) {
	if err := b.Store.FailBackfill(ctx, org, id, failure); errors.Is(err, operations.ErrNotRunning) {
		return Progress{}, nil
	} else if err != nil {
		return Progress{}, err
	}
	return Progress{Done: true}, nil
}

// prepare derives and publishes one Version, returning its ordered coverage
// or skip commit. Only the segments the generation projects are filled: a plugin
// whose segments differ would need a rebuild.
func (b Backfiller) prepare(ctx context.Context, org string, t Target, c Candidate) (func(context.Context) error, error) {
	op, g := t.Operation, t.Generation
	skip := func(code string) (func(context.Context) error, error) {
		return func(ctx context.Context) error { return b.Store.SkipBackfill(ctx, org, op.ID, c.VersionID, code) }, nil
	}
	v, err := b.Content.TrustedVersion(ctx, org, op.CorpusID, c.RecordID, c.VersionID)
	if errors.Is(err, corpus.ErrNotFound) || errors.Is(err, content.ErrArtifactMissing) || errors.Is(err, content.ErrArtifactCorrupt) {
		return skip(SkipUnavailable)
	}
	if err != nil {
		return nil, err
	}
	var seg content.Segmentation
	var data []content.EmbeddingData
	if c.Independent {
		seg, data, err = b.Plugin.FillIndependent(ctx, org, op.CorpusID, v, op.Backfill.Spaces)
		if err != nil {
			reason, goneErr := b.Plugin.Gone(ctx, err)
			if goneErr != nil {
				return nil, goneErr
			}
			if reason != nil {
				return nil, terminal{failure: operations.Error{Code: reason.Code, Message: reason.Message}}
			}
			switch {
			case errors.Is(err, processing.ErrSegmentsDiffer):
				return skip(SkipSegmentationDiffers)
			case errors.Is(err, content.ErrIngestionRefused):
				return skip(SkipIngestionRefused)
			case errors.Is(err, content.ErrConflict), errors.Is(err, content.ErrArtifactCorrupt):
				return skip(SkipArtifactUnavailable)
			case errors.Is(err, processing.ErrPluginDeadline):
				return skip(SkipPluginDeadline)
			default:
				return nil, err
			}
		}
	} else {
		seg, err = b.Content.PluginSegmentationOf(ctx, org, v, c.Recipe)
		if errors.Is(err, corpus.ErrNotFound) || errors.Is(err, content.ErrConflict) {
			return skip(SkipUnavailable)
		}
		if err != nil {
			return nil, err
		}
		counter, _ := b.Content.(processing.DeadlineCounter)
		out := processing.Derive(ctx, org, b.Plugin, counter, processing.DerivationRequest{CorpusID: op.CorpusID, Version: v, Target: g, Kind: processing.FillVectors, Segmentation: seg, Spaces: op.Backfill.Spaces})
		if out.Terminal != nil {
			switch out.Terminal.Code {
			case "segmentation_differs":
				return skip(SkipSegmentationDiffers)
			case "ingestion_refused":
				return skip(SkipIngestionRefused)
			case "derivation_conflict", "artifact_unavailable":
				return skip(SkipArtifactUnavailable)
			case "plugin_deadline":
				return skip(SkipPluginDeadline)
			default:
				return nil, terminal{failure: operations.Error{Code: out.Terminal.Code, Message: out.Terminal.Message}}
			}
		}
		if out.Retry != nil {
			return nil, out.Retry
		}
		data = out.Data
	}
	targets := map[string]bool{}
	for _, space := range op.Backfill.Spaces {
		targets[space] = true
	}

	covered, err := b.Store.CoveredEmbeddings(ctx, org, g.ID, seg)
	if err != nil {
		return nil, err
	}
	all := make([]content.EmbeddingData, 0, len(covered)+len(data))
	for _, e := range covered {
		if targets[e.SpaceID] {
			continue
		}
		_, vector, err := b.Content.LoadEmbedding(ctx, org, e.DerivationID)
		if errors.Is(err, corpus.ErrNotFound) || errors.Is(err, content.ErrArtifactMissing) || errors.Is(err, content.ErrArtifactCorrupt) || errors.Is(err, content.ErrConflict) {
			// A stored vector no retry can read: the Version needs a rebuild.
			return skip(SkipArtifactUnavailable)
		}
		if err != nil {
			return nil, err
		}
		all = append(all, content.EmbeddingData{Artifact: e, Vector: vector})
	}
	all = append(all, data...)
	if c.Independent {
		if err = b.Projection.Publish(ctx, g, org, op.CorpusID, c.Namespace, v, seg); errors.Is(err, retrieval.ErrProjectionMissing) {
			return skip(SkipUnavailable)
		} else if err != nil {
			return nil, err
		}
	}
	if err = b.Projection.PublishEmbeddings(ctx, g, org, all); errors.Is(err, retrieval.ErrProjectionMissing) {
		// Withdrawn or superseded meanwhile: its objects are gone.
		return skip(SkipUnavailable)
	} else if err != nil {
		return nil, err
	}
	artifacts := make([]content.Embedding, len(data))
	for i, d := range data {
		artifacts[i] = d.Artifact
	}
	if c.Independent {
		return func(ctx context.Context) error {
			return b.Store.CoverBackfillEvaluation(ctx, org, op.ID, g, c.VersionID, seg, artifacts)
		}, nil
	}
	return func(ctx context.Context) error {
		return b.Store.CoverBackfill(ctx, org, op.ID, g, c.VersionID, artifacts)
	}, nil
}
