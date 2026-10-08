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
	"sync/atomic"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/processing"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

const embedderManifest = `id: acme.embedder
version: 0.1.0
compatibility:
  engine: ">=0.1.0 <0.3.0"
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

// Installed derivation identity must survive JSONB formatting and deployment
// relocation, while changes to configuration or the model declaration split it.
func TestIngestionDerivationIdentity(t *testing.T) {
	base, err := plugins.LoadPinManifest([]byte(embedderManifest), "embedder", plugins.PinConfig{Endpoint: "http://127.0.0.1:9900", Configuration: json.RawMessage(`{"limit":6,"model":{"name":"small","revision":9007199254740993}}`)})
	if err != nil {
		t.Fatal(err)
	}
	before := (pluginhttp.Ingestor{Pin: base}).Descriptor()
	for _, c := range []struct {
		name, config, manifest string
		same                   bool
	}{
		{"JSONB formatting", `{"model": {"revision": 9007199254740993.0, "name": "small"}, "limit": 6.0}`, embedderManifest, true},
		{"segment limit", `{"limit":3,"model":{"name":"small","revision":9007199254740993}}`, embedderManifest, false},
		{"model setting", `{"limit":6,"model":{"name":"large","revision":9007199254740993}}`, embedderManifest, false},
		{"adjacent large number", `{"limit":6,"model":{"name":"small","revision":9007199254740992}}`, embedderManifest, false},
		{"model declaration", string(base.Configuration), strings.ReplaceAll(embedderManifest, "model: acme/small", "model: acme/large"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			pin, err := plugins.LoadPinManifest([]byte(c.manifest), "embedder", plugins.PinConfig{Endpoint: "http://127.0.0.1:9901", Configuration: json.RawMessage(c.config)})
			if err != nil {
				t.Fatal(err)
			}
			pin.Registration = "another-registration"
			pin.Spaces = map[string]string{"acme.embedder.small": "evaluation"}
			after := (pluginhttp.Ingestor{Pin: pin}).Descriptor()
			if (before.Recipe == after.Recipe) != c.same || (before.Producer == after.Producer) != c.same {
				t.Fatalf("recipe/producer identity equal: %v/%v, want %v", before.Recipe == after.Recipe, before.Producer == after.Producer, c.same)
			}
			if owner := content.PluginOfRecipe(after.Recipe); owner != "acme.embedder" {
				t.Fatalf("recipe owner %q, want acme.embedder", owner)
			}
		})
	}
	// An omitted configuration and its resolved empty object converge too.
	var emptyRecipe string
	for _, config := range []json.RawMessage{nil, json.RawMessage(`{}`)} {
		pin, err := plugins.LoadPinManifest([]byte(embedderManifest), "embedder", plugins.PinConfig{Endpoint: "http://127.0.0.1:9900", Configuration: config})
		if err != nil {
			t.Fatal(err)
		}
		recipe := (pluginhttp.Ingestor{Pin: pin}).Descriptor().Recipe
		if emptyRecipe != "" && recipe != emptyRecipe {
			t.Fatal("omitted configuration and empty object produced different recipes")
		}
		emptyRecipe = recipe
	}
}

// Execution declarations must change provider pacing without changing cuts or
// vectors. This descriptor boundary owns configuration/manifest recipe identity.
func TestIngestionExecutionIdentity(t *testing.T) {
	manifest := embedderManifest + `
configuration:
  execution_keys: [max_concurrent_requests, batch_size, max_batch_tokens, request_timeout_ms, call_budget_ms]
  schema:
    type: object
    properties:
      max_concurrent_requests: {const: 4}
`
	configuration := `{"model":"small","document_template":"{prefix}{text}","body_tokens":512,"max_tokens_per_segment":512,"max_concurrent_requests":4,"batch_size":16,"max_batch_tokens":8192,"request_timeout_ms":4000,"call_budget_ms":30000}`
	load := func(manifest, configuration string) *plugins.Pin {
		t.Helper()
		pin, err := plugins.LoadPinManifest([]byte(manifest), "embedder", plugins.PinConfig{Endpoint: "http://127.0.0.1:9900", Configuration: json.RawMessage(configuration)})
		if err != nil {
			t.Fatal(err)
		}
		return pin
	}
	before := (pluginhttp.Ingestor{Pin: load(manifest, configuration)}).Descriptor()
	for _, tc := range []struct {
		name, old, next string
		same            bool
	}{
		{"concurrency and generated const", `"max_concurrent_requests":4`, `"max_concurrent_requests":16`, true},
		{"batch size", `"batch_size":16`, `"batch_size":32`, true},
		{"batch tokens", `"max_batch_tokens":8192`, `"max_batch_tokens":16384`, true},
		{"request timeout", `"request_timeout_ms":4000`, `"request_timeout_ms":8000`, true},
		{"call budget", `"call_budget_ms":30000`, `"call_budget_ms":60000`, true},
		{"omitted tuning", `,"batch_size":16`, ``, true},
		{"model", `"model":"small"`, `"model":"large"`, false},
		{"template", `"document_template":"{prefix}{text}"`, `"document_template":"title: {title} | text: {text}"`, false},
		{"body budget", `"body_tokens":512`, `"body_tokens":256`, false},
		{"tokens per segment", `"max_tokens_per_segment":512`, `"max_tokens_per_segment":256`, false},
		{"undeclared setting", `"body_tokens":512`, `"body_tokens":512,"runtime_note":1`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nextManifest := manifest
			if tc.name == "concurrency and generated const" {
				nextManifest = strings.Replace(manifest, "const: 4", "const: 16", 1)
			}
			after := (pluginhttp.Ingestor{Pin: load(nextManifest, strings.Replace(configuration, tc.old, tc.next, 1))}).Descriptor()
			if (before.Recipe == after.Recipe) != tc.same {
				t.Fatalf("recipe equal = %v, want %v: %s -> %s", before.Recipe == after.Recipe, tc.same, before.Recipe, after.Recipe)
			}
		})
	}
	for _, change := range []struct {
		name, manifest string
		same           bool
	}{
		{"declaration order", strings.Replace(manifest, "[max_concurrent_requests, batch_size, max_batch_tokens, request_timeout_ms, call_budget_ms]", "[call_budget_ms, request_timeout_ms, max_batch_tokens, batch_size, max_concurrent_requests]", 1), true},
		{"declaration contract", strings.Replace(manifest, "batch_size, ", "", 1), false},
		{"plugin version", strings.Replace(manifest, "version: 0.1.0", "version: 0.2.0", 1), false},
		{"model declaration", strings.Replace(manifest, "model: acme/small", "model: acme/large", 1), false},
	} {
		t.Run(change.name, func(t *testing.T) {
			after := (pluginhttp.Ingestor{Pin: load(change.manifest, configuration)}).Descriptor()
			if (before.Recipe == after.Recipe) != change.same {
				t.Fatalf("recipe equal = %v, want %v", before.Recipe == after.Recipe, change.same)
			}
		})
	}
	// Manifest admission accepts YAML-only object keys by normalizing them to
	// JSON strings. Recipe hashing must retain those semantic schema constants.
	yamlManifest := manifest + "      semantic: {enum: [{1: first}]}\n"
	first := (pluginhttp.Ingestor{Pin: load(yamlManifest, configuration)}).Descriptor()
	second := (pluginhttp.Ingestor{Pin: load(strings.Replace(yamlManifest, "1: first", "1: second", 1), configuration)}).Descriptor()
	if first.Recipe == second.Recipe {
		t.Fatal("different valid YAML schema constants shared a recipe")
	}
}

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

// An upgraded sidecar may replace the manifest at the old endpoint. Live
// imports retain the exact pin through that outage, beyond the operation budget.
func TestPinnedImportRecoversWhenItsBuildReturns(t *testing.T) {
	var serving *plugins.Pin
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDiscovery(w, r, serving) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"segments":[{"part_key":"body","start":0,"end":11,"vectors":{"acme.embedder.small":[1,0]}}]}`)
	}))
	defer server.Close()
	load := func(manifest, registration string) (*plugins.Pin, *plugins.PinSet) {
		t.Helper()
		pin, err := plugins.LoadPinManifest([]byte(manifest), "embedder", plugins.PinConfig{Endpoint: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		pin.Registration = registration
		set, err := plugins.NewPinSet([]*plugins.Pin{pin})
		if err != nil {
			t.Fatal(err)
		}
		return pin, set
	}
	a, planA := load(embedderManifest, "registration_a")
	b, planB := load(strings.Replace(embedderManifest, "version: 0.1.0", "version: 0.2.0", 1), "registration_b")
	live, err := plugins.NewLive("plan_a", planA)
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	count := func(context.Context) (int, error) { attempts++; return attempts, nil }
	work, err := live.Pin(context.Background(), plugins.Work{Kind: plugins.WorkIngestion, Plan: "plan_a"}, count, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err = live.Store("plan_b", planB); err != nil {
		t.Fatal(err)
	}
	serving = b
	ingestor := pluginhttp.LiveIngestor{Live: live}.Ingestor(work, "text/plain")
	v := content.Version{RecordID: "record", ID: "version", Manifest: content.Manifest{Kind: "document", Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: "a paragraph"}}}}}
	spaces := []string{"acme.embedder.small@1"}
	for attempt := range 4 {
		_, cause := ingestor.SegmentAndEmbed(work, "org", "corpus", v, spaces)
		if !errors.Is(cause, pluginhttp.ErrUnavailable) {
			t.Fatalf("attempt %d reached the replacement: %v", attempt+1, cause)
		}
		if reason, err := ingestor.(processing.Pinned).Gone(work, cause); err != nil || reason != nil {
			t.Fatalf("attempt %d terminally stopped an import during an upgrade: %+v (%v)", attempt+1, reason, err)
		}
	}
	serving = a
	segments, err := ingestor.SegmentAndEmbed(work, "org", "corpus", v, spaces)
	if err != nil || len(segments) != 1 || !slices.Equal(segments[0].Vectors[spaces[0]], []float32{1, 0}) {
		t.Fatalf("restoring the exact old build did not resume the import: %+v (%v)", segments, err)
	}
	// An incompatible owner is still bounded, even for live imports.
	reason, err := ingestor.(processing.Pinned).Gone(work, processing.ErrSpaceUnowned)
	if err != nil || reason != nil {
		t.Fatalf("first incompatible-owner failure must retry: %+v (%v)", reason, err)
	}
	reason, err = ingestor.(processing.Pinned).Gone(work, processing.ErrSpaceUnowned)
	if err != nil || reason == nil || reason.Code != plugins.CodePinnedPluginUnavailable {
		t.Fatalf("incompatible owner must retain its budget: %+v (%v)", reason, err)
	}
}

// Work a rollback stopped (pinned_work=stop), even during an attempt already
// running, never calls its ingestion owner again. An outgoing source owner
// can remain an active evaluation member: the call still fails without reaching
// it, and the diagnostic names the stopped plan without spending the budget.
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
	if _, err = ingestor.SegmentAndEmbed(work, "org", "corpus", version, space); !errors.Is(err, pluginhttp.ErrUnavailable) || calls != 1 {
		t.Fatalf("stopped while its owner remains an active member: %d calls (%v), want unavailable without calling", calls, err)
	}
	if err = live.Store("plan_a", nil); err != nil {
		t.Fatal(err)
	}
	_, err = ingestor.SegmentAndEmbed(work, "org", "corpus", version, space)
	if !errors.Is(err, pluginhttp.ErrUnavailable) || calls != 1 {
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
	var serving atomic.Pointer[plugins.Pin]
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDiscovery(w, r, serving.Load()) {
			return
		}
		_, _ = io.Copy(io.Discard, r.Body) // the server notices the caller leave once the body is read
		<-r.Context().Done()
	}))
	defer hang.Close()
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	raw, err := os.ReadFile("../../../contracts/plugins/v0/fixtures/manifests/valid/ingestion-paged.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ring, err := plugins.NewSigningKeys()
	if err != nil {
		t.Fatal(err)
	}
	signing, _ := json.Marshal(map[string]plugins.SigningKeys{"example.paged": ring})
	t.Setenv(plugins.EnvSigningKeys, string(signing))
	version := content.Version{ID: "version", Manifest: content.Manifest{Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: "text"}}}}}
	for _, c := range []struct {
		name, endpoint  string
		deadline, paged bool
	}{{"hanging plugin", hang.URL, true, false}, {"plugin down", down.URL, false, false}, {"hanging paged plugin", hang.URL, true, true}, {"paged plugin down", down.URL, false, true}} {
		manifest := []byte(embedderManifest)
		if c.paged {
			manifest = raw
		}
		pin, err := plugins.LoadPinManifest(manifest, "embedder", plugins.PinConfig{Endpoint: c.endpoint})
		if err != nil {
			t.Fatal(err)
		}
		pin.Manifest.Contributions.Ingestion.TimeoutMS = 50
		pin.Registration = "registration_a"
		set, err := plugins.NewPinSet([]*plugins.Pin{pin})
		if err != nil {
			t.Fatal(err)
		}
		live, err := plugins.NewLive("plan_a", set)
		if err != nil {
			t.Fatal(err)
		}
		attempts := 0
		work, err := live.Pin(t.Context(), plugins.Work{Kind: plugins.WorkIngestion, Plan: "plan_a"}, func(context.Context) (int, error) { attempts++; return attempts, nil }, 2)
		if err != nil {
			t.Fatal(err)
		}
		if err = live.Store("plan_b", nil); err != nil {
			t.Fatal(err)
		}
		serving.Store(pin) // publish only after this case has finished configuring its pin
		ingestor := pluginhttp.Ingestor{Pin: pin}
		for attempt := range 2 {
			if c.paged {
				_, err = ingestor.SegmentAndEmbedPage(work, "org", "corpus", version, nil, nil)
			} else {
				_, err = ingestor.SegmentAndEmbed(work, "org", "corpus", version, []string{plugins.SpaceKey("acme.embedder.small", "1")})
			}
			// Bounded pages preserve completed work and do not consume the
			// legacy per-item deadline budget, but retain the pin budget.
			if !errors.Is(err, pluginhttp.ErrUnavailable) || errors.Is(err, processing.ErrPluginDeadline) != (c.deadline && !c.paged) {
				t.Fatalf("%s: %v; want unavailability, a deadline: %v", c.name, err, c.deadline && !c.paged)
			}
			reason, goneErr := ingestor.Gone(work, err)
			if goneErr != nil {
				t.Fatal(goneErr)
			}
			if c.deadline && attempt == 1 {
				if reason == nil || reason.Code != plugins.CodePinnedPluginUnavailable {
					t.Fatalf("%s: deadline must retain the pinned budget: %+v", c.name, reason)
				}
			} else if reason != nil {
				t.Fatalf("%s: attempt %d must retry: %+v", c.name, attempt+1, reason)
			}
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
