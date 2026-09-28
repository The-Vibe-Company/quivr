package m365mail

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
)

var (
	// errResync reports an expired or reset delta state.
	errResync = errors.New("delta resync required")
	// errGone reports a message that disappeared between listing and reading.
	errGone = errors.New("message gone")
)

// Retry policy: short Retry-After delays are waited in-run; longer ones end
// the run and reach the scheduler through connectors.Error.RetryAfter.
const (
	maxRetries      = 2
	maxInRunWait    = 10 * time.Second
	maxResponseSize = 32 << 20
)

// do performs an authenticated GET and returns a 200 response whose body the
// caller must close. Failures are typed connector errors carrying only codes.
func (s session) do(ctx context.Context, link string) (*http.Response, error) {
	renewed := false
	for attempt := 0; ; attempt++ {
		token, err := s.token(ctx)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
		if err != nil {
			return nil, connectors.SourceError("invalid_delta_response")
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Prefer", `odata.maxpagesize=`+strconv.Itoa(pageSize)+`, IdType="ImmutableId"`)
		resp, err := s.c.client.Do(req)
		if err != nil {
			if ctx.Err() != nil || attempt >= maxRetries {
				return nil, connectors.TransientError("source_unavailable")
			}
			if err = s.c.Sleep(ctx, backoff(attempt)); err != nil {
				return nil, connectors.TransientError("source_unavailable")
			}
			continue
		}
		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}
		code := errorCode(resp)
		wait := retryAfter(resp.Header.Get("Retry-After"))
		status := resp.StatusCode
		resp.Body.Close()
		switch {
		case status == http.StatusGone || code == "syncStateNotFound" || code == "syncStateInvalid" || code == "resyncRequired":
			return nil, errResync
		case status == http.StatusUnauthorized && !renewed:
			// An access token can be revoked or expire early: renew it once.
			s.forgetToken()
			renewed = true
			attempt--
			continue
		case status == http.StatusUnauthorized:
			return nil, connectors.AccessError("unauthorized")
		case status == http.StatusForbidden:
			return nil, connectors.AccessError("mailbox_access_denied")
		case status == http.StatusNotFound:
			return nil, notFound(code)
		case status == http.StatusTooManyRequests || status >= 500:
			errCode := "source_unavailable"
			if status == http.StatusTooManyRequests {
				errCode = "throttled"
			}
			if attempt >= maxRetries || wait > maxInRunWait {
				return nil, &connectors.Error{Class: connectors.ClassTransient, Code: errCode, RetryAfter: wait}
			}
			if wait <= 0 {
				wait = backoff(attempt)
			}
			if err = s.c.Sleep(ctx, wait); err != nil {
				return nil, connectors.TransientError(errCode)
			}
		default:
			return nil, connectors.SourceError("graph_request_rejected")
		}
	}
}

// notFound distinguishes an unknown mailbox from an unknown folder or item.
func notFound(code string) error {
	switch code {
	case "ErrorInvalidUser", "MailboxNotEnabledForRESTAPI", "MailboxNotFound", "ErrorNonExistentMailbox":
		return connectors.AccessError("mailbox_not_found")
	}
	return errGone
}

func (s session) getJSON(ctx context.Context, link string, out any) error {
	resp, err := s.do(ctx, link)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if json.NewDecoder(io.LimitReader(resp.Body, maxResponseSize)).Decode(out) != nil {
		return connectors.SourceError("invalid_delta_response")
	}
	return nil
}

func errorCode(resp *http.Response) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
	return e.Error.Code
}

// retryAfter parses delay-seconds or an HTTP date.
func retryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func backoff(attempt int) time.Duration { return time.Second << attempt }
