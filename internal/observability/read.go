package observability

import (
	"context"
	"time"
)

// Reader answers the admin stats reads from the rollup rows.
type Reader struct {
	Store Store
	// RecordQueryText mirrors the deployment setting: when off, no query
	// text is listed.
	RecordQueryText bool
}

// Summary aggregates a series over a window. Latencies are in milliseconds
// and meaningful only when Count is positive.
type Summary struct {
	Count, Errors, Items int64
	// OverObjective counts the searches slower than their profile's
	// latency objective.
	OverObjective        int64
	P50MS, P95MS, MeanMS float64
	LastErrorCode        string
	LastErrorAt          time.Time
}

// Point is one bucket of a series.
type Point struct {
	Start         time.Time
	Count, Errors int64
	P50MS, P95MS  float64
}

// Series is one key of a series over a window: its summary and its
// non-empty buckets, oldest first.
type Series struct {
	Key     string
	Summary Summary
	Points  []Point
}

// Report is one series read over a window.
type Report struct {
	Window   Window
	From, To time.Time
	Series   []Series
}

// Report reads the requested keys of series for org over the window, from
// the tier that serves it. With no keys it reads every key.
func (r Reader) Report(ctx context.Context, org, series string, w Window, keys ...string) (Report, error) {
	to := time.Now().UTC()
	from := to.Add(-w.Span).Truncate(w.Tier.Resolution)
	out := Report{Window: w, From: from, To: to, Series: []Series{}}
	rows, err := r.Store.ReadRollups(ctx, org, series, w.Tier.Resolution, from, keys...)
	if err != nil {
		return Report{}, err
	}
	var current *Series
	var total Row
	closeSeries := func() {
		if current != nil {
			current.Summary = summarize(total)
			out.Series = append(out.Series, *current)
		}
	}
	for _, row := range rows {
		if current == nil || current.Key != row.Key {
			closeSeries()
			current, total = &Series{Key: row.Key, Points: []Point{}}, Row{}
		}
		total.merge(row)
		s := summarize(row)
		current.Points = append(current.Points, Point{Start: row.Start, Count: row.Count, Errors: row.Errors, P50MS: s.P50MS, P95MS: s.P95MS})
	}
	closeSeries()
	return out, nil
}

// CountPoint is one non-empty bucket of a counted key.
type CountPoint struct {
	Start time.Time
	Count int64
}

// CountSeries is one key of a counted series: its count over the window and
// its non-empty buckets, oldest first.
type CountSeries struct {
	Key    string
	Count  int64
	Points []CountPoint
}

// Counts is the largest keys of a series over a window with their buckets,
// and the count and number of every key, listed or not.
type Counts struct {
	Window     Window
	Resolution time.Duration
	From, To   time.Time
	Total      int64
	Distinct   int
	Series     []CountSeries
}

// Counts reads the limit largest keys of series for org over the window,
// with their buckets. Query text is counted at its own hourly tier. A read
// touches the buckets of at most limit keys, however many keys clients
// created.
func (r Reader) Counts(ctx context.Context, org, series string, w Window, limit int) (Counts, error) {
	tier := w.Tier
	if series == SeriesSearchQuery {
		tier = QueryTier
	}
	to := time.Now().UTC()
	out := Counts{Window: w, Resolution: tier.Resolution, From: to.Add(-w.Span).Truncate(tier.Resolution), To: to, Series: []CountSeries{}}
	ranking, err := r.Store.TopKeys(ctx, org, series, tier.Resolution, out.From, limit)
	if err != nil || len(ranking.Keys) == 0 {
		return out, err
	}
	out.Total, out.Distinct = ranking.Total, ranking.Distinct
	keys := make([]string, len(ranking.Keys))
	index := map[string]int{}
	for i, k := range ranking.Keys {
		keys[i], index[k.Key] = k.Key, i
		out.Series = append(out.Series, CountSeries{Key: k.Key, Count: k.Count, Points: []CountPoint{}})
	}
	rows, err := r.Store.ReadRollups(ctx, org, series, tier.Resolution, out.From, keys...)
	if err != nil {
		return Counts{}, err
	}
	for _, row := range rows {
		if i, ok := index[row.Key]; ok {
			out.Series[i].Points = append(out.Series[i].Points, CountPoint{Start: row.Start, Count: row.Count})
		}
	}
	return out, nil
}

// TopQueries lists the most frequent normalized queries of org over the
// window, at most limit, with their hourly counts. It is empty when query
// text is not recorded.
func (r Reader) TopQueries(ctx context.Context, org string, w Window, limit int) (Counts, error) {
	if !r.RecordQueryText {
		return Counts{Window: w, Resolution: QueryTier.Resolution, Series: []CountSeries{}}, nil
	}
	return r.Counts(ctx, org, SeriesSearchQuery, w, limit)
}

func summarize(r Row) Summary {
	s := Summary{Count: r.Count, Errors: r.Errors, Items: r.Items, OverObjective: r.OverObjective, LastErrorCode: r.LastErrorCode, LastErrorAt: r.LastErrorAt}
	if r.Count > 0 {
		s.MeanMS = r.DurationSumMS / float64(r.Count)
		s.P50MS, _ = Quantile(0.5, r.Buckets[:])
		s.P95MS, _ = Quantile(0.95, r.Buckets[:])
	}
	return s
}
