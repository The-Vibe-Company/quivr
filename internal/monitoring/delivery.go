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

	"github.com/The-Vibe-Company/quivr/internal/netguard"
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
	// RetryAfter is a valid Retry-After the receiver sent (never persisted).
	RetryAfter    time.Duration
	HasRetryAfter bool
}

// Retry tells the store how to schedule a failed Delivery: Delay is the
// policy wait before the next attempt; Window bounds every wait and, once
// elapsed since the Delivery's creation, ends it exhausted.
type Retry struct {
	Delay, Window time.Duration
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
	// is usable for its Organization. Pending work claimed after its delivery
	// window ends exhausted (reason window_elapsed) without an attempt: the
	// window is never extended by disabling and re-enabling.
	Admit(ctx context.Context, w DeliveryWork, window time.Duration, configured func(org, destinationID string) bool) (AdmittedAttempt, string, error)
	// Record appends the attempt outcome and updates the logical Delivery:
	// delivered on acknowledgement, exhausted on a permanent failure or an
	// elapsed window, otherwise pending until the window-bounded retry time.
	Record(ctx context.Context, a AdmittedAttempt, o AttemptOutcome, r Retry) error
}

// DeliveryBatchStore optionally groups same-Organization delivery facts.
// Admission and recording results follow input order; each error leaves that
// item's work recoverable without preventing successful siblings' progress.
type DeliveryBatchStore interface {
	ClaimDeliveries(ctx context.Context, lease time.Duration, limit int) ([]DeliveryWork, error)
	AdmitDeliveries(ctx context.Context, works []DeliveryWork, window time.Duration, configured func(org, destinationID string) bool) ([]AdmittedAttempt, []string, []error)
	RecordDeliveries(ctx context.Context, attempts []AdmittedAttempt, outcomes []AttemptOutcome, retries []Retry) []error
}

const deliveryBatchLimit = 32
const deliveryBatchConcurrency = 8

// Deliverer sends admitted notices to deployment-configured destinations.
// Durable state lives in the store; a crash loses at most a lease.
type Deliverer struct {
	Store        DeliveryStore
	Destinations map[string]Destination
	Workers      int
	Lease        time.Duration
	// Timeout bounds one HTTP attempt (default 10 s).
	Timeout time.Duration
	// Retry schedules failed attempts (accepted defaults when zero).
	Retry RetryPolicy
	// Metrics counts attempt outcomes when set.
	Metrics *DeliveryMetrics
	// Poll is the initial idle poll interval, doubled up to MaxPoll while idle.
	Poll, MaxPoll time.Duration
	Now           func() time.Time
	// AllowPrivateAddresses lifts the dial-time refusal of loopback, private,
	// link-local and other non-public receivers. Use only for trusted internal receivers; increases SSRF exposure.
	AllowPrivateAddresses bool
	client                *http.Client
}

const maxReceiverBody = 64 << 10

// NewWebhookClient returns the delivery HTTP client: bounded time, no
// redirects, no cookies, no proxy. Unless allowPrivate is set, every
// connection is refused when the address actually dialed (after DNS
// resolution) is not public, so a destination cannot resolve or rebind to an
// internal address.
func NewWebhookClient(timeout time.Duration, allowPrivate bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	if !allowPrivate {
		dialer.Control = netguard.Control
	}
	// A proxy would be the dialed address, hiding the receiver from the guard.
	transport.Proxy, transport.DialContext = nil, dialer.DialContext
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
	d.client = NewWebhookClient(d.timeout(), d.AllowPrivateAddresses)
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
	if store, ok := d.Store.(DeliveryBatchStore); ok {
		return d.stepBatch(ctx, store)
	}
	w, err := d.Store.ClaimDelivery(ctx, d.lease())
	if errors.Is(err, ErrNoWork) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	policy := d.Retry.WithDefaults()
	a, refused, err := d.Store.Admit(ctx, w, policy.Window, d.configured)
	if err != nil {
		return true, err
	}
	if refused != "" {
		slog.Info("delivery not admitted", "organization", w.Organization, "delivery_id", w.DeliveryID, "reason", refused)
		return true, nil
	}
	sent := time.Now()
	outcome, known := d.send(ctx, a)
	elapsed := time.Since(sent)
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
	retry := Retry{Window: policy.Window}
	if outcome.Outcome == AttemptRetryableError {
		retry.Delay = policy.Delay(a.Number, outcome)
	}
	if err = d.Store.Record(record, a, outcome, retry); err != nil {
		return true, err
	}
	d.observeAttempt(a, outcome, retry, elapsed)
	return true, nil
}

func (d Deliverer) stepBatch(ctx context.Context, store DeliveryBatchStore) (bool, error) {
	works, err := store.ClaimDeliveries(ctx, d.lease(), deliveryBatchLimit)
	if errors.Is(err, ErrNoWork) || (err == nil && len(works) == 0) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	policy := d.Retry.WithDefaults()
	admitted, refused, admissionErrors := store.AdmitDeliveries(ctx, works, policy.Window, d.configured)
	if len(admitted) != len(works) || len(refused) != len(works) || len(admissionErrors) != len(works) {
		return true, errors.New("delivery batch admission results differ from inputs")
	}
	var attempts []AdmittedAttempt
	var errs []error
	for i, w := range works {
		if admissionErrors[i] != nil {
			errs = append(errs, admissionErrors[i])
		} else if refused[i] != "" {
			slog.Info("delivery not admitted", "organization", w.Organization, "delivery_id", w.DeliveryID, "reason", refused[i])
		} else {
			attempts = append(attempts, admitted[i])
		}
	}
	if d.client == nil {
		d.client = NewWebhookClient(d.timeout(), d.AllowPrivateAddresses)
	}
	outcomes, elapsed, known := make([]AttemptOutcome, len(attempts)), make([]time.Duration, len(attempts)), make([]bool, len(attempts))
	pending := make(chan int, len(attempts))
	for i := range attempts {
		pending <- i
	}
	close(pending)
	var wg sync.WaitGroup
	for range deliveryBatchConcurrency {
		wg.Go(func() {
			for i := range pending {
				if ctx.Err() != nil {
					return
				}
				start := time.Now()
				outcomes[i], known[i] = d.send(ctx, attempts[i])
				elapsed[i] = time.Since(start)
			}
		})
	}
	wg.Wait()
	var recordAttempts []AdmittedAttempt
	var recordOutcomes []AttemptOutcome
	var retries []Retry
	var durations []time.Duration
	for i, a := range attempts {
		if !known[i] {
			// Interrupted and not-yet-started admitted attempts have no known
			// receiver outcome. Their leases retain the existing recovery path.
			slog.Warn("delivery attempt interrupted", "organization", a.Organization, "delivery_id", a.DeliveryID, "attempt", a.Number)
			continue
		}
		retry := Retry{Window: policy.Window}
		if outcomes[i].Outcome == AttemptRetryableError {
			retry.Delay = policy.Delay(a.Number, outcomes[i])
		}
		recordAttempts, recordOutcomes = append(recordAttempts, a), append(recordOutcomes, outcomes[i])
		retries, durations = append(retries, retry), append(durations, elapsed[i])
	}
	if len(recordAttempts) > 0 {
		record, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		recorded := store.RecordDeliveries(record, recordAttempts, recordOutcomes, retries)
		cancel()
		if len(recorded) != len(recordAttempts) {
			return true, errors.Join(append(errs, errors.New("delivery batch recording results differ from inputs"))...)
		}
		for i, err := range recorded {
			if err != nil {
				errs = append(errs, err)
			} else {
				d.observeAttempt(recordAttempts[i], recordOutcomes[i], retries[i], durations[i])
			}
		}
	}
	return true, errors.Join(errs...)
}

func (d Deliverer) observeAttempt(a AdmittedAttempt, outcome AttemptOutcome, retry Retry, elapsed time.Duration) {
	d.Metrics.Observe(outcome.Outcome)
	d.Metrics.ObserveDuration(elapsed)
	slog.Info("delivery attempt", "organization", a.Organization, "delivery_id", a.DeliveryID, "attempt", a.Number, "outcome", outcome.Outcome, "http_status", outcome.HTTPStatus, "error_code", outcome.ErrorCode, "retry_delay_ms", retry.Delay.Milliseconds(), "duration_ms", elapsed.Milliseconds())
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
		client = NewWebhookClient(d.timeout(), d.AllowPrivateAddresses)
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
		var refused *netguard.RefusedError
		if errors.As(err, &refused) {
			// The refused IP is for operators only; the recorded outcome stays address-free.
			slog.Warn("delivery destination address refused", "organization", a.Organization, "delivery_id", a.DeliveryID, "destination_id", a.DestinationID, "address", refused.Address)
			return AttemptOutcome{Outcome: AttemptPermanentError, ErrorCode: "destination_address_refused", ErrorMessage: "destination address is not allowed"}, nil
		}
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			return AttemptOutcome{Outcome: AttemptRetryableError, ErrorCode: "webhook_timeout", ErrorMessage: "receiver did not respond before the request timeout"}, err
		}
		return AttemptOutcome{Outcome: AttemptRetryableError, ErrorCode: "webhook_connection_failed", ErrorMessage: "connection to receiver failed"}, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxReceiverBody))
	_ = resp.Body.Close()
	o := ClassifyStatus(resp.StatusCode)
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
		o.RetryAfter, o.HasRetryAfter = ParseRetryAfter(resp.Header.Get("Retry-After"), d.now())
	}
	return o, nil
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
