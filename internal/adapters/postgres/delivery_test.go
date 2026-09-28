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

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

// TestDeliveryAdmissionAndAppendOnlyAttempts proves delivery admission and
// outcome facts against the real journal: leased claims, admission refusals
// that park work without an attempt, the attempt fact committed before I/O,
// acknowledged/failed outcomes, crash recovery as an unknown outcome, feed-only
// delivery.updated events and append-only attempt history.
func TestDeliveryAdmissionAndAppendOnlyAttempts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	run := fmt.Sprint(time.Now().UnixNano())
	org := "adapter-delivery-" + run
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read", "monitoring:read", "monitoring:write"}, Corpora: []string{"*"}}
	a, _, err := corpus.Service{Store: postgres.Store{Pool: pool}}.Create(ctx, scope, corpus.CreateInput{Key: "a", Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.ContentStore{Pool: pool}
	contents := content.Service{Repository: store, Baseline: store}
	service := monitoring.Service{Store: store, Corpora: store, Destinations: map[string]monitoring.Destination{"dest": {Organization: org}}, MatchStore: store}
	q, err := service.CreateSavedQuery(ctx, scope, monitoring.SavedQueryInput{Key: "q", Name: "Q", Definition: monitoring.Definition{CorpusIDs: []string{a.ID}, Expression: map[string]any{}, RetrievalProfile: "balanced", TemporalPolicy: "from_activation"}})
	if err != nil {
		t.Fatal(err)
	}
	subs := make([]monitoring.Subscription, 5)
	for i := range subs {
		if subs[i], err = service.CreateSubscription(ctx, scope, monitoring.SubscriptionInput{Key: fmt.Sprint("s", i), Name: fmt.Sprint("S", i), SavedQueryID: q.ID, SavedQueryVersionID: q.Current.VersionID,
			Evaluator: monitoring.Evaluator{PluginID: monitoring.FixtureEvaluator, Version: monitoring.FixtureEvaluatorVersion, Configuration: map[string]any{"decisions": map[string]any{"default": "match"}}}, DestinationID: "dest"}); err != nil {
			t.Fatal(err)
		}
	}
	// One promoted Version, then one committed Match and pending Delivery per Subscription.
	cmd := content.Command{Key: "hit", Source: content.Source{CorpusID: a.ID, Namespace: "delivery", RecordKey: "hit"}, Content: content.Text{Kind: "text", Text: "Dépêche hit"}}
	r, err := contents.Accept(ctx, scope, cmd)
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := store.Work(ctx, org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Publish(ctx, work, publication(content.Blob{Key: "fixture/hit", SHA256: "text-" + run, Size: 10}, content.Blob{Key: "fixture/m-hit", SHA256: "manifest-" + run, Size: 2})); err != nil {
		t.Fatal(err)
	}
	// Keep fixture work (no S3 content, unconfigured destination) away from a live worker.
	if _, err = pool.Exec(ctx, `UPDATE ingestion_outbox SET dispatched=true WHERE organization=$1`, org); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE delivery_outbox SET available_at='infinity' WHERE organization=$1`, org)
	})
	v := content.Version{ID: work.VersionID, RecordID: work.RecordID, Manifest: content.ManifestFor(cmd)}
	seg := wholeBodySegmentation(org, v)
	if err = contents.SaveSegmentation(ctx, org, v, seg); err != nil {
		t.Fatal(err)
	}
	generation, err := store.Generation(ctx, org, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = contents.Promote(ctx, org, seg, generation); err != nil {
		t.Fatal(err)
	}
	evaluation := postgres.EvaluationStore{ContentStore: store}
	deliveries := make([]string, len(subs))
	for i, s := range subs {
		in := monitoring.Intent{Organization: org, SubscriptionID: s.ID, SubscriptionVersionID: s.Current.VersionID, Sequence: int64(1000 + i), CorpusID: a.ID, RecordID: work.RecordID, VersionID: work.VersionID}
		if outcome, err := evaluation.CommitMatch(ctx, in, monitoring.MatchEvidence{Evaluator: s.Current.Evaluator, Explanation: "fixture"}); err != nil || outcome != monitoring.OutcomeMatched {
			t.Fatal(outcome, err)
		}
		deliveries[i] = content.StableID("delivery", org, content.StableID("match", org, s.Current.VersionID, work.VersionID), "dest", "match.created")
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	attempts := func(id string) int {
		return count(`SELECT count(*) FROM delivery_attempts WHERE organization=$1 AND delivery_id=$2`, org, id)
	}
	updates := func(id string) int {
		return count(`SELECT count(*) FROM change_events WHERE organization=$1 AND event_type='delivery.updated' AND resource_type='delivery' AND resource_id=$2 AND corpus_id=$3`, org, id, a.ID)
	}
	read := func(id string) monitoring.Delivery {
		t.Helper()
		d, err := store.Delivery(ctx, org, id)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	ds := postgres.DeliveryStore{ContentStore: store, Organization: org}
	configured := func(o, dest string) bool { return o == org && dest == "dest" }

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
	if len(claimed) != len(subs) {
		t.Fatalf("claimed %d of %d", len(claimed), len(subs))
	}
	unlease := func(id string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE delivery_outbox SET lease_until='-infinity',available_at=now() WHERE organization=$1 AND delivery_id=$2`, org, id); err != nil {
			t.Fatal(err)
		}
	}
	claimOne := func(id string) monitoring.DeliveryWork {
		t.Helper()
		unlease(id)
		w, err := ds.ClaimDelivery(ctx, time.Minute)
		if err != nil || w.DeliveryID != id || w.Lease.IsZero() {
			t.Fatal("claim", id, w, err)
		}
		return w
	}
	admit := func(id string, configured func(string, string) bool) (monitoring.AdmittedAttempt, string) {
		t.Helper()
		a, refused, err := ds.Admit(ctx, claimOne(id), configured)
		if err != nil {
			t.Fatal(err)
		}
		return a, refused
	}
	parked := func(id string) bool {
		return count(`SELECT count(*) FROM delivery_outbox WHERE organization=$1 AND delivery_id=$2 AND available_at='infinity'`, org, id) == 1
	}

	// An unconfigured destination parks the work without an attempt.
	d0 := deliveries[0]
	if _, refused := admit(d0, func(string, string) bool { return false }); refused != "destination_unavailable" || attempts(d0) != 0 || !parked(d0) || read(d0).State != "pending" {
		t.Fatal("unconfigured destination", refused)
	}
	unlease(d0)
	// Admission commits the attempt fact and delivering state before I/O.
	first, refused := admit(d0, configured)
	notice := read(d0)
	if refused != "" || first.Number != 1 || first.EventID == "" || string(first.Body) != string(notice.Event) || first.DestinationID != "dest" ||
		notice.State != "delivering" || notice.AttemptCount != 1 || attempts(d0) != 1 || updates(d0) != 1 {
		t.Fatalf("admission %+v %+v", first, notice)
	}
	page, err := store.Attempts(ctx, org, d0, 0, 10)
	if err != nil || len(page) != 1 || page[0].Outcome != monitoring.AttemptInFlight || page[0].ID != first.AttemptID || page[0].Number != 1 {
		t.Fatal("in-flight attempt", page, err)
	}
	// 2xx acknowledges: delivered, terminal admission, work removed. Recording twice is a no-op.
	for i := 0; i < 2; i++ {
		if err = ds.Record(ctx, first, monitoring.AttemptOutcome{Outcome: monitoring.AttemptAcknowledged, HTTPStatus: 204}); err != nil {
			t.Fatal(err)
		}
	}
	notice = read(d0)
	if notice.State != "delivered" || notice.LastOutcome != monitoring.AttemptAcknowledged || notice.LastErrorCode != "" || notice.Admission.Allowed || notice.Admission.Reason != "terminal" ||
		updates(d0) != 2 || count(`SELECT count(*) FROM delivery_outbox WHERE organization=$1 AND delivery_id=$2`, org, d0) != 0 {
		t.Fatalf("delivered %+v updates=%d", notice, updates(d0))
	}
	if page, err = store.Attempts(ctx, org, d0, 0, 10); err != nil || len(page) != 1 || page[0].Outcome != monitoring.AttemptAcknowledged || page[0].HTTPStatus != 204 {
		t.Fatal("acknowledged attempt", page, err)
	}

	// A retryable failure keeps truthful pending work, parked for the retry slice.
	d1 := deliveries[1]
	unlease(d1)
	failed, _ := admit(d1, configured)
	if err = ds.Record(ctx, failed, monitoring.AttemptOutcome{Outcome: monitoring.AttemptRetryableError, HTTPStatus: 503, ErrorCode: "webhook_http_status", ErrorMessage: "receiver returned HTTP 503"}); err != nil {
		t.Fatal(err)
	}
	notice = read(d1)
	if notice.State != "pending" || notice.AttemptCount != 1 || notice.LastOutcome != monitoring.AttemptRetryableError || notice.LastErrorCode != "webhook_http_status" || !notice.Admission.Allowed || !parked(d1) || updates(d1) != 2 {
		t.Fatalf("retryable failure %+v", notice)
	}
	if w, err := ds.ClaimDelivery(ctx, time.Minute); !errors.Is(err, monitoring.ErrNoWork) {
		t.Fatal("parked work claimed", w, err)
	}
	// Disable serializes with admission: no new attempt.
	if _, err = service.DisableSubscription(ctx, scope, "disable-s1", subs[1].ID); err != nil {
		t.Fatal(err)
	}
	unlease(d1)
	if _, refused := admit(d1, configured); refused != "subscription_disabled" || attempts(d1) != 1 || !parked(d1) {
		t.Fatal("disabled admission", refused, attempts(d1))
	}

	// A lost in-flight attempt (expired lease) is recorded unknown and re-admitted once.
	d2 := deliveries[2]
	unlease(d2)
	lost, _ := admit(d2, configured)
	unlease(d2)
	w, err := ds.ClaimDelivery(ctx, time.Minute)
	if err != nil || w.DeliveryID != d2 {
		t.Fatal("reclaim", w, err)
	}
	second, refused := admit(d2, configured)
	if refused != "" || second.Number != 2 || second.EventID != lost.EventID || string(second.Body) != string(lost.Body) {
		t.Fatalf("recovery admission %+v", second)
	}
	// The late outcome of the lost attempt does not overwrite the recovery fact.
	if err = ds.Record(ctx, lost, monitoring.AttemptOutcome{Outcome: monitoring.AttemptAcknowledged, HTTPStatus: 200}); err != nil {
		t.Fatal(err)
	}
	if err = ds.Record(ctx, second, monitoring.AttemptOutcome{Outcome: monitoring.AttemptPermanentError, HTTPStatus: 400, ErrorCode: "webhook_http_status", ErrorMessage: "receiver returned HTTP 400"}); err != nil {
		t.Fatal(err)
	}
	notice = read(d2)
	page, err = store.Attempts(ctx, org, d2, 0, 10)
	if err != nil || len(page) != 2 || page[0].Outcome != monitoring.AttemptUnknown || page[1].Outcome != monitoring.AttemptPermanentError || page[1].HTTPStatus != 400 ||
		notice.State != "pending" || notice.AttemptCount != 2 || notice.LastOutcome != monitoring.AttemptPermanentError {
		t.Fatalf("recovery history %+v %+v %v", page, notice, err)
	}
	if page, err = store.Attempts(ctx, org, d2, 1, 10); err != nil || len(page) != 1 || page[0].Number != 2 {
		t.Fatal("attempt keyset page", page, err)
	}

	// The delivery engine over the real store sends the exact stored bytes once.
	d4 := deliveries[4]
	unlease(d4)
	var got []byte
	var gotID string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		gotID = r.Header.Get("webhook-id")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer receiver.Close()
	engine := monitoring.Deliverer{Store: ds, Destinations: map[string]monitoring.Destination{"dest": {Organization: org, URL: receiver.URL, Secret: "whsec_AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="}}}
	if progressed, err := engine.Step(ctx); !progressed || err != nil {
		t.Fatal("engine step", progressed, err)
	}
	notice = read(d4)
	if notice.State != "delivered" || string(got) != string(notice.Event) || gotID == "" || notice.AttemptCount != 1 {
		t.Fatalf("engine delivery %+v body=%s", notice, got)
	}
	if progressed, err := engine.Step(ctx); progressed || err != nil {
		t.Fatal("no further work expected", progressed, err)
	}

	// A claim whose lease another worker took over is fenced: no attempt, no parking.
	d3 := deliveries[3]
	stale := claimOne(d3)
	if _, err = pool.Exec(ctx, `UPDATE delivery_outbox SET lease_until=now()+interval '1 hour' WHERE organization=$1 AND delivery_id=$2`, org, d3); err != nil {
		t.Fatal(err)
	}
	if _, refused, err := ds.Admit(ctx, stale, configured); err != nil || refused != "lease_lost" || attempts(d3) != 0 || parked(d3) {
		t.Fatal("stale lease admitted", refused, err)
	}

	// A withdrawn Record is not admitted.
	if _, err = contents.Withdraw(ctx, scope, content.Withdrawal{Key: "withdraw-hit", Source: content.Source{CorpusID: a.ID, Namespace: "delivery", RecordKey: "hit"}}); err != nil {
		t.Fatal(err)
	}
	unlease(d3)
	if _, refused := admit(d3, configured); refused != "record_withdrawn" || attempts(d3) != 0 {
		t.Fatal("withdrawn admission", refused)
	}

	// delivery.updated is feed-only: it creates no Delivery or outbox work.
	if n := count(`SELECT count(*) FROM deliveries WHERE organization=$1`, org); n != len(subs) {
		t.Fatalf("deliveries %d", n)
	}
	// Attempt facts are append-only.
	if _, err = pool.Exec(ctx, `UPDATE delivery_attempt_outcomes SET outcome='acknowledged' WHERE organization=$1`, org); err == nil {
		t.Fatal("attempt outcomes must be append-only")
	}
	if _, err = pool.Exec(ctx, `DELETE FROM delivery_attempts WHERE organization=$1`, org); err == nil {
		t.Fatal("attempts must be append-only")
	}
}
