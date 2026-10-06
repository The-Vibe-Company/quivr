package postgres_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

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
	rs := postgres.RecordStore{Pool: pool}
	publish := func(id, key, language, date string, tags []string, number float64, flag bool) string {
		t.Helper()
		receipt, err := svc.Accept(ctx, scope, content.Command{Key: key, Source: content.Source{CorpusID: id, Namespace: "source", RecordKey: key}, Content: content.Text{Kind: "text", Text: key}})
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
		if err = rs.SaveProjectionMetadata(ctx, scope.Organization, work.VersionID, g.ID, map[string]any{"metadata.language": language, "metadata.tags": tags, "metadata.published_at": date, "rating": number, "flag": flag}); err != nil {
			t.Fatal(err)
		}
		return work.RecordID
	}
	publish(a.ID, "one", "en", "2026-01-31T23:00:00Z", []string{"sea", "sea", "wind"}, 2, true)
	publish(b.ID, "two", "en", "2026-02-01T00:00:00Z", []string{"sea"}, 2, false)
	publish(b.ID, "three", "fr", "2025-12-31T23:00:00Z", []string{"wind"}, 3, true)
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
		return got
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
	filtered = q
	filtered.Records.FilterRoutes = nil
	if got := read(filtered); len(got[0].Buckets) != 0 {
		t.Fatalf("excluded corpora leaked %#v", got)
	}
	// Two documents with equal values across every requested field must retain
	// their multiplicity when the adapter groups facet tuples before counting.
	publish(b.ID, "four", "en", "2026-01-31T23:00:00Z", []string{"sea", "sea", "wind"}, 2, true)
	got = read(q)
	weighted := [][]content.FacetBucket{{{Value: "en", Count: 3}}, {{Value: "sea", Count: 3}, {Value: "wind", Count: 3}}, {{Value: float64(2), Count: 3}, {Value: float64(3), Count: 1}}, {{Value: true, Count: 3}, {Value: false, Count: 1}}, {{Value: "2025-12-01T00:00:00Z", Count: 1}, {Value: "2026-01-01T00:00:00Z", Count: 2}, {Value: "2026-02-01T00:00:00Z", Count: 1}}}
	for i, want := range weighted {
		if !reflect.DeepEqual(got[i].Buckets, want) {
			t.Fatalf("weighted facet %s = %#v, want %#v", got[i].Field, got[i].Buckets, want)
		}
	}
}
