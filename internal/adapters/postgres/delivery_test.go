package postgres_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

const testWindow = time.Hour

// TestDeliveryAdmissionAndAppendOnlyAttempts proves delivery admission and
// outcome facts against the real journal: leased claims, admission refusals
// that park work without an attempt, the attempt fact committed before I/O,
// acknowledged/failed outcomes, crash recovery as an unknown outcome, feed-only
// delivery.updated events and append-only attempt history.
func TestDeliveryAdmissionAndAppendOnlyAttempts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := newDeliveryFixture(t, ctx, adapterPool(t, ctx), "adapter-delivery-", 5)
	org, store, ds, deliveries := f.org, f.store, f.ds, f.deliveries
	configured := f.configured
	retry := monitoring.Retry{Delay: 30 * time.Second, Window: testWindow}

	// Claims lease each Delivery once.
	claimed := map[string]bool{}
	for {
		w, err := ds.ClaimDelivery(ctx, time.Minute)
		if errors.Is(err, monitoring.ErrNoWork) {
			break
		}
		if err != nil || w.Organization != org || claimed[w.DeliveryID] {
			t.Fatal("claim", w, err)
		}
		claimed[w.DeliveryID] = true
	}
	if len(claimed) != len(deliveries) {
		t.Fatalf("claimed %d of %d", len(claimed), len(deliveries))
	}

	// An unconfigured destination parks the work without an attempt.
	d0 := deliveries[0]
	if _, refused := f.admit(d0, testWindow, func(string, string) bool { return false }); refused != "destination_unavailable" || f.attempts(d0) != 0 || !f.parked(d0) || f.read(d0).State != "pending" {
		t.Fatal("unconfigured destination", refused)
	}
	// Admission commits the attempt fact and delivering state before I/O.
	first, refused := f.admit(d0, testWindow, configured)
	notice := f.read(d0)
	if refused != "" || first.Number != 1 || first.EventID == "" || string(first.Body) != string(notice.Event) || first.DestinationID != "dest" ||
		notice.State != "delivering" || notice.AttemptCount != 1 || f.attempts(d0) != 1 || f.updates(d0) != 1 || notice.NextAttemptAt != nil {
		t.Fatalf("admission %+v %+v", first, notice)
	}
	page, err := store.Attempts(ctx, org, d0, 0, 10)
	if err != nil || len(page) != 1 || page[0].Outcome != monitoring.AttemptInFlight || page[0].ID != first.AttemptID || page[0].Number != 1 {
		t.Fatal("in-flight attempt", page, err)
	}
	// 2xx acknowledges: delivered, terminal admission, work removed. Recording twice is a no-op.
	for i := 0; i < 2; i++ {
		if err = ds.Record(ctx, first, monitoring.AttemptOutcome{Outcome: monitoring.AttemptAcknowledged, HTTPStatus: 204}, monitoring.Retry{Window: testWindow}); err != nil {
			t.Fatal(err)
		}
	}
	notice = f.read(d0)
	if notice.State != "delivered" || notice.LastOutcome != monitoring.AttemptAcknowledged || notice.LastErrorCode != "" || notice.Admission.Allowed || notice.Admission.Reason != "terminal" ||
		f.updates(d0) != 2 || f.count(`SELECT count(*) FROM delivery_outbox WHERE organization=$1 AND delivery_id=$2`, org, d0) != 0 {
		t.Fatalf("delivered %+v updates=%d", notice, f.updates(d0))
	}
	if page, err = store.Attempts(ctx, org, d0, 0, 10); err != nil || len(page) != 1 || page[0].Outcome != monitoring.AttemptAcknowledged || page[0].HTTPStatus != 204 {
		t.Fatal("acknowledged attempt", page, err)
	}

	// A retryable failure keeps truthful pending work, scheduled for its retry.
	d1 := deliveries[1]
	failed, _ := f.admit(d1, testWindow, configured)
	before := time.Now()
	if err = ds.Record(ctx, failed, monitoring.AttemptOutcome{Outcome: monitoring.AttemptRetryableError, HTTPStatus: 503, ErrorCode: "webhook_http_status", ErrorMessage: "receiver returned HTTP 503"}, retry); err != nil {
		t.Fatal(err)
	}
	notice = f.read(d1)
	at, _ := f.outbox(d1)
	if notice.State != "pending" || notice.AttemptCount != 1 || notice.LastOutcome != monitoring.AttemptRetryableError || notice.LastErrorCode != "webhook_http_status" || !notice.Admission.Allowed || f.parked(d1) || f.updates(d1) != 2 ||
		notice.NextAttemptAt == nil || !notice.NextAttemptAt.Equal(at) || at.Before(before.Add(25*time.Second)) || at.After(time.Now().Add(35*time.Second)) {
		t.Fatalf("retryable failure %+v at=%v", notice, at)
	}
	// Not yet due: no claim.
	f.noWork()
	// Disable serializes with admission: once due, the retry makes no new attempt.
	if _, err = f.service.DisableSubscription(ctx, f.scope, "disable-s1", f.subs[1].ID); err != nil {
		t.Fatal(err)
	}
	if notice = f.read(d1); notice.NextAttemptAt != nil || notice.Admission.Reason != "subscription_disabled" || notice.State != "pending" {
		t.Fatalf("disabled read view %+v", notice)
	}
	if _, refused := f.admit(d1, testWindow, configured); refused != "subscription_disabled" || f.attempts(d1) != 1 || !f.parked(d1) {
		t.Fatal("disabled admission", refused, f.attempts(d1))
	}

	// A lost in-flight attempt (expired lease) is recorded unknown and re-admitted once.
	d2 := deliveries[2]
	lost, _ := f.admit(d2, testWindow, configured)
	f.unlease(d2)
	w, err := ds.ClaimDelivery(ctx, time.Minute)
	if err != nil || w.DeliveryID != d2 {
		t.Fatal("reclaim", w, err)
	}
	second, refused, err := ds.Admit(ctx, w, testWindow, configured)
	if err != nil || refused != "" || second.Number != 2 || second.EventID != lost.EventID || string(second.Body) != string(lost.Body) {
		t.Fatalf("recovery admission %+v %v", second, err)
	}
	// The late outcome of the lost attempt does not overwrite the recovery fact.
	if err = ds.Record(ctx, lost, monitoring.AttemptOutcome{Outcome: monitoring.AttemptAcknowledged, HTTPStatus: 200}, monitoring.Retry{Window: testWindow}); err != nil {
		t.Fatal(err)
	}
	// A permanent failure ends automatic attempts: exhausted, work removed.
	if err = ds.Record(ctx, second, monitoring.AttemptOutcome{Outcome: monitoring.AttemptPermanentError, HTTPStatus: 400, ErrorCode: "webhook_http_status", ErrorMessage: "receiver returned HTTP 400"}, monitoring.Retry{Window: testWindow}); err != nil {
		t.Fatal(err)
	}
	notice = f.read(d2)
	page, err = store.Attempts(ctx, org, d2, 0, 10)
	if _, queued := f.outbox(d2); err != nil || len(page) != 2 || page[0].Outcome != monitoring.AttemptUnknown || page[1].Outcome != monitoring.AttemptPermanentError || page[1].HTTPStatus != 400 ||
		notice.State != "exhausted" || notice.AttemptCount != 2 || notice.LastOutcome != monitoring.AttemptPermanentError || notice.Admission.Reason != "terminal" || notice.LastErrorCode != "webhook_http_status" ||
		queued || f.exhaustedReason(d2) != "permanent_error" || notice.NextAttemptAt != nil {
		t.Fatalf("recovery history %+v %+v %v", page, notice, err)
	}
	if page, err = store.Attempts(ctx, org, d2, 1, 10); err != nil || len(page) != 1 || page[0].Number != 2 {
		t.Fatal("attempt keyset page", page, err)
	}

	// The delivery engine over the real store sends the exact stored bytes once.
	d4 := deliveries[4]
	f.unlease(d4)
	var got []byte
	var gotID string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		gotID = r.Header.Get("webhook-id")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer receiver.Close()
	engine := monitoring.Deliverer{AllowPrivateAddresses: true, Store: ds, Destinations: map[string]monitoring.Destination{"dest": {Organization: org, URL: receiver.URL, Secret: "whsec_AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="}}}
	if progressed, err := engine.Step(ctx); !progressed || err != nil {
		t.Fatal("engine step", progressed, err)
	}
	notice = f.read(d4)
	if notice.State != "delivered" || string(got) != string(notice.Event) || gotID == "" || notice.AttemptCount != 1 {
		t.Fatalf("engine delivery %+v body=%s", notice, got)
	}
	if progressed, err := engine.Step(ctx); progressed || err != nil {
		t.Fatal("no further work expected", progressed, err)
	}

	// A claim whose lease another worker took over is fenced: no attempt, no parking.
	d3 := deliveries[3]
	stale := f.claimOne(d3)
	if _, err = f.pool.Exec(ctx, `UPDATE delivery_outbox SET lease_until=now()+interval '1 hour' WHERE organization=$1 AND delivery_id=$2`, org, d3); err != nil {
		t.Fatal(err)
	}
	if _, refused, err := ds.Admit(ctx, stale, testWindow, configured); err != nil || refused != "lease_lost" || f.attempts(d3) != 0 || f.parked(d3) {
		t.Fatal("stale lease admitted", refused, err)
	}

	// A withdrawn Record is not admitted.
	if _, err = f.contents.Withdraw(ctx, f.scope, content.Withdrawal{Key: "withdraw-hit", Source: content.Source{CorpusID: f.corpusID, Namespace: "delivery", RecordKey: "hit"}}); err != nil {
		t.Fatal(err)
	}
	if _, refused := f.admit(d3, testWindow, configured); refused != "record_withdrawn" || f.attempts(d3) != 0 {
		t.Fatal("withdrawn admission", refused)
	}

	// delivery.updated is feed-only: it creates no Delivery or outbox work.
	if n := f.count(`SELECT count(*) FROM deliveries WHERE organization=$1`, org); n != len(deliveries) {
		t.Fatalf("deliveries %d", n)
	}
	// Attempt facts are append-only.
	if _, err = f.pool.Exec(ctx, `UPDATE delivery_attempt_outcomes SET outcome='acknowledged' WHERE organization=$1`, org); err == nil {
		t.Fatal("attempt outcomes must be append-only")
	}
	if _, err = f.pool.Exec(ctx, `DELETE FROM delivery_attempts WHERE organization=$1`, org); err == nil {
		t.Fatal("attempts must be append-only")
	}
}
