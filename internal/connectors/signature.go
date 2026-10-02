package connectors

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Signature declares provider-owned cryptographic verification and engine
// freshness/replay protection. TimestampHeader, when set, must be signed by
// the provider. GET challenges do not use the POST signature guard.
type Signature struct {
	Header          string `json:"header"`
	TimestampHeader string `json:"timestamp_header,omitempty"`
	WindowSeconds   int    `json:"window_seconds"`
}

var signatureHeaderName = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,128}$")

func validateSignature(route APIRoute) error {
	s := route.Signature
	if route.Auth != "signature" {
		if s != nil {
			return fmt.Errorf("route %q signature metadata requires signature auth", route.Name)
		}
		return nil
	}
	if s == nil || !signatureHeaderName.MatchString(s.Header) || s.WindowSeconds < 1 || s.WindowSeconds > 86400 {
		return fmt.Errorf("route %q requires a signature header and window_seconds between 1 and 86400", route.Name)
	}
	for _, h := range []string{s.Header, s.TimestampHeader} {
		if h != "" && (!signatureHeaderName.MatchString(h) || strings.EqualFold(h, "authorization") || strings.EqualFold(h, "cookie") || strings.EqualFold(h, "idempotency-key") || signatureHopHeader(h)) {
			return fmt.Errorf("route %q signature headers must be forwarded provider headers", route.Name)
		}
	}
	if s.TimestampHeader != "" && strings.EqualFold(s.Header, s.TimestampHeader) {
		return fmt.Errorf("route %q signature and timestamp headers must differ", route.Name)
	}
	return nil
}
func signatureHopHeader(h string) bool {
	switch strings.ToLower(h) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	}
	return false
}

// ReplayStore reserves all fingerprint keys atomically under one instance.
// A duplicate reserves nothing. Expiry is exclusive; release only removes
// entries still owned by the reservation token. Implementations must share
// reservations across replicas and persist them across engine restarts.
type ReplayStore interface {
	ReserveReplay(ctx context.Context, org, id, token string, keys []string, now, expires time.Time) (bool, error)
	ReleaseReplay(ctx context.Context, org, id, token string) error
}

var (
	ErrAPIKeyRequired    = errors.New("connector API key required")
	ErrInvalidSignature  = errors.New("invalid connector signature or timestamp")
	ErrReplay            = errors.New("replayed connector push")
	ErrReplayUnavailable = errors.New("connector replay protection unavailable")
)

// deliverRoute keeps route validation/auth ahead of credential access and
// provider calls for both the declared address and the legacy alias.
func (r Relay) deliverRoute(ctx context.Context, target Target, connector Connector, receiver Receiver, route *apiRoute, path string, req Relayed) (RelayAnswer, error) {
	deliver := func() (RelayAnswer, error) {
		body := json.RawMessage(req.Body)
		if len(body) == 0 && req.Method == "GET" {
			body = json.RawMessage(`null`)
		}
		if !json.Valid(body) {
			return RelayAnswer{}, ErrInvalidAPIBody
		}
		if route.schema != nil {
			if err := validateJSON(route.schema, body); err != nil {
				return RelayAnswer{}, ErrInvalidAPIRequest
			}
		}
		delete(req.Headers, "authorization")
		req.Path = path
		answer, err := r.deliver(ctx, target, connector, receiver, req, route.Name, body)
		answer.DeclaredAPI = true
		return answer, err
	}
	if route.Auth != "signature" || req.Method == "GET" {
		return deliver()
	}
	sig, ok := singleHeader(req.Headers, route.Signature.Header)
	if !ok {
		return RelayAnswer{}, ErrInvalidSignature
	}
	now := time.Now()
	window := time.Duration(route.Signature.WindowSeconds) * time.Second
	if route.Signature.TimestampHeader != "" {
		raw, ok := singleHeader(req.Headers, route.Signature.TimestampHeader)
		stamp, err := strconv.ParseInt(raw, 10, 64)
		// Compare integral Unix seconds without subtraction overflow.
		if !ok || err != nil || stamp < now.Unix()-int64(route.Signature.WindowSeconds) || stamp > now.Unix()+int64(route.Signature.WindowSeconds) {
			return RelayAnswer{}, ErrInvalidSignature
		}
	}
	keys := []string{replayFingerprint("signature", sig)}
	if values := req.Headers["idempotency-key"]; len(values) > 0 {
		key, ok := singleHeader(req.Headers, "idempotency-key")
		if !ok {
			return RelayAnswer{}, ErrInvalidSignature
		}
		keys = append(keys, replayFingerprint("idempotency", key))
	}
	if r.Replays == nil {
		return RelayAnswer{}, ErrReplayUnavailable
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return RelayAnswer{}, ErrReplayUnavailable
	}
	token := hex.EncodeToString(random[:])
	reserved, err := r.Replays.ReserveReplay(ctx, target.Organization, target.ID, token, keys, now, now.Add(window))
	if err != nil {
		return RelayAnswer{}, ErrReplayUnavailable
	}
	if !reserved {
		return RelayAnswer{}, ErrReplay
	}
	answer, err := deliver()
	if err != nil || answer.Receipts == nil {
		// Client cancellation may detach cleanup, but the original delivery
		// deadline still bounds it. Unreleased reservations expire safely.
		cleanup := context.WithoutCancel(ctx)
		if deadline, ok := ctx.Deadline(); ok {
			if !deadline.After(time.Now()) {
				return RelayAnswer{}, ErrReplayUnavailable
			}
			var cancel context.CancelFunc
			cleanup, cancel = context.WithDeadline(cleanup, deadline)
			defer cancel()
		}
		cleanup, cancel := context.WithTimeout(cleanup, 5*time.Second)
		defer cancel()
		if releaseErr := r.Replays.ReleaseReplay(cleanup, target.Organization, target.ID, token); releaseErr != nil {
			return RelayAnswer{}, ErrReplayUnavailable
		}
	}
	return answer, err
}
func singleHeader(headers map[string][]string, name string) (string, bool) {
	values := headers[strings.ToLower(name)]
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return "", false
	}
	return values[0], true
}
func replayFingerprint(kind, value string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + value))
	return hex.EncodeToString(sum[:])
}
