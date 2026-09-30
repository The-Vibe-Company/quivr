package pluginhttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
)

const embedderManifest = `id: acme.embedder
version: 0.1.0
compatibility:
  engine: ">=0.1.0 <0.2.0"
  plugin_api: ">=0.6.0 <0.7.0"
contributions:
  ingestion:
    spaces:
      acme.embedder.small:
        version: "1"
        model: acme/small
        dimensions: 2
        metric: cosine
        indexes: [text]
        query_modalities: [text]
`

// A plugin refuses a query terminally: query_too_long names its limit to the
// caller, any other code is a refusal of the query without detail.
func TestEncodeQueryPassesOnTheLimitAPluginNames(t *testing.T) {
	for _, c := range []struct {
		code, want string
		err        error
	}{
		{code: "query_too_long", err: retrieval.ErrQueryTooLong, want: "query exceeds 128 tokens"},
		{code: "unsupported_query", err: content.ErrInvalid},
	} {
		t.Run(c.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(422)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": c.code, "message": "query exceeds 128 tokens", "retryable": false})
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), plugins.ManifestFile)
			if err := os.WriteFile(path, []byte(embedderManifest), 0o600); err != nil {
				t.Fatal(err)
			}
			pin, err := plugins.LoadPin(plugins.PinConfig{Manifest: path, Endpoint: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			_, err = pluginhttp.Ingestor{Pin: pin}.EncodeQuery(context.Background(), "org", plugins.SpaceKey("acme.embedder.small", "1"), "a long query")
			if !errors.Is(err, c.err) || publicerr.Detail(err) != c.want {
				t.Fatalf("error %v (detail %q), want %v (detail %q)", err, publicerr.Detail(err), c.err, c.want)
			}
		})
	}
}

// Every invocation reaches the observer with its plugin, operation and
// Organization: a valid answer without an error code, a declared error with
// the plugin's code, and a plugin that does not answer as unavailable.
func TestObserverSeesEveryInvocationOutcome(t *testing.T) {
	var calls []pluginhttp.Call
	pluginhttp.Observe(func(c pluginhttp.Call) {
		if c.Organization == "org_observed" {
			calls = append(calls, c)
		}
	})
	defer pluginhttp.Observe(func(pluginhttp.Call) {})
	answers := []func(http.ResponseWriter){
		func(w http.ResponseWriter) {
			_ = json.NewEncoder(w).Encode(map[string]any{"vector": []float64{0.6, 0.8}})
		},
		func(w http.ResponseWriter) {
			w.WriteHeader(422)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "unsupported_query", "message": "no", "retryable": false})
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		answers[0](w)
		answers = answers[1:]
	}))
	path := filepath.Join(t.TempDir(), plugins.ManifestFile)
	if err := os.WriteFile(path, []byte(embedderManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	pin, err := plugins.LoadPin(plugins.PinConfig{Manifest: path, Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ingestor := pluginhttp.Ingestor{Pin: pin}
	space := plugins.SpaceKey("acme.embedder.small", "1")
	for range 2 {
		_, _ = ingestor.EncodeQuery(context.Background(), "org_observed", space, "a query")
	}
	server.Close()
	_, _ = ingestor.EncodeQuery(context.Background(), "org_observed", space, "a query")
	var codes []string
	for _, c := range calls {
		if c.PluginID != "acme.embedder" || c.Version != "0.1.0" || c.Operation != pluginhttp.OpEmbedQuery {
			t.Fatalf("observed call %+v; want acme.embedder 0.1.0 embed_query", c)
		}
		codes = append(codes, c.ErrorCode)
	}
	if want := []string{"", "unsupported_query", "plugin_unavailable"}; !slices.Equal(codes, want) {
		t.Fatalf("observed error codes %q; want %q", codes, want)
	}
}

// Work a rollback stopped (pinned_work=stop), even during an attempt already
// running, never calls a plugin of its plan once that plugin has left the
// active plan: the call fails as unavailable without reaching the plugin, and
// the work stops at once with pinned_plan_stopped naming its plan, without
// spending the attempt budget. While the plugin still serves the active plan,
// the work calls it.
func TestStoppedWorkNeverCallsTheAbandonedVersion(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(503)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), plugins.ManifestFile)
	if err := os.WriteFile(path, []byte(embedderManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	pin, err := plugins.LoadPin(plugins.PinConfig{Manifest: path, Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	pin.Registration = "registration_b"
	set, err := plugins.NewPinSet([]*plugins.Pin{pin})
	if err != nil {
		t.Fatal(err)
	}
	live, err := plugins.NewLive("plan_b", set)
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	count := func(context.Context) (int, error) { attempts++; return attempts, nil }
	// The rollback marks the work after this attempt was pinned.
	marked := false
	stopMarked := func(context.Context) bool { return marked }
	work, err := live.Pin(context.Background(), plugins.Work{Kind: plugins.WorkIngestion, Organization: "org", ID: "receipt", Plan: "plan_b", StopMarked: stopMarked}, count, 10)
	if err != nil {
		t.Fatal(err)
	}
	ingestor := pluginhttp.Ingestor{Pin: pin}
	version := content.Version{RecordID: "record", ID: "version", Manifest: content.Manifest{Kind: "document", Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: "a paragraph"}}}}}
	space := []string{plugins.SpaceKey("acme.embedder.small", "1")}
	if _, err = ingestor.SegmentAndEmbed(work, "org", "corpus", version, space); calls != 1 {
		t.Fatalf("while its plugin serves the active plan, stopped work called it %d times (%v); want 1", calls, err)
	}

	marked = true
	if _, err = ingestor.SegmentAndEmbed(work, "org", "corpus", version, space); calls != 2 {
		t.Fatalf("stopped while its plugin still serves the active plan: %d calls (%v), want the plugin called", calls, err)
	}
	if err = live.Store("plan_a", nil); err != nil {
		t.Fatal(err)
	}
	_, err = ingestor.SegmentAndEmbed(work, "org", "corpus", version, space)
	if !errors.Is(err, pluginhttp.ErrUnavailable) || calls != 2 {
		t.Fatalf("after the rollback: %v with %d calls, want unavailable without calling the plugin", err, calls)
	}
	reason, err := ingestor.Gone(work, err)
	if err != nil || reason == nil || reason.Code != plugins.CodePinnedPlanStopped || reason.Plan != "plan_b" || reason.PluginVersion != "0.1.0" || attempts != 0 {
		t.Fatalf("the stopped work: %+v (%v), %d attempts; want pinned_plan_stopped naming plan_b at once", reason, err, attempts)
	}
}
