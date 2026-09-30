package observability

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// The p50 and p95 the admin reads report come from the fixed buckets; the
// expected values are worked by hand from the bucket bounds.
func TestQuantileInterpolatesInsideTheBucket(t *testing.T) {
	var (
		one      = [Buckets]int64{3: 10}          // ten events in (25, 50] ms
		spread   = [Buckets]int64{1, 1, 2}        // [0,5], (5,10], (10,25]x2
		overflow = [Buckets]int64{Buckets - 1: 4} // all slower than 30 s
		empty    [Buckets]int64
	)
	for _, c := range []struct {
		name    string
		buckets [Buckets]int64
		q, want float64
		ok      bool
	}{
		{"p50 in one bucket", one, 0.5, 37.5, true},
		{"p95 in one bucket", one, 0.95, 48.75, true},
		{"p50 on a bucket edge", spread, 0.5, 10, true},
		{"p95 across buckets", spread, 0.95, 23.5, true},
		{"overflow is the last bound", overflow, 0.95, 30000, true},
		{"nothing counted", empty, 0.5, 0, false},
	} {
		got, ok := Quantile(c.q, c.buckets[:])
		if got != c.want || ok != c.ok {
			t.Errorf("%s: Quantile(%v) = %v, %v; want %v, %v", c.name, c.q, got, ok, c.want, c.ok)
		}
	}
}

// memoryStore keeps flushed rows as written; the SQL merge of rows of the
// same bucket is the PostgreSQL adapter's contract.
type memoryStore struct {
	rows []Row
	fail int
}

func (s *memoryStore) UpsertRollups(_ context.Context, rows []Row) error {
	if s.fail > 0 {
		s.fail--
		return errors.New("database unavailable")
	}
	s.rows = append(s.rows, rows...)
	return nil
}

func (s *memoryStore) PruneRollups(context.Context, time.Duration, time.Time) (int64, error) {
	return 0, nil
}

func (s *memoryStore) ReadRollups(_ context.Context, org, series string, resolution time.Duration, from time.Time, keys ...string) ([]Row, error) {
	var out []Row
	for _, r := range s.rows {
		if r.Organization == org && r.Series == series && r.Resolution == resolution && !r.Start.Before(from) && (len(keys) == 0 || slices.Contains(keys, r.Key)) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Start.Before(out[j].Start)
	})
	return out, nil
}

// TopKeys ranks keys the way the PostgreSQL adapter's contract says: count
// then key, total and number over every key.
func (s *memoryStore) TopKeys(ctx context.Context, org, series string, resolution time.Duration, from time.Time, limit int) (Ranking, error) {
	rows, _ := s.ReadRollups(ctx, org, series, resolution, from)
	sums := map[string]int64{}
	var out Ranking
	for _, r := range rows {
		sums[r.Key] += r.Count
		out.Total += r.Count
	}
	for k, n := range sums {
		out.Keys = append(out.Keys, KeyCount{Key: k, Count: n})
	}
	sort.Slice(out.Keys, func(i, j int) bool {
		if out.Keys[i].Count != out.Keys[j].Count {
			return out.Keys[i].Count > out.Keys[j].Count
		}
		return out.Keys[i].Key < out.Keys[j].Key
	})
	out.Distinct = len(out.Keys)
	out.Keys = out.Keys[:min(limit, len(out.Keys))]
	return out, nil
}

// rowsOf sums the flushed rows of one series, key and resolution: events
// recorded across a bucket boundary land in two rows.
func (s *memoryStore) rowsOf(series, key string, resolution time.Duration) (total Row, rows int) {
	for _, r := range s.rows {
		if r.Series == series && r.Key == key && r.Resolution == resolution {
			total.merge(r)
			rows++
		}
	}
	return total, rows
}

func TestRecorderAggregatesEventsIntoEveryTier(t *testing.T) {
	store := &memoryStore{}
	r := NewRecorder(store, Config{}, false)
	call := PluginCall{Organization: "org_a", Plugin: "core.ingest", Version: "1.0.0", Operation: "embed_query"}
	ok, failed := call, call
	ok.Duration = 30 * time.Millisecond
	failed.Duration, failed.ErrorCode = 3*time.Second, "plugin_unavailable"
	r.PluginCall(ok)
	r.PluginCall(failed)
	r.Search(Search{Organization: "org_a", Mode: "hybrid", Profile: "default", Query: "  Secret   Plans ", Results: 3, Duration: time.Millisecond})
	r.Received("org_a", "news-feed")
	r.Received("org_a", "news-feed")
	r.Matched("org_a", "keywords")
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	key := Key("core.ingest", "1.0.0", "embed_query")
	for _, tier := range Tiers {
		got, _ := store.rowsOf(SeriesPluginCall, key, tier.Resolution)
		if got.Count != 2 || got.Errors != 1 || got.LastErrorCode != "plugin_unavailable" || got.Buckets[3] != 1 || got.Buckets[9] != 1 {
			t.Fatalf("tier %s: plugin call row = %+v; want 2 calls, 1 error, one in (25,50] ms and one in (2.5,5] s", tier.Resolution, got)
		}
		search, _ := store.rowsOf(SeriesSearch, Key("hybrid", "default"), tier.Resolution)
		if search.Count != 1 || search.Items != 3 || search.Errors != 0 {
			t.Fatalf("tier %s: search row = %+v; want 1 search with 3 results", tier.Resolution, search)
		}
		if received, _ := store.rowsOf(SeriesReceived, "news-feed", tier.Resolution); received.Count != 2 {
			t.Fatalf("tier %s: received row = %+v; want 2 documents", tier.Resolution, received)
		}
		if matched, _ := store.rowsOf(SeriesMatch, "keywords", tier.Resolution); matched.Count != 1 {
			t.Fatalf("tier %s: match row = %+v; want 1 Match", tier.Resolution, matched)
		}
	}
	for _, row := range store.rows {
		if row.Series == SeriesSearchQuery {
			t.Fatalf("query text stored while record_query_text is off: %+v", row)
		}
	}
}

func TestRecorderCountsNormalizedQueriesOnlyWhenEnabled(t *testing.T) {
	store := &memoryStore{}
	r := NewRecorder(store, Config{RecordQueryText: true}, false)
	for _, q := range []string{"  Secret   Plans ", "secret plans", "other"} {
		r.Search(Search{Organization: "org_a", Mode: "lexical", Profile: "default", Query: q})
	}
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.rowsOf(SeriesSearchQuery, "secret plans", QueryTier.Resolution)
	if got.Count != 2 {
		t.Fatalf("normalized query count = %d; want 2 (rows %+v)", got.Count, store.rows)
	}
	for _, row := range store.rows {
		if row.Series == SeriesSearchQuery && row.Resolution != QueryTier.Resolution {
			t.Fatalf("query text written at %s; want only the %s tier", row.Resolution, QueryTier.Resolution)
		}
	}
}

// Source namespaces come from clients: a burst of new ones between two
// flushes stops at maxPendingKeys per Organization, while keys already
// buffered keep counting and the next flush takes new keys again.
func TestRecorderCapsNewSourceNamespacesPerFlush(t *testing.T) {
	store := &memoryStore{}
	r := NewRecorder(store, Config{}, false)
	for i := range maxPendingKeys + 5 {
		r.Received("org_a", fmt.Sprintf("source-%d", i))
	}
	r.Received("org_a", "source-0")
	r.Received("org_b", "own-source")
	var metrics strings.Builder
	r.WriteMetrics(&metrics)
	if !strings.Contains(metrics.String(), "quivr_observability_dropped_events_total 5\n") {
		t.Fatalf("dropped events not counted:\n%s", metrics.String())
	}
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.Received("org_a", "late-source")
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	keys := map[string]int64{}
	for _, row := range store.rows {
		if row.Series == SeriesReceived && row.Resolution == time.Minute {
			keys[row.Key] += row.Count
		}
	}
	if len(keys) != maxPendingKeys+2 || keys["source-0"] != 2 || keys["late-source"] != 1 || keys["own-source"] != 1 {
		t.Fatalf("flushed %d namespaces, source-0=%d, late-source=%d, own-source=%d; want %d, 2, 1 and 1 (another Organization is not capped)",
			len(keys), keys["source-0"], keys["late-source"], keys["own-source"], maxPendingKeys+2)
	}
}

// A database outage must not lose the buffered counts: they are written by
// the next flush that succeeds.
func TestRecorderKeepsCountsAcrossAFailedFlush(t *testing.T) {
	store := &memoryStore{fail: 1}
	r := NewRecorder(store, Config{}, false)
	r.Step("org_a", "baseline", time.Second, "")
	if err := r.Flush(context.Background()); err == nil {
		t.Fatal("first flush succeeded; want the store failure")
	}
	r.Step("org_a", "baseline", time.Second, "baseline_unavailable")
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.rowsOf(SeriesStep, "baseline", time.Minute)
	if got.Count != 2 || got.Errors != 1 {
		t.Fatalf("after a failed then a successful flush: %+v; want 2 steps, 1 error", got)
	}
}

func TestReportSummarizesBucketsOfTheWindowTier(t *testing.T) {
	start := time.Now().UTC().Truncate(time.Minute)
	older := Row{Organization: "org_a", Series: SeriesSearch, Key: Key("hybrid", "default"), Resolution: time.Minute, Start: start.Add(-2 * time.Minute),
		Count: 2, Errors: 1, DurationSumMS: 60, LastErrorCode: "search_unavailable", LastErrorAt: start.Add(-90 * time.Second)}
	older.Buckets[3] = 2
	newer := Row{Organization: "org_a", Series: SeriesSearch, Key: Key("hybrid", "default"), Resolution: time.Minute, Start: start, Count: 2, DurationSumMS: 1000}
	newer.Buckets[6] = 2
	outside := newer
	outside.Start = start.Add(-3 * time.Hour)
	otherTier := newer
	otherTier.Resolution = 15 * time.Minute
	store := &memoryStore{rows: []Row{newer, older, outside, otherTier}}
	report, err := Reader{Store: store}.Report(context.Background(), "org_a", SeriesSearch, Windows[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Series) != 1 || len(report.Series[0].Points) != 2 {
		t.Fatalf("report = %+v; want one series with the two buckets of the last hour", report)
	}
	s := report.Series[0]
	if !s.Points[0].Start.Equal(older.Start) || s.Points[0].P50MS != 37.5 {
		t.Fatalf("first point = %+v; want the older bucket with p50 37.5 ms", s.Points[0])
	}
	// Four searches: two in (25,50] ms and two in (250,500] ms.
	want := Summary{Count: 4, Errors: 1, MeanMS: 265, P50MS: 50, P95MS: 475, LastErrorCode: "search_unavailable", LastErrorAt: older.LastErrorAt}
	if s.Summary != want {
		t.Fatalf("summary = %+v; want %+v", s.Summary, want)
	}
}

// A counted read lists the largest keys with their buckets, and totals every
// key; query text is read from its hourly tier, and not at all when off.
func TestCountsListLargestKeysAndTotalEveryKey(t *testing.T) {
	now := time.Now().UTC()
	row := func(series, key string, resolution time.Duration, ago time.Duration, count int64) Row {
		return Row{Organization: "org_a", Series: series, Key: key, Resolution: resolution, Start: now.Add(-ago).Truncate(resolution), Count: count}
	}
	store := &memoryStore{rows: []Row{
		row(SeriesReceived, "news-feed", 15*time.Minute, 3*time.Hour, 4),
		row(SeriesReceived, "news-feed", 15*time.Minute, 0, 5),
		row(SeriesReceived, "api-uploads", 15*time.Minute, 0, 3),
		row(SeriesReceived, "archive", 15*time.Minute, 0, 1),
		row(SeriesReceived, "news-feed", time.Minute, 0, 100),
		row(SeriesSearchQuery, "solar energy", time.Hour, 2*time.Hour, 2),
		row(SeriesSearchQuery, "solar energy", time.Hour, 0, 1),
	}}
	day, _ := ParseWindow("24h")
	got, err := Reader{Store: store}.Counts(context.Background(), "org_a", SeriesReceived, day, 2)
	if err != nil {
		t.Fatal(err)
	}
	// Ranking and totals are the store's contract (adapter test); the read
	// joins each listed key to its buckets of the window's tier.
	if got.Resolution != 15*time.Minute || len(got.Series) != 2 || got.Series[0].Key != "news-feed" || len(got.Series[0].Points) != 2 ||
		got.Series[1].Key != "api-uploads" || len(got.Series[1].Points) != 1 {
		t.Fatalf("received counts = %+v; want news-feed with its 2 quarter-hour buckets, then api-uploads with 1", got)
	}
	top, err := Reader{Store: store, RecordQueryText: true}.TopQueries(context.Background(), "org_a", day, 20)
	if err != nil {
		t.Fatal(err)
	}
	if top.Resolution != time.Hour || len(top.Series) != 1 || top.Series[0].Count != 3 || len(top.Series[0].Points) != 2 {
		t.Fatalf("top queries = %+v; want solar energy, 3 searches in 2 hourly buckets", top)
	}
	if off, _ := (Reader{Store: store}).TopQueries(context.Background(), "org_a", day, 20); len(off.Series) != 0 {
		t.Fatalf("top queries with recording off = %+v; want none", off)
	}
}
