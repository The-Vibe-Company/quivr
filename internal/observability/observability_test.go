package observability

import (
	"context"
	"errors"
	"sort"
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

func (s *memoryStore) ReadRollups(_ context.Context, org, series string, resolution time.Duration, from time.Time) ([]Row, error) {
	var out []Row
	for _, r := range s.rows {
		if r.Organization == org && r.Series == series && r.Resolution == resolution && !r.Start.Before(from) {
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

func (s *memoryStore) TopKeys(context.Context, string, string, time.Duration, time.Time, int) ([]KeyCount, error) {
	return nil, nil
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
