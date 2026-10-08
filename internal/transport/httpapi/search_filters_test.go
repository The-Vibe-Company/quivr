package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
)

type identitySearch struct{ query retrieval.Request }

func (*identitySearch) Authorize(context.Context, corpus.Scope, []string) error { return nil }
func (*identitySearch) Generation(context.Context, string, string) (content.Generation, error) {
	return content.Generation{ID: "generation", Collection: "Search", ProfileVersion: retrieval.ProfileVersion, ItemKeywordsProjected: true, MetadataProjected: true}, nil
}

// The dependency returns an infrastructure failure; retrieval and HTTP own
// its public classification. The ranker only requests a query vector.
type unavailableQueryModel struct{ identitySearch }

func (*unavailableQueryModel) Generation(context.Context, string, string) (content.Generation, error) {
	return content.Generation{ID: "generation", Collection: "Search", ProfileVersion: retrieval.ProfileVersion, SpaceID: "space"}, nil
}
func (*unavailableQueryModel) VectorSpaces(context.Context, string, string) (content.Generation, []content.SpaceCoverage, int64, error) {
	space := content.SpaceCoverage{GenerationRole: content.SpaceServed}
	space.ID, space.Metric, space.QueryModalities = "space", "cosine", []string{"text"}
	space.Dimensions = 1
	return content.Generation{}, []content.SpaceCoverage{space}, 1, nil
}
func (*unavailableQueryModel) Round(context.Context, plugins.SearchRequest) ([]byte, error) {
	return []byte(`{"requests":[{"primitive":"near_vector","space":"space","query_text":"harbour","k":10}]}`), nil
}
func (*unavailableQueryModel) Space() content.VectorSpace { return content.VectorSpace{ID: "space"} }
func (*unavailableQueryModel) Embed(context.Context, string) ([]float32, error) {
	return nil, errors.New("private inference transport details")
}
func (*unavailableQueryModel) Owns(space string) bool { return space == "space" }
func (m *unavailableQueryModel) EncodeQuery(ctx context.Context, _, _, query string) ([]float32, error) {
	return m.Embed(ctx, query)
}

// This owns propagation through real search routing, candidate serving and the
// HTTP handler. The overload owner separately checks search_unavailable.
func TestSearchModelFailureResponse(t *testing.T) {
	for _, encoder := range []string{"legacy", "plugin"} {
		t.Run(encoder, func(t *testing.T) {
			model := &unavailableQueryModel{}
			search := retrieval.Service{Ranker: model, Routing: model, Projection: model, Registry: model}
			if encoder == "legacy" {
				search.Embedder = model
			} else {
				search.Spaces = model
			}
			handler, err := httpapi.New(knownCorpora{}, content.Service{}, search, uploads.Service{}, map[string]corpus.Scope{observer: {Organization: "org_a", Actions: []string{"content:read", "search:query"}, Corpora: []string{"*"}}}, catalogCursorKey)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodPost, "/v0/search", strings.NewReader(`{"query":"harbour","corpus_ids":["corpus_a"],"mode":"semantic"}`))
			r.Header.Set("Authorization", "Bearer "+observer)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			checkedAPI(t, handler).ServeHTTP(w, r)
			var body struct {
				Code, Message string
				Retryable     bool
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != 503 || body.Code != "model_unavailable" || body.Message != "model unavailable" || !body.Retryable || w.Header().Get("Retry-After") != "" {
				t.Fatalf("model failure: status=%d body=%s Retry-After=%q", w.Code, w.Body.String(), w.Header().Get("Retry-After"))
			}
		})
	}
}
func (*identitySearch) Publish(context.Context, content.Generation, string, string, string, content.Version, content.Segmentation) error {
	return nil
}
func (*identitySearch) PublishEmbeddings(context.Context, content.Generation, string, []content.EmbeddingData) error {
	return nil
}
func (s *identitySearch) Search(_ context.Context, _ []retrieval.Route, _ corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
	s.query = q
	return nil, nil
}
func (*identitySearch) Manifest() *plugins.Manifest {
	return &plugins.Manifest{ID: "example.search", Version: "1.0.0", Contributions: plugins.Contributions{Retrieval: &plugins.Retrieval{Profiles: map[string]plugins.RetrievalProfile{"default": {MaxLatencyMS: 500}}, Limits: plugins.RetrievalLimits{MaxRounds: 2, MaxRequests: 1, MaxCandidates: 50}}}}
}
func (*identitySearch) Configuration() json.RawMessage { return json.RawMessage(`{}`) }
func (*identitySearch) Round(_ context.Context, q plugins.SearchRequest) ([]byte, error) {
	if q.Round == 1 {
		return []byte(`{"requests":[{"primitive":"bm25","query_text":"harbour","k":10}]}`), nil
	}
	return []byte(`{"ranking":{"hits":[]}}`), nil
}

// Transport owns the literal external keys and their schema bounds. Adapter
// tests own matching; this recorder does not implement filtering.
func TestSearchIdentityFilterWireContract(t *testing.T) {
	s := &identitySearch{}
	handler, err := httpapi.New(knownCorpora{}, content.Service{}, retrieval.Service{Ranker: s, Routing: s, Projection: s}, uploads.Service{}, map[string]corpus.Scope{observer: {Organization: "org_a", Actions: []string{"content:read", "search:query"}, Corpora: []string{"*"}}}, []byte("cursor-key-0123456789abcdef0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		filter string
		status int
		record string
	}{
		{`{"record_ids":["record-a"],"version_ids":["version-a"]}`, 200, "record-a"},
		{`{"record_ids":["` + strings.Repeat("é", 200) + `"],"version_ids":["version-a"]}`, 200, strings.Repeat("é", 200)},
		{`{"record_ids":["` + strings.Repeat("é", 201) + `"]}`, 422, ""},
		{`{"record_ids":[]}`, 422, ""},
		{`{"version_ids":["version-a","version-a"]}`, 422, ""},
		{`{"record_ids":[7]}`, 422, ""},
	} {
		r := httptest.NewRequest(http.MethodPost, "/v0/search", strings.NewReader(`{"query":"harbour","corpus_ids":["corpus_a"],"mode":"lexical","filter":`+tc.filter+`}`))
		r.Header.Set("Authorization", "Bearer "+observer)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		checkedAPI(t, handler).ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.filter, w.Code, w.Body.String())
		}
		if tc.status == 200 && (!reflect.DeepEqual(s.query.RecordIDs, []string{tc.record}) || !reflect.DeepEqual(s.query.VersionIDs, []string{"version-a"})) {
			t.Fatalf("identity filters lost: %+v", s.query)
		}
	}
}
