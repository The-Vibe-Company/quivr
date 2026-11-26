package weaviate

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
)

// Authentication sits below tracing and is retained when purge copies the client.
type authTransport struct {
	base http.RoundTripper
	key  string
}

func (t authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.key)
	return t.base.RoundTrip(clone)
}

func (t authTransport) Unwrap() http.RoundTripper {
	return t.base
}

func (t authTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (s *Store) authenticate(base http.RoundTripper, keys []string) http.RoundTripper {
	if len(keys) == 0 || keys[0] == "" {
		return base
	}
	s.apiKey = keys[0]
	return authTransport{base: base, key: s.apiKey}
}

func (s *Store) redact(text string) string {
	if s.apiKey == "" {
		return text
	}
	return strings.ReplaceAll(text, s.apiKey, "[REDACTED]")
}

// Keep timeout/cancellation classification without an unwrap chain containing a key.
func (s *Store) safeError(err error) error {
	if err == nil || s.apiKey == "" || !strings.Contains(err.Error(), s.apiKey) {
		return err
	}
	var network net.Error
	sanitized := redactedError{message: s.redact(err.Error()), canceled: errors.Is(err, context.Canceled), deadline: errors.Is(err, context.DeadlineExceeded)}
	if errors.As(err, &network) {
		sanitized.timeout = network.Timeout()
		sanitized.temporary = network.Temporary()
	}
	return sanitized
}

type redactedError struct {
	message                                string
	timeout, temporary, canceled, deadline bool
}

func (e redactedError) Error() string   { return e.message }
func (e redactedError) Timeout() bool   { return e.timeout }
func (e redactedError) Temporary() bool { return e.temporary }
func (e redactedError) Is(target error) bool {
	return (e.canceled && target == context.Canceled) || (e.deadline && target == context.DeadlineExceeded)
}
