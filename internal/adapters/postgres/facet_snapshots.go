package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
)

// Fast facet counting (THE-1387). Exact aggregation reads every current
// document and takes tens of seconds on a million of them. A fast request is
// answered, in order, from:
//   - each Corpus's stored snapshot of its unfiltered counts, labelled with
//     the time it was taken, when the request has no predicate, or only
//     whole UTC days of the date field it counts;
//   - an exact count, when it ends within a short attempt;
//   - a uniform sample of the Corpora's Records, scaled and labelled.
//
// Reading a snapshot that is missing or old refreshes it in the background,
// at most once at a time per process and per Corpus across processes.
const (
	facetExactAttempt = time.Second
	// About this many Records are counted in a sample.
	facetSampleRecords = 50000
	// A snapshot keeps this many values of a field, and days of a date field.
	facetSnapshotValues = 1000
	facetSnapshotDays   = 100000
	// A snapshot is refreshed once older than ten times its last refresh,
	// and never more than once a minute: upkeep costs at most a tenth of one
	// connection per Corpus being read.
	facetSnapshotMinAge = time.Minute
	facetSnapshotCost   = 10
	// A refresh that fails keeps its lease until it lapses: the back-off.
	facetSnapshotDeadline = 5 * time.Minute
	facetSnapshotLease    = 6 * time.Minute
	// Record IDs are this prefix and a SHA-256 in hex: a range of IDs is a
	// uniform sample, read from the catalog index.
	recordIDPrefix = "record_"
	// SQLSTATE of a statement stopped by statement_timeout.
	queryCanceled = "57014"
)

var _ content.FastFacetReader = RecordStore{}

// One snapshot refresh at a time in this process.
var facetRefreshes = make(chan struct{}, 1)

type facetSnapshot struct {
	Fields map[string]snapshotField `json:"fields"`
}
type snapshotField struct {
	Type string `json:"type"`
	// Complete when the field has fewer values than the snapshot keeps.
	Complete bool        `json:"complete"`
	Buckets  []rawBucket `json:"buckets"`
}

// dayRange is the whole UTC days a timeline request keeps; zero is unbounded.
type dayRange struct{ from, to time.Time }

func (s RecordStore) CountFacetsFast(ctx context.Context, org string, q content.FacetQuery) (content.FacetCounts, error) {
	if days, ok := snapshotScope(q); ok && len(q.Records.FilterRoutes) > 0 {
		counts, ok, err := s.snapshotFacets(ctx, org, q, days)
		if err != nil || ok {
			return counts, err
		}
	}
	counted, err := s.boundedFacetRows(ctx, org, q, "", facetExactAttempt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == queryCanceled && ctx.Err() == nil {
		return s.sampleFacets(ctx, org, q)
	}
	if err != nil {
		return content.FacetCounts{}, err
	}
	items, err := decodeFacets(q.Fields, counted)
	return content.FacetCounts{Items: items}, err
}

// boundedFacetRows counts in a read-only transaction whose statement
// PostgreSQL stops itself after limit. A cancelled context only closes the
// client's connection, and the server would finish the scan regardless.
func (s RecordStore) boundedFacetRows(ctx context.Context, org string, q content.FacetQuery, below string, limit time.Duration) ([][]rawBucket, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err = tx.Exec(ctx, `SELECT set_config('statement_timeout',$1,true)`, fmt.Sprint(max(limit.Milliseconds(), 1))); err != nil {
		return nil, err
	}
	return facetRows(ctx, tx, org, q, below)
}

// snapshotScope says whether snapshots can answer the request exactly: no
// predicate, or only whole UTC days of the single date field it counts.
func snapshotScope(q content.FacetQuery) (dayRange, bool) {
	var days dayRange
	if len(q.SourceNamespaces) > 0 || q.Records.AcceptedAfter != nil || q.Records.AcceptedBefore != nil {
		return days, false
	}
	for i, route := range q.Records.FilterRoutes {
		if len(route.Filters) == 0 {
			if i > 0 && days != (dayRange{}) {
				return days, false
			}
			continue
		}
		f := route.Filters[0]
		if len(route.Filters) > 1 || len(q.Fields) != 1 || q.Fields[0].Type != "datetime" || f.Field != q.Fields[0].Field || len(f.AnyOf) > 0 {
			return days, false
		}
		var r dayRange
		var ok bool
		if r.from, ok = wholeDay(f.Gte, 0); !ok {
			return days, false
		}
		if r.to, ok = wholeDay(f.Lte, time.Millisecond); !ok {
			return days, false
		}
		if i > 0 && r != days {
			return days, false
		}
		days = r
	}
	return days, true
}

// wholeDay reads a bound that lies `before` a UTC midnight: filter dates and
// projected dates are kept to the millisecond, so a day ends 1 ms before the next.
func wholeDay(bound string, before time.Duration) (time.Time, bool) {
	if bound == "" {
		return time.Time{}, true
	}
	t, err := time.Parse(time.RFC3339Nano, bound)
	if err != nil {
		return time.Time{}, false
	}
	t = t.UTC().Add(before)
	day := t.Truncate(24 * time.Hour)
	return day.Add(-before).Truncate(24 * time.Hour), day.Equal(t)
}

// snapshotFacets merges the routed Corpora's snapshots. It answers only when
// every Corpus has one of its routed generation and, across several Corpora,
// every field kept all its values; it refreshes those missing or old.
func (s RecordStore) snapshotFacets(ctx context.Context, org string, q content.FacetQuery, days dayRange) (content.FacetCounts, bool, error) {
	routes := map[string]string{}
	ids := []string{}
	for _, r := range q.Records.FilterRoutes {
		routes[r.CorpusID] = r.GenerationID
		ids = append(ids, r.CorpusID)
	}
	rows, err := database(ctx, s.Pool).Query(ctx, `SELECT c.id,c.archived,s.generation_id,s.data,s.taken_at,
 s.taken_at < now()-greatest($3::float8*interval '1 second',s.duration_ms*$4::float8*interval '1 millisecond')
 FROM corpora c LEFT JOIN facet_snapshots s ON s.organization=c.organization AND s.corpus_id=c.id AND s.data IS NOT NULL
 WHERE c.organization=$1 AND c.id=ANY($2::text[])`, org, ids, facetSnapshotMinAge.Seconds(), float64(facetSnapshotCost))
	if err != nil {
		return content.FacetCounts{}, false, err
	}
	snapshots := []facetSnapshot{}
	var asOf *time.Time
	usable := true
	refresh := []string{}
	for rows.Next() {
		var id string
		var archived bool
		var generation *string
		var data []byte
		var taken *time.Time
		var old *bool
		if err = rows.Scan(&id, &archived, &generation, &data, &taken, &old); err != nil {
			rows.Close()
			return content.FacetCounts{}, false, err
		}
		// An archived Corpus counts nothing, as in a live count.
		if archived {
			continue
		}
		if generation == nil || *generation != routes[id] {
			usable = false
			refresh = append(refresh, id)
			continue
		}
		var snap facetSnapshot
		if err = json.Unmarshal(data, &snap); err != nil {
			rows.Close()
			return content.FacetCounts{}, false, err
		}
		if *old {
			refresh = append(refresh, id)
		}
		snapshots = append(snapshots, snap)
		if asOf == nil || taken.Before(*asOf) {
			asOf = taken
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return content.FacetCounts{}, false, err
	}
	stale := []snapshotTarget{}
	for _, id := range refresh {
		if declared, ok := q.Declared[id]; ok {
			stale = append(stale, snapshotTarget{corpusID: id, generation: routes[id], declared: declared})
		}
	}
	if len(stale) > 0 {
		s.refreshLater(ctx, org, stale)
	}
	if !usable {
		return content.FacetCounts{}, false, nil
	}
	merged := make([][]rawBucket, len(q.Fields))
	for i, f := range q.Fields {
		var ok bool
		if merged[i], ok = mergeSnapshots(snapshots, f, days); !ok {
			return content.FacetCounts{}, false, nil
		}
	}
	items, err := decodeFacets(q.Fields, merged)
	return content.FacetCounts{Items: items, AsOf: asOf}, err == nil, err
}

// mergeSnapshots selects a field's buckets as the live aggregation does:
// the most frequent first, ties by JSON text in byte order, and date periods
// then in time order.
func mergeSnapshots(snapshots []facetSnapshot, f content.FacetField, days dayRange) ([]rawBucket, bool) {
	counts := map[string]int64{}
	for _, snap := range snapshots {
		field, ok := snap.Fields[f.Field]
		// Merged values, and days summed into periods or kept by a window,
		// are exact only when every value was kept.
		if !ok || field.Type != f.Type || (!field.Complete && (len(snapshots) > 1 || f.Type == "datetime")) {
			return nil, false
		}
		for _, b := range field.Buckets {
			key := string(b.Value)
			if f.Type == "datetime" {
				var ok bool
				if key, ok = period(b.Value, f.Interval, days); !ok {
					return nil, false
				}
				if key == "" {
					continue
				}
			}
			counts[key] += b.Count
		}
	}
	out := make([]rawBucket, 0, len(counts))
	for key, n := range counts {
		out = append(out, rawBucket{Value: json.RawMessage(key), Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return bytes.Compare(out[i].Value, out[j].Value) < 0
	})
	if len(out) > f.Limit {
		out = out[:f.Limit]
	}
	if f.Type == "datetime" {
		sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i].Value, out[j].Value) < 0 })
	}
	return out, true
}

// period is the JSON text of the interval a day bucket falls in, or "" when
// the day lies outside the range kept.
func period(raw json.RawMessage, interval string, days dayRange) (string, bool) {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	day, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "", false
	}
	if !days.from.IsZero() && day.Before(days.from) || !days.to.IsZero() && day.After(days.to) {
		return "", true
	}
	switch interval {
	case "month":
		day = time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
	case "year":
		day = time.Date(day.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	}
	text, _ := json.Marshal(day.Format("2006-01-02T15:04:05Z"))
	return string(text), true
}

type snapshotTarget struct {
	corpusID, generation string
	declared             []content.FacetField
}

// refreshLater refreshes, one after another, those of the Corpora whose
// lease it takes. It outlives the request but not the process's grace.
func (s RecordStore) refreshLater(ctx context.Context, org string, targets []snapshotTarget) {
	if _, admitted := lifecycle.Admit(ctx); !admitted {
		return
	}
	select {
	case facetRefreshes <- struct{}{}:
	default:
		return
	}
	work, cancel := lifecycle.CleanupContext(ctx, time.Duration(len(targets))*facetSnapshotDeadline)
	go func() {
		defer func() { <-facetRefreshes }()
		defer cancel()
		for _, t := range targets {
			if err := s.refreshFacetSnapshot(work, org, t.corpusID, t.generation, t.declared); err != nil {
				slog.WarnContext(work, "facet snapshot refresh failed; fast counts fall back to samples", "event", "quivr.facets.snapshot_failed", "corpus_id", t.corpusID, "error", err)
			}
		}
	}()
}

// refreshFacetSnapshot takes the Corpus's refresh lease when its snapshot is
// still missing or old, counts every declared field without predicates, and
// stores the counts with the time the count started.
func (s RecordStore) refreshFacetSnapshot(ctx context.Context, org, corpusID, generation string, declared []content.FacetField) error {
	var started time.Time
	err := s.Pool.QueryRow(ctx, `INSERT INTO facet_snapshots(organization,corpus_id,refreshing_until) VALUES($1,$2,now()+$4::float8*interval '1 second')
 ON CONFLICT(organization,corpus_id) DO UPDATE SET refreshing_until=EXCLUDED.refreshing_until
 WHERE (facet_snapshots.refreshing_until IS NULL OR facet_snapshots.refreshing_until<now())
 AND (facet_snapshots.data IS NULL OR facet_snapshots.generation_id<>$3
  OR facet_snapshots.taken_at<now()-greatest($5::float8*interval '1 second',facet_snapshots.duration_ms*$6::float8*interval '1 millisecond'))
 RETURNING clock_timestamp()`, org, corpusID, generation, facetSnapshotLease.Seconds(), facetSnapshotMinAge.Seconds(), float64(facetSnapshotCost)).Scan(&started)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	fields := append([]content.FacetField(nil), declared...)
	for i := range fields {
		fields[i].Limit, fields[i].Interval = facetSnapshotValues, ""
		if fields[i].Type == "datetime" {
			fields[i].Limit, fields[i].Interval = facetSnapshotDays, "day"
		}
	}
	q := content.FacetQuery{Records: content.RecordQuery{CorpusIDs: []string{corpusID}, FilterRoutes: []content.CatalogFilterRoute{{CorpusID: corpusID, GenerationID: generation}}}, Fields: fields}
	counted, err := s.boundedFacetRows(ctx, org, q, "", facetSnapshotDeadline)
	if err != nil {
		return err
	}
	snap := facetSnapshot{Fields: map[string]snapshotField{}}
	for i, f := range fields {
		snap.Fields[f.Field] = snapshotField{Type: f.Type, Complete: len(counted[i]) < f.Limit, Buckets: append([]rawBucket{}, counted[i]...)}
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `UPDATE facet_snapshots SET generation_id=$3,data=$4,taken_at=$5,duration_ms=$6,refreshing_until=NULL WHERE organization=$1 AND corpus_id=$2`,
		org, corpusID, generation, data, started, time.Since(started).Milliseconds())
	return err
}

// sampleFacets counts the Records whose ID falls in a range sized for about
// facetSampleRecords of them, and scales the counts up.
func (s RecordStore) sampleFacets(ctx context.Context, org string, q content.FacetQuery) (content.FacetCounts, error) {
	ids := []string{}
	for _, r := range q.Records.FilterRoutes {
		ids = append(ids, r.CorpusID)
	}
	// 1/256 of the IDs tells the Corpora's size.
	var probe int64
	if err := database(ctx, s.Pool).QueryRow(ctx, `SELECT count(*) FROM records WHERE organization=$1 AND corpus_id=ANY($2::text[]) AND id COLLATE "C">=$3 AND id COLLATE "C"<$4`,
		org, ids, recordIDPrefix, recordIDPrefix+"01").Scan(&probe); err != nil {
		return content.FacetCounts{}, err
	}
	const span = 1 << 16
	bound := span
	if probe > 0 {
		bound = int(math.Ceil(float64(facetSampleRecords) * span / 256 / float64(probe)))
	}
	// The rest of the request's time bounds the count on the server too.
	limit := facetSnapshotDeadline
	if deadline, ok := ctx.Deadline(); ok {
		limit = time.Until(deadline)
	}
	below := ""
	if bound < span {
		below = recordIDPrefix + fmt.Sprintf("%04x", bound)
	}
	counted, err := s.boundedFacetRows(ctx, org, q, below, limit)
	if err != nil {
		return content.FacetCounts{}, err
	}
	if below == "" {
		items, err := decodeFacets(q.Fields, counted)
		return content.FacetCounts{Items: items}, err
	}
	fraction := float64(bound) / span
	for _, buckets := range counted {
		for i := range buckets {
			buckets[i].Count = int64(math.Round(float64(buckets[i].Count) / fraction))
		}
	}
	items, err := decodeFacets(q.Fields, counted)
	return content.FacetCounts{Items: items, SampleFraction: fraction}, err
}
