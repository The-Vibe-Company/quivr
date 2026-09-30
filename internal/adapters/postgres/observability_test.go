package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/observability"
)

// Two flushes of the same bucket (the api's and the worker's, or two
// intervals) add up in PostgreSQL, bucket by bucket, and the later error
// wins whatever order the flushes arrive in. Pruning one resolution leaves
// the others and every recent row.
func TestObservabilityRollupsMergeAndPrune(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := postgres.ObservabilityStore{Pool: adapterPool(t, ctx)}
	org := fmt.Sprintf("adapter-rollups-%d", time.Now().UnixNano())
	// Far in the past, so the prune below never reaches a live row of a
	// shared verification database.
	old := time.Date(2001, 1, 1, 10, 0, 0, 0, time.UTC)
	recent := time.Now().UTC().Truncate(time.Minute)
	row := func(start time.Time, resolution time.Duration, count, errors int64, bucket int, code string, at time.Time) observability.Row {
		r := observability.Row{Organization: org, Series: observability.SeriesPluginCall, Key: observability.Key("core.ingest", "1.0.0", "embed_query"),
			Resolution: resolution, Start: start, Count: count, Errors: errors, Items: count, DurationSumMS: float64(count) * 10, LastErrorCode: code, LastErrorAt: at}
		r.Buckets[bucket] = count
		return r
	}
	later, earlier := recent.Add(20*time.Second), recent.Add(10*time.Second)
	for _, flush := range [][]observability.Row{
		{row(old, time.Minute, 1, 0, 0, "", time.Time{}), row(old, time.Hour, 1, 0, 0, "", time.Time{}), row(recent, time.Minute, 2, 1, 1, "plugin_unavailable", later)},
		{row(recent, time.Minute, 3, 1, 12, "invalid_output", earlier)},
	} {
		if err := store.UpsertRollups(ctx, flush); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.ReadRollups(ctx, org, observability.SeriesPluginCall, time.Minute, recent)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v; want the one recent minute bucket", rows)
	}
	got := rows[0]
	var want [observability.Buckets]int64
	want[1], want[12] = 2, 3
	if got.Count != 5 || got.Errors != 2 || got.Items != 5 || got.DurationSumMS != 50 || got.Buckets != want ||
		got.LastErrorCode != "plugin_unavailable" || !got.LastErrorAt.Equal(later) {
		t.Fatalf("merged row = %+v; want 5 calls, 2 errors, buckets %v and the later error plugin_unavailable", got, want)
	}
	if _, err := store.PruneRollups(ctx, time.Minute, old.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		resolution time.Duration
		from       time.Time
		want       int
	}{{time.Minute, old, 1}, {time.Hour, old, 1}} {
		rows, err := store.ReadRollups(ctx, org, observability.SeriesPluginCall, c.resolution, c.from)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != c.want {
			t.Fatalf("after pruning 1-minute rows before %s, %s rows = %+v; want %d", old.Add(time.Hour), c.resolution, rows, c.want)
		}
	}
}

// A ranking lists the largest keys only, but its total and number of keys
// cover every key; a read of chosen keys returns only their buckets. The
// counted admin reads (documents per source, top queries) rely on both to
// stay bounded however many keys clients created.
func TestObservabilityRankingTotalsEveryKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := postgres.ObservabilityStore{Pool: adapterPool(t, ctx)}
	org := fmt.Sprintf("adapter-ranking-%d", time.Now().UnixNano())
	start := time.Now().UTC().Truncate(15 * time.Minute)
	var rows []observability.Row
	for _, r := range []struct {
		key   string
		ago   time.Duration
		count int64
	}{{"news-feed", 0, 5}, {"news-feed", time.Hour, 4}, {"api-uploads", 0, 3}, {"archive", 0, 1}} {
		rows = append(rows, observability.Row{Organization: org, Series: observability.SeriesReceived, Key: r.key, Resolution: 15 * time.Minute, Start: start.Add(-r.ago), Count: r.count})
	}
	if err := store.UpsertRollups(ctx, rows); err != nil {
		t.Fatal(err)
	}
	from := start.Add(-24 * time.Hour)
	ranking, err := store.TopKeys(ctx, org, observability.SeriesReceived, 15*time.Minute, from, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := []observability.KeyCount{{Key: "news-feed", Count: 9}, {Key: "api-uploads", Count: 3}}
	if len(ranking.Keys) != 2 || ranking.Keys[0] != want[0] || ranking.Keys[1] != want[1] || ranking.Total != 13 || ranking.Distinct != 3 {
		t.Fatalf("ranking = %+v; want %v of 13 documents from 3 keys", ranking, want)
	}
	only, err := store.ReadRollups(ctx, org, observability.SeriesReceived, 15*time.Minute, from, "api-uploads", "archive")
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 2 || only[0].Key != "api-uploads" || only[1].Key != "archive" {
		t.Fatalf("rows of two keys = %+v; want api-uploads then archive", only)
	}
}
