package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

const (
	// DefaultAPI is the public X API origin.
	DefaultAPI          = "https://api.x.com"
	maxRetryAfter       = 15 * time.Minute
	defaultRetryAfter   = time.Minute
	maxResponseBytes    = 8 << 20
	problemNotFound     = "resource-not-found"
	problemUnauthorized = "not-authorized-for-resource"
)

// client performs authenticated X API requests. Response bodies and the
// token never reach an error message.
type client struct {
	base  string
	token string
	http  *http.Client
	// shortRecheck is the pin's allow_short_recheck (loopback test fakes only).
	shortRecheck bool
}

type problem struct {
	Type         string `json:"type"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	Value        string `json:"value"`
}

func (p problem) is(kind string) bool { return strings.HasSuffix(p.Type, "/"+kind) }

// errBadRequest is X's 400, which an expired pagination token also returns.
var errBadRequest = quivrplugin.SourceError("invalid_request", "X refused the request")

// answer is a non-2xx X API answer.
type answer struct {
	status int
	reset  string
}

// send performs one X API request with the bearer token and decodes a 2xx
// JSON body into out. A non-2xx answer is returned as *answer; transport and
// decoding failures as classified errors. The token and response bodies
// never reach an error message.
func (c client) send(ctx context.Context, method, path string, q url.Values, body any, out any) (*answer, error) {
	target := c.base + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, quivrplugin.SourceError("invalid_request", "the X API request cannot be built")
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, quivrplugin.SourceError("invalid_request", "the X API request cannot be built")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, quivrplugin.TransientError("source_unavailable", "X is unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &answer{status: resp.StatusCode, reset: resp.Header.Get("x-rate-limit-reset")}, nil
	}
	if out != nil && json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(out) != nil {
		return nil, quivrplugin.SourceError("invalid_response", "X returned an unreadable response")
	}
	return nil, nil
}

// get maps failures to classified connector errors with the codes the
// built-in kind used, so Connector Health reads the same.
func (c client) get(ctx context.Context, path string, q url.Values, now time.Time, out any) error {
	failed, err := c.send(ctx, http.MethodGet, path, q, nil, out)
	if err != nil || failed == nil {
		return err
	}
	switch failed.status {
	case http.StatusBadRequest:
		return errBadRequest
	case http.StatusUnauthorized:
		return quivrplugin.AccessError("unauthorized", "X refused the bearer token")
	case http.StatusPaymentRequired:
		return quivrplugin.AccessError("credits_depleted", "the X API credits are depleted")
	case http.StatusForbidden:
		return quivrplugin.AccessError("forbidden", "X refused access to the list")
	case http.StatusNotFound:
		return quivrplugin.AccessError("list_not_found", "X does not know the list")
	case http.StatusTooManyRequests:
		return quivrplugin.TransientError("rate_limited", "X rate-limited the request").WithRetryAfter(retryAfter(failed.reset, now))
	default:
		return quivrplugin.TransientError("source_unavailable", "X answered HTTP "+strconv.Itoa(failed.status))
	}
}

// retryAfter converts the x-rate-limit-reset epoch into a bounded delay.
func retryAfter(reset string, now time.Time) time.Duration {
	epoch, err := strconv.ParseInt(reset, 10, 64)
	if err != nil {
		return defaultRetryAfter
	}
	wait := time.Unix(epoch, 0).Sub(now).Round(time.Second)
	switch {
	case wait <= 0:
		return defaultRetryAfter
	case wait > maxRetryAfter:
		return maxRetryAfter
	}
	return wait
}
