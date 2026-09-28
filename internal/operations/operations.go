// Package operations owns durable, trackable administrative execution. Technical
// retries and restarts preserve an Operation's identity; intentional reruns of a
// terminal Operation create a new linked Operation.
package operations

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

// KindProjectionRebuild rebuilds one Corpus's search projection from canonical
// content and durable artifacts.
const KindProjectionRebuild = "projection_rebuild"

// KindRetrievalConfiguration builds a replacement generation that pins a new
// Corpus retrieval configuration; the configuration becomes effective only
// when that generation is validated and routed.
const KindRetrievalConfiguration = "retrieval_configuration"

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
)

// Terminal reports whether an Operation has a final outcome.
func Terminal(state string) bool {
	return state == StateSucceeded || state == StateFailed || state == StateCanceled
}

var (
	// ErrConflict reports an idempotency key reused with a different canonical request.
	ErrConflict = errors.New("idempotency_conflict")
	// ErrNoDispatch means no Operation dispatch intent is currently claimable.
	ErrNoDispatch = errors.New("no operation dispatch")
	// ErrNotRunning means the Operation left the running state (terminal or
	// cancellation requested), so no further effects such as activation apply.
	ErrNotRunning = errors.New("operation not running")
	// ErrNotTerminal rejects a rerun of an Operation without a final outcome.
	ErrNotTerminal = errors.New("operation_not_terminal")
	// ErrUnsupportedKind rejects control of an Operation kind without a
	// registered command permission and cancellation-aware worker.
	ErrUnsupportedKind = errors.New("unsupported_operation_kind")
	// ErrSuperseded fails a generation whose pinned retrieval configuration is
	// older than the one the Corpus already serves, so it can never revert it.
	ErrSuperseded = errors.New("retrieval_configuration_superseded")
)

// commandPermissions names, per controllable Operation kind, the permission of
// the command that created it. Rerun revalidates it. Register a kind only once
// its worker honors cancel_requested and the store can create its rerun target.
var commandPermissions = map[string]string{KindProjectionRebuild: "projections:rebuild", KindRetrievalConfiguration: "corpora:write"}

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
}

// Dispatch is committed intent to start an Operation's durable execution.
type Dispatch struct{ Organization, OperationID string }

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
}

type Service struct{ Store Store }

// RequestRebuild authorizes and durably accepts a scoped projection rebuild.
func (s Service) RequestRebuild(ctx context.Context, scope corpus.Scope, corpusID, key string) (Operation, error) {
	if !scope.Allows("projections:rebuild") {
		return Operation{}, corpus.ErrForbidden
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
func (s Service) ConfigureRetrieval(ctx context.Context, scope corpus.Scope, corpusID, key string, cfg corpus.Retrieval) (Operation, error) {
	if !scope.Allows("corpora:write") || !scope.Allows("operations:write") {
		return Operation{}, corpus.ErrForbidden
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
	if !scope.Allows("operations:read") {
		return Operation{}, corpus.ErrForbidden
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
	if !scope.Allows("operations:write") {
		return Operation{}, corpus.ErrForbidden
	}
	op, err := s.Store.Operation(ctx, scope.Organization, id)
	if err != nil {
		return Operation{}, err
	}
	if !scope.Contains(op.CorpusID) {
		return Operation{}, corpus.ErrNotFound
	}
	if _, ok := commandPermissions[op.Kind]; !ok {
		return Operation{}, ErrUnsupportedKind
	}
	return op, nil
}

// Cancel stops remaining work without undoing committed effects. Idempotency
// follows Operation state: repeating the request, with any key, returns the
// current state, and a terminal Operation keeps its outcome.
func (s Service) Cancel(ctx context.Context, scope corpus.Scope, id, key string) (Operation, error) {
	if _, err := s.controlled(ctx, scope, id); err != nil {
		return Operation{}, err
	}
	return s.Store.CancelOperation(ctx, scope.Organization, id)
}

// Rerun intentionally repeats a terminal Operation under a new linked identity,
// revalidating the caller's current command permission and Corpus scope.
func (s Service) Rerun(ctx context.Context, scope corpus.Scope, id, key string) (Operation, error) {
	op, err := s.controlled(ctx, scope, id)
	if err != nil {
		return Operation{}, err
	}
	if !scope.Allows(commandPermissions[op.Kind]) {
		return Operation{}, corpus.ErrForbidden
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
