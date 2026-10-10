package postgres_test

import (
	"context"
	"fmt"
	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"testing"
	"time"
)

// The ledger owner uses canonical fixtures; this owner protects the distinct
// risk of a real publication/acceptance transaction omitting its observation.
func TestCorpusStatsPublicationLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	scope := corpus.Scope{Organization: fmt.Sprintf("adapter-corpus-stats-%d", time.Now().UnixNano()), Actions: []string{"corpora:write", "content:read", "content:write"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "totals", Name: "Totals"})
	if err != nil {
		t.Fatal(err)
	}
	store := contentStores(pool)
	stats := postgres.CorpusStatsStore{Pool: pool}
	service := content.Service{Submissions: store, Receipts: store, Materialization: store, Baseline: store, Stats: stats}
	check := func(eligible, catalog int64) {
		t.Helper()
		got, err := stats.CorpusStats(ctx, scope.Organization, c.ID, content.CorpusStatsQuery{Sources: true, Histogram: true})
		if err != nil || got.Total != eligible || got.CatalogTotal != catalog {
			t.Fatalf("lifecycle totals: %+v %v, want %d/%d", got, err, eligible, catalog)
		}
	}
	publish := func(text, connector string, at time.Time) content.Work {
		t.Helper()
		cmd := content.Command{Key: text, ConnectorInstanceID: connector, Source: content.Source{CorpusID: c.ID, Namespace: "source", RecordKey: "one"}, Content: content.Text{Kind: "text", Text: text}}
		receipt, err := service.TrustedAccept(ctx, scope.Organization, c.ID, cmd)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `UPDATE ingestion_receipts SET accepted_at=$3 WHERE organization=$1 AND id=$2`, scope.Organization, receipt.ID, at); err != nil {
			t.Fatal(err)
		}
		if text == "first" {
			pending, err := stats.CorpusStats(ctx, scope.Organization, c.ID, content.CorpusStatsQuery{Sources: true})
			if err != nil || pending.Total != 0 || len(pending.Sources.Items) != 1 || pending.Sources.Items[0].ConnectorID != connector {
				t.Fatalf("pending connector attribution: %+v %v", pending, err)
			}
		}
		work, _, err := store.Work(ctx, scope.Organization, receipt.ID)
		if err != nil {
			t.Fatal(err)
		}
		blob := content.Blob{Key: "totals/" + text, SHA256: content.Hash([]byte(text)), Size: int64(len(text))}
		if err = store.Publish(ctx, work, publication(blob, blob)); err != nil {
			t.Fatal(err)
		}
		v := content.Version{ID: work.VersionID, RecordID: work.RecordID, Manifest: content.ManifestFor(cmd)}
		seg, err := wholeParts(scope.Organization, v)
		if err != nil {
			t.Fatal(err)
		}
		if err = service.SaveSegmentation(ctx, scope.Organization, v, seg); err != nil {
			t.Fatal(err)
		}
		g, err := store.Generation(ctx, scope.Organization, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err = service.Promote(ctx, scope.Organization, seg, g); err != nil {
				t.Fatal(err)
			}
		}
		return work
	}
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	publish("first", "connector-first", day)
	check(1, 1)
	publish("corrected", "connector-next", day.Add(24*time.Hour))
	check(1, 1)
	got, err := stats.CorpusStats(ctx, scope.Organization, c.ID, content.CorpusStatsQuery{Sources: true, Histogram: true})
	if err != nil || len(got.Sources.Items) != 1 || got.Sources.Items[0].ConnectorID != "connector-next" || len(got.Histogram.Items) != 1 || !got.Histogram.Items[0].Start.Equal(day.Add(24*time.Hour)) {
		t.Fatalf("corrected attribution/history: %+v %v", got, err)
	}

	// Every public submission entry point clears internal attribution; only the
	// explicitly trusted path above can stamp an accepted connector identity.
	for _, kind := range []string{"direct", "submit", "batch"} {
		cmd := content.Command{Key: kind, ConnectorInstanceID: "forged", Source: content.Source{CorpusID: c.ID, Namespace: "source", RecordKey: kind}, Content: content.Text{Kind: "text", Text: "public"}, Provenance: map[string]any{"producer": "connector-next"}}
		accept := func(sub content.Submitter) error { _, err := sub.Accept(ctx, cmd); return err }
		switch kind {
		case "direct":
			_, err = service.Accept(ctx, scope, cmd)
		case "submit":
			err = service.Submit(scope, accept)
		case "batch":
			err = service.Batch(scope, accept)
		}
		if err != nil {
			t.Fatal(kind, err)
		}
	}
	check(1, 4)
	got, err = stats.CorpusStats(ctx, scope.Organization, c.ID, content.CorpusStatsQuery{Sources: true})
	if err != nil || len(got.Sources.Items) != 2 || got.Sources.Items[1].ConnectorID != "unknown" || got.Sources.Items[1].CatalogCount != 3 {
		t.Fatalf("public attribution: %+v %v", got, err)
	}
	if _, err = service.Withdraw(ctx, scope, content.Withdrawal{Key: "withdraw", Source: content.Source{CorpusID: c.ID, Namespace: "source", RecordKey: "one"}}); err != nil {
		t.Fatal(err)
	}
	check(0, 4)
}
