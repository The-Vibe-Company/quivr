package monitoring

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Attempt outcomes. An attempt without an outcome fact reads as in_flight.
const (
	AttemptInFlight       = "in_flight"
	AttemptAcknowledged   = "acknowledged"
	AttemptRetryableError = "retryable_error"
	AttemptPermanentError = "permanent_error"
	AttemptUnknown        = "unknown"
)

// DeliveryWork is one leased unit of durable delivery work.
type DeliveryWork struct {
	Organization string
	DeliveryID   string
	// Lease fences the claim: admission is refused once another claim or a
	// recorded outcome replaced it.
	Lease time.Time
}

// AdmittedAttempt is an attempt whose admission fact is already committed. It
// carries the exact immutable notice bytes to send.
type AdmittedAttempt struct {
	Organization  string
	DeliveryID    string
	AttemptID     string
	Number        int
	EventID       string
	DestinationID string
	Body          []byte
}

// AttemptOutcome is the append-only result of one transport attempt. Its
// error text is bounded and never contains the destination URL or receiver body.
type AttemptOutcome struct {
	Outcome      string
	HTTPStatus   int
	ErrorCode    string
	ErrorMessage string
}

// Attempt is the read view of one attempt's admission and outcome facts.
type Attempt struct {
	ID           string
	DeliveryID   string
	Number       int
	Outcome      string
	HTTPStatus   int
	ErrorCode    string
	ErrorMessage string
}

// DeliveryStore owns durable delivery work, admission and outcome facts.
type DeliveryStore interface {
	// ClaimDelivery leases one due unit of delivery work or returns ErrNoWork.
	ClaimDelivery(ctx context.Context, lease time.Duration) (DeliveryWork, error)
	// Admit rechecks canonical admission and, when allowed, commits the attempt
	// fact before any I/O. A refusal returns its reason and parks the work
	// without an attempt. configured reports whether the Delivery's destination
	// is usable for its Organization.
	Admit(ctx context.Context, w DeliveryWork, configured func(org, destinationID string) bool) (AdmittedAttempt, string, error)
	// Record appends the attempt outcome and updates the logical Delivery.
	Record(ctx context.Context, a AdmittedAttempt, o AttemptOutcome) error
}

// Deliverer sends admitted notices to deployment-configured destinations.
// Durable state lives in the store; a crash loses at most a lease.
type Deliverer struct {
	Store        DeliveryStore
	Destinations map[string]Destination
	Workers      int
	Lease        time.Duration
	// Timeout bounds one HTTP attempt (default 10 s).
	Timeout time.Duration
	// Poll is the initial idle poll interval, doubled up to MaxPoll while idle.
	Poll, MaxPoll time.Duration
	Now           func() time.Time
	client        *http.Client
}

const maxReceiverBody = 64 << 10

// NewWebhookClient returns the delivery HTTP client: bounded time, no
// redirects, no cookies.
func NewWebhookClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxResponseHeaderBytes = 16 << 10
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (d Deliverer) timeout() time.Duration {
	if d.Timeout <= 0 {
		return 10 * time.Second
	}
	return d.Timeout
}

func (d Deliverer) lease() time.Duration {
	if d.Lease <= 0 {
		return time.Minute
	}
	return d.Lease
}

func (d Deliverer) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Run delivers until ctx ends. Idle workers back off from Poll to MaxPoll and
// reset on work, keeping database load low.
func (d Deliverer) Run(ctx context.Context) {
	d.client = NewWebhookClient(d.timeout())
	poll, maxPoll := d.Poll, d.MaxPoll
	if poll <= 0 {
		poll = 200 * time.Millisecond
	}
	if maxPoll < poll {
		maxPoll = 2 * time.Second
	}
	var wg sync.WaitGroup
	for i := 0; i < max(d.Workers, 1); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wait := poll
			for ctx.Err() == nil {
				step, cancel := context.WithTimeout(ctx, d.lease())
				progressed, err := d.Step(step)
				cancel()
				if err != nil && ctx.Err() == nil {
					slog.Warn("delivery worker unavailable", "error", boundedError(err))
				}
				if progressed && err == nil {
					wait = poll
					continue
				}
				select {
				case <-ctx.Done():
				case <-time.After(wait):
				}
				wait = min(wait*2, maxPoll)
			}
		}()
	}
	wg.Wait()
}

// Step claims, admits, sends and records one Delivery attempt. It returns
// false when no work was due.
func (d Deliverer) Step(ctx context.Context) (bool, error) {
	w, err := d.Store.ClaimDelivery(ctx, d.lease())
	if errors.Is(err, ErrNoWork) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	a, refused, err := d.Store.Admit(ctx, w, d.configured)
	if err != nil {
		return true, err
	}
	if refused != "" {
		slog.Info("delivery not admitted", "organization", w.Organization, "delivery_id", w.DeliveryID, "reason", refused)
		return true, nil
	}
	outcome, known := d.send(ctx, a)
	if !known {
		// Shutdown interrupted the request after admission: the receiver may
		// have processed it. Leave the attempt without an outcome; lease
		// recovery records it unknown and admits the notice again.
		slog.Warn("delivery attempt interrupted", "organization", a.Organization, "delivery_id", a.DeliveryID, "attempt", a.Number)
		return true, nil
	}
	// The outcome is recorded even when the step context is ending.
	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err = d.Store.Record(record, a, outcome); err != nil {
		return true, err
	}
	slog.Info("delivery attempt", "organization", a.Organization, "delivery_id", a.DeliveryID, "attempt", a.Number, "outcome", outcome.Outcome, "http_status", outcome.HTTPStatus, "error_code", outcome.ErrorCode)
	return true, nil
}

func (d Deliverer) configured(org, destinationID string) bool {
	dest, ok := d.Destinations[destinationID]
	if !ok || dest.Organization != org {
		return false
	}
	_, err := ParseSecret(dest.Secret)
	return err == nil
}

// send performs one signed POST of the stored notice bytes. It never follows
// redirects, discards a bounded prefix of the receiver body and returns only
// fixed, URL-free error text. known is false when shutdown interrupted the
// request, whose effect on the receiver is then unknown.
func (d Deliverer) send(ctx context.Context, a AdmittedAttempt) (AttemptOutcome, bool) {
	o, err := d.attempt(ctx, a)
	if err != nil && errors.Is(ctx.Err(), context.Canceled) {
		return AttemptOutcome{}, false
	}
	return o, true
}

func (d Deliverer) attempt(ctx context.Context, a AdmittedAttempt) (AttemptOutcome, error) {
	dest := d.Destinations[a.DestinationID]
	key, err := ParseSecret(dest.Secret)
	if err != nil {
		return AttemptOutcome{Outcome: AttemptPermanentError, ErrorCode: "destination_unavailable", ErrorMessage: "destination signing secret unavailable"}, nil
	}
	client := d.client
	if client == nil {
		client = NewWebhookClient(d.timeout())
	}
	ctx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dest.URL, bytes.NewReader(a.Body))
	if err != nil {
		return AttemptOutcome{Outcome: AttemptPermanentError, ErrorCode: "destination_unavailable", ErrorMessage: "destination URL unusable"}, nil
	}
	timestamp := strconv.FormatInt(d.now().Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "quivr-webhook/0")
	req.Header.Set("webhook-id", a.EventID)
	req.Header.Set("webhook-timestamp", timestamp)
	req.Header.Set("webhook-signature", Sign(key, a.EventID, timestamp, a.Body))
	resp, err := client.Do(req)
	if err != nil {
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			return AttemptOutcome{Outcome: AttemptRetryableError, ErrorCode: "webhook_timeout", ErrorMessage: "receiver did not respond before the request timeout"}, err
		}
		return AttemptOutcome{Outcome: AttemptRetryableError, ErrorCode: "webhook_connection_failed", ErrorMessage: "connection to receiver failed"}, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxReceiverBody))
	_ = resp.Body.Close()
	return ClassifyStatus(resp.StatusCode), nil
}

// ClassifyStatus maps a receiver HTTP status to an attempt outcome: 2xx
// acknowledges; 408, 429 and 5xx are retryable; everything else, including
// an unfollowed redirect or a status outside 100-599, ends automatic attempts.
func ClassifyStatus(status int) AttemptOutcome {
	if status < 100 || status > 599 {
		return AttemptOutcome{Outcome: AttemptPermanentError, ErrorCode: "webhook_invalid_status", ErrorMessage: "receiver returned an invalid HTTP status"}
	}
	o := AttemptOutcome{HTTPStatus: status}
	switch {
	case status >= 200 && status < 300:
		o.Outcome = AttemptAcknowledged
		return o
	case status >= 300 && status < 400:
		o.Outcome, o.ErrorCode, o.ErrorMessage = AttemptPermanentError, "webhook_redirect_refused", fmt.Sprintf("receiver returned HTTP %d; redirects are not followed", status)
		return o
	case status == 408 || status == 429 || status >= 500:
		o.Outcome = AttemptRetryableError
	default:
		o.Outcome = AttemptPermanentError
	}
	o.ErrorCode, o.ErrorMessage = "webhook_http_status", fmt.Sprintf("receiver returned HTTP %d", status)
	return o
}
