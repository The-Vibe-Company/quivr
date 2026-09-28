package connectors

import (
	"testing"
	"time"
)

func TestHealthPrecedence(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	base := HealthInput{Enabled: true, CreatedAt: now.Add(-time.Hour), SilentAfter: 24 * time.Hour, CredentialWarning: 14 * 24 * time.Hour}
	cases := []struct {
		name string
		edit func(*HealthInput)
		want string
	}{
		{"fresh instance is active", func(*HealthInput) {}, HealthActive},
		{"recent item keeps a quiet-but-young source active", func(h *HealthInput) { h.CreatedAt = now.Add(-48 * time.Hour); h.LastItemAt = at(-time.Hour) }, HealthActive},
		{"no item past the threshold is silent", func(h *HealthInput) { h.CreatedAt = now.Add(-48 * time.Hour); h.LastItemAt = at(-25 * time.Hour) }, HealthSilent},
		{"never produced past the threshold is silent", func(h *HealthInput) { h.CreatedAt = now.Add(-25 * time.Hour) }, HealthSilent},
		{"credential expiring within the warning", func(h *HealthInput) { h.CredentialExpiresAt = at(24 * time.Hour) }, HealthCredentialExpiring},
		{"credential beyond the warning", func(h *HealthInput) { h.CredentialExpiresAt = at(30 * 24 * time.Hour) }, HealthActive},
		{"expiring beats silent", func(h *HealthInput) { h.CreatedAt = now.Add(-48 * time.Hour); h.CredentialExpiresAt = at(time.Hour) }, HealthCredentialExpiring},
		{"access error beats expiring and silent", func(h *HealthInput) {
			h.CreatedAt = now.Add(-48 * time.Hour)
			h.CredentialExpiresAt = at(time.Hour)
			h.AccessErrorAt = at(-time.Minute)
		}, HealthAccessError},
		{"a later transient failure does not clear an access error", func(h *HealthInput) {
			h.AccessErrorAt = at(-time.Minute)
			h.LastError = &RunError{Class: ClassTransient, Code: "source_unavailable", At: now}
		}, HealthAccessError},
		{"transient error is not a state", func(h *HealthInput) {
			h.LastError = &RunError{Class: ClassTransient, Code: "source_unavailable", At: now.Add(-time.Minute)}
		}, HealthActive},
		{"disabled beats everything", func(h *HealthInput) {
			h.Enabled = false
			h.AccessErrorAt = at(0)
		}, HealthDisabled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := base
			c.edit(&in)
			if got := Evaluate(in, now); got != c.want {
				t.Fatalf("got %s want %s", got, c.want)
			}
		})
	}
}
