package weaviate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/outbound"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

func TestTenantNamePreservesTupleBoundaries(t *testing.T) {
	a := tenantName("North/West", "a_b", "c")
	for _, b := range []string{
		tenantName("North_West", "a_b", "c"),
		tenantName("North/West_a", "b", "c"),
		tenantName("North/West", "a", "b_c"),
		tenantName("north/west", "a_b", "c"),
		tenantName("North/West", "a_b", "d"),
		tenantName(strings.Repeat("世界/", 128), "a_b", "c"),
	} {
		if a == b || !regexp.MustCompile("^[a-z0-9_-]{4,64}$").MatchString(b) {
			t.Fatalf("tenant tuple normalized into %q (original %q)", b, a)
		}
	}
}

func TestFuseRouteRanksAddsRanksAndIgnoresScoreScales(t *testing.T) {
	top := content.Candidate{GenerationID: "one", SegmentID: "top", Score: 10000}
	shared := content.Candidate{GenerationID: "one", SegmentID: "shared", Score: 10}
	other := content.Candidate{GenerationID: "two", SegmentID: "other", Score: .2}
	got := fuseRouteRanks([][]content.Candidate{{top, shared, shared}, {other, shared}}, 2)
	if len(got) != 2 || got[0].SegmentID != "shared" || math.Abs(got[0].Score-(1./62+1./61)) > 1e-12 || got[1].SegmentID != "top" {
		t.Fatalf("RRF should accumulate ranks across lists and ignore raw score scales: %+v", got)
	}
}

func TestTenantFanoutCancelsInFlightQueries(t *testing.T) {
	started := make(chan struct{}, 16)
	finished := make(chan struct{}, 16)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var query map[string]string
		if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
			t.Error(err)
			http.Error(w, "invalid query", http.StatusBadRequest)
			return
		}
		if !strings.Contains(query["query"], "tenant:") || strings.Contains(query["query"], "where:") {
			t.Errorf("unfiltered tenant query should carry only the physical route: %s", query["query"])
		}
		started <- struct{}{}
		<-r.Context().Done()
		finished <- struct{}{}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	routes := make([]retrieval.Route, 16)
	for i := range routes {
		routes[i] = retrieval.Route{CorpusID: fmt.Sprint(i), Generation: content.Generation{ID: "default", Collection: InitialCollection}}
	}
	done := make(chan error, 1)
	go func() {
		_, err := New(server.URL).Search(ctx, routes, corpus.Scope{Organization: "example"}, retrieval.Request{Query: "beacon", Mode: "lexical", K: 1})
		done <- err
	}()
	for range maxTenantSearches {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("tenant fanout did not fill its parallelism bound")
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled fanout: %v", err)
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for range maxTenantSearches {
		select {
		case <-finished:
		case <-timer.C:
			t.Fatal("tenant fanout left an active HTTP request")
		}
	}
	if got := requests.Load(); got != maxTenantSearches {
		t.Fatalf("started %d requests, want %d", got, maxTenantSearches)
	}
}

func TestNativeTenantLifecycleAndSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	httpStore, grpcStore := realTenantStores(t)
	if err := httpStore.Bootstrap(ctx, InitialCollection); err != nil {
		t.Fatal(err)
	}
	legacy := fmt.Sprintf("QuivrTenantLegacy%x", time.Now().UnixNano())
	if err := httpStore.Bootstrap(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = httpStore.call(cleanup, "DELETE", "/v1/schema/"+legacy, nil, nil)
	})

	org := fmt.Sprintf("tenant-adapter-%d", time.Now().UnixNano())
	scope := corpus.Scope{Organization: org}
	space := "fixture-" + fmt.Sprintf("%x", time.Now().UnixNano())
	first := retrieval.Route{CorpusID: "first", Generation: content.Generation{
		ID: "default", Collection: InitialCollection, ItemKeywordsProjected: true, MetadataProjected: true,
		SpaceID: space, SpacesProjected: true, Spaces: []content.GenerationSpace{{ID: space, Metric: "cosine"}},
	}}
	second, old := first, first
	second.CorpusID = "second"
	second.Generation.Spaces = []content.GenerationSpace{{ID: space, Metric: "cosine", Index: &content.VectorIndex{Quantization: content.QuantizationNone}}}
	old.CorpusID, old.Generation.Collection = "legacy", legacy
	for _, route := range []retrieval.Route{first, second} {
		for range 2 {
			if err := httpStore.EnsureGeneration(ctx, route.Generation.Collection, org, route.CorpusID, route.Generation.ID); err != nil {
				t.Fatal(err)
			}
		}
		routeCtx, err := tenantContext(ctx, InitialCollection, org, route.CorpusID, route.Generation.ID)
		if err != nil {
			t.Fatal(err)
		}
		if exists, err := httpStore.tenantExists(routeCtx, InitialCollection); err != nil || !exists {
			t.Fatalf("tenant %s missing after ensure: %v", route.CorpusID, err)
		}
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			_, _ = httpStore.PurgeGeneration(cleanup, InitialCollection, org, route.CorpusID, route.Generation.ID)
		})
	}
	a := []float32{1, 0, 0, 0}
	b := []float32{0, 1, 0, 0}
	// Before the first write, both queries must add their different indexes
	// to the same schema without overwriting the other's configuration.
	emptyQuery := retrieval.Request{Mode: "semantic", Vector: a, K: 1}
	if rows, err := grpcStore.Search(ctx, []retrieval.Route{first, second}, scope, emptyQuery); err != nil || len(rows) != 0 {
		t.Fatalf("parallel named-vector setup on empty tenants: %v (%v)", rows, err)
	}
	for _, route := range []retrieval.Route{first, second, old} {
		publisher := grpcStore
		if route.CorpusID == first.CorpusID {
			publisher = httpStore
		}
		projectTenantDocument(t, ctx, publisher, org, route, route.CorpusID+"-beacon", "cobalt beacon", a)
		projectTenantDocument(t, ctx, publisher, org, route, route.CorpusID+"-meadow", "ochre meadow", b)
	}

	for _, mode := range []string{"lexical", "semantic", "hybrid"} {
		q := retrieval.Request{Query: "beacon", Vector: a, Mode: mode, GroupBy: "record", K: 4}
		for _, reader := range []*Store{httpStore, grpcStore} {
			rows, err := reader.Search(ctx, []retrieval.Route{first}, scope, q)
			legacyRows, legacyErr := reader.Search(ctx, []retrieval.Route{old}, scope, q)
			if err != nil || legacyErr != nil || len(rows) == 0 || len(rows) != len(legacyRows) {
				t.Fatalf("%s single corpus: V5 %v (%v), legacy %v (%v)", mode, rows, err, legacyRows, legacyErr)
			}
			for i, row := range rows {
				if row.SegmentID != strings.Replace(legacyRows[i].SegmentID, "legacy-", "first-", 1) ||
					math.Abs(row.Score-legacyRows[i].Score) > 1e-6 {
					t.Fatalf("%s parity: V5 %v, legacy %v", mode, rows, legacyRows)
				}
			}

			q.K = 2
			for _, neighbor := range []retrieval.Route{second, old} {
				mixed, err := reader.Search(ctx, []retrieval.Route{neighbor, first}, scope, q)
				if err != nil || len(mixed) != 2 || mixed[0].SegmentID != "first-beacon" ||
					mixed[1].SegmentID != neighbor.CorpusID+"-beacon" || mixed[0].Score != 1./61 || mixed[1].Score != 1./61 {
					t.Fatalf("%s across first and %s: %v (%v)", mode, neighbor.CorpusID, mixed, err)
				}
			}
			q.K = 4
		}
	}
	if err := httpStore.SetGenerationActive(ctx, InitialCollection, org, first.CorpusID, first.Generation.ID, false); err != nil {
		t.Fatal(err)
	}
	tenantCtx, _ := tenantContext(ctx, InitialCollection, org, first.CorpusID, first.Generation.ID)
	if result, err := grpcStore.PurgeVersionInGeneration(ctx, InitialCollection, org, first.CorpusID, first.Generation.ID, "first-meadow", true); err != nil || !result.Complete || result.Deleted != 2 {
		t.Fatalf("version purge must delete the item and its unified segment: %+v (%v)", result, err)
	}
	var status tenantSpec
	if _, err := httpStore.call(ctx, "GET", tenantsPath(InitialCollection)+"/"+tenantFrom(tenantCtx), nil, &status); err != nil ||
		(status.ActivityStatus != "INACTIVE" && status.ActivityStatus != "COLD") {
		t.Fatalf("archived tenant should remain inactive after routine version purge: %+v (%v)", status, err)
	}
	if err := httpStore.SetGenerationActive(ctx, InitialCollection, org, first.CorpusID, first.Generation.ID, true); err != nil {
		t.Fatal(err)
	}
	q := retrieval.Request{Query: "beacon", Mode: "lexical", GroupBy: "record", K: 2}
	rows, err := grpcStore.Search(ctx, []retrieval.Route{first}, scope, q)
	if err != nil || len(rows) != 1 || rows[0].SegmentID != "first-beacon" {
		t.Fatalf("reactivated tenant lost results: %v (%v)", rows, err)
	}

	started := time.Now()
	result, err := grpcStore.PurgeGeneration(ctx, InitialCollection, org, first.CorpusID, first.Generation.ID)
	elapsed := time.Since(started)
	if err != nil || !result.Complete || result.Deleted != 0 || elapsed >= time.Second {
		t.Fatalf("whole-tenant purge took %v: %+v (%v)", elapsed, result, err)
	}
	if exists, err := httpStore.tenantExists(tenantCtx, InitialCollection); exists || err != nil {
		t.Fatalf("purged tenant still exists: %v (%v)", exists, err)
	}
	rows, err = grpcStore.Search(ctx, []retrieval.Route{second}, scope, q)
	if err != nil || len(rows) != 1 || rows[0].SegmentID != "second-beacon" {
		t.Fatalf("purge affected a neighbor in the same generation: %v (%v)", rows, err)
	}
	if result, err := httpStore.PurgeGeneration(ctx, legacy, org, old.CorpusID, old.Generation.ID); err != nil || result.Deleted != 5 {
		t.Fatalf("legacy generation purge: %+v (%v)", result, err)
	}
	if result, err := httpStore.PurgeGeneration(ctx, legacy, org, old.CorpusID, old.Generation.ID); err != nil || !result.Complete || result.Deleted != 0 {
		t.Fatalf("legacy generation purge did not drain: %+v (%v)", result, err)
	}
	t.Logf("whole-tenant purge acknowledged in %v", elapsed)
}

func projectTenantDocument(t testing.TB, ctx context.Context, store *Store, org string, route retrieval.Route, id, text string, vector []float32) {
	t.Helper()
	v := content.Version{ID: id, RecordID: id, Manifest: content.Manifest{Parts: []content.Part{
		{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: text}},
	}}}
	seg := content.Segmentation{ID: id, VersionID: id, Segments: []content.Segment{{ID: id, PartKey: "body", Text: text}}}
	if strings.HasSuffix(id, "-beacon") {
		seg.Segments = append(seg.Segments, content.Segment{ID: "a-footer-" + id, PartKey: "body", Text: "Page 1"})
		seg.Segments[0].Text = text + " that identifies the entrance to the northern inlet for approaching vessels"
	}
	g := route.Generation
	if err := store.Publish(ctx, g, org, route.CorpusID, "example", v, seg); err != nil {
		t.Fatalf("publish %s: %v", id, err)
	}
	tenantCtx, _ := tenantContext(ctx, g.Collection, org, route.CorpusID, g.ID)
	anchorID := itemAnchor(g, org, route.CorpusID, "example", v, nil).ID
	before, found, err := store.object(tenantCtx, g.Collection, anchorID)
	if err != nil || !found {
		t.Fatalf("read anchor %s: %v", id, err)
	}
	raw, _ := content.VectorBytes(vector)
	e := content.Embedding{Organization: org, CorpusID: route.CorpusID, SegmentID: id, SpaceID: g.SpaceID, Payload: content.Blob{SHA256: content.Hash(raw)}}
	if err := store.PublishEmbeddings(ctx, g, org, []content.EmbeddingData{{Artifact: e, Vector: vector}}); err != nil {
		t.Fatalf("embed %s: %v", id, err)
	}
	if err := store.Publish(ctx, g, org, route.CorpusID, "example", v, seg); err != nil {
		t.Fatalf("replay %s: %v", id, err)
	}
	after, found, err := store.object(tenantCtx, g.Collection, anchorID)
	if err != nil || !found || !reflect.DeepEqual(before, after) || len(after.Vectors) != 0 {
		t.Fatalf("item anchor changed on enrichment or replay: before %+v, after %+v (%v)", before, after, err)
	}
}

func realTenantStores(t testing.TB) (*Store, *Store) {
	t.Helper()
	path := os.Getenv("QUIVR_ADAPTER_CONFIG")
	if path == "" {
		t.Skip("make verify real adapters")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		URL  string `json:"weaviate_url"`
		GRPC string `json:"weaviate_grpc_address"`
		Key  string `json:"weaviate_api_key"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatal(err)
	}
	grpcStore, closeStore, err := NewWithTransport(cfg.URL, cfg.GRPC, outbound.TLS{}, cfg.Key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeStore)
	return New(cfg.URL, cfg.Key), grpcStore
}
