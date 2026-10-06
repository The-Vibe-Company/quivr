package quarantine

import (
	"context"
	"errors"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/operations"
)

// Item phases. A reprocess takes each Version once, in a durable phase, so a
// retry or a restart resumes it where it stopped and never leaves a Version
// neither quarantined nor processed.
const (
	// PhasePending: not started.
	PhasePending = "pending"
	// PhaseRenormalizing: the previous normalization was moved aside; the
	// Version stays quarantined until its normalization is recorded again.
	PhaseRenormalizing = "renormalizing"
	// PhaseReleased: the quarantine was lifted; the Version goes through
	// its baseline and enrichment like a new one.
	PhaseReleased = "released"
	// Outcomes.
	PhaseRecovered   = "recovered"
	PhaseQuarantined = "quarantined"
	PhaseSkipped     = "skipped"
)

// Skip codes, counted on the Operation as skipped_<code>.
const (
	// SkipWithdrawn: the Record was withdrawn.
	SkipWithdrawn = "withdrawn"
	// SkipSuperseded: the Record desires a newer revision.
	SkipSuperseded = "superseded"
	// SkipNotQuarantined: the Version left quarantine meanwhile.
	SkipNotQuarantined = "not_quarantined"
	// SkipCanceled: the reprocess was canceled while the Version was being
	// reprocessed; it keeps or gets back its previous reason.
	SkipCanceled = "canceled"
	// SkipNotRepublishable: its normalization succeeded but it cannot be
	// published again (it was segmented meanwhile, or it is not a Blob); it
	// stays quarantined.
	SkipNotRepublishable = "not_republishable"
	// SkipNotSettled: its processing ended neither searchable nor
	// quarantined; it gets back its previous reason.
	SkipNotSettled = "not_settled"
)

// Item is one Version a reprocess took.
type Item struct {
	VersionID, RecordID, ReceiptID string
	// Stage is the restart step, defaulting to the step it failed at.
	Stage string
	Phase string
}

// RunStore keeps a reprocess's state. Starting an item is fenced on the
// Operation running; an item once started is finished, or abandoned when
// the reprocess is canceled.
type RunStore interface {
	// BeginReprocess moves a queued reprocess to running. It returns the
	// Operation, in whatever state it is, and the item a previous step
	// started and did not finish, if any.
	BeginReprocess(ctx context.Context, org, id string) (operations.Operation, *Item, error)
	// StartReprocessItem takes the next pending item; nil when none is
	// left, operations.ErrNotRunning once the reprocess is paused or
	// stopped. In the same transaction it skips, with a counted reason, an
	// item whose Version left quarantine or can no longer become current,
	// and otherwise prepares it: a normalization failure is moved aside
	// (PhaseRenormalizing), an ingestion quarantine lifted (PhaseReleased).
	StartReprocessItem(ctx context.Context, org, id string) (*Item, error)
	// RepublishItem publishes a renormalizing Version again with the
	// objects of its new normalization and lifts its quarantine
	// (PhaseReleased), announced by record.materialized. It reports whether
	// it released the Version, and is content.ErrConflict when the Version
	// was segmented meanwhile.
	RepublishItem(ctx context.Context, org, id string, item Item, work content.Work, p content.Publication) (bool, error)
	// SkipItem ends a renormalizing item no retry can carry further; its
	// Version stays quarantined with its reason.
	SkipItem(ctx context.Context, org, id string, item Item, code string) error
	// RequarantineItem keeps a renormalizing Version quarantined with its
	// new reason and finishes the item as quarantined.
	RequarantineItem(ctx context.Context, org, id string, item Item, reason content.Diagnostic) error
	// FinishItem records a released item's outcome from its Version:
	// recovered once searchable, quarantined when quarantined again,
	// skipped when its Record was withdrawn or replaced meanwhile.
	FinishItem(ctx context.Context, org, id string, item Item) error
	// AbandonItem ends the started item of a canceled reprocess: a Version
	// it released that is not searchable yet is quarantined again with its
	// previous reason.
	AbandonItem(ctx context.Context, org, id string, item Item) error
	// CompleteReprocess records success once no pending item is left.
	CompleteReprocess(ctx context.Context, org, id string) error
}

// Normalizer records a new normalization outcome for a Version published
// quarantined, with the normalizer of the plan ctx carries
// (normalization.Service.Renormalize).
type Normalizer interface {
	Renormalize(ctx context.Context, org, receiptID string) error
}

// Publisher decides what a renormalized Version publishes
// (content.Service.Republication).
type Publisher interface {
	Republication(ctx context.Context, org, receiptID string) (content.Work, content.Publication, *content.Diagnostic, error)
}

// Processor runs a released Version's baseline and enrichment, exactly as
// for a Version seen for the first time (processing.Service).
type Processor interface {
	Run(ctx context.Context, org, receiptID string) error
	Enrich(ctx context.Context, org, receiptID string) error
}

// Settings pace a reprocess: Rate Versions per second at most, and a paused
// one checks every Poll whether it was resumed or canceled.
type Settings struct {
	Rate float64
	Poll time.Duration
}

// Default settings.
const (
	DefaultRate = 2.0
	DefaultPoll = 5 * time.Second
)

// WithDefaults fills unset settings.
func (s Settings) WithDefaults() Settings {
	if s.Rate <= 0 {
		s.Rate = DefaultRate
	}
	if s.Poll <= 0 {
		s.Poll = DefaultPoll
	}
	return s
}

// maxBatch bounds one step, so its heartbeats stay frequent and a pause
// takes effect quickly.
const maxBatch = 25

// Progress says whether the Operation needs no further step, and otherwise
// how long to wait before the next one.
type Progress struct {
	Done bool
	Wait time.Duration
}

// Cancellation settles a stopped worker's cancellation request.
type Cancellation interface {
	ConfirmCancel(context.Context, string, string) error
}

// Reprocessor runs quarantine reprocess Operations one bounded step at a
// time. Every call it makes resolves plugins in the plan ctx is pinned to.
type Reprocessor struct {
	Cancellation Cancellation
	Store        RunStore
	Normalizer   Normalizer
	Publisher    Publisher
	Processor    Processor
	Settings     Settings
}

// Step reprocesses at most one batch of Versions. Transient failures return
// an error so the caller retries with backoff; the started item resumes.
func (r Reprocessor) Step(ctx context.Context, org, id string) (Progress, error) {
	settings := r.Settings.WithDefaults()
	op, started, err := r.Store.BeginReprocess(ctx, org, id)
	if err != nil {
		return Progress{}, err
	}
	switch op.State {
	case operations.StateCancelRequested:
		if started != nil {
			if err = r.Store.AbandonItem(ctx, org, id, *started); err != nil {
				return Progress{}, err
			}
		}
		return Progress{Done: true}, r.Cancellation.ConfirmCancel(ctx, org, id)
	case operations.StatePaused:
		// A pause holds the next Versions, never one half reprocessed.
		if started != nil {
			return Progress{}, r.item(ctx, org, id, *started)
		}
		return Progress{Wait: settings.Poll}, nil
	case operations.StateRunning:
	default:
		return Progress{Done: true}, nil
	}
	began := time.Now()
	batch, taken := min(max(1, int(settings.Rate)), maxBatch), 0
	if started != nil {
		if err = r.item(ctx, org, id, *started); err != nil {
			return Progress{}, err
		}
		taken++
	}
	for taken < batch {
		it, err := r.Store.StartReprocessItem(ctx, org, id)
		switch {
		case errors.Is(err, operations.ErrNotRunning):
			// Paused or canceled meanwhile: the next step reads which.
			return Progress{}, nil
		case err != nil:
			return Progress{}, err
		case it == nil:
			return Progress{Done: true}, r.Store.CompleteReprocess(ctx, org, id)
		}
		if err = r.item(ctx, org, id, *it); err != nil {
			return Progress{}, err
		}
		taken++
	}
	wait := time.Duration(float64(taken)/settings.Rate*float64(time.Second)) - time.Since(began)
	return Progress{Wait: max(wait, 0)}, nil
}

// item carries one started Version from its phase to its outcome.
func (r Reprocessor) item(ctx context.Context, org, id string, it Item) error {
	if it.Phase == PhaseRenormalizing {
		if err := r.Normalizer.Renormalize(ctx, org, it.ReceiptID); err != nil {
			return err
		}
		work, p, reason, err := r.Publisher.Republication(ctx, org, it.ReceiptID)
		switch {
		case errors.Is(err, content.ErrRepublicationWithdrawn):
			// FinishItem reads the withdrawal and skips it.
			return r.Store.FinishItem(ctx, org, id, it)
		case errors.Is(err, content.ErrInvalid):
			return r.Store.SkipItem(ctx, org, id, it, SkipNotRepublishable)
		case err != nil:
			return err
		case reason != nil:
			return r.Store.RequarantineItem(ctx, org, id, it, *reason)
		}
		released, err := r.Store.RepublishItem(ctx, org, id, it, work, p)
		if errors.Is(err, content.ErrConflict) {
			return r.Store.SkipItem(ctx, org, id, it, SkipNotRepublishable)
		}
		if err != nil || !released {
			// Not released: the item was settled otherwise meanwhile.
			return err
		}
		it.Phase = PhaseReleased
	}
	if it.Phase != PhaseReleased {
		return nil
	}
	// The baseline makes the Version current and searchable, announced by
	// record.retrieval_ready, from which alerts are evaluated; a refusal
	// quarantines it again with the new reason.
	if err := r.Processor.Run(ctx, org, it.ReceiptID); err != nil {
		return err
	}
	if err := r.Processor.Enrich(ctx, org, it.ReceiptID); err != nil {
		return err
	}
	return r.Store.FinishItem(ctx, org, id, it)
}
