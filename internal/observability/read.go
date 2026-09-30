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

// Report reads every key of series for org over the window, from the tier
// that serves it.
func (r Reader) Report(ctx context.Context, org, series string, w Window) (Report, error) {
	to := time.Now().UTC()
	from := to.Add(-w.Span).Truncate(w.Tier.Resolution)
	out := Report{Window: w, From: from, To: to, Series: []Series{}}
	rows, err := r.Store.ReadRollups(ctx, org, series, w.Tier.Resolution, from)
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

// TopQueries lists the most frequent normalized queries of org over the
// window, at most limit. It is empty when query text is not recorded.
func (r Reader) TopQueries(ctx context.Context, org string, w Window, limit int) ([]KeyCount, error) {
	if !r.RecordQueryText {
		return []KeyCount{}, nil
	}
	from := time.Now().UTC().Add(-w.Span).Truncate(QueryTier.Resolution)
	return r.Store.TopKeys(ctx, org, SeriesSearchQuery, QueryTier.Resolution, from, limit)
}

func summarize(r Row) Summary {
	s := Summary{Count: r.Count, Errors: r.Errors, Items: r.Items, LastErrorCode: r.LastErrorCode, LastErrorAt: r.LastErrorAt}
	if r.Count > 0 {
		s.MeanMS = r.DurationSumMS / float64(r.Count)
		s.P50MS, _ = Quantile(0.5, r.Buckets[:])
		s.P95MS, _ = Quantile(0.95, r.Buckets[:])
	}
	return s
}
