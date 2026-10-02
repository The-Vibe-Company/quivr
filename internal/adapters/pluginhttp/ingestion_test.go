package pluginhttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/processing"
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
			var pin *plugins.Pin
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveDiscovery(w, r, pin) {
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(422)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": c.code, "message": "query exceeds 128 tokens", "retryable": false})
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), plugins.ManifestFile)
			if err := os.WriteFile(path, []byte(embedderManifest), 0o600); err != nil {
				t.Fatal(err)
			}
			var err error
			pin, err = plugins.LoadPin(plugins.PinConfig{Manifest: path, Endpoint: server.URL})
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
	var pin *plugins.Pin
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDiscovery(w, r, pin) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		answers[0](w)
		answers = answers[1:]
	}))
	path := filepath.Join(t.TempDir(), plugins.ManifestFile)
	if err := os.WriteFile(path, []byte(embedderManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	pin, err = plugins.LoadPin(plugins.PinConfig{Manifest: path, Endpoint: server.URL})
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
	var pin *plugins.Pin
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDiscovery(w, r, pin) {
			return
		}
		calls++
		w.WriteHeader(503)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), plugins.ManifestFile)
	if err := os.WriteFile(path, []byte(embedderManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	pin, err = plugins.LoadPin(plugins.PinConfig{Manifest: path, Endpoint: server.URL})
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

// A segment_and_embed call that reaches the plugin's deadline is told apart
// from an outage: enrichment counts deadlines toward a bound, never outages.
func TestSegmentAndEmbedReportsItsDeadline(t *testing.T) {
	var pin *plugins.Pin
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDiscovery(w, r, pin) {
			return
		}
		_, _ = io.Copy(io.Discard, r.Body) // the server notices the caller leave once the body is read
		<-r.Context().Done()
	}))
	defer hang.Close()
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	path := filepath.Join(t.TempDir(), plugins.ManifestFile)
	if err := os.WriteFile(path, []byte(embedderManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	version := content.Version{ID: "version", Manifest: content.Manifest{Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: "text"}}}}}
	for _, c := range []struct {
		name, endpoint string
		deadline       bool
	}{{"hanging plugin", hang.URL, true}, {"plugin down", down.URL, false}} {
		var err error
		pin, err = plugins.LoadPin(plugins.PinConfig{Manifest: path, Endpoint: c.endpoint})
		if err != nil {
			t.Fatal(err)
		}
		pin.Manifest.Contributions.Ingestion.TimeoutMS = 50
		_, err = pluginhttp.Ingestor{Pin: pin}.SegmentAndEmbed(context.Background(), "org", "corpus", version, []string{plugins.SpaceKey("acme.embedder.small", "1")})
		if !errors.Is(err, pluginhttp.ErrUnavailable) || errors.Is(err, processing.ErrPluginDeadline) != c.deadline {
			t.Errorf("%s: %v; want unavailability, a deadline: %v", c.name, err, c.deadline)
		}
	}
}

// Source routes and query ownership resolve from the same immutable work plan,
// even after the deployment switches its default and source routes.
func TestLiveIngestionRoutesSourceAndQueryToPinnedOwners(t *testing.T) {
	ctx := context.Background()
	var calls, bound []string
	makePin := func(id string, vector []float32) *plugins.Pin {
		t.Helper()
		var pin *plugins.Pin
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if serveDiscovery(w, r, pin) {
				return
			}
			calls = append(calls, id+r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			if strings.HasSuffix(r.URL.Path, "embed_query") {
				_ = json.NewEncoder(w).Encode(map[string]any{"vector": vector})
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		}))
		t.Cleanup(server.Close)
		var err error
		pin, err = plugins.LoadPinManifest([]byte(strings.ReplaceAll(embedderManifest, "acme.embedder", id)), id, plugins.PinConfig{Endpoint: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		pin.Registration = "registration-" + id
		return pin
	}
	pdf := makePin("example.pdf", []float32{1, 0})
	text := makePin("example.text", []float32{0, 1})
	set, err := plugins.NewPinSet([]*plugins.Pin{pdf, text})
	if err != nil {
		t.Fatal(err)
	}
	if err = set.ConfigureIngestion(plugins.IngestionRouting{Default: "example.pdf", Routes: map[string]string{"text/plain": "example.text", "application/pdf": "example.pdf"}}); err != nil {
		t.Fatal(err)
	}
	live, err := plugins.NewLive("plan-routed", set)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := live.Pin(ctx, plugins.Work{Kind: plugins.WorkIngestion, Organization: "org", ID: "receipt", Plan: "plan-routed", BindIngestion: func(_ context.Context, registration string) error { bound = append(bound, registration); return nil }}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	next, _ := plugins.NewPinSet([]*plugins.Pin{pdf, text})
	if err = next.ConfigureIngestion(plugins.IngestionRouting{Default: "example.text"}); err != nil {
		t.Fatal(err)
	}
	if err = live.Store("plan-next", next); err != nil {
		t.Fatal(err)
	}
	ingestor := pluginhttp.LiveIngestor{Live: live}
	for _, c := range []struct{ media, owner string }{{"application/pdf", "example.pdf"}, {"text/plain", "example.text"}, {"text/html", "example.pdf"}, {"", "example.text"}} {
		v := content.Version{ID: "version", SourceMediaType: c.media, Manifest: content.Manifest{Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: "normalized text"}}}}}
		space := c.owner + ".small@1"
		if _, err = ingestor.SegmentAndEmbed(pinned, "org", "corpus", v, []string{space}); !errors.Is(err, pluginhttp.ErrUnavailable) {
			t.Fatalf("source %s: %v", c.media, err)
		}
		if got := bound[len(bound)-1]; got != "registration-"+c.owner {
			t.Fatalf("source %s bound to %s, want %s", c.media, got, c.owner)
		}
		vector, err := ingestor.EncodeQuery(pinned, "org", space, "query")
		want := []float32{1, 0}
		if c.owner == "example.text" {
			want = []float32{0, 1}
		}
		if err != nil || !slices.Equal(vector, want) {
			t.Fatalf("space %s: %v %v, want %v", space, vector, err, want)
		}
		if got := calls[len(calls)-2:]; !strings.HasPrefix(got[0], c.owner) || !strings.HasPrefix(got[1], c.owner) {
			t.Fatalf("source %s reached %v, want %s", c.media, got, c.owner)
		}
	}
	// The PDF owner leaves the active plan while an earlier generation still
	// carries its space. Query encoding keeps reaching its recorded owner.
	remaining, err := plugins.NewPinSet([]*plugins.Pin{text})
	if err != nil {
		t.Fatal(err)
	}
	if err = live.Store("plan-without-pdf", remaining); err != nil {
		t.Fatal(err)
	}
	ingestor.Owners = retainedIngestionOwner{pin: pdf}
	vector, err := ingestor.EncodeQuery(ctx, "org", "example.pdf.small@1", "query")
	if err != nil || !slices.Equal(vector, []float32{1, 0}) || !ingestor.Owns("example.pdf.small@1") {
		t.Fatalf("retained PDF owner: %v %v", vector, err)
	}
}

type retainedIngestionOwner struct{ pin *plugins.Pin }

func (r retainedIngestionOwner) IngestionSpaceOwner(_ context.Context, space string) (*plugins.Pin, error) {
	for name, declared := range r.pin.Manifest.Contributions.Ingestion.Spaces {
		if plugins.SpaceKey(name, declared.Version) == space {
			return r.pin, nil
		}
	}
	return nil, nil
}

// serveDiscovery gives HTTP peers the pin's discovery contract. Contribution
// assertions still observe only POST requests.
func serveDiscovery(w http.ResponseWriter, r *http.Request, pin *plugins.Pin) bool {
	if r.URL.Path != "/v0/discovery" {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"plugin_api": pin.PluginAPI(), "plugin": map[string]string{"id": pin.Manifest.ID, "version": pin.Manifest.Version}, "manifest_digest": pin.ManifestDigest, "contributions": pin.Manifest.Contributions.Names()})
	return true
}
