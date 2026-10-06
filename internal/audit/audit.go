// Package audit defines the durable trail of sensitive operator commands.
package audit

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Event contains identifiers and bounded transport facts, never command bodies.
type Event struct {
	ID           int64     `json:"id"`
	Time         time.Time `json:"time"`
	Actor        string    `json:"actor"`
	Action       string    `json:"action"`
	TargetType   string    `json:"target_type"`
	TargetID     string    `json:"target_id"`
	Organization string    `json:"organization"`
	Outcome      string    `json:"outcome"`
	RequestID    string    `json:"request_id"`
	Detail       Detail    `json:"detail"`
}

var ErrReadOnly = errors.New("audited command was a read-only estimate")

type Detail struct {
	CredentialVersion int    `json:"credential_version,omitempty"`
	Status            int    `json:"status"`
	ErrorCode         string `json:"error_code,omitempty"`
	PlanID            string `json:"plan_id,omitempty"`
}
type Filter struct {
	Since, Until                        time.Time
	Actor, Action, TargetType, TargetID string
	After                               int64
	Limit                               int
}

// Store runs the command in the same transaction as its event. A refused
// outcome rolls back command writes before committing the refusal event.
type Store interface {
	Record(context.Context, *Event, func(context.Context) error) error
	List(context.Context, string, Filter) ([]Event, error)
}

func Log(ctx context.Context, e Event) {
	slog.InfoContext(ctx, "sensitive action", "event", "quivr.audit", "audit_id", e.ID,
		"time", e.Time, "actor", e.Actor, "action", e.Action, "target_type", e.TargetType,
		"target_id", e.TargetID, "organization", e.Organization, "outcome", e.Outcome,
		"request_id", e.RequestID, "detail", e.Detail)
}

type targetRecorderKey struct{}

// WithTargetRecorder receives resolved command identifiers before public
// response redaction. The transport bounds the identifiers it records.
func WithTargetRecorder(ctx context.Context, record func(string, string)) context.Context {
	return context.WithValue(ctx, targetRecorderKey{}, record)
}

func RecordTarget(ctx context.Context, targetType, targetID string) {
	if record, ok := ctx.Value(targetRecorderKey{}).(func(string, string)); ok {
		record(targetType, targetID)
	}
}

type commitHooksKey struct{}

// WithCommitHooks defers process-local side effects until the durable action
// and audit transaction commits. Without a transaction, hooks run immediately.
func WithCommitHooks(ctx context.Context) (context.Context, func(bool)) {
	hooks := []func(context.Context){}
	return context.WithValue(ctx, commitHooksKey{}, &hooks), func(committed bool) {
		if committed {
			for _, hook := range hooks {
				hook(ctx)
			}
		}
	}
}
func AfterCommit(ctx context.Context, hook func(context.Context)) {
	if hooks, ok := ctx.Value(commitHooksKey{}).(*[]func(context.Context)); ok {
		*hooks = append(*hooks, hook)
	} else {
		hook(ctx)
	}
}
