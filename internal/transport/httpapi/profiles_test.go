package httpapi_test

import (
	"context"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

type deadlineRouting struct{ remaining time.Duration }

func (d *deadlineRouting) Authorize(ctx context.Context, _ corpus.Scope, _ []string) error {
	deadline, _ := ctx.Deadline()
	d.remaining = time.Until(deadline)
	return retrieval.ErrUnavailable
}
func (*deadlineRouting) Generation(context.Context, string, string) (content.Generation, error) {
	return content.Generation{}, retrieval.ErrUnavailable
}

// A router-backed search reaches authorization under its selected profile's
// hard bound, even when it exceeds the ordinary HTTP request deadline.
func TestSearchUsesRoutedProfileDeadline(t *testing.T) {
	set, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: "../../../sdks/go/examples/fusion-retriever/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:1"}})
	if err != nil {
		t.Fatal(err)
	}
	live, err := plugins.NewLive("first", set)
	if err != nil {
		t.Fatal(err)
	}
	routing := &deadlineRouting{}
	s := retrieval.Service{Routing: routing, ProfilesRouter: pluginhttp.LiveRetriever{Live: live}}
	handler, err := httpapi.New(knownCorpora{}, content.Service{}, s, uploads.Service{}, map[string]corpus.Scope{
		observer: {Organization: "org_a", Actions: []string{"search:query", "content:read"}, Corpora: []string{"*"}},
	}, []byte("cursor-key-0123456789abcdef0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	res, body := operationCall(t, server, "POST", "/v0/search", observer, "application/json", `{"query":"library","corpus_ids":["corpus_a"],"profile":"deep"}`)
	if res.StatusCode != 503 || body["code"] != "search_unavailable" || routing.remaining < 8*time.Second || routing.remaining > 9*time.Second {
		t.Fatalf("profile deadline %s, response %d: %v", routing.remaining, res.StatusCode, body)
	}
}

// The public list serializes the full name and aliases, including an empty
// array for profiles without aliases. Authorization and unknown-name errors
// stay observable through the same HTTP boundary.
func TestSearchProfilesExposeDeploymentNames(t *testing.T) {
	set, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: "../../../sdks/go/examples/fusion-retriever/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:1"}})
	if err != nil {
		t.Fatal(err)
	}
	live, err := plugins.NewLive("first", set)
	if err != nil {
		t.Fatal(err)
	}
	s := retrieval.Service{ProfilesRouter: pluginhttp.LiveRetriever{Live: live, Aliases: map[string]string{"default": "example.fusion_retriever/deep", "careful": "example.fusion_retriever/deep"}}}
	handler, err := httpapi.New(knownCorpora{}, content.Service{}, s, uploads.Service{}, map[string]corpus.Scope{
		observer:       {Organization: "org_a", Actions: []string{"search:query", "content:read"}, Corpora: []string{"*"}},
		fencedObserver: {Organization: "org_a", Actions: []string{"content:read"}, Corpora: []string{"*"}},
	}, []byte("cursor-key-0123456789abcdef0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	body := getJSON(t, server, "/v0/search/profiles", observer, 200)
	items := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("profiles %v", body)
	}
	deep := items[0].(map[string]any)
	plain := items[1].(map[string]any)
	if deep["full_name"] != "example.fusion_retriever/deep" || !reflect.DeepEqual(deep["aliases"], []any{"careful", "default"}) || plain["full_name"] != "example.fusion_retriever/default" || !reflect.DeepEqual(plain["aliases"], []any{}) {
		t.Fatalf("profiles %v", body)
	}
	if deep["provider"].(map[string]any)["plugin_id"] != "example.fusion_retriever" {
		t.Fatalf("provider %v", deep)
	}
	if e := getJSON(t, server, "/v0/search/profiles", fencedObserver, 403); e["code"] != "forbidden" {
		t.Fatalf("forbidden %v", e)
	}
	res, e := operationCall(t, server, "POST", "/v0/search", observer, "application/json", `{"query":"library","corpus_ids":["corpus_a"],"profile":"missing/plugin"}`)
	if res.StatusCode != 422 || e["code"] != "unsupported_profile" {
		t.Fatalf("unknown full profile %v", e)
	}
}
