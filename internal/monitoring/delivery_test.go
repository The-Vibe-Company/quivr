package monitoring_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/monitoring"
)

const testSecret = "whsec_AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="

// deliveryFake is an in-memory DeliveryStore holding one Delivery.
type deliveryFake struct {
	mu        sync.Mutex
	work      []monitoring.DeliveryWork
	refuse    string
	admitted  []monitoring.AdmittedAttempt
	recorded  []monitoring.AttemptOutcome
	retries   []monitoring.Retry
	windows   []time.Duration
	destCheck []string
}

func (f *deliveryFake) ClaimDelivery(context.Context, time.Duration) (monitoring.DeliveryWork, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.work) == 0 {
		return monitoring.DeliveryWork{}, monitoring.ErrNoWork
	}
	w := f.work[0]
	f.work = f.work[1:]
	return w, nil
}

func (f *deliveryFake) Admit(_ context.Context, w monitoring.DeliveryWork, window time.Duration, configured func(org, destinationID string) bool) (monitoring.AdmittedAttempt, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.windows = append(f.windows, window)
	f.destCheck = append(f.destCheck, w.DeliveryID)
	if !configured(w.Organization, "receiver") {
		return monitoring.AdmittedAttempt{}, "destination_unavailable", nil
	}
	if f.refuse != "" {
		return monitoring.AdmittedAttempt{}, f.refuse, nil
	}
	a := monitoring.AdmittedAttempt{Organization: w.Organization, DeliveryID: w.DeliveryID, AttemptID: "attempt_" + strconv.Itoa(len(f.admitted)+1), Number: len(f.admitted) + 1,
		EventID: "event_match_1", DestinationID: "receiver", Body: []byte(`{"event_id":"event_match_1","type":"match.created"}`)}
	f.admitted = append(f.admitted, a)
	return a, "", nil
}

func (f *deliveryFake) Record(_ context.Context, _ monitoring.AdmittedAttempt, o monitoring.AttemptOutcome, r monitoring.Retry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recorded = append(f.recorded, o)
	f.retries = append(f.retries, r)
	return nil
}

type captured struct {
	header http.Header
	body   []byte
}

func receiver(t *testing.T, status int, header map[string]string) (*httptest.Server, *[]captured) {
	var mu sync.Mutex
	got := &[]captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		*got = append(*got, captured{r.Header.Clone(), b})
		mu.Unlock()
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte("receiver-private-body"))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func deliverOnce(t *testing.T, url string, fake *deliveryFake) monitoring.AttemptOutcome {
	t.Helper()
	fake.work = append(fake.work, monitoring.DeliveryWork{Organization: "org_a", DeliveryID: "delivery_1"})
	d := monitoring.Deliverer{AllowPrivateAddresses: true, Store: fake, Destinations: map[string]monitoring.Destination{"receiver": {Organization: "org_a", URL: url, Secret: testSecret}}, Timeout: 500 * time.Millisecond}
	progressed, err := d.Step(context.Background())
	if !progressed || err != nil {
		t.Fatal("step", progressed, err)
	}
	if len(fake.recorded) == 0 {
		t.Fatal("no outcome recorded")
	}
	return fake.recorded[len(fake.recorded)-1]
}

func TestDeliverySignsStoredBytesAndMapsOutcomes(t *testing.T) {
	cases := []struct {
		status    int
		outcome   string
		code      string
		retryable bool
	}{
		{204, monitoring.AttemptAcknowledged, "", false},
		{200, monitoring.AttemptAcknowledged, "", false},
		{503, monitoring.AttemptRetryableError, "webhook_http_status", true},
		{500, monitoring.AttemptRetryableError, "webhook_http_status", true},
		{408, monitoring.AttemptRetryableError, "webhook_http_status", true},
		{429, monitoring.AttemptRetryableError, "webhook_http_status", true},
		{400, monitoring.AttemptPermanentError, "webhook_http_status", false},
		{410, monitoring.AttemptPermanentError, "webhook_http_status", false},
	}
	for _, c := range cases {
		srv, got := receiver(t, c.status, nil)
		fake := &deliveryFake{}
		o := deliverOnce(t, srv.URL+"/hook?token=private", fake)
		if o.Outcome != c.outcome || o.HTTPStatus != c.status || o.ErrorCode != c.code || (c.code != "" && !strings.Contains(o.ErrorMessage, strconv.Itoa(c.status))) {
			t.Fatalf("status %d: %+v", c.status, o)
		}
		if strings.Contains(o.ErrorMessage, "private") || strings.Contains(o.ErrorMessage, "127.0.0.1") {
			t.Fatal("error leaks destination or receiver body", o.ErrorMessage)
		}
		if len(*got) != 1 {
			t.Fatal("requests", len(*got))
		}
		req := (*got)[0]
		body := string(fake.admitted[0].Body)
		ts := req.header.Get("webhook-timestamp")
		key, _ := monitoring.ParseSecret(testSecret)
		if string(req.body) != body || req.header.Get("webhook-id") != "event_match_1" || req.header.Get("content-type") != "application/json" ||
			req.header.Get("webhook-signature") != monitoring.Sign(key, "event_match_1", ts, req.body) {
			t.Fatalf("signed request: %v %s", req.header, req.body)
		}
		n, err := strconv.ParseInt(ts, 10, 64)
		if err != nil || time.Since(time.Unix(n, 0)).Abs() > time.Minute {
			t.Fatal("timestamp must be fresh unix seconds", ts)
		}
	}
}

func TestDeliveryDoesNotFollowRedirects(t *testing.T) {
	target, targetHits := receiver(t, 204, nil)
	srv, _ := receiver(t, 307, map[string]string{"Location": target.URL + "/redirected"})
	o := deliverOnce(t, srv.URL, &deliveryFake{})
	if o.Outcome != monitoring.AttemptPermanentError || o.HTTPStatus != 307 || o.ErrorCode != "webhook_redirect_refused" || len(*targetHits) != 0 {
		t.Fatalf("redirect: %+v hits=%d", o, len(*targetHits))
	}
}

func TestDeliveryNetworkFailuresAreRetryableAndBounded(t *testing.T) {
	// Connection refused: a closed listener.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	o := deliverOnce(t, "http://"+addr+"/hook", &deliveryFake{})
	if o.Outcome != monitoring.AttemptRetryableError || o.ErrorCode != "webhook_connection_failed" || o.HTTPStatus != 0 || strings.Contains(o.ErrorMessage, addr) {
		t.Fatalf("refused: %+v", o)
	}
	// Timeout: a receiver that never answers within the request timeout.
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer slow.Close()
	defer close(release)
	o = deliverOnce(t, slow.URL, &deliveryFake{})
	if o.Outcome != monitoring.AttemptRetryableError || o.ErrorCode != "webhook_timeout" {
		t.Fatalf("timeout: %+v", o)
	}
}

func TestDeliveryRefusedAdmissionMakesNoRequest(t *testing.T) {
	srv, got := receiver(t, 204, nil)
	fake := &deliveryFake{refuse: "subscription_disabled", work: []monitoring.DeliveryWork{{Organization: "org_a", DeliveryID: "delivery_1"}}}
	d := monitoring.Deliverer{AllowPrivateAddresses: true, Store: fake, Destinations: map[string]monitoring.Destination{"receiver": {Organization: "org_a", URL: srv.URL, Secret: testSecret}}}
	if progressed, err := d.Step(context.Background()); !progressed || err != nil {
		t.Fatal(progressed, err)
	}
	// A destination configured for another Organization is not usable.
	fake2 := &deliveryFake{work: []monitoring.DeliveryWork{{Organization: "org_a", DeliveryID: "delivery_1"}}}
	d2 := monitoring.Deliverer{AllowPrivateAddresses: true, Store: fake2, Destinations: map[string]monitoring.Destination{"receiver": {Organization: "org_b", URL: srv.URL, Secret: testSecret}}}
	if progressed, err := d2.Step(context.Background()); !progressed || err != nil {
		t.Fatal(progressed, err)
	}
	if len(*got) != 0 || len(fake.recorded) != 0 || len(fake2.admitted) != 0 {
		t.Fatal("refused admission must not send or record", len(*got), fake.recorded)
	}
	// No work: no progress.
	if progressed, err := d.Step(context.Background()); progressed || err != nil {
		t.Fatal("idle step", progressed, err)
	}
}

func TestDeliveryTimestampRefreshesPerAttempt(t *testing.T) {
	srv, got := receiver(t, 503, nil)
	fake := &deliveryFake{}
	now := time.Unix(1789387200, 0)
	d := monitoring.Deliverer{AllowPrivateAddresses: true, Store: fake, Destinations: map[string]monitoring.Destination{"receiver": {Organization: "org_a", URL: srv.URL, Secret: testSecret}}, Now: func() time.Time { now = now.Add(time.Second); return now }}
	for i := 0; i < 2; i++ {
		fake.work = append(fake.work, monitoring.DeliveryWork{Organization: "org_a", DeliveryID: "delivery_1"})
		if _, err := d.Step(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	a, b := (*got)[0], (*got)[1]
	if string(a.body) != string(b.body) || a.header.Get("webhook-id") != b.header.Get("webhook-id") ||
		a.header.Get("webhook-timestamp") == b.header.Get("webhook-timestamp") || a.header.Get("webhook-signature") == b.header.Get("webhook-signature") {
		t.Fatal("event id/body immutable, timestamp/signature fresh", a.header, b.header)
	}
}

func TestClassifyStatusBoundsInvalidStatuses(t *testing.T) {
	for _, status := range []int{0, 99, 600, 999} {
		o := monitoring.ClassifyStatus(status)
		if o.Outcome != monitoring.AttemptPermanentError || o.HTTPStatus != 0 || o.ErrorCode != "webhook_invalid_status" {
			t.Fatalf("status %d: %+v", status, o)
		}
	}
	srv, _ := receiver(t, 600, nil)
	if o := deliverOnce(t, srv.URL, &deliveryFake{}); o.Outcome != monitoring.AttemptPermanentError || o.HTTPStatus != 0 {
		t.Fatalf("receiver status 600: %+v", o)
	}
}

// Worker shutdown mid-request leaves the attempt without an outcome: the
// receiver may have processed it, so lease recovery records it unknown.
func TestDeliveryShutdownRecordsNoFalseOutcome(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	}))
	defer slow.Close()
	defer close(release)
	fake := &deliveryFake{work: []monitoring.DeliveryWork{{Organization: "org_a", DeliveryID: "delivery_1"}}}
	d := monitoring.Deliverer{AllowPrivateAddresses: true, Store: fake, Destinations: map[string]monitoring.Destination{"receiver": {Organization: "org_a", URL: slow.URL, Secret: testSecret}}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	if progressed, err := d.Step(ctx); !progressed || err != nil {
		t.Fatal(progressed, err)
	}
	if len(fake.admitted) != 1 || len(fake.recorded) != 0 {
		t.Fatalf("admitted %d recorded %v", len(fake.admitted), fake.recorded)
	}
}

// The group transport boundary owns bounded fanout and cancellation: one
// acknowledged sibling is recorded using a fresh context while eight other
// requests are interrupted, and remaining admitted notices are never started.
func TestDeliveryBatchBoundsSendsAndRecordsOnlyKnownOutcomes(t *testing.T) {
	fake := &batchDeliveryFake{attempts: map[string]monitoring.AdmittedAttempt{}}
	for i := 0; i < 32; i++ {
		id := strconv.Itoa(i)
		fake.work = append(fake.work, monitoring.DeliveryWork{Organization: "org_a", DeliveryID: id})
		fake.attempts[id] = monitoring.AdmittedAttempt{Organization: "org_a", DeliveryID: id, AttemptID: "attempt_" + id, Number: 1, EventID: "event_" + id, DestinationID: "receiver", Body: []byte(`{"event_id":"event_` + id + `","type":"match.created"}`)}
	}
	started := make(chan struct{}, 32)
	var active, peak atomic.Int32
	var mu sync.Mutex
	got := map[string][]byte{}
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got[r.Header.Get("webhook-id")] = body
		mu.Unlock()
		if r.Header.Get("webhook-id") == "event_0" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer receiver.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := monitoring.Deliverer{AllowPrivateAddresses: true, Store: fake, Timeout: 5 * time.Second, Destinations: map[string]monitoring.Destination{"receiver": {Organization: "org_a", URL: receiver.URL, Secret: testSecret}}}
	done := make(chan struct{})
	var progressed bool
	var stepError error
	go func() { defer close(done); progressed, stepError = d.Step(ctx) }()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for i := 0; i < 8; i++ {
		select {
		case <-started:
		case <-deadline.C:
			cancel()
			<-done
			t.Fatalf("only %d of eight send slots reached the receiver", i)
		}
	}
	select {
	case <-started:
		t.Error("more than eight requests started while receiver slots were occupied")
	default:
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("delivery group did not finish after cancellation")
	}
	if !progressed || stepError != nil || peak.Load() != 8 {
		t.Fatalf("group progress=%t err=%v peak=%d, want progress and eight bounded sends", progressed, stepError, peak.Load())
	}
	if len(fake.admitted) != 32 || len(fake.recorded) != 1 || fake.recorded[0].Outcome != monitoring.AttemptAcknowledged || len(fake.recordAttempts) != 1 || fake.recordAttempts[0].DeliveryID != "0" || fake.recordContextError != nil {
		t.Fatalf("unknown/unstarted group outcomes: admitted=%d recorded=%+v attempts=%+v record context=%v", len(fake.admitted), fake.recorded, fake.recordAttempts, fake.recordContextError)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 9 {
		t.Fatalf("started %d requests, want one acknowledged and eight interrupted", len(got))
	}
	for _, a := range fake.admitted {
		if body, sent := got[a.EventID]; sent && string(body) != string(a.Body) {
			t.Fatalf("notice %s bytes changed: got %s, want %s", a.EventID, body, a.Body)
		}
	}
}

// batchDeliveryFake supplies immutable admitted bytes; PostgreSQL owns durable
// claim, admission, outcome and lease semantics in its adapter tests.
type batchDeliveryFake struct {
	deliveryFake
	attempts           map[string]monitoring.AdmittedAttempt
	recordAttempts     []monitoring.AdmittedAttempt
	recordContextError error
}

func (f *batchDeliveryFake) ClaimDeliveries(ctx context.Context, lease time.Duration, limit int) ([]monitoring.DeliveryWork, error) {
	var works []monitoring.DeliveryWork
	for range limit {
		w, err := f.ClaimDelivery(ctx, lease)
		if err == monitoring.ErrNoWork {
			break
		}
		if err != nil {
			return nil, err
		}
		works = append(works, w)
	}
	return works, nil
}

func (f *batchDeliveryFake) AdmitDeliveries(_ context.Context, works []monitoring.DeliveryWork, _ time.Duration, _ func(string, string) bool) ([]monitoring.AdmittedAttempt, []string, []error) {
	admitted := make([]monitoring.AdmittedAttempt, len(works))
	for i, w := range works {
		admitted[i] = f.attempts[w.DeliveryID]
	}
	f.admitted = admitted
	return admitted, make([]string, len(works)), make([]error, len(works))
}

func (f *batchDeliveryFake) RecordDeliveries(ctx context.Context, attempts []monitoring.AdmittedAttempt, outcomes []monitoring.AttemptOutcome, retries []monitoring.Retry) []error {
	f.recordAttempts, f.recordContextError = attempts, ctx.Err()
	errs := make([]error, len(attempts))
	for i, a := range attempts {
		errs[i] = f.Record(ctx, a, outcomes[i], retries[i])
	}
	return errs
}

// The deliverer hands the store a policy delay for retryable failures only,
// honours a valid Retry-After on 429/503 and passes the delivery window to
// both admission and recording.
func TestDeliverySchedulesRetriesFromPolicy(t *testing.T) {
	policy := monitoring.RetryPolicy{Initial: 2 * time.Second, Max: 8 * time.Second, Window: time.Minute, Jitter: func() float64 { return 0.999 }}
	step := func(status int, header map[string]string) (monitoring.AttemptOutcome, monitoring.Retry, *deliveryFake) {
		srv, _ := receiver(t, status, header)
		fake := &deliveryFake{work: []monitoring.DeliveryWork{{Organization: "org_a", DeliveryID: "delivery_1"}}}
		d := monitoring.Deliverer{AllowPrivateAddresses: true, Store: fake, Destinations: map[string]monitoring.Destination{"receiver": {Organization: "org_a", URL: srv.URL, Secret: testSecret}}, Retry: policy, Metrics: &monitoring.DeliveryMetrics{}, Timeout: time.Second}
		if progressed, err := d.Step(context.Background()); !progressed || err != nil {
			t.Fatal(progressed, err)
		}
		return fake.recorded[0], fake.retries[0], fake
	}
	o, r, fake := step(500, nil)
	if o.Outcome != monitoring.AttemptRetryableError || r.Window != time.Minute || r.Delay < time.Second || r.Delay > 2*time.Second || fake.windows[0] != time.Minute {
		t.Fatalf("500: %+v %+v %v", o, r, fake.windows)
	}
	o, r, _ = step(503, map[string]string{"Retry-After": "20"})
	if !o.HasRetryAfter || o.RetryAfter != 20*time.Second || r.Delay != 20*time.Second {
		t.Fatalf("503 Retry-After: %+v %+v", o, r)
	}
	// Retry-After is ignored outside 429/503 and when invalid.
	if _, r, _ = step(500, map[string]string{"Retry-After": "20"}); r.Delay > 2*time.Second {
		t.Fatalf("500 must ignore Retry-After: %+v", r)
	}
	if o, r, _ = step(429, map[string]string{"Retry-After": "later"}); o.HasRetryAfter || r.Delay > 2*time.Second {
		t.Fatalf("invalid Retry-After: %+v %+v", o, r)
	}
	// Terminal outcomes carry no delay; the store ends or completes them.
	if _, r, _ = step(404, nil); r.Delay != 0 || r.Window != time.Minute {
		t.Fatalf("404: %+v", r)
	}
	if _, r, _ = step(204, nil); r.Delay != 0 {
		t.Fatalf("204: %+v", r)
	}
	// Defaults apply when no policy is configured.
	srv, _ := receiver(t, 503, nil)
	fake = &deliveryFake{work: []monitoring.DeliveryWork{{Organization: "org_a", DeliveryID: "delivery_1"}}}
	d := monitoring.Deliverer{AllowPrivateAddresses: true, Store: fake, Destinations: map[string]monitoring.Destination{"receiver": {Organization: "org_a", URL: srv.URL, Secret: testSecret}}}
	if _, err := d.Step(context.Background()); err != nil || fake.retries[0].Window != 24*time.Hour || fake.retries[0].Delay > time.Second {
		t.Fatalf("defaults: %+v %v", fake.retries, err)
	}
}

// Without the private-address allowance the worker refuses, at dial time,
// a receiver on loopback whether addressed by IP or by a name resolving to it.
func TestDeliveryRefusesPrivateDestinationsWithoutSending(t *testing.T) {
	srv, got := receiver(t, 204, nil)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	for _, target := range []string{srv.URL + "/hook", "http://localhost:" + port + "/hook"} {
		fake := &deliveryFake{work: []monitoring.DeliveryWork{{Organization: "org_a", DeliveryID: "delivery_1"}}}
		d := monitoring.Deliverer{Store: fake, Destinations: map[string]monitoring.Destination{"receiver": {Organization: "org_a", URL: target, Secret: testSecret}}, Timeout: 2 * time.Second}
		if progressed, err := d.Step(context.Background()); !progressed || err != nil {
			t.Fatal("step", progressed, err)
		}
		if len(fake.recorded) != 1 {
			t.Fatalf("%s: outcomes %+v", target, fake.recorded)
		}
		o := fake.recorded[0]
		if o.Outcome != monitoring.AttemptPermanentError || o.ErrorCode != "destination_address_refused" || o.HTTPStatus != 0 || fake.retries[0].Delay != 0 {
			t.Fatalf("%s: %+v %+v", target, o, fake.retries[0])
		}
		for _, leak := range []string{"127.0.0.1", "::1", "localhost", port} {
			if strings.Contains(o.ErrorMessage, leak) {
				t.Fatalf("error text leaks the address: %q", o.ErrorMessage)
			}
		}
	}
	if len(*got) != 0 {
		t.Fatalf("%d requests reached a private receiver", len(*got))
	}
}
