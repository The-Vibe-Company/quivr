package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pruneFixture is one isolated Organization with a Corpus and helpers over
// the real journal. Every prune in these tests names its Organization, so a
// shared verification database is never pruned outside the fixture.
type pruneFixture struct {
	t      *testing.T
	ctx    context.Context
	pool   *pgxpool.Pool
	store  postgres.ContentStore
	scope  corpus.Scope
	corpus string
}

func newPruneFixture(t *testing.T, ctx context.Context, name string) *pruneFixture {
	t.Helper()
	pool := adapterPool(t, ctx)
	scope := corpus.Scope{Organization: fmt.Sprintf("adapter-prune-%s-%d", name, time.Now().UnixNano()), Actions: []string{"corpora:write", "content:write", "content:read", "monitoring:read", "monitoring:write"}, Corpora: []string{"*"}}
	c, _, err := corpus.Service{Store: postgres.Store{Pool: pool}}.Create(ctx, scope, corpus.CreateInput{Key: "a", Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	return &pruneFixture{t: t, ctx: ctx, pool: pool, store: postgres.ContentStore{Pool: pool}, scope: scope, corpus: c.ID}
}

// accept commits one Record: receipt.pending then record.accepted.
func (f *pruneFixture) accept(key string) {
	f.t.Helper()
	if _, err := (content.Service{Repository: f.store}).Accept(f.ctx, f.scope, content.Command{Key: key, Source: content.Source{CorpusID: f.corpus, Namespace: "prune", RecordKey: key}, Content: content.Text{Kind: "text", Text: "Prune " + key}}); err != nil {
		f.t.Fatal(err)
	}
	// Keep fixture Receipts away from a live worker.
	if _, err := f.pool.Exec(f.ctx, `UPDATE ingestion_outbox SET dispatched=true WHERE organization=$1`, f.scope.Organization); err != nil {
		f.t.Fatal(err)
	}
}

// age backdates the journal events at or below through.
func (f *pruneFixture) age(through int64, by time.Duration) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, `UPDATE change_events SET occurred_at=now()-make_interval(secs => $3::double precision) WHERE organization=$1 AND sequence<=$2`, f.scope.Organization, through, by.Seconds()); err != nil {
		f.t.Fatal(err)
	}
}

func (f *pruneFixture) prune(retention time.Duration, batch, batches int) int {
	f.t.Helper()
	n, err := f.store.PruneChanges(f.ctx, retention, []string{f.scope.Organization}, batch, batches)
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *pruneFixture) int(query string) int64 {
	f.t.Helper()
	var n int64
	if err := f.pool.QueryRow(f.ctx, query, f.scope.Organization).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *pruneFixture) retained() (int64, int64) {
	return f.int(`SELECT count(*) FROM change_events WHERE organization=$1`), f.int(`SELECT coalesce(min(sequence),0) FROM change_events WHERE organization=$1`)
}

func (f *pruneFixture) watermark() int64 {
	return f.int(`SELECT coalesce((SELECT pruned_through FROM change_journal_prunes WHERE organization=$1),0)`)
}

// TestPruneDeletesAgedPrefixAndExpiresCursorsBeforeIt proves a bounded,
// idempotent prune of the aged journal prefix, the recorded watermark, exact
// cursor expiry against it under any retention, and untouched canonical rows.
func TestPruneDeletesAgedPrefixAndExpiresCursorsBeforeIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := newPruneFixture(t, ctx, "prefix")
	for i := range 3 {
		f.accept(fmt.Sprint("r", i))
	}
	// Six positions; the first four are older than the one-hour retention.
	f.age(4, 2*time.Hour)
	records := f.int(`SELECT count(*) FROM records WHERE organization=$1`)
	receipts := f.int(`SELECT count(*) FROM ingestion_receipts WHERE organization=$1`)

	if n, err := f.store.PruneChanges(ctx, time.Hour, []string{"adapter-prune-not-this-organization"}, 1000, 10); err != nil || n != 0 {
		t.Fatal("prune outside the named Organizations", n, err)
	}
	if count, _ := f.retained(); count != 6 {
		t.Fatal("an unnamed Organization was pruned", count)
	}
	if n := f.prune(time.Hour, 3, 1); n != 3 || f.watermark() != 3 {
		t.Fatal("one bounded batch", n, f.watermark())
	}
	if n := f.prune(time.Hour, 3, 10); n != 1 || f.watermark() != 4 {
		t.Fatal("prune must stop at the first event inside retention", n, f.watermark())
	}
	if n := f.prune(time.Hour, 3, 10); n != 0 || f.watermark() != 4 {
		t.Fatal("a second run is a no-op", n, f.watermark())
	}
	if count, first := f.retained(); count != 2 || first != 5 {
		t.Fatal("retained suffix", count, first)
	}
	if f.int(`SELECT count(*) FROM records WHERE organization=$1`) != records || f.int(`SELECT count(*) FROM ingestion_receipts WHERE organization=$1`) != receipts {
		t.Fatal("prune touched canonical rows")
	}

	const week = 7 * 24 * time.Hour
	for _, after := range []int64{0, 3} {
		w, err := f.store.ReadChanges(ctx, f.scope.Organization, f.corpus, after, 10, week)
		if err != nil || !w.Expired {
			t.Fatal("a cursor before the pruned watermark must expire whatever the retention", after, w, err)
		}
	}
	w, err := f.store.ReadChanges(ctx, f.scope.Organization, f.corpus, 4, 10, week)
	if err != nil || w.Expired || len(w.Events) != 2 || w.Events[0].Position != 5 || w.Events[1].Position != 6 {
		t.Fatal("a cursor at the watermark must read every retained event", w, err)
	}
	head, err := f.store.ReadChanges(ctx, f.scope.Organization, f.corpus, 6, 0, week)
	if err != nil || head.Expired || head.Head != 6 {
		t.Fatal("start at head", head, err)
	}
}

// TestPruneWithoutOrganizationListPrunesEveryOrganization is the production
// default: an omitted change_prune.organizations (a nil list) means all
// Organizations. The retention is far longer than any other test data, so
// only this fixture's backdated events qualify on a shared database.
func TestPruneWithoutOrganizationListPrunesEveryOrganization(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := newPruneFixture(t, ctx, "all")
	f.accept("a")
	f.age(2, 20000*time.Hour)
	if _, err := f.store.PruneChanges(ctx, 10000*time.Hour, nil, 1000, 10); err != nil {
		t.Fatal(err)
	}
	if count, first := f.retained(); f.watermark() != 2 || count != 0 || first != 0 {
		t.Fatal("a nil Organization list pruned nothing", f.watermark(), count, first)
	}
}

// TestPruneWaitsForTheEvaluationCheckpoint lets evaluation dispatch lag
// behind retention: aged events above the activation boundary, then above the
// dispatch checkpoint, survive until dispatch passes them.
func TestPruneWaitsForTheEvaluationCheckpoint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := newPruneFixture(t, ctx, "checkpoint")
	f.accept("before")
	service := monitoring.Service{Evaluators: monitoring.FixtureEvaluators(), Store: f.store, Corpora: f.store, Destinations: map[string]monitoring.Destination{"dest": {Organization: f.scope.Organization}}}
	q, err := service.CreateSavedQuery(ctx, f.scope, monitoring.SavedQueryInput{Key: "q", Name: "Q", Definition: monitoring.Definition{CorpusIDs: []string{f.corpus}, Expression: map[string]any{}, RetrievalProfile: "default", TemporalPolicy: "from_activation"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.CreateSubscription(ctx, f.scope, monitoring.SubscriptionInput{Key: "s", Name: "S", SavedQueryID: q.ID, SavedQueryVersionID: q.Current.VersionID,
		Evaluator: monitoring.Evaluator{PluginID: monitoring.FixtureEvaluator, Version: monitoring.FixtureEvaluatorVersion, Configuration: map[string]any{"decisions": map[string]any{"default": "match"}}}, DestinationID: "dest"}); err != nil {
		t.Fatal(err)
	}
	activation := f.int(`SELECT min(activation_position) FROM subscription_versions WHERE organization=$1`)
	f.accept("after-1")
	// An aged trigger event above the lagging checkpoint must still become evaluation work.
	if _, err = f.pool.Exec(ctx, `WITH s AS (UPDATE organization_journals SET last_sequence=last_sequence+1 WHERE organization=$1 RETURNING last_sequence)
INSERT INTO change_events(organization,sequence,event_id,corpus_id,event_type,resource_type,resource_id,record_version_id) SELECT $1,s.last_sequence,$1||'-trigger-'||s.last_sequence,$2,'record.retrieval_ready','record','record_prune_fixture','version_prune_fixture' FROM s`, f.scope.Organization, f.corpus); err != nil {
		t.Fatal(err)
	}
	trigger := f.int(`SELECT last_sequence FROM organization_journals WHERE organization=$1`)
	// Leave no evaluation work for a live worker: the fixture Version does not exist.
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `UPDATE evaluation_intents SET state='done',outcome='test_cleanup' WHERE organization=$1 AND state='pending'`, f.scope.Organization)
	})
	f.accept("after-2")
	head := f.int(`SELECT last_sequence FROM organization_journals WHERE organization=$1`)
	f.age(head, 2*time.Hour)

	// No checkpoint yet: the activation boundary is where dispatch will start.
	f.prune(time.Hour, 1000, 10)
	if count, first := f.retained(); f.watermark() != activation || first != activation+1 || count != head-activation {
		t.Fatal("prune passed the activation boundary before dispatch started", activation, f.watermark(), first, count)
	}
	evaluation := postgres.EvaluationStore{ContentStore: f.store}
	if _, err = evaluation.FanOut(ctx); err != nil {
		t.Fatal(err)
	}
	// Dispatch lags: its checkpoint stands one position above the boundary.
	lag := activation + 1
	if _, err = f.pool.Exec(ctx, `UPDATE monitoring_checkpoints SET position=$2 WHERE organization=$1`, f.scope.Organization, lag); err != nil {
		t.Fatal(err)
	}
	// Forget earlier dispatch, so only the catch-up below can produce the trigger's intent.
	if _, err = f.pool.Exec(ctx, `DELETE FROM evaluation_intents WHERE organization=$1`, f.scope.Organization); err != nil {
		t.Fatal(err)
	}
	f.prune(time.Hour, 1000, 10)
	if _, first := f.retained(); f.watermark() != lag || first != lag+1 {
		t.Fatal("prune passed a lagging dispatch checkpoint", lag, f.watermark(), first)
	}
	for i := 0; i < 5; i++ {
		if _, err = evaluation.FanOut(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.int(`SELECT position FROM monitoring_checkpoints WHERE organization=$1`); got != head {
		t.Fatal("dispatch did not catch up", got, head)
	}
	if n := f.int(`SELECT count(*) FROM evaluation_intents WHERE organization=$1 AND sequence=` + fmt.Sprint(trigger)); n != 1 {
		t.Fatal("an aged trigger event behind a lagging checkpoint was pruned before dispatch", trigger, n)
	}
	f.prune(time.Hour, 1000, 10)
	if count, _ := f.retained(); f.watermark() != head || count != 0 {
		t.Fatal("events dispatch has passed must be pruned", f.watermark(), count)
	}
}

// TestPruneNeverWaitsOnTheJournalLock prunes while a writer holds the
// Organization journal lock, and skips an Organization another prune holds.
func TestPruneNeverWaitsOnTheJournalLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := newPruneFixture(t, ctx, "lock")
	f.accept("a")
	f.accept("b")
	f.age(4, 2*time.Hour)

	writer, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(ctx)
	if _, err = writer.Exec(ctx, `SELECT last_sequence FROM organization_journals WHERE organization=$1 FOR UPDATE`, f.scope.Organization); err != nil {
		t.Fatal(err)
	}
	quick, stop := context.WithTimeout(ctx, 2*time.Second)
	n, err := f.store.PruneChanges(quick, time.Hour, []string{f.scope.Organization}, 1, 1)
	stop()
	if err != nil || n != 1 {
		t.Fatal("prune waited on the journal lock", n, err)
	}
	if err = writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	other, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Rollback(ctx)
	if _, err = other.Exec(ctx, `SELECT 1 FROM change_journal_prunes WHERE organization=$1 FOR UPDATE`, f.scope.Organization); err != nil {
		t.Fatal(err)
	}
	quick, stop = context.WithTimeout(ctx, 2*time.Second)
	n, err = f.store.PruneChanges(quick, time.Hour, []string{f.scope.Organization}, 1000, 10)
	stop()
	if err != nil || n != 0 {
		t.Fatal("a concurrent prune must skip the held Organization", n, err)
	}
	if err = other.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if n = f.prune(time.Hour, 1000, 10); n != 3 || f.watermark() != 4 {
		t.Fatal("prune resumes after the other run", n, f.watermark())
	}
}
