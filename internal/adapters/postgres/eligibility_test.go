package postgres_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

// Enrichment only serves search for a Record's current, eligible Version; any
// other Version's enrichment must end instead of retrying.
func TestEnrichmentEligibilityFollowsCurrentEligibleVersion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := rebuildAdapterPool(t, ctx)
	org := "adapter-eligibility-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "eligibility", Name: "Eligibility"})
	if err != nil {
		t.Fatal(err)
	}
	store := contentStores(pool)
	service := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store, Baseline: store, Embeddings: store}
	source := content.Source{CorpusID: c.ID, Namespace: "adapter", RecordKey: "eligible"}
	// promoted accepts, publishes, segments and promotes one Version of the Record.
	promoted := func(key, text string) string {
		t.Helper()
		r, err := service.Accept(ctx, scope, content.Command{Key: key, Source: source, Content: content.Text{Kind: "text", Text: text}})
		if err != nil {
			t.Fatal(err)
		}
		work, _, err := store.Work(ctx, org, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		blob := content.Blob{Key: "fixture/" + key, SHA256: "eligibility-text-" + key, Size: int64(len(text))}
		if err = store.Publish(ctx, work, publication(blob, content.Blob{Key: "fixture/m-" + key, SHA256: "eligibility-manifest-" + key, Size: 2})); err != nil {
			t.Fatal(err)
		}
		seg := content.Segmentation{ID: content.StableID("segmentation", org, work.VersionID, "fixture"), VersionID: work.VersionID, Recipe: "fixture", Provenance: json.RawMessage(`{}`), Segments: []content.Segment{{ID: content.StableID("segment", work.VersionID), PartKey: "body", End: len([]rune(text)), Text: text}}}
		if err = store.SaveSegmentation(ctx, org, seg); err != nil {
			t.Fatal(err)
		}
		g, err := store.Generation(ctx, org, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Promote(ctx, org, seg, g); err != nil {
			t.Fatal(err)
		}
		return work.VersionID
	}
	eligible := func(id string, want bool) {
		t.Helper()
		got, err := service.EnrichmentEligible(ctx, org, id)
		if err != nil || got != want {
			t.Fatalf("EnrichmentEligible(%s) = %v, %v; want %v", id, got, err, want)
		}
	}
	first := promoted("first", "First version")
	eligible(first, true)
	correction := promoted("correction", "Corrected version")
	eligible(correction, true)
	eligible(first, false) // superseded by the correction
	eligible("version-unknown", false)
	if _, err = pool.Exec(ctx, `UPDATE record_versions SET quarantined=true WHERE organization=$1 AND id=$2`, org, correction); err != nil {
		t.Fatal(err)
	}
	eligible(correction, false)
	if _, err = pool.Exec(ctx, `UPDATE record_versions SET quarantined=false WHERE organization=$1 AND id=$2`, org, correction); err != nil {
		t.Fatal(err)
	}
	eligible(correction, true)
	if _, err = service.Withdraw(ctx, scope, content.Withdrawal{Key: "withdraw", Source: source}); err != nil {
		t.Fatal(err)
	}
	eligible(correction, false)
}
