package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRecordCatalogKeysetTraversal reads the real catalog: stable key order,
// withdrawn Records included, Corpus and Organization isolation, and bounded
// exclusive-key pages.
func TestRecordCatalogKeysetTraversal(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := contentStores(pool)
	service := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store, Catalog: store}
	corpora := corpus.Service{Store: postgres.Store{Pool: pool}}
	scope := corpus.Scope{Organization: "adapter-catalog", Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	other := scope
	other.Organization = "adapter-catalog-other"
	a, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "catalog-a", Name: "Catalog A"})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "catalog-b", Name: "Catalog B"})
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for i := range 5 {
		r, err := service.Accept(ctx, scope, content.Command{Key: fmt.Sprint("catalog-", i), Source: content.Source{CorpusID: a.ID, Namespace: "adapter", RecordKey: fmt.Sprint("r", i)}, Content: content.Text{Kind: "text", Text: fmt.Sprint("Record ", i)}})
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, r.RecordID)
	}
	sort.Strings(want)
	if _, err = service.Accept(ctx, scope, content.Command{Key: "catalog-b", Source: content.Source{CorpusID: b.ID, Namespace: "adapter", RecordKey: "r0"}, Content: content.Text{Kind: "text", Text: "Elsewhere"}}); err != nil {
		t.Fatal(err)
	}
	withdrawn, err := service.Withdraw(ctx, scope, content.Withdrawal{Key: "catalog-wd", Source: content.Source{CorpusID: a.ID, Namespace: "adapter", RecordKey: "r3"}})
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	after := ""
	for {
		page, err := service.Records(ctx, scope, a.ID, content.RecordQuery{AfterID: after, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if len(page) > 2 {
			t.Fatalf("page exceeds limit: %d", len(page))
		}
		for _, r := range page {
			if r.Source.CorpusID != a.ID || r.Withdrawn != (r.ID == withdrawn.RecordID) || r.Source.Namespace != "adapter" {
				t.Fatalf("catalog entry %+v", r)
			}
			got = append(got, r.ID)
		}
		if len(page) < 2 {
			break
		}
		after = page[len(page)-1].ID
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("keyset traversal %v, want %v", got, want)
	}
	if foreign, err := store.Records(ctx, other.Organization, a.ID, content.RecordQuery{Limit: 10}); err != nil || len(foreign) != 0 {
		t.Fatalf("catalog crossed Organizations: %v %v", foreign, err)
	}
}

// Owns the storage contracts for current-Version dates, range counts, and tuple
// paging under arrivals. The ID-order owner above covers resynchronization.
func TestRecordCatalogAcceptanceTraversal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	store := contentStores(pool)
	scope := corpus.Scope{Organization: "adapter-catalog-dates", Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	corpora := corpus.Service{Store: postgres.Store{Pool: pool}}
	c, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "dates", Name: "Dates"})
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "other", Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	service := content.Service{Corpora: postgres.Store{Pool: pool}, Submissions: store, Materialization: store, Catalog: store}
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	publish := func(corpusID, key, text string, at time.Time, current bool) content.Work {
		t.Helper()
		receipt, err := service.Accept(ctx, scope, content.Command{Key: key + text, Source: content.Source{CorpusID: corpusID, Namespace: "date-test", RecordKey: key}, Content: content.Text{Kind: "text", Text: text}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, "UPDATE ingestion_receipts SET accepted_at=$3 WHERE organization=$1 AND id=$2", scope.Organization, receipt.ID, at); err != nil {
			t.Fatal(err)
		}
		work, _, err := store.Work(ctx, scope.Organization, receipt.ID)
		if err != nil {
			t.Fatal(err)
		}
		blob := content.Blob{Key: "date-test/" + text, SHA256: "date-test-" + text, Size: int64(len(text))}
		if err = store.Publish(ctx, work, publication(blob, blob)); err != nil {
			t.Fatal(err)
		}
		if current {
			if _, err = pool.Exec(ctx, "UPDATE records SET current_version_id=$3 WHERE organization=$1 AND id=$2", scope.Organization, work.RecordID, work.VersionID); err != nil {
				t.Fatal(err)
			}
		}
		return work
	}
	oldest := publish(c.ID, "oldest", "old", day.Add(-time.Hour), true)
	low := publish(c.ID, "lower", "low", day, true)
	tie := publish(c.ID, "tie", "tie", day, true)
	high := publish(c.ID, "upper", "high", day.Add(24*time.Hour), true)
	publish(other.ID, "elsewhere", "other", day, true)
	// A pending Record and a first-seen withdrawal have no current Version.
	pending, err := service.Accept(ctx, scope, content.Command{Key: "pending", Source: content.Source{CorpusID: c.ID, Namespace: "date-test", RecordKey: "pending"}, Content: content.Text{Kind: "text", Text: "pending"}})
	if err != nil {
		t.Fatal(err)
	}
	tombstone, err := service.Withdraw(ctx, scope, content.Withdrawal{Key: "unseen-wd", Source: content.Source{CorpusID: c.ID, Namespace: "date-test", RecordKey: "unseen"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Withdraw(ctx, scope, content.Withdrawal{Key: "low-wd", Source: content.Source{CorpusID: c.ID, Namespace: "date-test", RecordKey: "lower"}}); err != nil {
		t.Fatal(err)
	}
	ids := func(rows []content.Record) []string {
		result := make([]string, len(rows))
		for i, r := range rows {
			result[i] = r.ID
		}
		return result
	}
	check := func(q content.RecordQuery, want []string) []content.Record {
		t.Helper()
		rows, err := service.Records(ctx, scope, c.ID, q)
		if err != nil || fmt.Sprint(ids(rows)) != fmt.Sprint(want) {
			t.Fatalf("query %+v: records %v, want %v; err %v", q, ids(rows), want, err)
		}
		return rows
	}
	ties := []string{low.RecordID, tie.RecordID}
	sort.Sort(sort.Reverse(sort.StringSlice(ties)))
	nulls := []string{pending.RecordID, tombstone.RecordID}
	sort.Sort(sort.Reverse(sort.StringSlice(nulls)))
	want := append([]string{high.RecordID}, ties...)
	want = append(want, oldest.RecordID)
	want = append(want, nulls...)
	q := content.RecordQuery{Order: content.AcceptedAtDesc, Limit: 20}
	rows := check(q, want)
	for _, r := range rows {
		if r.Withdrawn != (r.ID == low.RecordID || r.ID == tombstone.RecordID) {
			t.Fatalf("withdrawn flag %+v", r)
		}
	}
	before := day.Add(24 * time.Hour)
	bounded := content.RecordQuery{Order: content.AcceptedAtDesc, AcceptedAfter: &day, AcceptedBefore: &before, Limit: 20}
	check(bounded, ties)
	// PostgreSQL stores microseconds. A fractional bound must compare against
	// that stored instant, rather than being rounded down by the parameter codec.
	fraction := day.Add(time.Nanosecond)
	check(content.RecordQuery{Order: content.AcceptedAtDesc, AcceptedAfter: &fraction, Limit: 20}, []string{high.RecordID})
	check(content.RecordQuery{Order: content.AcceptedAtDesc, AcceptedBefore: &fraction, Limit: 20}, append(append([]string{}, ties...), oldest.RecordID))

	// Each bound also works alone, excluding undated Records.
	check(content.RecordQuery{Order: content.AcceptedAtDesc, AcceptedAfter: &day, Limit: 20}, append([]string{high.RecordID}, ties...))
	check(content.RecordQuery{Order: content.AcceptedAtDesc, AcceptedBefore: &before, Limit: 20}, append(append([]string{}, ties...), oldest.RecordID))
	for _, tc := range []struct {
		q    content.RecordQuery
		want int64
	}{
		{q, 6}, {bounded, 2}, {content.RecordQuery{AcceptedAfter: &day, AcceptedBefore: &day}, 0},
	} {
		if count, err := service.CountRecords(ctx, scope, c.ID, tc.q); err != nil || count != tc.want {
			t.Fatalf("count %+v = %d, want %d; err %v", tc.q, count, tc.want, err)
		}
	}
	for _, org := range []string{scope.Organization, "adapter-catalog-foreign"} {
		corpusID := c.ID
		if org == scope.Organization {
			corpusID = other.ID
		}
		expected := int64(0)
		if org == scope.Organization {
			expected = 1
		}
		if count, err := store.CountRecords(ctx, org, corpusID, bounded); err != nil || count != expected {
			t.Fatalf("isolated count %s/%s = %d, want %d; err %v", org, corpusID, count, expected, err)
		}
	}
	// Freeze the first exclusive tuple, insert a newer Record, then finish the
	// original traversal. Neither tie nor null pages shift under that arrival.
	q.Limit = 2
	first := check(q, want[:2])
	publish(c.ID, "arrival", "new", day.Add(48*time.Hour), true)
	got := ids(first)
	last := first[len(first)-1]
	for {
		q.AfterID, q.AfterAcceptedAt = last.ID, last.CurrentAcceptedAt
		page, err := service.Records(ctx, scope, c.ID, q)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		got = append(got, ids(page)...)
		last = page[len(page)-1]
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("arrival traversal %v, want %v", got, want)
	}
	// An accepted correction dates the Record only after it becomes current.
	correction := publish(c.ID, "oldest", "corrected", day.Add(72*time.Hour), false)
	page, err := service.Records(ctx, scope, c.ID, content.RecordQuery{Order: content.AcceptedAtDesc, Limit: 1})
	if err != nil || len(page) != 1 || page[0].ID == oldest.RecordID {
		t.Fatalf("pending correction changed date: %+v, %v", page, err)
	}
	if _, err = pool.Exec(ctx, "UPDATE records SET current_version_id=$3 WHERE organization=$1 AND id=$2", scope.Organization, correction.RecordID, correction.VersionID); err != nil {
		t.Fatal(err)
	}
	page = check(content.RecordQuery{Order: content.AcceptedAtDesc, Limit: 1}, []string{oldest.RecordID})
	if page[0].CurrentAcceptedAt == nil || !page[0].CurrentAcceptedAt.Equal(day.Add(72*time.Hour)) {
		t.Fatalf("correction date %+v", page[0])
	}
	if count, err := service.CountRecords(ctx, scope, c.ID, content.RecordQuery{AcceptedBefore: &day}); err != nil || count != 0 {
		t.Fatalf("old correction range count %d, %v", count, err)
	}
}
