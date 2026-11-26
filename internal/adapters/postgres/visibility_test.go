package postgres_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/audit"
	"github.com/The-Vibe-Company/quivr/internal/changes"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
	"github.com/The-Vibe-Company/quivr/internal/visibility"
)

// PostgreSQL owns concealment, counts and rechecks. Supply two records that
// disagree with their source's ACL and a stale search candidate after revocation.
func TestCanonicalVisibilityAndGrantRevocation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	org := fmt.Sprintf("visibility-%d", time.Now().UnixNano())
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read", "search:query", "changes:read"}, Corpora: []string{"*"}}
	corpora := postgres.Store{Pool: pool}
	c, _, err := (corpus.Service{Store: corpora}).Create(ctx, scope, corpus.CreateInput{Key: "visibility", Name: "Visibility"})
	if err != nil {
		t.Fatal(err)
	}
	store := contentStores(pool)
	grants := &postgres.VisibilityStore{Pool: pool}
	remote := &postgres.VisibilityStore{Pool: pool}
	objects := &objectMemory{objects: map[string][]byte{}}
	stats := postgres.CorpusStatsStore{Pool: pool}
	service := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store,
		Catalog: store, Stats: stats, Facets: store, Blobs: objects, Baseline: store, Corpora: corpora}
	if err := grants.SetGrant(ctx, org, c.ID, "open", visibility.Grant{Visibility: visibility.Public}); err != nil {
		t.Fatal(err)
	}
	if err := grants.SetGrant(ctx, org, c.ID, "closed", visibility.Grant{Visibility: visibility.Restricted, AllowedPrincipals: []string{"user:other"}}); err != nil {
		t.Fatal(err)
	}
	reader := scope
	reader.Principals = []string{"group:readers"}
	caller := visibility.WithPrincipals(ctx, reader.Principals)
	feed := changes.Service{Journal: store, Corpora: corpora, Key: []byte("fixture-change-cursor-key-0000000")}
	boundary, err := feed.Start(caller, reader, c.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	token := feed.CursorResume(reader, c.ID, boundary)
	type document struct {
		record, version, receipt string
		candidate                content.Candidate
	}
	docs := map[string]document{}
	g, err := store.Generation(ctx, org, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		key, namespace string
		acl            []string
	}{
		{"allowed", "open", nil},
		{"withheld", "open", []string{"user:other"}},
		{"denied", "closed", nil},
		{"granted", "closed", []string{"group:readers"}},
		{"unconfigured", "absent", nil},
	} {
		text := "Part content for " + item.key
		cmd := content.Command{Key: item.key, Source: content.Source{CorpusID: c.ID, Namespace: item.namespace, RecordKey: item.key}, ACL: item.acl, Content: content.Text{Kind: "text", Text: text}}
		r, err := service.Accept(ctx, scope, cmd)
		if err != nil {
			t.Fatal(err)
		}
		work, _, err := store.Work(ctx, org, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		manifest := content.ManifestFor(cmd)
		markup, _ := json.Marshal(manifest)
		textBlob, _ := objects.Put(ctx, org, []byte(text))
		manifestBlob, _ := objects.Put(ctx, org, markup)
		if err = store.Publish(ctx, work, publication(textBlob, manifestBlob)); err != nil {
			t.Fatal(err)
		}
		version := content.Version{ID: work.VersionID, RecordID: work.RecordID, Manifest: manifest}
		cut, err := wholeParts(org, version)
		if err != nil {
			t.Fatal(err)
		}
		if err = service.SaveSegmentation(ctx, org, version, cut); err != nil {
			t.Fatal(err)
		}
		if err = service.Promote(ctx, org, cut, g); err != nil {
			t.Fatal(err)
		}
		if err = store.SaveProjectionMetadata(ctx, org, version.ID, g.ID, map[string]any{"kind": item.key}); err != nil {
			t.Fatal(err)
		}
		docs[item.key] = document{work.RecordID, version.ID, r.ID, content.Candidate{GenerationID: g.ID, SegmentID: cut.Segments[0].ID}}
	}
	want := []string{docs["allowed"].record, docs["granted"].record}
	slices.Sort(want)
	q := content.RecordQuery{CorpusIDs: []string{c.ID}, Limit: 1}
	first, err := service.Records(caller, reader, c.ID, q)
	if err != nil || len(first) != 1 || first[0].ID != want[0] {
		t.Fatalf("first page = %+v (%v), want %s", first, err, want[0])
	}
	q.AfterID = first[0].ID
	second, err := service.Records(caller, reader, c.ID, q)
	if err != nil || len(second) != 1 || second[0].ID != want[1] {
		t.Fatalf("second page = %+v (%v), want %s", second, err, want[1])
	}
	unrestricted, err := service.Records(ctx, scope, c.ID, content.RecordQuery{Limit: 10})
	if err != nil || len(unrestricted) != len(docs) {
		t.Fatalf("legacy catalog got %d (%v), want %d", len(unrestricted), err, len(docs))
	}

	meta := corpus.MetadataFilter{Field: "kind", AnyOf: []any{"allowed", "withheld", "denied", "granted", "unconfigured"}}
	matched, err := service.Records(caller, reader, c.ID, content.RecordQuery{
		CorpusIDs: []string{c.ID}, Limit: 10, Metadata: []corpus.MetadataFilter{meta},
		FilterRoutes: []content.CatalogFilterRoute{{CorpusID: c.ID, GenerationID: g.ID, Filters: []corpus.TypedFilter{{MetadataFilter: meta, Type: "string"}}}},
	})
	if err != nil || len(matched) != 2 || matched[0].ID != want[0] || matched[1].ID != want[1] {
		t.Fatalf("delegated metadata page = %+v (%v), want %v", matched, err, want)
	}

	var candidates []content.Candidate
	var wantHydrated []int
	for _, key := range []string{"allowed", "withheld", "denied", "granted", "unconfigured"} {
		d := docs[key]
		visible := slices.Contains(want, d.record)
		if visible {
			wantHydrated = append(wantHydrated, len(candidates))
		}
		candidates = append(candidates, d.candidate)
		_, recordErr := service.Record(caller, reader, d.record)
		_, receiptErr := service.Receipt(caller, reader, d.receipt)
		version, versionErr := service.Version(caller, reader, d.record, d.version)
		for _, e := range []error{recordErr, receiptErr, versionErr} {
			if visible && e != nil || !visible && !errors.Is(e, corpus.ErrNotFound) {
				t.Fatalf("%s direct visibility error = %v, visible=%v", key, e, visible)
			}
		}
		if visible && (len(version.Manifest.Parts) != 1 || version.Manifest.Parts[0].Content.Text != "Part content for "+key) {
			t.Fatalf("%s expected canonical Part, got %+v", key, version.Manifest.Parts)
		}
	}
	projection := postgres.ProjectionStore{Pool: pool}
	located, err := projection.Hydrate(caller, reader, candidates)
	if err != nil || len(located) != len(wantHydrated) {
		t.Fatalf("hydrated %v (%v)", located, err)
	}
	for _, i := range wantHydrated {
		if located[i].Hydrated.Segment.ID != candidates[i].SegmentID {
			t.Fatalf("missing authorized candidate %d", i)
		}
	}
	count, err := service.RecordCount(caller, reader, c.ID, content.RecordQuery{})
	if err != nil || count.Count != 2 || count.Approximate {
		t.Fatalf("count %+v (%v)", count, err)
	}
	totals, err := service.CorpusStats(caller, reader, c.ID, content.CorpusStatsQuery{Histogram: true, Sources: true})
	if err != nil || totals.Total != 2 || totals.CatalogTotal != 2 || totals.Histogram == nil || len(totals.Histogram.Items) != 1 || totals.Histogram.Items[0].Count != 2 || totals.Sources == nil || len(totals.Sources.Items) != 2 {
		t.Fatalf("stats %+v (%v)", totals, err)
	}
	facets, err := store.CountFacetsFast(caller, org, content.FacetQuery{
		Records: content.RecordQuery{CorpusIDs: []string{c.ID}, FilterRoutes: []content.CatalogFilterRoute{{CorpusID: c.ID, GenerationID: g.ID}}},
		Fields:  []content.FacetField{{Field: "kind", Type: "string", Limit: 10}}, Fast: true,
	})
	if err != nil || len(facets.Items) != 1 || len(facets.Items[0].Buckets) != 2 {
		t.Fatalf("facets %+v (%v)", facets, err)
	}
	for _, b := range facets.Items[0].Buckets {
		if b.Count != 1 || (b.Value != "allowed" && b.Value != "granted") {
			t.Fatalf("wrong facet bucket %+v", b)
		}
	}

	page, err := feed.Poll(caller, reader, c.ID, token, 1)
	if err != nil || len(page.Items) != 1 || !page.HasMore {
		t.Fatalf("poll %+v (%v)", page, err)
	}
	handler, err := httpapi.New(corpora, service, retrieval.Service{}, uploads.Service{}, map[string]corpus.Scope{
		"reader":  {Organization: org, Actions: reader.Actions, Corpora: reader.Corpora, VisibilityMode: "enforce"},
		"manager": {Organization: org, Actions: []string{"visibility:read", "visibility:write"}, Corpora: []string{c.ID}},
	}, feed.Key, httpapi.WithVisibility(grants), httpapi.WithChanges(feed, 0), httpapi.WithAudit(postgres.AuditStore{Pool: pool}))
	if err != nil {
		t.Fatal(err)
	}
	for _, lookup := range []struct {
		record, principals string
		want               int
	}{
		{docs["allowed"].record, "group:readers", 200},
		{docs["allowed"].record, "", 200},
		{docs["granted"].record, "", 404},
	} {
		r := httptest.NewRequest("GET", "/v0/records/"+lookup.record, nil)
		r.Header.Set("Authorization", "Bearer reader")
		r.Header.Set("X-Quivr-Actor", "38e61a6d8a6f4849aca7b0f7dd7880d6")
		r.Header.Set("X-Quivr-Principals", lookup.principals)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != lookup.want {
			t.Fatalf("delegated GET with principal list %q: status=%d want %d, response %s", lookup.principals, w.Code, lookup.want, w.Body)
		}
	}

	grantURL := "/v0/corpora/" + c.ID + "/visibility-grants?source_namespace=open"
	request := func(method, path, key, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := request("GET", grantURL, "manager", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"visibility":"public"`) {
		t.Fatalf("grant GET %d %s", w.Code, w.Body)
	}
	snapshot, err := remote.Sources(ctx, org, c.ID, reader.Principals)
	if err != nil || !slices.Equal(snapshot.Allowed, []string{"open"}) || !snapshot.RecordOverride {
		t.Fatalf("initial projection source partition %+v (%v)", snapshot, err)
	}
	beforeRevocation := snapshot.Revision
	w = request("DELETE", grantURL, "manager", "")
	if w.Code != 204 {
		t.Fatalf("grant DELETE %d %s", w.Code, w.Body)
	}
	allowed, err := projection.RecheckHydration(caller, reader, candidates)
	if err != nil || len(allowed) != 1 || !allowed[3] {
		t.Fatalf("revocation recheck %+v (%v)", allowed, err)
	}
	if _, err := service.Version(caller, reader, docs["allowed"].record, docs["allowed"].version); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatalf("revoked version err=%v", err)
	}
	// The remote API instance may still hold its pre-write snapshot, so wait
	// for the observable cache result with a deadline below one second.
	expiry, stop := context.WithTimeout(ctx, 900*time.Millisecond)
	defer stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot, err = remote.Sources(expiry, org, c.ID, reader.Principals)
		if err != nil {
			t.Fatalf("projection cache failed to refresh: %v", err)
		}
		if len(snapshot.Allowed) == 0 {
			break
		}
		select {
		case <-expiry.Done():
			t.Fatalf("grant revocation remained cached: %+v", snapshot)
		case <-ticker.C:
		}
	}
	// The query's revision fence rejects scores from the stale partition
	// independently of the TTL and canonical hydration.
	current, err := remote.Current(ctx, org, map[string]int64{c.ID: beforeRevocation})
	if err != nil || current {
		t.Fatalf("pre-revocation index results were accepted: %v (%v)", current, err)
	}
	current, err = remote.Current(ctx, org, map[string]int64{c.ID: snapshot.Revision})
	if err != nil || !current {
		t.Fatalf("refreshed index results were refused: %v (%v)", current, err)
	}
	page, err = feed.Poll(caller, reader, c.ID, token, 100)
	if err != nil || len(page.Items) < 2 || page.HasMore {
		t.Fatalf("revoked poll %+v (%v)", page, err)
	}
	checkEvent := func(kind, id string) {
		t.Helper()
		if !(kind == "record" && id == docs["granted"].record || kind == "receipt" && id == docs["granted"].receipt) {
			t.Fatalf("event leaked %s %s", kind, id)
		}
	}
	for _, e := range page.Items {
		checkEvent(e.ResourceKind, e.ResourceID)
	}
	// Re-open the source before the SSE page read. Revoke it when the first
	// event is written, while subsequent allowed-source events are buffered.
	w = request("PUT", grantURL, "manager", `{"visibility":"public","allowed_principals":[]}`)
	if w.Code != 204 {
		t.Fatalf("reopen for SSE: %d %s", w.Code, w.Body)
	}
	revocation := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(&revokeDuringSSE{ResponseWriter: w, revoke: func() {
			revocation <- grants.DeleteGrant(ctx, org, c.ID, "open")
		}}, r)
	}))
	defer server.Close()
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	r, err := http.NewRequestWithContext(streamCtx, "GET", server.URL+"/v0/changes/stream?corpus_id="+c.ID+"&cursor="+token, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer reader")
	r.Header.Set("X-Quivr-Actor", "38e61a6d8a6f4849aca7b0f7dd7880d6")
	r.Header.Set("X-Quivr-Principals", "group:readers")
	response, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("SSE status %d", response.StatusCode)
	}
	scanner := bufio.NewScanner(response.Body)
	seen := 0
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		var event struct{ Resource struct{ ID, Kind string } }
		if err = json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatal(err)
		}
		if seen == 0 {
			if event.Resource.Kind != "receipt" || event.Resource.ID != docs["allowed"].receipt {
				t.Fatalf("SSE must authorize the first event before revocation: %+v", event)
			}
		} else {
			checkEvent(event.Resource.Kind, event.Resource.ID)
		}
		seen++
		if seen == len(page.Items)+1 {
			break
		}
	}
	if seen != len(page.Items)+1 {
		t.Fatalf("stream emitted %d of %d permitted events (%v)", seen, len(page.Items)+1, scanner.Err())
	}
	if err := <-revocation; err != nil {
		t.Fatalf("mid-stream revocation: %v", err)
	}
	w = request("PUT", grantURL, "manager", `{"visibility":"restricted","allowed_principals":["group:readers"]}`)
	if w.Code != 204 {
		t.Fatalf("grant PUT %d %s", w.Code, w.Body)
	}
	snapshot, err = grants.Sources(ctx, org, c.ID, reader.Principals)
	if err != nil || !slices.Equal(snapshot.Allowed, []string{"open"}) {
		t.Fatalf("local regrant %+v (%v)", snapshot, err)
	}
	audited, err := (postgres.AuditStore{Pool: pool}).List(ctx, org, audit.Filter{Action: "visibility.set", TargetID: c.ID, Limit: 10})
	if err != nil || len(audited) != 2 || audited[0].Outcome != "accepted" || audited[1].Outcome != "accepted" {
		t.Fatalf("grant transaction audit: %+v (%v)", audited, err)
	}

	// The ranker can receive a legitimate excerpt and take time to rank it.
	// Revocation inside the rank call must still hide it from the response.
	search := retrieval.Service{
		Routing: projection, Content: service, Projection: coverageIndex{candidate: docs["allowed"].candidate},
		Ranker: revokeDuringRanking{revoke: func(ctx context.Context) error {
			return grants.DeleteGrant(ctx, org, c.ID, "open")
		}},
	}
	result, err := search.Search(caller, reader, retrieval.Request{Query: "alpha", CorpusIDs: []string{c.ID}, Mode: "lexical"})
	if err != nil || len(result.Hits) != 0 || result.Usage == nil || result.Usage.Rounds != 2 {
		t.Fatalf("revoked candidate was returned or never reached ranking: %+v (%v)", result, err)
	}
}

// The first change can leave the server. Its write then revokes the source
// synchronously, so later events from the same SQL page need a fresh check.
type revokeDuringSSE struct {
	http.ResponseWriter
	revoke func()
}

func (w *revokeDuringSSE) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *revokeDuringSSE) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if w.revoke != nil && strings.Contains(string(p), "event: change\n") {
		w.revoke()
		w.revoke = nil
	}
	return n, err
}

type revokeDuringRanking struct {
	coverageRanker
	revoke func(context.Context) error
}

func (r revokeDuringRanking) Round(ctx context.Context, req plugins.SearchRequest) ([]byte, error) {
	ranking, err := r.coverageRanker.Round(ctx, req)
	if err != nil || req.Round != 2 {
		return ranking, err
	}
	if err := r.revoke(ctx); err != nil {
		return nil, err
	}
	return ranking, nil
}
