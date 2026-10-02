package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost/fakeplugin"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

// deliveryFixture is one Organization with a promoted Record Version and one
// committed Match plus pending Delivery per Subscription (destination "dest").
type deliveryFixture struct {
	t          *testing.T
	ctx        context.Context
	pool       *pgxpool.Pool
	org        string
	scope      corpus.Scope
	corpusID   string
	store      fixtureContentStores
	contents   content.Service
	service    monitoring.Service
	subs       []monitoring.Subscription
	deliveries []string
	ds         postgres.DeliveryStore
}

func newDeliveryFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, prefix string, n int) *deliveryFixture {
	t.Helper()
	run := fmt.Sprint(time.Now().UnixNano())
	org := prefix + run
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read", "monitoring:read", "monitoring:write"}, Corpora: []string{"*"}}
	a, _, err := corpus.Service{Store: postgres.Store{Pool: pool}}.Create(ctx, scope, corpus.CreateInput{Key: "a", Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	store := contentStores(pool)
	contents := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store, Baseline: store}
	service := monitoring.Service{Evaluators: fakeplugin.FixtureEvaluators(), Store: store, Corpora: store, Destinations: map[string]monitoring.Destination{"dest": {Organization: org}}, MatchStore: store}
	q, err := service.CreateSavedQuery(ctx, scope, monitoring.SavedQueryInput{Key: "q", Name: "Q", Definition: monitoring.Definition{CorpusIDs: []string{a.ID}, Expression: map[string]any{}, RetrievalProfile: "default", TemporalPolicy: "from_activation"}})
	if err != nil {
		t.Fatal(err)
	}
	subs := make([]monitoring.Subscription, n)
	for i := range subs {
		if subs[i], err = service.CreateSubscription(ctx, scope, monitoring.SubscriptionInput{Key: fmt.Sprint("s", i), Name: fmt.Sprint("S", i), SavedQueryID: q.ID, SavedQueryVersionID: q.Current.VersionID,
			Evaluator: monitoring.Evaluator{PluginID: fakeplugin.FixtureEvaluator, Version: fakeplugin.FixtureEvaluatorVersion, Configuration: map[string]any{"decisions": map[string]any{"default": "match"}}}, DestinationID: "dest"}); err != nil {
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
	evaluation := postgres.EvaluationStore{ContentStore: store.ContentStore}
	deliveries := make([]string, len(subs))
	for i, s := range subs {
		in := monitoring.Intent{Organization: org, SubscriptionID: s.ID, SubscriptionVersionID: s.Current.VersionID, Sequence: int64(1000 + i), CorpusID: a.ID, RecordID: work.RecordID, VersionID: work.VersionID}
		if outcome, err := evaluation.CommitMatch(ctx, in, monitoring.MatchEvidence{Evaluator: s.Current.Evaluator, Explanation: "fixture"}); err != nil || outcome != monitoring.OutcomeMatched {
			t.Fatal(outcome, err)
		}
		deliveries[i] = content.StableID("delivery", org, content.StableID("match", org, s.Current.VersionID, work.VersionID), "dest", "match.created")
	}
	return &deliveryFixture{t: t, ctx: ctx, pool: pool, org: org, scope: scope, corpusID: a.ID, store: store, contents: contents, service: service, subs: subs, deliveries: deliveries,
		ds: postgres.DeliveryStore{ContentStore: store.ContentStore, Organization: org}}
}

func (f *deliveryFixture) configured(o, dest string) bool { return o == f.org && dest == "dest" }

func (f *deliveryFixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(f.ctx, query, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *deliveryFixture) attempts(id string) int {
	return f.count(`SELECT count(*) FROM delivery_attempts WHERE organization=$1 AND delivery_id=$2`, f.org, id)
}

func (f *deliveryFixture) updates(id string) int {
	return f.count(`SELECT count(*) FROM change_events WHERE organization=$1 AND event_type='delivery.updated' AND resource_type='delivery' AND resource_id=$2 AND corpus_id=$3`, f.org, id, f.corpusID)
}

func (f *deliveryFixture) read(id string) monitoring.Delivery {
	f.t.Helper()
	d, err := f.store.Delivery(f.ctx, f.org, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return d
}

// unlease makes a Delivery's work due now and unclaimed.
func (f *deliveryFixture) unlease(id string) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, `UPDATE delivery_outbox SET lease_until='-infinity',available_at=now() WHERE organization=$1 AND delivery_id=$2`, f.org, id); err != nil {
		f.t.Fatal(err)
	}
}

func (f *deliveryFixture) claimOne(id string) monitoring.DeliveryWork {
	f.t.Helper()
	f.unlease(id)
	w, err := f.ds.ClaimDelivery(f.ctx, time.Minute)
	if err != nil || w.DeliveryID != id || w.Lease.IsZero() {
		f.t.Fatal("claim", id, w, err)
	}
	return w
}

func (f *deliveryFixture) admit(id string, window time.Duration, configured func(string, string) bool) (monitoring.AdmittedAttempt, string) {
	f.t.Helper()
	a, refused, err := f.ds.Admit(f.ctx, f.claimOne(id), window, configured)
	if err != nil {
		f.t.Fatal(err)
	}
	return a, refused
}

func (f *deliveryFixture) parked(id string) bool {
	return f.count(`SELECT count(*) FROM delivery_outbox WHERE organization=$1 AND delivery_id=$2 AND available_at='infinity'`, f.org, id) == 1
}

func (f *deliveryFixture) outbox(id string) (time.Time, bool) {
	f.t.Helper()
	var at time.Time
	err := f.pool.QueryRow(f.ctx, `SELECT available_at FROM delivery_outbox WHERE organization=$1 AND delivery_id=$2`, f.org, id).Scan(&at)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

func (f *deliveryFixture) exhaustedReason(id string) string {
	f.t.Helper()
	var reason string
	if err := f.pool.QueryRow(f.ctx, `SELECT exhausted_reason FROM deliveries WHERE organization=$1 AND id=$2`, f.org, id).Scan(&reason); err != nil {
		f.t.Fatal(err)
	}
	return reason
}

// age moves a Delivery's window start into the past.
func (f *deliveryFixture) age(id string, by time.Duration) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, `UPDATE deliveries SET created_at=created_at-make_interval(secs => $3::double precision) WHERE organization=$1 AND id=$2`, f.org, id, by.Seconds()); err != nil {
		f.t.Fatal(err)
	}
}

// rewind lets time pass for a Delivery: its window start and scheduled work
// both move into the past, keeping their relative schedule.
func (f *deliveryFixture) rewind(id string, by time.Duration) {
	f.t.Helper()
	f.age(id, by)
	if _, err := f.pool.Exec(f.ctx, `UPDATE delivery_outbox SET available_at=available_at-make_interval(secs => $3::double precision) WHERE organization=$1 AND delivery_id=$2`, f.org, id, by.Seconds()); err != nil {
		f.t.Fatal(err)
	}
}

// claimDue claims work that is due by its own schedule, without resetting it.
// Other due work claimed on the way stays leased until reset by unlease.
func (f *deliveryFixture) claimDue(id string) monitoring.DeliveryWork {
	f.t.Helper()
	for {
		w, err := f.ds.ClaimDelivery(f.ctx, time.Minute)
		if err != nil {
			f.t.Fatal("claim due", id, err)
		}
		if w.DeliveryID == id {
			return w
		}
	}
}

// leaseAll claims every due Delivery so single-Delivery steps claim only the
// work they reset.
func (f *deliveryFixture) leaseAll() {
	f.t.Helper()
	for {
		if _, err := f.ds.ClaimDelivery(f.ctx, time.Minute); errors.Is(err, monitoring.ErrNoWork) {
			return
		} else if err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *deliveryFixture) noWork() {
	f.t.Helper()
	if w, err := f.ds.ClaimDelivery(f.ctx, time.Minute); !errors.Is(err, monitoring.ErrNoWork) {
		f.t.Fatal("unexpected claimable work", w, err)
	}
}
