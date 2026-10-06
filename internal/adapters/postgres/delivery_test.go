package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/jackc/pgx/v5/pgconn"
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
	f := newDeliveryFixture(t, ctx, adapterPool(t, ctx), "adapter-delivery-", 38)
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

	// Bounded claims keep one Organization and stable arrival order, lease at
	// most 32 rows even for a larger request, and leave held rows unclaimable.
	if _, err = f.pool.Exec(ctx, `UPDATE delivery_outbox SET available_at=now(),lease_until='-infinity' WHERE organization=$1 AND delivery_id=ANY($2::text[])`, org, deliveries[5:]); err != nil {
		t.Fatal(err)
	}
	works, err := ds.ClaimDeliveries(ctx, time.Minute, 1000)
	if err != nil || len(works) != 32 {
		t.Fatalf("bounded claim: got %d works, want 32 (%v)", len(works), err)
	}
	spare, err := ds.ClaimDeliveries(ctx, time.Minute, 32)
	if err != nil || len(spare) != 1 {
		t.Fatalf("remaining claim: got %d works, want 1 (%v)", len(spare), err)
	}
	f.noWork()
	for i, w := range works {
		if w.Organization != org || w.Lease.IsZero() || (i > 0 && w.DeliveryID <= works[i-1].DeliveryID) {
			t.Fatalf("group claim %d has wrong scope, lease or arrival order: %+v", i, w)
		}
	}
	head := func() int64 {
		t.Helper()
		var n int64
		if err := f.pool.QueryRow(ctx, `SELECT last_sequence FROM organization_journals WHERE organization=$1`, org).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Fail the journal-event statement after attempts and delivery states have
	// been written. The trigger observes those writes inside the transaction.
	if _, err = f.pool.Exec(ctx, `CREATE FUNCTION fail_fixture_delivery_admission() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.organization=TG_ARGV[0] AND NEW.event_type='delivery.updated' THEN
  IF NOT EXISTS(SELECT 1 FROM deliveries d JOIN delivery_attempts a ON (a.organization,a.delivery_id)=(d.organization,d.id) WHERE d.organization=NEW.organization AND d.id=NEW.resource_id AND d.state='delivering') THEN RAISE EXCEPTION 'admission writes absent'; END IF;
  RAISE EXCEPTION 'synthetic later admission interruption';
 END IF; RETURN NEW; END $$;`+fmt.Sprintf(`CREATE TRIGGER fail_fixture_delivery_admission BEFORE INSERT ON change_events FOR EACH ROW EXECUTE FUNCTION fail_fixture_delivery_admission('%s')`, org)); err != nil {
		t.Fatal(err)
	}
	defer f.pool.Exec(context.Background(), "DROP TRIGGER IF EXISTS fail_fixture_delivery_admission ON change_events; DROP FUNCTION IF EXISTS fail_fixture_delivery_admission()")
	beforeHead := head()
	_, _, admissionErrors := ds.AdmitDeliveries(ctx, works, testWindow, configured)
	for i, err := range admissionErrors {
		var sqlError *pgconn.PgError
		if !errors.As(err, &sqlError) || sqlError.Code != "P0001" || sqlError.Message != "synthetic later admission interruption" {
			t.Fatalf("group admission %d must reach later journal write: %v", i, err)
		}
	}
	for _, w := range works {
		if d := f.read(w.DeliveryID); d.State != "pending" || d.AttemptCount != 0 || f.attempts(w.DeliveryID) != 0 || f.updates(w.DeliveryID) != 0 {
			t.Fatalf("failed admission leaked facts for %s: %+v", w.DeliveryID, d)
		}
	}
	if head() != beforeHead {
		t.Fatalf("failed admission advanced head to %d from %d", head(), beforeHead)
	}
	if _, err = f.pool.Exec(ctx, "DROP TRIGGER fail_fixture_delivery_admission ON change_events; DROP FUNCTION fail_fixture_delivery_admission()"); err != nil {
		t.Fatal(err)
	}
	// Seed a lost attempt separately to retain the real unknown-outcome owner.
	lostWork := works[len(works)-1]
	lostID := content.StableID("attempt", org, lostWork.DeliveryID, "1")
	if _, err = f.pool.Exec(ctx, `INSERT INTO delivery_attempts(organization,id,delivery_id,number) VALUES($1,$2,$3,1)`, org, lostID, lostWork.DeliveryID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE deliveries SET state='delivering',attempt_count=1 WHERE organization=$1 AND id=$2`, org, lostWork.DeliveryID); err != nil {
		t.Fatal(err)
	}
	commonWorks := append(append([]monitoring.DeliveryWork(nil), works[:len(works)-1]...), spare[0])
	attempts, refusals, admissionErrors := ds.AdmitDeliveries(ctx, commonWorks, testWindow, configured)
	for i, a := range attempts {
		if admissionErrors[i] != nil || refusals[i] != "" || a.Number != 1 || a.DeliveryID != commonWorks[i].DeliveryID || a.AttemptID != content.StableID("attempt", org, a.DeliveryID, "1") || string(a.Body) != string(f.read(a.DeliveryID).Event) {
			t.Fatalf("common admission %d: %+v refusal=%q err=%v", i, a, refusals[i], admissionErrors[i])
		}
	}
	if head() != beforeHead+32 {
		t.Fatalf("common admission head %d, want %d", head(), beforeHead+32)
	}
	// Fail outbox deletion after outcomes, terminal states and events were
	// written. Their transaction must restore all facts and retryable work.
	outcomes, retries := make([]monitoring.AttemptOutcome, len(attempts)), make([]monitoring.Retry, len(attempts))
	for i := range outcomes {
		outcomes[i], retries[i] = monitoring.AttemptOutcome{Outcome: monitoring.AttemptAcknowledged, HTTPStatus: 204}, monitoring.Retry{Window: testWindow}
	}
	if _, err = f.pool.Exec(ctx, `CREATE FUNCTION fail_fixture_delivery_record() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF OLD.organization=TG_ARGV[0] THEN
  IF NOT EXISTS(SELECT 1 FROM deliveries d JOIN delivery_attempts a ON (a.organization,a.delivery_id)=(d.organization,d.id) JOIN delivery_attempt_outcomes o ON (o.organization,o.attempt_id)=(a.organization,a.id) WHERE d.organization=OLD.organization AND d.id=OLD.delivery_id AND d.state='delivered' AND o.outcome='acknowledged') THEN RAISE EXCEPTION 'outcome writes absent'; END IF;
  RAISE EXCEPTION 'synthetic later outcome interruption';
 END IF; RETURN OLD; END $$;`+fmt.Sprintf(`CREATE TRIGGER fail_fixture_delivery_record BEFORE DELETE ON delivery_outbox FOR EACH ROW EXECUTE FUNCTION fail_fixture_delivery_record('%s')`, org)); err != nil {
		t.Fatal(err)
	}
	defer f.pool.Exec(context.Background(), "DROP TRIGGER IF EXISTS fail_fixture_delivery_record ON delivery_outbox; DROP FUNCTION IF EXISTS fail_fixture_delivery_record()")
	beforeHead = head()
	for i, err := range ds.RecordDeliveries(ctx, attempts, outcomes, retries) {
		var sqlError *pgconn.PgError
		if !errors.As(err, &sqlError) || sqlError.Code != "P0001" || sqlError.Message != "synthetic later outcome interruption" {
			t.Fatalf("group outcome %d must reach later outbox deletion: %v", i, err)
		}
	}
	for _, a := range attempts {
		if d := f.read(a.DeliveryID); d.State != "delivering" || d.LastOutcome != "" || f.updates(a.DeliveryID) != 1 || f.count(`SELECT count(*) FROM delivery_attempt_outcomes WHERE organization=$1 AND attempt_id=$2`, org, a.AttemptID) != 0 {
			t.Fatalf("failed outcomes leaked facts for %s: %+v", a.DeliveryID, d)
		}
		if _, queued := f.outbox(a.DeliveryID); !queued {
			t.Fatalf("failed outcomes removed work for %s", a.DeliveryID)
		}
	}
	if head() != beforeHead {
		t.Fatalf("failed outcomes advanced head to %d from %d", head(), beforeHead)
	}
	if _, err = f.pool.Exec(ctx, "DROP TRIGGER fail_fixture_delivery_record ON delivery_outbox; DROP FUNCTION fail_fixture_delivery_record()"); err != nil {
		t.Fatal(err)
	}
	for replay := 0; replay < 2; replay++ {
		for i, err := range ds.RecordDeliveries(ctx, attempts, outcomes, retries) {
			if err != nil {
				t.Fatalf("common outcome/replay %d item %d: %v", replay, i, err)
			}
		}
	}
	for _, a := range attempts {
		if d := f.read(a.DeliveryID); d.State != "delivered" || d.LastOutcome != monitoring.AttemptAcknowledged || d.AttemptCount != 1 || f.updates(a.DeliveryID) != 2 || string(d.Event) != string(a.Body) {
			t.Fatalf("common acknowledgement changed history/body for %s: %+v", a.DeliveryID, d)
		}
		if _, queued := f.outbox(a.DeliveryID); queued {
			t.Fatalf("acknowledged work still queued for %s", a.DeliveryID)
		}
	}
	if head() != beforeHead+32 {
		t.Fatalf("acknowledged replay advanced head to %d, want %d", head(), beforeHead+32)
	}
	// One exceptional item chooses the existing path for the whole group:
	// recovery appends unknown, disable parks, and a stale lease touches none.
	recovery, refusals, admissionErrors := ds.AdmitDeliveries(ctx, []monitoring.DeliveryWork{lostWork, f.claimOne(d1), stale}, testWindow, configured)
	if admissionErrors[0] != nil || admissionErrors[1] != nil || admissionErrors[2] != nil || refusals[0] != "" || refusals[1] != "subscription_disabled" || refusals[2] != "lease_lost" || recovery[0].Number != 2 || !f.parked(d1) || f.attempts(d3) != 0 {
		t.Fatalf("group fallback: attempts=%+v refusals=%v errors=%v", recovery, refusals, admissionErrors)
	}
	lostAttempt := recovery[0]
	lostAttempt.AttemptID, lostAttempt.Number = lostID, 1
	missingAttempt := recovery[0]
	missingAttempt.AttemptID = "attempt_not_admitted"
	recorded := ds.RecordDeliveries(ctx, []monitoring.AdmittedAttempt{lostAttempt, recovery[0], missingAttempt}, []monitoring.AttemptOutcome{{Outcome: monitoring.AttemptAcknowledged, HTTPStatus: 204}, fail503, {Outcome: monitoring.AttemptAcknowledged, HTTPStatus: 204}}, []monitoring.Retry{retry, retry, retry})
	if recorded[0] != nil || recorded[1] != nil || recorded[2] == nil {
		t.Fatalf("per-item fallback progress/errors: %v", recorded)
	}
	history, err := store.Attempts(ctx, org, lostWork.DeliveryID, 0, 10)
	if err != nil || len(history) != 2 || history[0].Outcome != monitoring.AttemptUnknown || history[1].Outcome != monitoring.AttemptRetryableError || f.read(lostWork.DeliveryID).State != "pending" {
		t.Fatalf("fallback replaced unknown or lost retry: %+v %v", history, err)
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
