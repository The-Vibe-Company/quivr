// Package operations owns durable, trackable administrative execution. Technical
// retries and restarts preserve an Operation's identity; intentional reruns of a
// terminal Operation create a new linked Operation.
package operations

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
)

// KindProjectionRebuild rebuilds one Corpus's search projection from canonical
// content and durable artifacts.
const KindProjectionRebuild = "projection_rebuild"

// KindRetrievalConfiguration builds a replacement generation that pins a new
// Corpus retrieval configuration; the configuration becomes effective only
// when that generation is validated and routed.
const KindRetrievalConfiguration = "retrieval_configuration"

// KindBackfill fills vector spaces of a Corpus's routed generation for the
// Versions it already serves, through the active ingestion plugin, below live
// ingestion (Spec 5). It never creates a Record Version.
const KindBackfill = "backfill"

// KindQuarantineReprocess reruns, with the Pipeline Plan active when it is
// accepted, the step a Corpus's quarantined Versions failed at, and carries
// the ones that succeed through the normal publication path (Spec 5).
const KindQuarantineReprocess = "quarantine_reprocess"

// Operation states follow the public contract.
const (
	StateQueued    = "queued"
	StateRunning   = "running"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
	// StateCancelRequested means work must stop; the worker settles it as
	// StateCanceled once no further effect can commit.
	StateCancelRequested = "cancel_requested"
	StateCanceled        = "canceled"
	// StatePaused holds a backfill: it makes no progress until resumed, and
	// keeps its checkpoint and its pinned plan.
	StatePaused = "paused"
)

// Terminal reports whether an Operation has a final outcome.
func Terminal(state string) bool {
	return state == StateSucceeded || state == StateFailed || state == StateCanceled
}

var (
	// ErrConflict reports an idempotency key reused with a different canonical request.
	ErrConflict = publicerr.IdempotencyConflict
	// ErrNotRunning means the Operation left the running state (terminal or
	// cancellation requested), so no further effects such as activation apply.
	ErrNotRunning = errors.New("operation not running")
	// ErrNotTerminal rejects a rerun of an Operation without a final outcome.
	ErrNotTerminal = publicerr.OperationNotTerminal
	// ErrUnsupportedKind rejects control of an Operation kind without a
	// registered command permission and cancellation-aware worker.
	ErrUnsupportedKind = publicerr.UnsupportedOperationKind
	// ErrSuperseded fails a generation whose pinned retrieval configuration is
	// older than the one the Corpus already serves, so it can never revert it.
	ErrSuperseded = errors.New("retrieval_configuration_superseded")
	// ErrOperationSuperseded fails an Operation whose generation is outranked
	// by the one a later-accepted Operation of the same Corpus already
	// activated with the same retrieval configuration version.
	ErrOperationSuperseded = errors.New("operation_superseded")
)

// controlActions ties operation controls to the command that created the work.
var controlActions = map[string]struct{ Pause, Resume, Rerun corpus.Action }{
	KindProjectionRebuild:      {Rerun: corpus.ActionOperationRerunRebuild},
	KindRetrievalConfiguration: {Rerun: corpus.ActionOperationRerunRetrieval},
	KindBackfill:               {corpus.ActionOperationPauseBackfill, corpus.ActionOperationResumeBackfill, corpus.ActionOperationRerunBackfill},
	KindQuarantineReprocess:    {corpus.ActionOperationPauseReprocess, corpus.ActionOperationResumeReprocess, corpus.ActionOperationRerunReprocess},
}

// BackfillPermission is the operator permission that starts, pauses and
// resumes backfills and quarantine reprocessing: the plugin registry's
// plugins:admin.
const BackfillPermission = "plugins:admin"

// pausable lists the kinds whose worker honors paused.
var pausable = map[string]bool{KindBackfill: true, KindQuarantineReprocess: true}

// Error is a bounded, terminal diagnostic recorded on the Operation.
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type Operation struct {
	ID                 string
	Organization       string
	Kind               string
	CorpusID           string
	State              string
	PreviousID         string
	TargetGenerationID string
	Counters           map[string]int
	Errors             []Error
	// ResultGenerationID names the activated logical generation once succeeded.
	ResultGenerationID string
	// Backfill describes a backfill Operation; nil for other kinds.
	Backfill *Backfill
	// Reprocess describes a quarantine reprocess; nil for other kinds.
	Reprocess *Reprocess
}

// Reprocess is what a quarantine reprocess covers: the Versions of the
// Operation's Corpus quarantined when it was accepted, kept by these filters.
type Reprocess struct {
	// Plugin and Code, when set, keep the Versions whose quarantine reason
	// names that plugin id or has that code.
	Plugin string `json:"plugin,omitempty"`
	Code   string `json:"code,omitempty"`
	// QuarantinedAfter and QuarantinedBefore bound, when set, when the
	// Versions were quarantined: after inclusive, before exclusive.
	QuarantinedAfter  *time.Time `json:"quarantined_after,omitempty"`
	QuarantinedBefore *time.Time `json:"quarantined_before,omitempty"`
	// PlanID is the Pipeline Plan it runs with: the one active when it was
	// accepted.
	PlanID string `json:"plan_id,omitempty"`
	// Estimate is the dry run it was accepted after.
	Estimate ReprocessEstimate `json:"estimate"`
}

// ReprocessEstimate is what a quarantine reprocess dry run counts: the
// Versions in scope, by the step they failed at and by reason code.
type ReprocessEstimate struct {
	Versions int64 `json:"versions"`
	// Stages counts them by the step they failed at: normalization or
	// ingestion.
	Stages map[string]int64 `json:"stages"`
	Codes  map[string]int64 `json:"codes"`
}

// Backfill is what a backfill Operation fills and how far it got.
type Backfill struct {
	// RegistrationID is the ingestion plugin registration it runs with.
	RegistrationID string `json:"registration_id"`
	// Spaces are the vector space ids it fills.
	Spaces []string `json:"spaces"`
	// AcceptedAfter and AcceptedBefore bound, when set, the time Quivr
	// accepted the Versions it covers: after inclusive, before exclusive.
	AcceptedAfter  *time.Time `json:"accepted_after,omitempty"`
	AcceptedBefore *time.Time `json:"accepted_before,omitempty"`
	// PlanID is the Pipeline Plan its first step was pinned to.
	PlanID string `json:"plan_id,omitempty"`
	// Checkpoint is the last Version id it finished, in Version id order.
	Checkpoint string `json:"checkpoint,omitempty"`
	// Estimate is the dry run it was accepted after.
	Estimate BackfillEstimate `json:"estimate"`
}

// BackfillEstimate is what a backfill dry run reports before anything starts.
type BackfillEstimate struct {
	RegistrationID string   `json:"registration_id"`
	Spaces         []string `json:"spaces"`
	// Versions and Segments count the Versions in scope that miss a vector
	// in a target space, and their segments.
	Versions int64 `json:"versions"`
	Segments int64 `json:"segments"`
	// InputTokens estimates the text the plugin embeds: one token per four
	// code points of the segments.
	InputTokens int64 `json:"input_tokens"`
	// EstimatedSeconds is how long the backfill should take at the
	// deployment's pace and the recent throughput; DurationBasis names what
	// the throughput came from.
	EstimatedSeconds float64 `json:"estimated_seconds"`
	DurationBasis    string  `json:"duration_basis"`
	// EstimatedCostUSD is nil, the cost unknown, when no target space
	// declares a price.
	EstimatedCostUSD *float64 `json:"estimated_cost_usd,omitempty"`
	// ConfirmationRequired reports that the cost exceeds the deployment's
	// threshold, so the backfill needs confirm_cost.
	ConfirmationRequired bool `json:"confirmation_required"`
}

// Dispatch is committed intent to start an Operation's durable execution.
type Dispatch struct{ Organization, OperationID, Kind, TraceContext string }

type Store interface {
	// AcceptRebuild commits a queued rebuild Operation, its target generation,
	// dispatch intent and journal event, or returns the existing Operation for
	// the same Organization + Corpus + key + canonical request.
	AcceptRebuild(ctx context.Context, org, corpusID, key string, canonical []byte) (Operation, error)
	Operation(ctx context.Context, org, id string) (Operation, error)
	// CancelOperation cancels a queued Operation, requests cancellation of a
	// running one, and otherwise returns the Operation unchanged.
	CancelOperation(ctx context.Context, org, id string) (Operation, error)
	// AcceptRerun commits a new queued Operation linked to a terminal source,
	// its own target, dispatch intent and journal event, or returns the rerun
	// already accepted for the same source + key + canonical request.
	AcceptRerun(ctx context.Context, org, sourceID, key string, canonical []byte) (Operation, error)
	// AcceptRetrievalConfiguration commits the next configuration version of
	// the Corpus as a queued Operation whose target generation pins resolved,
	// supersedes older pending configuration Operations, or returns the
	// existing Operation for the same Organization + Corpus + key + request.
	AcceptRetrievalConfiguration(ctx context.Context, org, corpusID, key string, canonical, resolved []byte) (Operation, error)
	// PauseOperation pauses a queued or running Operation and otherwise
	// returns it unchanged.
	PauseOperation(ctx context.Context, org, id string) (Operation, error)
	// ResumeOperation resumes a paused Operation and otherwise returns it
	// unchanged.
	ResumeOperation(ctx context.Context, org, id string) (Operation, error)
}

type Service struct{ Store Store }

// RequestRebuild authorizes and durably accepts a scoped projection rebuild.
func (s Service) RequestRebuild(ctx context.Context, scope corpus.Scope, corpusID, key string, prepare ...func() (string, string, error)) (Operation, error) {
	if err := scope.Require(corpus.ActionOperationsRequestRebuild); err != nil {
		return Operation{}, err
	}
	if corpusID == "" || !scope.Contains(corpusID) {
		return Operation{}, corpus.ErrNotFound
	}

	for _, load := range prepare {
		var err error
		corpusID, key, err = load()
		if err != nil {
			return Operation{}, err
		}
	}
	if corpusID == "" || !scope.Contains(corpusID) {
		return Operation{}, corpus.ErrNotFound
	}
	canonical, err := json.Marshal(struct {
		Key string `json:"idempotency_key"`
	}{key})
	if err != nil {
		return Operation{}, err
	}
	return s.Store.AcceptRebuild(ctx, scope.Organization, corpusID, key, canonical)
}

// ConfigureRetrieval authorizes and durably accepts a resolved retrieval
// configuration change. The Corpus keeps serving its prior configuration until
// the replacement generation is validated and routed.
func (s Service) ConfigureRetrieval(ctx context.Context, scope corpus.Scope, corpusID, key string, cfg corpus.Retrieval, prepare ...func() (string, string, corpus.Retrieval, error)) (Operation, error) {
	if err := scope.Require(corpus.ActionOperationsConfigureRetrieval); err != nil {
		return Operation{}, err
	}
	if corpusID == "" || !scope.Contains(corpusID) {
		return Operation{}, corpus.ErrNotFound
	}

	for _, load := range prepare {
		var err error
		corpusID, key, cfg, err = load()
		if err != nil {
			return Operation{}, err
		}
	}
	if corpusID == "" || !scope.Contains(corpusID) {
		return Operation{}, corpus.ErrNotFound
	}
	if cfg.Fields == nil {
		cfg.Fields = []corpus.Field{}
	}
	resolved, err := json.Marshal(cfg)
	if err != nil {
		return Operation{}, err
	}
	canonical, err := json.Marshal(struct {
		Key       string          `json:"idempotency_key"`
		Retrieval json.RawMessage `json:"retrieval"`
	}{key, resolved})
	if err != nil {
		return Operation{}, err
	}
	return s.Store.AcceptRetrievalConfiguration(ctx, scope.Organization, corpusID, key, canonical, resolved)
}

// Read conceals Operations whose Corpus lies outside the caller's scope.
func (s Service) Read(ctx context.Context, scope corpus.Scope, id string) (Operation, error) {
	if err := scope.Require(corpus.ActionOperationsRead); err != nil {
		return Operation{}, err
	}
	op, err := s.Store.Operation(ctx, scope.Organization, id)
	if err != nil {
		return Operation{}, err
	}
	if !scope.Contains(op.CorpusID) {
		return Operation{}, corpus.ErrNotFound
	}
	return op, nil
}

// controlled loads an Operation the caller may act on with operations:write.
func (s Service) controlled(ctx context.Context, scope corpus.Scope, id string) (Operation, error) {
	op, err := s.Store.Operation(ctx, scope.Organization, id)
	if err != nil {
		return Operation{}, err
	}
	if !scope.Contains(op.CorpusID) {
		return Operation{}, corpus.ErrNotFound
	}
	if _, ok := controlActions[op.Kind]; !ok {
		return Operation{}, ErrUnsupportedKind
	}
	return op, nil
}

// Cancel stops remaining work without undoing committed effects. Idempotency
// follows Operation state: repeating the request, with any key, returns the
// current state, and a terminal Operation keeps its outcome.
func (s Service) Cancel(ctx context.Context, scope corpus.Scope, id, key string, prepare ...func() (string, string, error)) (Operation, error) {
	if err := scope.Require(corpus.ActionOperationCancel); err != nil {
		return Operation{}, err
	}
	for _, load := range prepare {
		var err error
		id, key, err = load()
		if err != nil {
			return Operation{}, err
		}
	}
	if _, err := s.controlled(ctx, scope, id); err != nil {
		return Operation{}, err
	}
	return s.Store.CancelOperation(ctx, scope.Organization, id)
}

// Pause holds a pausable Operation (a backfill) until it is resumed, without
// undoing what it committed. Like cancel, repeating it returns the current
// state, and a terminal Operation keeps its outcome.
func (s Service) Pause(ctx context.Context, scope corpus.Scope, id string, prepare ...func() (string, error)) (Operation, error) {
	var op Operation
	err := scope.Require(corpus.ActionOperationPause, func() (corpus.Action, error) {
		for _, load := range prepare {
			var err error
			id, err = load()
			if err != nil {
				return "", err
			}
		}
		var err error
		op, err = s.pausable(ctx, scope, id)
		if err != nil {
			return "", err
		}
		return controlActions[op.Kind].Pause, nil
	})
	if err != nil {
		return Operation{}, err
	}
	return s.Store.PauseOperation(ctx, scope.Organization, id)
}

// Resume lets a paused Operation continue from its checkpoint.
func (s Service) Resume(ctx context.Context, scope corpus.Scope, id string, prepare ...func() (string, error)) (Operation, error) {
	var op Operation
	err := scope.Require(corpus.ActionOperationResume, func() (corpus.Action, error) {
		for _, load := range prepare {
			var err error
			id, err = load()
			if err != nil {
				return "", err
			}
		}
		var err error
		op, err = s.pausable(ctx, scope, id)
		if err != nil {
			return "", err
		}
		return controlActions[op.Kind].Resume, nil
	})
	if err != nil {
		return Operation{}, err
	}
	return s.Store.ResumeOperation(ctx, scope.Organization, id)
}

// pausable loads an Operation the caller may pause or resume: a pausable
// kind, with the permission of the command that created it.
func (s Service) pausable(ctx context.Context, scope corpus.Scope, id string) (Operation, error) {
	op, err := s.controlled(ctx, scope, id)
	if err != nil {
		return op, err
	}
	if !pausable[op.Kind] {
		return Operation{}, ErrUnsupportedKind
	}
	return op, nil
}

// Rerun intentionally repeats a terminal Operation under a new linked identity,
// revalidating the caller's current command permission and Corpus scope.
func (s Service) Rerun(ctx context.Context, scope corpus.Scope, id, key string, prepare ...func() (string, string, error)) (Operation, error) {
	var op Operation
	err := scope.Require(corpus.ActionOperationRerun, func() (corpus.Action, error) {
		for _, load := range prepare {
			var err error
			id, key, err = load()
			if err != nil {
				return "", err
			}
		}
		var err error
		op, err = s.controlled(ctx, scope, id)
		if err != nil {
			return "", err
		}
		return controlActions[op.Kind].Rerun, nil
	})
	if err != nil {
		return Operation{}, err
	}
	canonical, err := json.Marshal(struct {
		Key    string `json:"idempotency_key"`
		Source string `json:"source_operation_id"`
	}{key, id})
	if err != nil {
		return Operation{}, err
	}
	return s.Store.AcceptRerun(ctx, scope.Organization, id, key, canonical)
}
