// Package operations owns durable, trackable administrative execution. Technical
// retries and restarts preserve an Operation's identity; intentional reruns (a
// later slice) create a new linked Operation.
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

// Operation states follow the public contract.
const (
	StateQueued    = "queued"
	StateRunning   = "running"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
)

var (
	// ErrConflict reports an idempotency key reused with a different canonical request.
	ErrConflict = errors.New("idempotency_conflict")
	// ErrNoDispatch means no Operation dispatch intent is currently claimable.
	ErrNoDispatch = errors.New("no operation dispatch")
	// ErrNotRunning means the Operation left the running state (terminal or
	// cancellation requested), so no further effects such as activation apply.
	ErrNotRunning = errors.New("operation not running")
)

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
