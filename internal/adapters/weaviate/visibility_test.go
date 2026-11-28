package weaviate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/visibility"
	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
	"google.golang.org/protobuf/proto"
)

type testSourcePartitions map[string]visibility.Sources

func (p testSourcePartitions) Sources(_ context.Context, _, id string, _ []string) (visibility.Sources, error) {
	return p[id], nil
}

func (p testSourcePartitions) Current(context.Context, string, map[string]int64) (bool, error) {
	return true, nil
}

func (p testSourcePartitions) SourceVersions(_ context.Context, _, corpusID string, namespaces []string, _ int) ([]string, error) {
	if slices.Equal(namespaces, []string{" permitted "}) {
		return []string{corpusID + "-padded"}, nil
	}
	return nil, fmt.Errorf("unexpected exact namespace request %v", namespaces)
}

func TestVisibilityPrefilterOnSharedAndTenantCollections(t *testing.T) {
	httpStore, grpcStore := realTenantStores(t)
	if grpcStore.grpc == nil {
		t.Fatal("gRPC integration requires weaviate_grpc_address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	org := fmt.Sprintf("visibility-%d", time.Now().UnixNano())
	legacy := "QuivrTextV4Visibility"
	parts := testSourcePartitions{}
	httpStore.Visibility, grpcStore.Visibility = parts, parts
	scope := corpus.Scope{Organization: org, Corpora: []string{"*"}}
	g := content.Generation{ID: "visibility", Collection: InitialCollection, SpaceID: "fixture", ItemKeywordsProjected: true, SourceNamespaceProjected: true}
	native := retrieval.Route{CorpusID: "native", Generation: g}
	shared := retrieval.Route{CorpusID: "shared", Generation: g}
	shared.Generation.Collection = legacy
	shared.Generation.ItemKeywordsProjected = false
	for _, route := range []retrieval.Route{native, shared} {
		if err := httpStore.Bootstrap(ctx, route.Generation.Collection); err != nil {
			t.Fatal(err)
		}
		if err := httpStore.EnsureGeneration(ctx, route.Generation.Collection, org, route.CorpusID, route.Generation.ID); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := httpStore.PurgeGeneration(ctx, route.Generation.Collection, org, route.CorpusID, route.Generation.ID); err != nil {
				t.Error(err)
			}
		})
		for i, doc := range []struct {
			id, namespace string
			acl           []string
		}{
			{"visible", "permitted", nil},
			{"hidden-source", "blocked", nil},
			{"hidden-acl", "permitted", []string{"user:other"}},
			{"override", "blocked", []string{"group:reader"}},
			{"padded", " permitted ", nil},
		} {
			id := route.CorpusID + "-" + doc.id
			text := "harbour report"
			if i == 1 || i == 2 {
				text = "harbour harbour harbour"
			}
			v := content.Version{ID: id, RecordID: id, RecordACL: doc.acl, Manifest: content.Manifest{Parts: []content.Part{
				{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: text}},
			}}}
			seg := content.Segmentation{ID: id, VersionID: id, Segments: []content.Segment{{ID: id, PartKey: "body", Text: text}}}
			gen := route.Generation
			if err := httpStore.Publish(ctx, gen, org, route.CorpusID, doc.namespace, v, seg); err != nil {
				t.Fatalf("publish %s: %v", id, err)
			}
			vec := []float32{0, 1, 0, 0}
			if i == 1 || i == 2 {
				vec = []float32{1, 0, 0, 0}
			}
			raw, _ := content.VectorBytes(vec)
			artifact := content.Embedding{Organization: org, CorpusID: route.CorpusID, SegmentID: id, SpaceID: gen.SpaceID, Payload: content.Blob{SHA256: content.Hash(raw)}}
			if err := grpcStore.PublishEmbeddings(ctx, gen, org, []content.EmbeddingData{{Artifact: artifact, Vector: vec}}); err != nil {
				t.Fatalf("embed %s: %v", id, err)
			}
		}
	}
	caller := visibility.WithPrincipals(ctx, []string{"group:reader"})
	for _, scenario := range []struct {
		name            string
		allowed, denied []string
		override        bool
		want            []string
	}{
		{"all", []string{"permitted", "blocked", " permitted "}, nil, true, []string{"hidden-source", "override", "padded", "visible"}},
		{"exclude", append(sourceNamespaceList("permitted", 599), " permitted "), sourceNamespaceList("blocked", 400), true, []string{"override", "padded", "visible"}},
		{"include", append(sourceNamespaceList("permitted", 499), " permitted "), sourceNamespaceList("blocked", 500), true, []string{"override", "padded", "visible"}},
		{"exact namespace", []string{" permitted "}, []string{"permitted", "blocked"}, true, []string{"override", "padded"}},
		{"override only", nil, []string{"permitted", "blocked"}, true, []string{"override"}},
		{"short circuit", nil, []string{"permitted", "blocked"}, false, nil},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			parts["native"] = visibility.Sources{Allowed: scenario.allowed, Denied: scenario.denied, RecordOverride: scenario.override}
			parts["shared"] = parts["native"]
			for _, client := range []*Store{httpStore, grpcStore} {
				for _, mode := range []string{"lexical", "semantic", "hybrid"} {
					var want []string
					for _, route := range []retrieval.Route{native, shared} {
						for _, suffix := range scenario.want {
							want = append(want, route.CorpusID+"-"+suffix)
						}
					}
					q := retrieval.Request{Mode: mode, Query: "harbour", Vector: []float32{1, 0, 0, 0}, K: 10}
					got, err := client.Search(caller, []retrieval.Route{native, shared}, scope, q)
					var ids []string
					for _, c := range got {
						ids = append(ids, c.SegmentID)
					}
					slices.Sort(ids)
					slices.Sort(want)
					if err != nil || !slices.Equal(ids, want) {
						t.Fatalf("mode=%s grpc=%v: got %v (%v), want %v", mode, client.grpc != nil, ids, err, want)
					}
				}
			}
		})
	}
	// An explicit empty set can see public sources, but cannot match a
	// record ACL. The compiler must also avoid empty ContainsAny operands.
	parts["native"] = visibility.Sources{Allowed: []string{"permitted", " permitted "}, Denied: []string{"blocked"}, RecordOverride: true}
	for _, client := range []*Store{httpStore, grpcStore} {
		actorOnly := visibility.WithPrincipals(ctx, []string{})
		got, err := client.Search(actorOnly, []retrieval.Route{native}, scope, retrieval.Request{Mode: "lexical", Query: "harbour", K: 10})
		var ids []string
		for _, c := range got {
			ids = append(ids, c.SegmentID)
		}
		slices.Sort(ids)
		if err != nil || !slices.Equal(ids, []string{"native-padded", "native-visible"}) {
			t.Fatalf("empty principal read grpc=%v: got %v (%v)", client.grpc != nil, ids, err)
		}
	}

	changed := &changingSourcePartitions{}
	httpStore.Visibility = changed
	got, err := httpStore.Search(caller, []retrieval.Route{native}, scope, retrieval.Request{Mode: "lexical", Query: "harbour", K: 1})
	if err != nil || len(got) != 0 || changed.reads != 2 {
		t.Fatalf("stale route ranking was returned: got=%v reads=%d err=%v", got, changed.reads, err)
	}

}

// Simulate a second API instance committing revocation while Weaviate ranks
// an already-resolved partition. Only the source resolver is substituted.
type changingSourcePartitions struct {
	testSourcePartitions
	reads int
}

func (p *changingSourcePartitions) Sources(context.Context, string, string, []string) (visibility.Sources, error) {
	p.reads++
	if p.reads == 1 {
		return visibility.Sources{Allowed: []string{"permitted", "blocked", " permitted "}, Revision: 1}, nil
	}
	return visibility.Sources{Revision: 2}, nil
}

func (p *changingSourcePartitions) Current(_ context.Context, _ string, revisions map[string]int64) (bool, error) {
	return revisions["native"] == 2, nil
}

func sourceNamespaceList(prefix string, count int) []string {
	values := []string{prefix}
	for i := 1; i < count; i++ {
		values = append(values, fmt.Sprintf("%s-%04d", prefix, i))
	}
	return values
}

func BenchmarkVisibilityFilter(b *testing.B) {
	for _, n := range []int{500, 1000} {
		sources := visibility.Sources{Allowed: sourceNamespaceList("allowed", n/2), Denied: sourceNamespaceList("denied", n/2)}
		for _, transport := range []string{"http", "grpc"} {
			b.Run(fmt.Sprintf("%s/%d", transport, n), func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					f, _ := compileVisibility(sources, []string{"group:reader"})
					if transport == "http" {
						_ = f.graphql()
					} else {
						if _, err := proto.Marshal(f.proto()); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}

// Reference JSON-marshal filter renderer used solely as golden oracle.
func referenceFilterString(f filter) string {
	op := graphOperator(f.operator)
	if f.field == "" {
		children := make([]string, 0, len(f.children))
		for _, child := range f.children {
			children = append(children, referenceFilterString(child))
		}
		return "{operator:" + op + ",operands:[" + strings.Join(children, ",") + "]}"
	}
	fieldRaw, _ := json.Marshal(f.field)
	valRaw, _ := json.Marshal(f.value)
	return "{path:[" + string(fieldRaw) + "],operator:" + op + "," + f.jsonKey + ":" + string(valRaw) + "}"
}

func referenceFilterGraphQL(f filter) string {
	if f.operator == 0 {
		return ""
	}
	return "where:" + referenceFilterString(f) + ","
}

func referenceBranchGraphQL(b searchBranch) string {
	q, _ := json.Marshal(b.query)
	p, _ := json.Marshal(b.properties)
	v, _ := json.Marshal(b.vector)
	t, _ := json.Marshal(b.target)
	switch b.mode {
	case "lexical":
		return fmt.Sprintf("bm25:{query:%s,properties:%s},", q, p)
	case "semantic":
		return fmt.Sprintf("nearVector:{vector:%s,targetVectors:[%s]},", v, t)
	case "hybrid":
		fusion := "relativeScoreFusion"
		if b.ranked {
			fusion = "rankedFusion"
		}
		return fmt.Sprintf("hybrid:{query:%s,vector:%s,alpha:%s,fusionType:%s,properties:%s,targetVectors:[%s]},",
			q, v, strconv.FormatFloat(b.alpha, 'f', -1, 64), fusion, p, t)
	}
	return ""
}

func TestFilterGraphQLGolden(t *testing.T) {
	tricky := []string{
		"",
		"simple-id",
		`quote"and\slash`,
		"controls:\x00\x01\x07\b\t\n\x0b\f\r\x1f\x7f",
		"html:<script>&\"'</script>",
		"unicode:éçà😀\u2028\u2029",
		"invalid-utf8:\xff\xfe\xc0\xaf",
		`injection"},{operator:Or,operands:[{path:["id"],operator:Equal,valueText:"x`,
	}
	filters := []filter{
		{},
		equal("organization", "org-1"),
		notEqual("sourceNamespace", "blocked"),
		containsAny("sourceNamespace", tricky),
		containsAny("versionId", []string{}),
		containsAny("versionId", nil),
		{operator: pb.Filters_OPERATOR_GREATER_THAN_EQUAL, field: "m_date", jsonKey: "valueDate", value: "2026-10-01T00:00:00Z"},
		{operator: pb.Filters_OPERATOR_LESS_THAN_EQUAL, field: "m_num", jsonKey: "valueNumber", value: 42.5},
		{operator: pb.Filters_OPERATOR_EQUAL, field: "recordAclRestricted", jsonKey: "valueBoolean", value: true},
		{operator: pb.Filters_OPERATOR_NOT_EQUAL, field: "recordAclRestricted", jsonKey: "valueBoolean", value: false},
		and(equal("organization", "org"), or(equal("itemKind", "passage"), equal("itemKind", "segment"))),
	}
	for _, n := range []int{500, 1000} {
		sources := visibility.Sources{Allowed: sourceNamespaceList("allowed", n/2), Denied: sourceNamespaceList("denied", n/2), RecordOverride: true}
		f, _ := compileVisibility(sources, []string{"group:reader", `user:"quoted"`})
		filters = append(filters, f)
		sourcesExcl := visibility.Sources{Allowed: sourceNamespaceList("allowed", n/2+10), Denied: sourceNamespaceList("denied", n/2-10), RecordOverride: true}
		fe, _ := compileVisibility(sourcesExcl, []string{"group:reader"})
		filters = append(filters, fe)
	}
	for i, f := range filters {
		if got, want := f.graphql(), referenceFilterGraphQL(f); got != want {
			t.Fatalf("case %d graphql mismatch:\ngot:  %s\nwant: %s", i, got, want)
		}
		if f.operator != 0 {
			if got, want := f.String(), referenceFilterString(f); got != want {
				t.Fatalf("case %d String mismatch:\ngot:  %s\nwant: %s", i, got, want)
			}
		}
	}

	vec := []float32{0, float32(math.Copysign(0, -1)), 1, -0.5, 1e-7, 1e-6, 1e-5, 1e20, 1e21, 1e22, math.MaxFloat32, math.SmallestNonzeroFloat32, -1e-7}
	branches := []searchBranch{
		{},
		{mode: "lexical", query: `harbour "ferry" <tag>`, properties: []string{"title^2", "body"}},
		{mode: "lexical", query: "nil-props", properties: nil},
		{mode: "semantic", vector: vec, target: "semantic_text_v1"},
		{mode: "semantic", vector: nil, target: "semantic_text_v1"},
		{mode: "hybrid", query: "harbour\nline2", vector: vec, alpha: 0.35, ranked: false, properties: []string{"title^2"}, target: "t"},
		{mode: "hybrid", query: "harbour", vector: []float32{}, alpha: 0.75, ranked: true, properties: []string{}, target: "t"},
	}
	for i, br := range branches {
		if got, want := br.graphql(), referenceBranchGraphQL(br); got != want {
			t.Fatalf("branch %d mismatch:\ngot:  %s\nwant: %s", i, got, want)
		}
	}
	for _, ord := range []searchOrder{{}, {field: "segmentId"}, {field: "embeddingRevision", desc: true}, {field: `bad"field`}} {
		want := ""
		if ord.field != "" {
			dir := "asc"
			if ord.desc {
				dir = "desc"
			}
			raw, _ := json.Marshal(ord.field)
			want = fmt.Sprintf("sort:[{path:[%s],order:%s}],", raw, dir)
		}
		if got := ord.graphql(); got != want {
			t.Fatalf("order %+v mismatch: got %s, want %s", ord, got, want)
		}
	}
}

func FuzzFilterQuoteEscaping(f *testing.F) {
	seeds := []string{
		"",
		"normal",
		`"quoted"`,
		`\backslash\`,
		"\x00\x08\x09\x0a\x0c\x0d\x1f\x7f",
		"<script>&</script>",
		"\u2028\u2029",
		"\xff\xfe\xc0\xaf",
		`"},{operator:Or,operands:[`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		wantRaw, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if got := quote(s); got != string(wantRaw) {
			t.Fatalf("quote(%q) = %s, want %s", s, got, wantRaw)
		}
		if got := string(appendJSONString(nil, s)); got != string(wantRaw) {
			t.Fatalf("appendJSONString(%q) = %s, want %s", s, got, wantRaw)
		}
		flt := equal("sourceNamespace", s)
		if got, want := flt.graphql(), referenceFilterGraphQL(flt); got != want {
			t.Fatalf("filter.graphql(%q) mismatch:\ngot:  %s\nwant: %s", s, got, want)
		}
	})
}

type delayedVisibilityResolver struct {
	delay       time.Duration
	inFlight    atomic.Int64
	maxInFlight atomic.Int64
	currentOK   func(attempt int) bool
	attempts    atomic.Int64
}

func (r *delayedVisibilityResolver) Sources(ctx context.Context, _, corpusID string, _ []string) (visibility.Sources, error) {
	cur := r.inFlight.Add(1)
	for {
		prev := r.maxInFlight.Load()
		if cur <= prev || r.maxInFlight.CompareAndSwap(prev, cur) {
			break
		}
	}
	defer r.inFlight.Add(-1)
	if r.delay > 0 {
		select {
		case <-ctx.Done():
			return visibility.Sources{}, ctx.Err()
		case <-time.After(r.delay):
		}
	}
	return visibility.Sources{
		Allowed:  []string{"ns-" + corpusID},
		Revision: 7,
	}, nil
}

func (r *delayedVisibilityResolver) Current(_ context.Context, _ string, _ map[string]int64) (bool, error) {
	n := int(r.attempts.Add(1))
	if r.currentOK != nil {
		return r.currentOK(n), nil
	}
	return true, nil
}

func (r *delayedVisibilityResolver) SourceVersions(context.Context, string, string, []string, int) ([]string, error) {
	return nil, nil
}

type schemaCountingTransport struct {
	gets  atomic.Int64
	posts atomic.Int64
}

func (t *schemaCountingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body := `{}`
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/schema/"):
		t.gets.Add(1)
		body = `{"properties":[]}`
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/properties"):
		t.posts.Add(1)
	case r.URL.Path == "/v1/graphql":
		body = `{"data":{"Get":{"` + InitialCollection + `":[]}}}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

func TestResolveVisibilityParallelBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resolver := &delayedVisibilityResolver{delay: 2 * time.Millisecond}
		transport := &schemaCountingTransport{}
		store := New("http://index.invalid")
		store.Client.Transport = transport
		store.Visibility = resolver

		const routeCount = 16
		routes := make([]retrieval.Route, routeCount)
		corpora := make([]string, routeCount)
		for i := range routeCount {
			cid := fmt.Sprintf("corpus-%02d", i)
			corpora[i] = cid
			routes[i] = retrieval.Route{
				CorpusID: cid,
				Generation: content.Generation{
					ID:                       fmt.Sprintf("gen-%02d", i),
					Collection:               InitialCollection,
					SourceNamespaceProjected: true,
				},
			}
		}
		scope := corpus.Scope{Organization: "org", Corpora: corpora}

		start := time.Now()
		allowed, filters, revisions, err := store.resolveVisibility(t.Context(), routes, scope, []string{"group:reader"})
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("resolveVisibility failed: %v", err)
		}
		if elapsed > 12*time.Millisecond {
			t.Fatalf("resolveVisibility took %v with 16 routes @ 2ms, want <= 12ms", elapsed)
		}
		if maxConc := resolver.maxInFlight.Load(); maxConc > maxVisibilityConcurrency || maxConc < 2 {
			t.Fatalf("max in-flight visibility calls = %d, want in [2, %d]", maxConc, maxVisibilityConcurrency)
		}
		if len(allowed) != routeCount || len(filters) != routeCount || len(revisions) != routeCount {
			t.Fatalf("got allowed=%d filters=%d revisions=%d, want %d", len(allowed), len(filters), len(revisions), routeCount)
		}
		for i, r := range allowed {
			if r.CorpusID != routes[i].CorpusID {
				t.Fatalf("allowed[%d] = %s, want deterministic input order %s", i, r.CorpusID, routes[i].CorpusID)
			}
		}
		if gets, posts := transport.gets.Load(), transport.posts.Load(); gets != 1 || posts != 2 {
			t.Fatalf("cold collection schema calls: gets=%d posts=%d, want 1 GET and 2 POSTs", gets, posts)
		}

		// Warm call must reuse memoized properties with zero additional schema requests.
		if _, _, _, err := store.resolveVisibility(t.Context(), routes, scope, []string{"group:reader"}); err != nil {
			t.Fatalf("warm resolveVisibility failed: %v", err)
		}
		if gets, posts := transport.gets.Load(), transport.posts.Load(); gets != 1 || posts != 2 {
			t.Fatalf("warm collection issued extra schema calls: gets=%d posts=%d", gets, posts)
		}

		// Visibility Current() recheck and 2-attempt retry fence must remain intact.
		resolver.attempts.Store(0)
		resolver.currentOK = func(attempt int) bool { return attempt == 2 }
		caller := visibility.WithPrincipals(t.Context(), []string{"group:reader"})
		if _, err := store.Search(caller, routes[:2], scope, retrieval.Request{Query: "harbour", Mode: "lexical", K: 5}); err != nil {
			t.Fatalf("Search retry after stale Current() failed: %v", err)
		}
		if got := resolver.attempts.Load(); got != 2 {
			t.Fatalf("Current() attempts = %d, want 2", got)
		}

		resolver.attempts.Store(0)
		resolver.currentOK = func(int) bool { return false }
		if _, err := store.Search(caller, routes[:2], scope, retrieval.Request{Query: "harbour", Mode: "lexical", K: 5}); !errors.Is(err, retrieval.ErrUnavailable) {
			t.Fatalf("persistent stale Current() err = %v, want retrieval.ErrUnavailable", err)
		}
	})
}

func TestDecodeItemRowsStreamingEdgeCases(t *testing.T) {
	largeText := strings.Repeat("harbour-passage-", 2500) // > 32 KiB buffer boundary
	raw := fmt.Sprintf(`{"extra":{"nested":[1,true,null,"ok"]},"data":{"Get":{"Other":[{"segmentId":"skip"}],"ItemSearch":[
		{"segmentId":"seg-\"1\"\\\n\t\u00e9\uD83D\uDE00","generationId":"gen-1","versionId":"ver-1","passageText":%q,"_additional":{"score":"1.25","distance":0.25,"ignored":123}},
		{"segmentId":"seg-2","generationId":"gen-1","versionId":"ver-2","passageText":"skipped-when-false","_additional":{"score":null,"distance":null}},
		{"segmentId":"seg-3","generationId":"gen-2","versionId":null,"_additional":null}
	]}},"errors":[]}`, largeText)

	rows, err := decodeItemRows(strings.NewReader(raw), "ItemSearch", true, true, 10)
	if err != nil {
		t.Fatalf("decodeItemRows(text=true) failed: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	if want := "seg-\"1\"\\\n\té😀"; rows[0].SegmentID != want {
		t.Fatalf("row[0].SegmentID = %q, want %q", rows[0].SegmentID, want)
	}
	if rows[0].Text != largeText {
		t.Fatalf("row[0].Text length = %d, want %d", len(rows[0].Text), len(largeText))
	}
	if got := rows[0].candidate(false).Score; got != 1.25 {
		t.Fatalf("sparse score = %v, want 1.25", got)
	}
	if got := rows[0].candidate(true).Score; got != 0.75 {
		t.Fatalf("dense score = %v, want 0.75", got)
	}
	if rows[1].GenerationID != "gen-1" || rows[2].GenerationID != "gen-2" {
		t.Fatalf("generationIds = %q, %q", rows[1].GenerationID, rows[2].GenerationID)
	}
	if rows[1].candidate(false).Score != 0 || rows[1].candidate(true).Score != 0 {
		t.Fatalf("null score/distance should decode as 0")
	}

	// wantText=false skips passageText without retaining it.
	rowsNoText, err := decodeItemRows(strings.NewReader(raw), "ItemSearch", true, false, 10)
	if err != nil || len(rowsNoText) != 3 || rowsNoText[0].Text != "" || rowsNoText[1].Text != "" {
		t.Fatalf("wantText=false decode: err=%v rows=%+v", err, rowsNoText)
	}

	// Non-empty errors fails even when data is present.
	if _, err := decodeItemRows(strings.NewReader(`{"data":{"Get":{"ItemSearch":[]}},"errors":[{"message":"boom"}]}`), "ItemSearch", true, false, 1); err == nil {
		t.Fatal("expected error for non-empty GraphQL errors array")
	}

	// Missing collection fails.
	if _, err := decodeItemRows(strings.NewReader(`{"data":{"Get":{}}}`), "ItemSearch", true, false, 1); err == nil {
		t.Fatal("expected error when collection is missing from Get")
	}

	// Truncated payload at io.LimitedReader boundary surfaces errResponseLimit.
	limited := &io.LimitedReader{R: bytes.NewReader([]byte(raw)), N: int64(len(raw) / 2)}
	_, err = decodeItemRows(limited, "ItemSearch", true, true, 10)
	if err == nil || limited.N != 0 {
		t.Fatalf("truncated decode: err=%v limited.N=%d, want error with N==0", err, limited.N)
	}
}
