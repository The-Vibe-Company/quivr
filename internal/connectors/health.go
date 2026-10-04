package connectors

import "time"

// Connector Health states, in precedence order.
const (
	HealthDisabled           = "disabled"
	HealthAccessError        = "access_error"
	HealthCredentialExpiring = "credential_expiring"
	HealthSilent             = "silent"
	HealthActive             = "active"
)

// Default health policy.
const (
	DefaultSilentAfter       = 24 * time.Hour
	DefaultCredentialWarning = 14 * 24 * time.Hour
)

// RunError is the last acquisition failure recorded on an instance.
type RunError struct {
	Class ErrorClass
	Code  string
	At    time.Time
	// Completed marks a run that reached the source and finished its pages
	// but had to report a problem (e.g. a rejected item): it still counts as
	// a successful poll.
	Completed bool
	// Skipped marks a run that did not poll the source (ErrNotDue): it only
	// schedules the next run, recording neither a success nor an error.
	Skipped bool
	// RetryAfter defers the next run beyond the interval (source rate limit).
	RetryAfter time.Duration
}

// HealthInput is everything Connector Health depends on.
type HealthInput struct {
	Enabled       bool
	CreatedAt     time.Time
	LastSuccessAt *time.Time
	LastItemAt    *time.Time
	LastError     *RunError
	// AccessErrorAt is set by a run refused access and cleared only by a
	// later successful run; other failures leave it unchanged.
	AccessErrorAt       *time.Time
	CredentialExpiresAt *time.Time
	SilentAfter         time.Duration
	CredentialWarning   time.Duration
	// PushAccessRefused: the push channel is refused access (the source
	// invalidated the webhook, say) while pull carries the collection.
	PushAccessRefused bool
}

// Evaluate derives Connector Health at now. An access failure is reported as
// access_error until a later successful run, so a source that refuses access
// is never mistaken for one that simply stopped publishing; so is a push
// channel refused access, until it recovers. Transient and source failures
// are visible only as the last error.
func Evaluate(h HealthInput, now time.Time) string {
	if !h.Enabled {
		return HealthDisabled
	}
	if h.AccessErrorAt != nil || h.PushAccessRefused {
		return HealthAccessError
	}
	if h.CredentialExpiresAt != nil && !h.CredentialExpiresAt.After(now.Add(h.CredentialWarning)) {
		return HealthCredentialExpiring
	}
	since := h.CreatedAt
	if h.LastItemAt != nil && h.LastItemAt.After(since) {
		since = *h.LastItemAt
	}
	if now.Sub(since) > h.SilentAfter {
		return HealthSilent
	}
	return HealthActive
}
