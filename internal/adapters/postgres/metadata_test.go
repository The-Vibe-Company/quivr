package postgres_test

import (
	"context"
	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"testing"
	"time"
)

// Owns SQL filtering before keyset limits. Version fixtures use the existing
// acceptance/materialization interfaces; assertions read only catalog pages.
func TestMetadataCatalogFiltersBeforePagination(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	stores := contentStores(pool)
	svc := content.Service{Submissions: stores, Receipts: stores, Materialization: stores, Catalog: stores}
	corpora := corpus.Service{Store: postgres.Store{Pool: pool}}
	scope := corpus.Scope{Organization: "adapter-metadata", Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	a, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "a", Name: "Metadata A"})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "b", Name: "Metadata B"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := (postgres.ProjectionStore{Pool: pool}).Generation(ctx, scope.Organization, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	recordStore := postgres.RecordStore{Pool: pool}
	publish := func(id, key, language, tag, date string) string {
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
		if _, err = pool.Exec(ctx, `UPDATE records SET current_version_id=$3 WHERE organization=$1 AND id=$2`, scope.Organization, work.RecordID, work.VersionID); err != nil {
			t.Fatal(err)
		}
		values := map[string]any{"metadata.language": language, "metadata.tags": []string{tag}, "metadata.published_at": date}
		if err = recordStore.SaveProjectionMetadata(ctx, scope.Organization, work.VersionID, g.ID, values); err != nil {
			t.Fatal(err)
		}
		return work.RecordID
	}
	publish(a.ID, "nonmatch", "fr", "sea weather", "2026-10-01T12:00:00.000Z")
	publish(a.ID, "wrong-tag", "en", "land", "2026-10-01T12:00:00.000Z")
	publish(a.ID, "too-early", "en", "sea weather", "2026-10-01T11:59:59.000Z")
	publish(a.ID, "too-late", "en", "sea weather", "2026-10-01T12:00:01.000Z")
	first := publish(a.ID, "matching-a", "en", "sea weather", "2026-10-01T12:00:00.000Z")
	second := publish(b.ID, "matching-b", "en", "sea weather", "2026-10-01T12:00:00.000Z")
	// Single-Corpus entry points cannot let a query select an unauthorized Corpus.
	restricted := scope
	restricted.Corpora = []string{a.ID}
	selected, err := svc.Records(ctx, restricted, a.ID, content.RecordQuery{CorpusIDs: []string{b.ID}, Limit: 100})
	if err != nil || len(selected) != 5 {
		t.Fatalf("single-Corpus selection: %v %v", selected, err)
	}
	for _, row := range selected {
		if row.Source.CorpusID != a.ID {
			t.Fatalf("foreign record: %v", row)
		}
	}
	counted, err := svc.CountRecords(ctx, restricted, a.ID, content.RecordQuery{CorpusIDs: []string{b.ID}, Limit: 100})
	if err != nil || counted != 5 {
		t.Fatalf("single-Corpus count: %d %v", counted, err)
	}
	filters := []corpus.MetadataFilter{{Field: "metadata.language", AnyOf: []any{"en"}}, {Field: "metadata.tags", AnyOf: []any{"sea weather"}}, {Field: "metadata.published_at", Gte: "2026-10-01T12:00:00Z", Lte: "2026-10-01T12:00:00Z"}}
	typed, _, err := corpus.ResolveFilters(filters, nil)
	if err != nil {
		t.Fatal(err)
	}
	q := content.RecordQuery{CorpusIDs: []string{a.ID, b.ID}, Metadata: filters, Order: content.AcceptedAtDesc, Limit: 1, FilterRoutes: []content.CatalogFilterRoute{{CorpusID: a.ID, GenerationID: g.ID, Filters: typed}, {CorpusID: b.ID, GenerationID: g.ID, Filters: typed}}}
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		rows, err := svc.RecordsAcross(ctx, scope, q.CorpusIDs, q, nil)
		if err != nil || len(rows) != 1 {
			t.Fatalf("page %d: %v %v", i, rows, err)
		}
		if seen[rows[0].ID] || (rows[0].ID != first && rows[0].ID != second) {
			t.Fatalf("unexpected or duplicated match: %v", rows)
		}
		seen[rows[0].ID] = true
		q.AfterID = rows[0].ID
		q.AfterAcceptedAt = rows[0].CurrentAcceptedAt
	}
	rows, err := svc.RecordsAcross(ctx, scope, q.CorpusIDs, q, nil)
	if err != nil || len(rows) != 0 {
		t.Fatalf("after last page: %v %v", rows, err)
	}
	q.AfterID = ""
	q.FilterRoutes[0].GenerationID = "unrouted"
	q.FilterRoutes[1].GenerationID = "unrouted"
	rows, err = svc.RecordsAcross(ctx, scope, q.CorpusIDs, q, nil)
	if err != nil || len(rows) != 0 {
		t.Fatalf("different generation leaked %v %v", rows, err)
	}
	// The same catalog fixture owns metadata retention: completing one Corpus's
	// generation purge must preserve another Corpus sharing that generation.
	purger := postgres.PurgeStore{Pool: pool}
	item := retrieval.PurgeItem{Kind: retrieval.PurgeGeneration, Organization: scope.Organization, CorpusID: a.ID, GenerationID: g.ID}
	if _, err = pool.Exec(ctx, `INSERT INTO projection_purges (organization,kind,corpus_id,generation_id,version_id) VALUES ($1,$2,$3,$4,'')`, scope.Organization, item.Kind, a.ID, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = purger.RecordPurge(ctx, item, 0, false, 1000); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM projection_metadata WHERE organization=$1`, scope.Organization).Scan(&count); err != nil || count != 6 {
		t.Fatalf("incomplete purge removed metadata: %d %v", count, err)
	}
	if _, err = purger.RecordPurge(ctx, item, 0, true, 1000); err != nil {
		t.Fatal(err)
	}
	var survivingVersion string
	if err = pool.QueryRow(ctx, `SELECT version_id FROM projection_metadata WHERE organization=$1`, scope.Organization).Scan(&survivingVersion); err != nil {
		t.Fatalf("shared generation purge removed other Corpus: %v", err)
	}
	var current string
	if err = pool.QueryRow(ctx, `SELECT current_version_id FROM records WHERE organization=$1 AND id=$2`, scope.Organization, second).Scan(&current); err != nil || current != survivingVersion {
		t.Fatalf("surviving metadata belongs to wrong Record: %s %s %v", current, survivingVersion, err)
	}
	// A repeated completion cannot purge metadata published after the first acknowledgement.
	if err = recordStore.SaveProjectionMetadata(ctx, scope.Organization, survivingVersion, g.ID, map[string]any{"metadata.language": "en"}); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO projection_purges (organization,kind,corpus_id,generation_id,version_id) VALUES ($1,$2,'','',$3)`, scope.Organization, retrieval.PurgeVersion, survivingVersion); err != nil {
		t.Fatal(err)
	}
	if _, err = purger.RecordPurge(ctx, retrieval.PurgeItem{Kind: retrieval.PurgeVersion, Organization: scope.Organization, VersionID: survivingVersion}, 0, true, 1000); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM projection_metadata WHERE organization=$1`, scope.Organization).Scan(&count); err != nil || count != 0 {
		t.Fatalf("completed Version purge kept metadata: %d %v", count, err)
	}
	if err = recordStore.SaveProjectionMetadata(ctx, scope.Organization, survivingVersion, g.ID, map[string]any{"metadata.language": "en"}); err != nil {
		t.Fatal(err)
	}
	if _, err = purger.RecordPurge(ctx, retrieval.PurgeItem{Kind: retrieval.PurgeVersion, Organization: scope.Organization, VersionID: survivingVersion}, 0, true, 1000); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM projection_metadata WHERE organization=$1`, scope.Organization).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replayed Version purge removed new metadata: %d %v", count, err)
	}
}
