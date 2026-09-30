// Package observability counts and times plugin calls, searches and
// processing steps as they happen (THE-795). Events are aggregated in memory
// and flushed every few seconds as rollup rows: one row per Organization,
// series, key and time bucket, carrying a count, an error count, a latency
// sum, fixed latency buckets and the last error. Reads never replay history:
// they read at most about a hundred buckets per key from the resolution tier
// that matches the window.
package observability

import (
	"math"
	"strings"
	"time"
)

// Series of the rollup table.
const (
	// SeriesPluginCall is one Contribution invocation; its key is plugin id,
	// plugin version and operation.
	SeriesPluginCall = "plugin_call"
	// SeriesSearch is one public search; its key is mode and profile.
	SeriesSearch = "search"
	// SeriesStep is one processing step; its key is the step name.
	SeriesStep = "step"
	// SeriesSearchQuery counts searches by normalized query text. It is
	// recorded only when the deployment enables record_query_text.
	SeriesSearchQuery = "search_query"
	// SeriesReceived is one document received: a command that reserved a new
	// revision of a Record. Its key is the source namespace (THE-798).
	SeriesReceived = "received"
	// SeriesMatch is one Match committed by an alert; its key is the
	// evaluator plugin id (THE-798).
	SeriesMatch = "match"
)

// BoundsMS are the upper bounds, in milliseconds, of the fixed latency
// buckets. A last overflow bucket counts slower events.
var BoundsMS = [...]float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000}

// Buckets is the number of latency buckets of a row, overflow included.
const Buckets = len(BoundsMS) + 1

// bucketOf is the index of the bucket that counts a duration of ms.
func bucketOf(ms float64) int {
	for i, b := range BoundsMS {
		if ms <= b {
			return i
		}
	}
	return len(BoundsMS)
}

// Quantile estimates the q-quantile (0 < q < 1), in milliseconds, of the
// durations counted in buckets, interpolating linearly inside the bucket
// that holds it as Prometheus histogram_quantile does. A quantile in the
// overflow bucket is the last finite bound. ok is false when nothing was
// counted.
func Quantile(q float64, buckets []int64) (ms float64, ok bool) {
	var total int64
	for _, n := range buckets {
		total += n
	}
	if total == 0 {
		return 0, false
	}
	rank := q * float64(total)
	var seen int64
	for i, n := range buckets {
		if n == 0 || float64(seen+n) < rank {
			seen += n
			continue
		}
		if i >= len(BoundsMS) {
			return BoundsMS[len(BoundsMS)-1], true
		}
		lower := 0.0
		if i > 0 {
			lower = BoundsMS[i-1]
		}
		return lower + (BoundsMS[i]-lower)*(rank-float64(seen))/float64(n), true
	}
	return BoundsMS[len(BoundsMS)-1], true
}

// Tier is one resolution of the rollup table and how long its rows are kept.
type Tier struct {
	Resolution, Retention time.Duration
}

// Tiers are the resolutions every plugin call, search and step is written
// at. Each window reads one tier, so a read touches at most about a hundred
// buckets per key whatever the traffic.
var Tiers = []Tier{
	{Resolution: time.Minute, Retention: 2 * time.Hour},
	{Resolution: 15 * time.Minute, Retention: 25 * time.Hour},
	{Resolution: 2 * time.Hour, Retention: 7 * 24 * time.Hour},
}

// QueryTier is the only resolution query text is counted at.
var QueryTier = Tier{Resolution: time.Hour, Retention: 7 * 24 * time.Hour}

// Window is a read window and the tier that serves it.
type Window struct {
	Name string
	Span time.Duration
	Tier Tier
}

// Windows are the read windows, shortest first.
var Windows = []Window{
	{Name: "1h", Span: time.Hour, Tier: Tiers[0]},
	{Name: "24h", Span: 24 * time.Hour, Tier: Tiers[1]},
	{Name: "7d", Span: 7 * 24 * time.Hour, Tier: Tiers[2]},
}

// ParseWindow resolves a window name; empty is 1h.
func ParseWindow(name string) (Window, bool) {
	if name == "" {
		return Windows[0], true
	}
	for _, w := range Windows {
		if w.Name == name {
			return w, true
		}
	}
	return Window{}, false
}

// Row is one rollup bucket.
type Row struct {
	Organization, Series, Key string
	Resolution                time.Duration
	Start                     time.Time
	Count, Errors             int64
	// Items sums a per-event quantity: the result count of searches.
	Items         int64
	DurationSumMS float64
	Buckets       [Buckets]int64
	LastErrorCode string
	LastErrorAt   time.Time
}

// ID identifies a row: its primary key.
type ID struct {
	Organization, Series, Key string
	Resolution                time.Duration
	Start                     time.Time
}

// ID is the row's primary key.
func (r Row) ID() ID {
	return ID{Organization: r.Organization, Series: r.Series, Key: r.Key, Resolution: r.Resolution, Start: r.Start}
}

// merge adds o, a row of the same bucket, to r. The later error wins.
func (r *Row) merge(o Row) {
	r.Count += o.Count
	r.Errors += o.Errors
	r.Items += o.Items
	r.DurationSumMS += o.DurationSumMS
	for i := range r.Buckets {
		r.Buckets[i] += o.Buckets[i]
	}
	if !o.LastErrorAt.IsZero() && !o.LastErrorAt.Before(r.LastErrorAt) {
		r.LastErrorCode, r.LastErrorAt = o.LastErrorCode, o.LastErrorAt
	}
}

// keySeparator joins the parts of a key. Parts come from deployment
// configuration and fixed sets; a separator inside a part is replaced.
const keySeparator = "|"

// Key joins key parts.
func Key(parts ...string) string {
	clean := make([]string, len(parts))
	for i, p := range parts {
		clean[i] = strings.ReplaceAll(p, keySeparator, "_")
	}
	return strings.Join(clean, keySeparator)
}

// KeyParts splits a key into n parts; missing parts are empty.
func KeyParts(key string, n int) []string {
	parts := strings.SplitN(key, keySeparator, n)
	for len(parts) < n {
		parts = append(parts, "")
	}
	return parts
}

// maxQueryRunes bounds a recorded query.
const maxQueryRunes = 200

// NormalizeQuery is the recorded form of a query: lowercased, with runs of
// white space collapsed, trimmed and cut to 200 code points.
func NormalizeQuery(q string) string {
	q = strings.Join(strings.Fields(strings.ToLower(q)), " ")
	if r := []rune(q); len(r) > maxQueryRunes {
		q = strings.TrimSpace(string(r[:maxQueryRunes]))
	}
	return q
}

// durationMS converts d to milliseconds, never negative.
func durationMS(d time.Duration) float64 {
	return math.Max(0, float64(d)/float64(time.Millisecond))
}
