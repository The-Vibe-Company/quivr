package postgres_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

// Owns aggregation on real storage: current eligible documents, distinct array
// members, typed values, UTC buckets and predicates before bounded top values.
func TestFacetsAcrossCorpora(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	stores := contentStores(pool)
	svc := content.Service{Submissions: stores, Receipts: stores, Materialization: stores, Facets: postgres.RecordStore{Pool: pool}}
	cs := corpus.Service{Store: postgres.Store{Pool: pool}}
	scope := corpus.Scope{Organization: fmt.Sprintf("adapter-facets-%d", time.Now().UnixNano()), Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	a, _, err := cs.Create(ctx, scope, corpus.CreateInput{Key: "a", Name: "Facets A"})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := cs.Create(ctx, scope, corpus.CreateInput{Key: "b", Name: "Facets B"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := (postgres.ProjectionStore{Pool: pool}).Generation(ctx, scope.Organization, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	publish := func(id, key, language, date string, tags []string, number float64, flag bool) string {
		t.Helper()
		return publishFaceted(t, ctx, pool, svc, scope, g.ID, id, key, map[string]any{"metadata.language": language, "metadata.tags": tags, "metadata.published_at": date, "rating": number, "flag": flag})
	}
	publish(a.ID, "one", "en", "2026-02-01T01:00:00+02:00", []string{"sea", "sea", "wind"}, 2, true)
	publish(b.ID, "two", "en", "2026-02-01T00:00:00Z", []string{"sea"}, 2, false)
	publish(b.ID, "three", "fr", "2026-01-01T01:00:00+02:00", []string{"wind"}, 3, true)
	hidden := publish(a.ID, "withdrawn", "fr", "2026-01-01T00:00:00Z", []string{"hidden"}, 9, true)
	if _, err = pool.Exec(ctx, `UPDATE records SET withdrawn=true WHERE organization=$1 AND id=$2`, scope.Organization, hidden); err != nil {
		t.Fatal(err)
	}
	quarantine := publish(b.ID, "quarantined", "fr", "2026-01-01T00:00:00Z", []string{"hidden"}, 9, true)
	if _, err = pool.Exec(ctx, `UPDATE record_versions SET quarantined=true WHERE organization=$1 AND record_id=$2`, scope.Organization, quarantine); err != nil {
		t.Fatal(err)
	}
	unready := publish(b.ID, "unready", "fr", "2026-01-01T00:00:00Z", []string{"hidden"}, 9, true)
	if _, err = pool.Exec(ctx, `UPDATE record_versions SET baseline_ready=false WHERE organization=$1 AND record_id=$2`, scope.Organization, unready); err != nil {
		t.Fatal(err)
	}
	tombstoned := publish(a.ID, "tombstoned", "fr", "2026-01-01T00:00:00Z", []string{"hidden"}, 9, true)
	if _, err = pool.Exec(ctx, `INSERT INTO tombstones(organization,record_id) VALUES($1,$2)`, scope.Organization, tombstoned); err != nil {
		t.Fatal(err)
	}

	q := content.FacetQuery{Records: content.RecordQuery{CorpusIDs: []string{a.ID, b.ID}, FilterRoutes: []content.CatalogFilterRoute{{CorpusID: a.ID, GenerationID: g.ID}, {CorpusID: b.ID, GenerationID: g.ID}}}, Fields: []content.FacetField{{Field: "metadata.language", Type: "string", Limit: 1}, {Field: "metadata.tags", Type: "string_array", Limit: 10}, {Field: "rating", Type: "number", Limit: 10}, {Field: "flag", Type: "boolean", Limit: 10}, {Field: "metadata.published_at", Type: "datetime", Interval: "month", Limit: 10}}}
	read := func(q content.FacetQuery) []content.Facet {
		t.Helper()
		got, err := svc.CountFacets(ctx, scope, q, nil)
		if err != nil {
			t.Fatal(err)
		}
		return got.Items
	}
	got := read(q)
	expected := [][]content.FacetBucket{{{Value: "en", Count: 2}}, {{Value: "sea", Count: 2}, {Value: "wind", Count: 2}}, {{Value: float64(2), Count: 2}, {Value: float64(3), Count: 1}}, {{Value: true, Count: 2}, {Value: false, Count: 1}}, {{Value: "2025-12-01T00:00:00Z", Count: 1}, {Value: "2026-01-01T00:00:00Z", Count: 1}, {Value: "2026-02-01T00:00:00Z", Count: 1}}}
	for i, want := range expected {
		if !reflect.DeepEqual(got[i].Buckets, want) {
			t.Fatalf("facet %s = %#v, want %#v", got[i].Field, got[i].Buckets, want)
		}
	}
	for _, tc := range []struct {
		interval string
		want     []content.FacetBucket
	}{{"day", []content.FacetBucket{{Value: "2025-12-31T00:00:00Z", Count: 1}, {Value: "2026-01-31T00:00:00Z", Count: 1}, {Value: "2026-02-01T00:00:00Z", Count: 1}}}, {"year", []content.FacetBucket{{Value: "2025-01-01T00:00:00Z", Count: 1}, {Value: "2026-01-01T00:00:00Z", Count: 2}}}} {
		dated := q
		dated.Fields = []content.FacetField{{Field: "metadata.published_at", Type: "datetime", Interval: tc.interval, Limit: 10}}
		if got := read(dated); !reflect.DeepEqual(got[0].Buckets, tc.want) {
			t.Fatalf("%s = %#v, want %#v", tc.interval, got, tc.want)
		}
	}
	filtered := q
	filtered.Records.Metadata = []corpus.MetadataFilter{{Field: "metadata.tags", AnyOf: []any{"wind"}}}
	typed, _, err := corpus.ResolveFilters(filtered.Records.Metadata, nil)
	if err != nil {
		t.Fatal(err)
	}
	filtered.Records.FilterRoutes = append([]content.CatalogFilterRoute(nil), q.Records.FilterRoutes...)
	for i := range filtered.Records.FilterRoutes {
		filtered.Records.FilterRoutes[i].Filters = typed
	}
	if got := read(filtered); !reflect.DeepEqual(got[1].Buckets, []content.FacetBucket{{Value: "wind", Count: 2}, {Value: "sea", Count: 1}}) {
		t.Fatalf("filtered tags = %#v", got)
	}
	filtered = q
	filtered.SourceNamespaces = []string{"other"}
	if got := read(filtered); len(got[0].Buckets) != 0 {
		t.Fatalf("source filter leaked %#v", got)
	}
	before := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	filtered = q
	filtered.Records.AcceptedBefore = &before
	if got := read(filtered); len(got[0].Buckets) != 0 {
		t.Fatalf("time filter leaked %#v", got)
	}
	restricted := scope
	restricted.Corpora = []string{a.ID}
	if _, err := svc.CountFacets(ctx, restricted, q, nil); err != corpus.ErrNotFound {
		t.Fatalf("unauthorized corpus: %v", err)
	}
	selected := q
	selected.Records.CorpusIDs = []string{a.ID}
	prepared, err := svc.CountFacets(ctx, restricted, selected, func() (content.FacetQuery, error) {
		// Mutate the caller's backing slice and return a broader query. Neither
		// may expand the already-authorized corpus set seen by the real reader.
		selected.Records.CorpusIDs[0] = b.ID
		return q, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.AsOf != nil || prepared.SampleFraction != 0 {
		t.Fatalf("exact count carries a marker: %#v", prepared)
	}
	authorized := [][]content.FacetBucket{{{Value: "en", Count: 1}}, {{Value: "sea", Count: 1}, {Value: "wind", Count: 1}}, {{Value: float64(2), Count: 1}}, {{Value: true, Count: 1}}, {{Value: "2026-01-01T00:00:00Z", Count: 1}}}
	for i, want := range authorized {
		if !reflect.DeepEqual(prepared.Items[i].Buckets, want) {
			t.Fatalf("preparation expanded authorization for %s: %#v, want %#v", prepared.Items[i].Field, prepared.Items[i].Buckets, want)
		}
	}
	filtered = q
	filtered.Records.FilterRoutes = nil
	if got := read(filtered); len(got[0].Buckets) != 0 {
		t.Fatalf("excluded corpora leaked %#v", got)
	}
	// Two documents with equal values across every requested field must retain
	// their multiplicity when the adapter groups facet tuples before counting.
	publish(b.ID, "four", "en", "2026-02-01T01:00:00+02:00", []string{"sea", "sea", "wind"}, 2, true)
	got = read(q)
	weighted := [][]content.FacetBucket{{{Value: "en", Count: 3}}, {{Value: "sea", Count: 3}, {Value: "wind", Count: 3}}, {{Value: float64(2), Count: 3}, {Value: float64(3), Count: 1}}, {{Value: true, Count: 3}, {Value: false, Count: 1}}, {{Value: "2025-12-01T00:00:00Z", Count: 1}, {Value: "2026-01-01T00:00:00Z", Count: 2}, {Value: "2026-02-01T00:00:00Z", Count: 1}}}
	for i, want := range weighted {
		if !reflect.DeepEqual(got[i].Buckets, want) {
			t.Fatalf("weighted facet %s = %#v, want %#v", got[i].Field, got[i].Buckets, want)
		}
	}
}

// publishFaceted publishes a ready current document with projected metadata.
func publishFaceted(t *testing.T, ctx context.Context, pool *pgxpool.Pool, svc content.Service, scope corpus.Scope, generation, corpusID, key string, metadata map[string]any) string {
	t.Helper()
	stores := contentStores(pool)
	receipt, err := svc.Accept(ctx, scope, content.Command{Key: key, Source: content.Source{CorpusID: corpusID, Namespace: "source", RecordKey: key}, Content: content.Text{Kind: "text", Text: key}})
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := stores.Work(ctx, scope.Organization, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	blob := content.Blob{Key: key, SHA256: content.Hash([]byte(key)), Size: int64(len(key))}
	if err = stores.Publish(ctx, work, publication(blob, blob)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE record_versions SET baseline_ready=true WHERE organization=$1 AND id=$2`, scope.Organization, work.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE records SET current_version_id=$3,current_accepted_at='2026-10-01T00:00:00Z' WHERE organization=$1 AND id=$2`, scope.Organization, work.RecordID, work.VersionID); err != nil {
		t.Fatal(err)
	}
	if err = (postgres.RecordStore{Pool: pool}).SaveProjectionMetadata(ctx, scope.Organization, work.VersionID, generation, metadata); err != nil {
		t.Fatal(err)
	}
	return work.RecordID
}

// Owns fast counting on real storage: a snapshot answers with exactly the
// live counts of when it was taken, keeps answering them as documents change
// until it is refreshed, serves whole days of the timeline, and steps aside
// for another generation; a sample is scaled and says so.
func TestFastFacets(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	stores := contentStores(pool)
	rs := postgres.RecordStore{Pool: pool}
	svc := content.Service{Submissions: stores, Receipts: stores, Materialization: stores, Facets: rs}
	cs := corpus.Service{Store: postgres.Store{Pool: pool}}
	scope := corpus.Scope{Organization: fmt.Sprintf("adapter-fast-facets-%d", time.Now().UnixNano()), Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	ids := []string{}
	for _, key := range []string{"a", "b", "sampled"} {
		c, _, err := cs.Create(ctx, scope, corpus.CreateInput{Key: key, Name: "Fast " + key})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.ID)
	}
	a, b, sampled := ids[0], ids[1], ids[2]
	g, err := (postgres.ProjectionStore{Pool: pool}).Generation(ctx, scope.Organization, a)
	if err != nil {
		t.Fatal(err)
	}
	document := func(id, key, language, date string, tags ...string) string {
		t.Helper()
		return publishFaceted(t, ctx, pool, svc, scope, g.ID, id, key, map[string]any{"metadata.language": language, "metadata.published_at": date, "metadata.tags": tags})
	}
	document(a, "one", "en", "2026-02-01T01:00:00+02:00", "sea", "wind")
	document(b, "two", "en", "2026-02-01T00:00:00Z", "sea")
	withdrawn := document(b, "three", "fr", "2025-12-31T23:00:00Z", "wind")

	declared := []content.FacetField{}
	for _, f := range corpus.FilterFields(nil) {
		declared = append(declared, content.FacetField{Field: f.Name, Type: f.Type})
	}
	routed := func(fields []content.FacetField, filters []corpus.TypedFilter, corpora ...string) content.FacetQuery {
		q := content.FacetQuery{Records: content.RecordQuery{CorpusIDs: corpora}, Fields: fields, Declared: map[string][]content.FacetField{}}
		for _, id := range corpora {
			q.Records.FilterRoutes = append(q.Records.FilterRoutes, content.CatalogFilterRoute{CorpusID: id, GenerationID: g.ID, Filters: filters})
			q.Declared[id] = declared
		}
		return q
	}
	fields := []content.FacetField{{Field: "metadata.language", Type: "string", Limit: 10}, {Field: "metadata.tags", Type: "string_array", Limit: 1}, {Field: "metadata.published_at", Type: "datetime", Interval: "month", Limit: 10}}
	q := routed(fields, nil, a, b)
	exact := func(q content.FacetQuery) []content.Facet {
		t.Helper()
		got, err := rs.CountFacets(ctx, scope.Organization, q)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	fast := func(q content.FacetQuery) content.FacetCounts {
		t.Helper()
		q.Fast = true
		got, err := svc.CountFacets(ctx, scope, q, nil)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	// Each read refreshes a missing or old snapshot in the background; wait
	// until the answer comes from snapshots taken after `after`.
	snapshotAfter := func(q content.FacetQuery, after time.Time) content.FacetCounts {
		t.Helper()
		for {
			got := fast(q)
			if got.AsOf != nil && got.AsOf.After(after) {
				return got
			}
			select {
			case <-ctx.Done():
				t.Fatalf("no snapshot after %v: %#v", after, got)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}

	// Without a snapshot, a quick exact count answers unmarked.
	before := exact(q)
	if got := fast(q); got.AsOf != nil || got.SampleFraction != 0 || !reflect.DeepEqual(got.Items, before) {
		t.Fatalf("first fast read = %#v, want exact %#v", got, before)
	}
	taken := snapshotAfter(q, time.Time{})
	if !reflect.DeepEqual(taken.Items, before) || taken.SampleFraction != 0 {
		t.Fatalf("snapshot = %#v, want the exact counts %#v", taken.Items, before)
	}

	// Later changes wait for a refresh, under the same as_of.
	document(a, "four", "de", "2026-03-02T00:00:00Z", "sea")
	if _, err = pool.Exec(ctx, `UPDATE records SET withdrawn=true WHERE organization=$1 AND id=$2`, scope.Organization, withdrawn); err != nil {
		t.Fatal(err)
	}
	if got := fast(q); !got.AsOf.Equal(*taken.AsOf) || !reflect.DeepEqual(got.Items, before) {
		t.Fatalf("fresh snapshot changed: %#v", got)
	}
	if _, err = pool.Exec(ctx, `UPDATE facet_snapshots SET taken_at=taken_at-interval '1 hour' WHERE organization=$1`, scope.Organization); err != nil {
		t.Fatal(err)
	}
	after := exact(q)
	if got := snapshotAfter(q, *taken.AsOf); !reflect.DeepEqual(got.Items, after) {
		t.Fatalf("refreshed snapshot = %#v, want %#v", got.Items, after)
	}

	// Whole UTC days of the date it counts come from the snapshot; any other
	// predicate counts live.
	day := []content.FacetField{{Field: "metadata.published_at", Type: "datetime", Interval: "day", Limit: 10}}
	for _, tc := range []struct {
		gte, lte string
		snapshot bool
	}{{"2026-01-31T00:00:00.000Z", "2026-02-01T23:59:59.999Z", true}, {"", "2026-02-01T23:59:59.999Z", true}, {"2026-01-31T00:00:00.000Z", "2026-02-01T12:00:00.000Z", false}} {
		window := routed(day, []corpus.TypedFilter{{MetadataFilter: corpus.MetadataFilter{Field: "metadata.published_at", Gte: tc.gte, Lte: tc.lte}, Type: "datetime"}}, a, b)
		if got := fast(window); (got.AsOf != nil) != tc.snapshot || !reflect.DeepEqual(got.Items, exact(window)) {
			t.Fatalf("window %s..%s = %#v, want %#v from snapshot %v", tc.gte, tc.lte, got, exact(window), tc.snapshot)
		}
	}
	filtered := routed(fields, []corpus.TypedFilter{{MetadataFilter: corpus.MetadataFilter{Field: "metadata.language", AnyOf: []any{"en"}}, Type: "string"}}, a, b)
	if got := fast(filtered); got.AsOf != nil || !reflect.DeepEqual(got.Items, exact(filtered)) {
		t.Fatalf("filtered = %#v, want live %#v", got, exact(filtered))
	}

	// A snapshot of another generation never answers.
	if _, err = pool.Exec(ctx, `UPDATE facet_snapshots SET generation_id='retired' WHERE organization=$1 AND corpus_id=$2`, scope.Organization, a); err != nil {
		t.Fatal(err)
	}
	if got := fast(q); got.AsOf != nil {
		t.Fatalf("retired generation answered: %#v", got)
	}

	// A sample reads a range of hashed Record IDs and scales what it counts.
	// Enough Records that a sample is smaller than the Corpus share two
	// published Versions, one in four the French one. The fixture writes
	// canonical rows directly, without row triggers: nothing here reads what
	// they maintain.
	versions := map[string]string{}
	for _, language := range []string{"en", "fr"} {
		record := document(sampled, "model-"+language, language, "2026-01-01T00:00:00Z")
		var version string
		if err = pool.QueryRow(ctx, `SELECT current_version_id FROM records WHERE organization=$1 AND id=$2`, scope.Organization, record).Scan(&version); err != nil {
			t.Fatal(err)
		}
		versions[language] = version
	}
	const copies = 80000
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO records(organization,id,corpus_id,namespace,record_key,current_version_id)
 SELECT $1,'record_'||encode(sha256(convert_to('copy'||i,'UTF8')),'hex'),$2,'source','copy'||i,CASE WHEN i%4=0 THEN $4 ELSE $3 END
 FROM generate_series(1,$5::int) i`, scope.Organization, sampled, versions["en"], versions["fr"], copies); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	estimate, err := rs.SampleFacets(ctx, scope.Organization, routed(fields[:1], nil, sampled))
	if err != nil {
		t.Fatal(err)
	}
	if estimate.SampleFraction <= 0 || estimate.SampleFraction >= 1 || estimate.AsOf != nil {
		t.Fatalf("sample marker = %#v", estimate)
	}
	for _, want := range []content.FacetBucket{{Value: "en", Count: copies*3/4 + 1}, {Value: "fr", Count: copies/4 + 1}} {
		var got content.FacetBucket
		for _, b := range estimate.Items[0].Buckets {
			if b.Value == want.Value {
				got = b
			}
		}
		if diff := float64(got.Count-want.Count) / float64(want.Count); diff < -0.05 || diff > 0.05 {
			t.Fatalf("%v estimated %d, want %d within 5%% (fraction %.3f)", want.Value, got.Count, want.Count, estimate.SampleFraction)
		}
	}
}
