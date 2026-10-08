package retrieval

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/processing"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
)

// RebuildTarget is a running rebuild Operation and its logical target generation.
type RebuildTarget struct {
	Operation  operations.Operation
	Generation content.Generation
	// Cursor is the durable exclusive resume position, independent of dispatch.
	Cursor string
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
	// RebuildCandidatesAfter lists forward from an explicit exclusive scan cursor.
	// Unlike RebuildCandidates it never wraps to gaps behind the cursor.
	RebuildCandidatesAfter(ctx context.Context, org, operationID, after string, limit int) ([]RebuildCandidate, error)
	// RebuildCandidatePending tests one Version against the canonical gap
	// predicate, even when it is beyond the durable cursor's first page.
	RebuildCandidatePending(ctx context.Context, org, operationID, versionID string) (bool, error)
	// CheckpointRebuild persists a verified exclusive resume position below all
	// unfinished work. Reconciliation may move it below its previous position.
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

// rebuildBatch bounds candidate buffering and the checkpoint completion interval.
const rebuildBatch = 25

// Rebuild concurrency bounds Version work inside one activity. Plugin/provider
// limits remain authoritative; activity slots are configured independently.
const (
	DefaultRebuildConcurrency = 8
	MaxRebuildConcurrency     = 256
)

// Cancellation settles a stopped worker's cancellation request.
type Cancellation interface {
	ConfirmCancel(context.Context, string, string) error
}

// Rebuilder reconstructs a Corpus projection from canonical text and stored
// vectors, then activates it through canonical PostgreSQL routing.
type Rebuilder struct {
	// Concurrency bounds Versions in flight per Step; zero selects 8.
	// The supported range is 1–256; 1 restores serial coverage.
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

// Step streams bounded pages through a sliding window of Version work. The
// durable cursor trails unfinished work while dispatch can continue ahead.
// A turn stops admission after one minute and joins existing document work.
func (r Rebuilder) Step(ctx context.Context, org, operationID string) (bool, error) {
	concurrency := r.Concurrency
	if concurrency == 0 {
		concurrency = DefaultRebuildConcurrency
	}
	if concurrency < 1 || concurrency > MaxRebuildConcurrency {
		return false, fmt.Errorf("rebuild concurrency must be between 1 and %d", MaxRebuildConcurrency)
	}
	work, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	turnEnded := errors.New("rebuild turn ended")
	// Leave half the remaining attempt for cleanup in short/legacy histories.
	budget := time.Minute
	if deadline, ok := ctx.Deadline(); ok {
		budget = min(budget, max(time.Until(deadline)/2, 0))
	}
	admission, endAdmission := context.WithCancelCause(work)
	defer endAdmission(nil)
	turn := time.AfterFunc(budget, func() { endAdmission(turnEnded) })
	defer turn.Stop()
	interrupted := func(err error) (bool, error) {
		if context.Cause(admission) == turnEnded && errors.Is(err, context.Canceled) && ctx.Err() == nil {
			return false, nil
		}
		return false, err
	}
	target, err := r.Store.BeginRebuild(admission, org, operationID)
	if err != nil {
		return interrupted(err)
	}
	if target.Operation.State != operations.StateRunning {
		return r.stop(ctx, org, operationID)
	}
	limit := max(rebuildBatch, concurrency)
	page, err := r.Store.RebuildCandidates(admission, org, operationID, limit)
	if err != nil {
		return interrupted(err)
	}
	if len(page) == 0 {
		activated, err := r.Store.ActivateRebuild(admission, org, operationID)
		if errors.Is(err, operations.ErrNotRunning) {
			return r.stop(ctx, org, operationID)
		}
		if err != nil {
			return interrupted(err)
		}
		return activated, nil
	}
	initial := slices.Clone(page)
	// A full gap sweep may have wrapped below the durable cursor.
	safe := target.Cursor
	if page[0].VersionID <= safe {
		safe = ""
	}
	scan := safe
	type unfinished struct{ id, before string }
	type result struct {
		candidate unfinished
		err       error
	}
	pending := make(map[string]unfinished, concurrency)
	results := make(chan result, concurrency)
	admissionDone := admission.Done()
	checkpoints := time.NewTicker(5 * time.Second)
	defer checkpoints.Stop()
	var barrier unfinished // earliest failed or canceled unfinished Version
	var retry error
	stopped, yielded, notRunning := false, false, false
	completed := 0
	remember := func(c unfinished) {
		if barrier.id == "" || c.id < barrier.id {
			barrier = c
		}
	}
	frontier := func() string {
		lowest := barrier
		for _, c := range pending {
			if lowest.id == "" || c.id < lowest.id {
				lowest = c
			}
		}
		if lowest.id != "" {
			return lowest.before
		}
		return scan
	}
	checkpoint := func(check context.Context, observe bool) error {
		after := frontier()
		if after != safe {
			// A nil document outcome is not proof that every gap disappeared:
			// enrichment or imports may have changed eligibility during work.
			gaps, err := r.Store.RebuildCandidatesAfter(check, org, operationID, safe, 1)
			if err != nil {
				return err
			}
			if len(gaps) > 0 && gaps[0].VersionID <= after {
				remember(unfinished{id: gaps[0].VersionID, before: safe})
				after = safe
			}
		}
		if after == safe && !observe {
			return nil
		}
		if err := r.Store.CheckpointRebuild(check, org, operationID, after); err != nil {
			return err
		}
		safe = after
		return nil
	}
	stop := func(err error) {
		if errors.Is(err, operations.ErrNotRunning) {
			notRunning = true
		}
		// A canceled sibling must not hide the actual outage which canceled it.
		if retry == nil || (errors.Is(retry, context.Canceled) && !errors.Is(err, context.Canceled)) {
			retry = err
		}
		stopped = true
		cancel(err)
	}
	stopAdmission := func(err error) {
		if context.Cause(admission) == turnEnded && (errors.Is(err, context.Canceled) || err == turnEnded) && ctx.Err() == nil {
			yielded, stopped = true, true
			return
		}
		stop(err)
	}

	for {
		if admission.Err() != nil {
			stopped = true
			if context.Cause(admission) == turnEnded {
				yielded = true
			}
		}
		for !stopped && len(pending) < concurrency {
			if admission.Err() != nil {
				stopAdmission(context.Cause(admission))
				break
			}
			if len(page) == 0 {
				page, err = r.Store.RebuildCandidatesAfter(admission, org, operationID, scan, limit)
				if err != nil {
					stopAdmission(err)
					break
				}
				if len(page) == 0 {
					stopped = true
					break
				}
			}
			if admission.Err() != nil {
				stopAdmission(context.Cause(admission))
				break
			}
			c := page[0]
			page = page[1:]
			u := unfinished{id: c.VersionID, before: scan}
			scan = c.VersionID
			pending[u.id] = u
			go func() {
				err := work.Err()
				if err == nil {
					err = r.coverCandidate(work, ctx, org, target, c)
				}
				if err != nil {
					cancel(err)
				}
				results <- result{candidate: u, err: err}
			}()
		}
		if len(pending) == 0 {
			break
		}
		select {
		case out := <-results:
			delete(pending, out.candidate.id)
			if out.err != nil {
				remember(out.candidate)
				stop(out.err)
			} else {
				completed++
			}
			if !stopped && completed >= rebuildBatch {
				if err := checkpoint(admission, false); err != nil {
					stopAdmission(err)
				}
				completed = 0
			}
		case <-checkpoints.C:
			// Observe Operation state even while a slow head prevents advancement.
			if err := checkpoint(work, true); err != nil {
				stop(err)
			}
		case <-admissionDone:
			stopAdmission(context.Cause(admission))
			admissionDone = nil
		case <-ctx.Done():
			stop(ctx.Err())
		}
	}
	// Every admitted candidate has reported after its canonical effects and
	// cleanup joined. Only now may cancellation settle or reconciliation wrap.
	if notRunning {
		return r.stop(ctx, org, operationID)
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if retry != nil {
		return false, retry
	}
	// Finish only bounded canonical bookkeeping after all document effects join.
	settle, settleCancel := context.WithTimeout(ctx, 5*time.Second)
	defer settleCancel()
	if !yielded {
		remaining, err := r.Store.RebuildCandidates(settle, org, operationID, limit)
		if err != nil {
			return false, err
		}
		if slices.Equal(initial, remaining) {
			return false, errors.New("rebuild coverage did not advance; retry after storage or routing recovers")
		}
	}
	if err := checkpoint(settle, false); err != nil {
		if errors.Is(err, operations.ErrNotRunning) {
			return r.stop(ctx, org, operationID)
		}
		return false, err
	}
	return false, nil
}

// coverCandidate isolates a terminal item without canceling healthy siblings.
// Quarantine uses the parent context so another candidate's outage cannot hide
// a deterministic refusal already observed before the join.
func (r Rebuilder) coverCandidate(work, parent context.Context, org string, target RebuildTarget, c RebuildCandidate) error {
	err := r.cover(work, org, target, c)
	var item terminal
	if errors.As(err, &item) {
		reason := content.Diagnostic{Code: item.failure.Code, Message: item.failure.Message}
		if item.reason != nil {
			reason = *item.reason
			reason.Code = item.failure.Code
		}
		err = r.Store.QuarantineRebuild(parent, org, target.Operation.ID, c.VersionID, reason)
	}
	return err
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
		// A bounded candidate page can exclude this Version; classify against
		// its actual canonical eligibility.
		pending, checkErr := r.Store.RebuildCandidatePending(ctx, org, target.Operation.ID, c.VersionID)
		if checkErr != nil {
			return checkErr
		}
		if pending {
			return terminal{failure: operations.Error{Code: "canonical_content_unavailable", Message: "an eligible rebuild Version cannot be read from canonical content"}}
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
