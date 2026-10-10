package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestChangeJournalWindowsAndRetention reads the real commit-ordered journal:
// Corpus filtering, bounded pages, head advancement over invisible positions
// and read-time retention with a short configured window.
func TestChangeJournalWindowsAndRetention(t *testing.T) {
	path := os.Getenv("QUIVR_ADAPTER_CONFIG")
	if path == "" {
		t.Skip("real PostgreSQL suite runs inside make verify")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		DatabaseURL string `json:"database_url"`
	}
	if err = json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	scope := corpus.Scope{Organization: fmt.Sprintf("adapter-changes-%d", time.Now().UnixNano()), Actions: []string{"corpora:write", "content:write"}, Corpora: []string{"*"}}
	corpora := corpus.Service{Store: postgres.Store{Pool: pool}}
	a, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "a", Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "b", Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	store := contentStores(pool)
	service := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store}
	accept := func(corpusID, key string) {
		t.Helper()
		if _, err := service.Accept(ctx, scope, content.Command{Key: key, Source: content.Source{CorpusID: corpusID, Namespace: "changes", RecordKey: key}, Content: content.Text{Kind: "text", Text: "Change " + key}}); err != nil {
			t.Fatal(err)
		}
	}
	const week = 7 * 24 * time.Hour
	empty, err := store.ReadChanges(ctx, scope.Organization, a.ID, 0, 10, week)
	if err != nil || empty.Head != 0 || empty.Through != 0 || len(empty.Events) != 0 || empty.Expired {
		t.Fatal("empty journal", empty, err)
	}
	accept(a.ID, "a1")
	accept(b.ID, "b1")
	accept(a.ID, "a2")
	// Each acceptance commits receipt.pending then record.accepted: six positions.
	all, err := store.ReadChanges(ctx, scope.Organization, a.ID, 0, 10, week)
	if err != nil || all.Head != 6 || all.Through != 6 || len(all.Events) != 4 {
		t.Fatal("visible window", all, err)
	}
	for i, e := range all.Events {
		if e.CorpusID != a.ID || e.ID == "" || (i > 0 && e.Position <= all.Events[i-1].Position) {
			t.Fatal("event not scoped or not commit ordered", all.Events)
		}
	}
	if all.Events[1].Type != "record.accepted" || all.Events[1].ResourceKind != "record" {
		t.Fatal("missing Record invalidation", all.Events[1])
	}
	// A caller authorized before archive must not receive a cursor that skips
	// the hidden window. The archive flag and event window share one snapshot.
	if _, err = (postgres.Store{Pool: pool}).Archive(ctx, scope.Organization, a.ID, true); err != nil {
		t.Fatal(err)
	}
	if hidden, err := store.ReadChanges(ctx, scope.Organization, a.ID, 0, 10, week); !errors.Is(err, corpus.ErrArchived) || hidden.Through != 0 {
		t.Fatalf("archive advanced feed cursor: %+v %v", hidden, err)
	}
	if _, err = (postgres.Store{Pool: pool}).Archive(ctx, scope.Organization, a.ID, false); err != nil {
		t.Fatal(err)
	}
	if restored, err := store.ReadChanges(ctx, scope.Organization, a.ID, 0, 10, week); err != nil || len(restored.Events) != len(all.Events) {
		t.Fatalf("restore skipped feed events: %+v %v", restored, err)
	}
	page, err := store.ReadChanges(ctx, scope.Organization, a.ID, 0, 3, week)
	if err != nil || len(page.Events) != 3 || page.Through != page.Events[2].Position || page.Head != 6 {
		t.Fatal("bounded page", page, err)
	}
	invisible, err := store.ReadChanges(ctx, scope.Organization, a.ID, 2, 10, week)
	if err != nil || invisible.Events[0].Position != 5 {
		t.Fatal("invisible positions not skipped", invisible, err)
	}
	head, err := store.ReadChanges(ctx, scope.Organization, a.ID, 6, 0, week)
	if err != nil || head.Through != 6 || head.Expired {
		t.Fatal("start at head", head, err)
	}

	// A 1 µs window, the shortest PostgreSQL expresses: position 1 committed
	// several round trips ago, so it is already past retention.
	stale, err := store.ReadChanges(ctx, scope.Organization, a.ID, 0, 10, time.Microsecond)
	if err != nil || !stale.Expired {
		t.Fatal("unconsumed position older than retention did not expire", stale, err)
	}
	caughtUp, err := store.ReadChanges(ctx, scope.Organization, a.ID, 6, 10, time.Microsecond)
	if err != nil || caughtUp.Expired {
		t.Fatal("cursor at head must not expire on a quiet journal", caughtUp, err)
	}

	// Sparse visible events must be reached in one read, regardless of how
	// many positions a neighbouring Corpus committed. The final row is beyond
	// the journal head and must remain invisible until that head advances.
	if _, err := pool.Exec(ctx, `INSERT INTO change_events
(organization,sequence,event_id,corpus_id,event_type,resource_type,resource_id)
SELECT $1,i,$1||'-sparse-'||i,
CASE WHEN i IN (10007,20007,20008) THEN $2 ELSE $3 END,
'record.accepted','record','sparse-'||i FROM generate_series(7,20008) i`, scope.Organization, a.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE organization_journals SET last_sequence=20007 WHERE organization=$1`, scope.Organization); err != nil {
		t.Fatal(err)
	}
	first, err := store.ReadChanges(ctx, scope.Organization, a.ID, 6, 1, week)
	if err != nil || len(first.Events) != 1 || first.Events[0].Position != 10007 || first.Through != 10007 || first.Head != 20007 {
		t.Fatalf("sparse first page: %+v, err=%v; want position/through 10007 and head 20007", first, err)
	}
	last, err := store.ReadChanges(ctx, scope.Organization, a.ID, first.Through, 1, week)
	if err != nil || len(last.Events) != 1 || last.Events[0].Position != 20007 || last.Through != 20007 {
		t.Fatalf("sparse last page leaked or skipped events: %+v, err=%v", last, err)
	}
	quiet, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "quiet", Name: "Quiet"})
	if err != nil {
		t.Fatal(err)
	}
	window, err := store.ReadChanges(ctx, scope.Organization, quiet.ID, 6, 10, week)
	if err != nil || len(window.Events) != 0 || window.Through != 20007 || window.Head != 20007 {
		t.Fatalf("quiet Corpus must reach head in one read: %+v, err=%v", window, err)
	}
	if err := postgres.EnsureIndexes(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "ANALYZE change_events"); err != nil {
		t.Fatal(err)
	}
	trace := &changeReadTrace{}
	cfgPool := pool.Config().Copy()
	cfgPool.ConnConfig.Tracer = trace
	traced, err := pgxpool.NewWithConfig(ctx, cfgPool)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	for _, id := range []string{a.ID, quiet.ID} {
		if _, err := (postgres.ChangeStore{Pool: traced}).ReadChanges(ctx, scope.Organization, id, 6, 1, week); err != nil {
			t.Fatal(err)
		}
		assertChangeReadPlan(t, ctx, pool, trace.query, 2)
	}
}

// The calling pool executes only ReadChanges; fixture and EXPLAIN statements
// use the untraced pool so the proof always analyzes the real adapter query.
type changeReadTrace struct{ query pgx.TraceQueryStartData }

func (r *changeReadTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	r.query = d
	return ctx
}
func (*changeReadTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func assertChangeReadPlan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query pgx.TraceQueryStartData, maxRows float64) {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+query.SQL, query.Args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plans []struct{ Plan changeReadPlan }
	if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
		t.Fatalf("decode change read plan: %s, err=%v", raw, err)
	}
	t.Logf("change read EXPLAIN: %s", raw)
	found := false
	var visit func(changeReadPlan, bool)
	visit = func(p changeReadPlan, limited bool) {
		limited = limited || p.NodeType == "Limit"
		if p.Relation == "change_events" && strings.Contains(p.Condition, "corpus_id") {
			found = true
			if !limited || p.Index != "change_events_by_corpus" || p.NodeType != "Index Scan" ||
				!strings.Contains(p.Condition, "organization") || !strings.Contains(p.Condition, "sequence >") ||
				!strings.Contains(p.Condition, "sequence <=") || p.Rows > maxRows || p.Removed != 0 {
				t.Errorf("Corpus read must seek at most limit+1 rows without filtering neighbours: %+v", p)
			}
		}
		for _, child := range p.Plans {
			visit(child, limited)
		}
	}
	visit(plans[0].Plan, false)
	if !found {
		t.Fatalf("bounded Corpus index lookup missing: %s", raw)
	}
}

type changeReadPlan struct {
	NodeType  string           `json:"Node Type"`
	Relation  string           `json:"Relation Name"`
	Index     string           `json:"Index Name"`
	Condition string           `json:"Index Cond"`
	Rows      float64          `json:"Actual Rows"`
	Removed   float64          `json:"Rows Removed by Filter"`
	Plans     []changeReadPlan `json:"Plans"`
}
