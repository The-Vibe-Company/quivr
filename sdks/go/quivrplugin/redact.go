package quivrplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// Redacted is what a Credential prints as.
const Redacted = "[redacted]"

// minSecretLength is the shortest credential string the SDK scrubs from
// messages: shorter values ("true", "en") would mangle ordinary text.
const minSecretLength = 4

// Credential is the decrypted Deposited Credential the core sends for one
// invocation. It never prints: fmt verbs, JSON encoding and slog all show
// [redacted]. Decode it into your own struct to use it, and do not log that
// struct.
type Credential struct {
	raw     json.RawMessage
	secrets []string
}

func newCredential(raw json.RawMessage) Credential {
	c := Credential{raw: raw}
	var v any
	if json.Unmarshal(raw, &v) == nil {
		set := map[string]bool{}
		collectSecrets(v, set)
		for s := range set {
			c.secrets = append(c.secrets, s)
		}
		// Longest first, so a secret containing another is scrubbed whole.
		sort.Slice(c.secrets, func(i, j int) bool { return len(c.secrets[i]) > len(c.secrets[j]) })
	}
	return c
}

// IsNull reports whether the kind takes no credential (the core sent null).
func (c Credential) IsNull() bool {
	return len(c.raw) == 0 || string(c.raw) == "null"
}

// Decode unmarshals the credential JSON object into v.
func (c Credential) Decode(v any) error {
	if c.IsNull() {
		return fmt.Errorf("no credential")
	}
	return json.Unmarshal(c.raw, v)
}

// Redact replaces every credential value in s with [redacted].
func (c Credential) Redact(s string) string {
	for _, secret := range c.secrets {
		s = strings.ReplaceAll(s, secret, Redacted)
	}
	return s
}

func (c Credential) String() string                  { return Redacted }
func (c Credential) GoString() string                { return Redacted }
func (c Credential) Format(f fmt.State, _ rune)      { _, _ = f.Write([]byte(Redacted)) }
func (c Credential) MarshalJSON() ([]byte, error)    { return json.Marshal(Redacted) }
func (c Credential) LogValue() slog.Value            { return slog.StringValue(Redacted) }
func (c Credential) MarshalText() ([]byte, error)    { return []byte(Redacted), nil }
func (c *Credential) UnmarshalJSON(raw []byte) error { *c = newCredential(raw); return nil }

func collectSecrets(v any, into map[string]bool) {
	switch v := v.(type) {
	case string:
		if len(v) >= minSecretLength {
			into[v] = true
			if b, err := json.Marshal(v); err == nil {
				// The value as it appears inside JSON text.
				if escaped := strings.Trim(string(b), `"`); escaped != v {
					into[escaped] = true
				}
			}
		}
	case map[string]any:
		for _, e := range v {
			collectSecrets(e, into)
		}
	case []any:
		for _, e := range v {
			collectSecrets(e, into)
		}
	}
}

// redactingHandler scrubs the credential's values from a record's message
// and attributes before the wrapped handler sees them.
type redactingHandler struct {
	next       slog.Handler
	credential Credential
}

func (h redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, h.credential.Redact(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.attr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h redactingHandler) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.credential.Redact(v.String()))
	case slog.KindGroup:
		attrs := v.Group()
		out := make([]any, len(attrs))
		for i, g := range attrs {
			out[i] = h.attr(g)
		}
		return slog.Group(a.Key, out...)
	case slog.KindAny:
		return slog.String(a.Key, h.credential.Redact(fmt.Sprint(v.Any())))
	}
	return slog.Attr{Key: a.Key, Value: v}
}

func (h redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	scrubbed := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		scrubbed[i] = h.attr(a)
	}
	return redactingHandler{next: h.next.WithAttrs(scrubbed), credential: h.credential}
}

func (h redactingHandler) WithGroup(name string) slog.Handler {
	return redactingHandler{next: h.next.WithGroup(name), credential: h.credential}
}
