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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

const alertsManifest = `id: acme.alerts
version: 0.1.0
compatibility:
  engine: ">=0.1.0 <0.2.0"
  plugin_api: ">=0.2.0 <0.3.0"
contributions:
  subscription:
    expression_schema:
      type: object
      additionalProperties: false
      required: [text]
      properties:
        text: {type: string, minLength: 1}
    configuration_schema:
      type: object
      additionalProperties: false
      properties:
        case_sensitive: {type: boolean}
    max_batch_size: 4
    timeout_ms: 2000
`

// alertsPlugin serves discovery and a substring rule, recording each request.
type alertsPlugin struct {
	mu       sync.Mutex
	digest   string
	requests []map[string]any
	// answer overrides the rule when set.
	answer func(req map[string]any) (int, any)
}

func (p *alertsPlugin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v0/discovery":
		p.mu.Lock()
		digest := p.digest
		p.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"plugin_api": "0.2.0", "plugin": map[string]any{"id": "acme.alerts", "version": "0.1.0"}, "manifest_digest": digest, "contributions": []string{"subscription"}})
	case "/v0/contributions/subscription":
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		p.mu.Lock()
		p.requests = append(p.requests, req)
		answer := p.answer
		p.mu.Unlock()
		if answer != nil {
			status, out := answer(req)
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		record := req["record"].(map[string]any)
		decisions := []any{}
		for _, raw := range req["evaluations"].([]any) {
			e := raw.(map[string]any)
			text := e["expression"].(map[string]any)["text"].(string)
			decision := map[string]any{"id": e["id"], "decision": "no_match"}
			for _, part := range record["parts"].([]any) {
				if strings.Contains(part.(map[string]any)["text"].(string), text) {
					decision = map[string]any{"id": e["id"], "decision": "match", "evidence": map[string]any{"explanation": text + " appears", "part_keys": []any{part.(map[string]any)["key"]}}}
				}
			}
			decisions = append(decisions, decision)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"decisions": decisions})
	default:
		http.NotFound(w, r)
	}
}

func alertsEvaluator(t *testing.T) (pluginhttp.Evaluator, *alertsPlugin) {
	t.Helper()
	path := filepath.Join(t.TempDir(), plugins.ManifestFile)
	if err := os.WriteFile(path, []byte(alertsManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	plugin := &alertsPlugin{}
	server := httptest.NewServer(plugin)
	t.Cleanup(server.Close)
	pin, err := plugins.LoadPin(plugins.PinConfig{Manifest: path, Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	plugin.digest = pin.ManifestDigest
	return pluginhttp.Evaluator{Pin: pin}, plugin
}

func article() monitoring.Batch {
	accepted := time.Date(2026, 9, 29, 8, 0, 3, 0, time.UTC)
	return monitoring.Batch{Organization: "org_a", CorpusID: "news", RecordID: "record_1", VersionID: "version_1", Enriched: true,
		Article: monitoring.Article{
			Parts: []monitoring.Part{{Key: "title", Role: "title", Text: "Harbour workers vote to strike"}},
			Metadata: monitoring.RecordMetadata{
				Source:     monitoring.SourceIdentity{Namespace: "wire", RecordKey: "story-42", Position: "p-9"},
				AcceptedAt: accepted,
				Provenance: monitoring.RecordProvenance{Origin: monitoring.OriginConnector, Producer: "connector-7", ProducerVersion: "rss/1", Connector: &monitoring.ConnectorOrigin{InstanceID: "connector-7", Kind: "rss"}},
				Extensions: map[string]any{"example.editorial": map[string]any{"schema_version": "1", "data": map[string]any{"author": "Jane Doe"}}},
			},
		},
		Items: []monitoring.BatchItem{
			{ID: "e1", Expression: map[string]any{"text": "strike"}, Configuration: map[string]any{}, Subscriptions: []monitoring.SubscriptionRef{{SubscriptionID: "s1", SubscriptionVersionID: "sv1", SavedQueryID: "q1", SavedQueryVersionID: "qv1"}, {SubscriptionID: "s2", SubscriptionVersionID: "sv2", SavedQueryID: "q1", SavedQueryVersionID: "qv1"}}},
			{ID: "e2", Expression: map[string]any{"text": "election"}, Configuration: map[string]any{"case_sensitive": true}, Subscriptions: []monitoring.SubscriptionRef{{SubscriptionID: "s3", SubscriptionVersionID: "sv3", SavedQueryID: "q2", SavedQueryVersionID: "qv2", Owner: "user-123"}}},
		}}
}

// The request carries the Record Version's text and metadata and every
// distinct evaluation; the answer becomes one Outcome per item.
func TestEvaluatorSendsOneValidRequestPerBatch(t *testing.T) {
	evaluator, plugin := alertsEvaluator(t)
	if evaluator.MaxBatch() != 4 {
		t.Fatal("max batch", evaluator.MaxBatch())
	}
	out, err := evaluator.Evaluate(context.Background(), article())
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Decision != monitoring.DecisionMatch || out[0].Explanation != "strike appears" || out[0].PartKeys[0] != "title" || out[1].Decision != monitoring.DecisionNoMatch {
		t.Fatalf("outcomes %+v", out)
	}
	if len(plugin.requests) != 1 {
		t.Fatalf("requests %d", len(plugin.requests))
	}
	raw, _ := json.Marshal(plugin.requests[0])
	if issues := plugins.ValidateDocument("subscription-request.schema.json", raw); len(issues) > 0 {
		t.Fatalf("request violates the contract: %v", issues)
	}
	record := plugin.requests[0]["record"].(map[string]any)
	if record["accepted_at"] != "2026-09-29T08:00:03Z" || record["source"].(map[string]any)["record_key"] != "story-42" ||
		record["provenance"].(map[string]any)["connector"].(map[string]any)["kind"] != "rss" || record["enriched"] != true {
		t.Fatalf("record metadata %v", record)
	}
	if record["extensions"].(map[string]any)["example.editorial"].(map[string]any)["data"].(map[string]any)["author"] != "Jane Doe" {
		t.Fatalf("extensions %v", record["extensions"])
	}
	// The same batch replays the same logical invocation.
	again, _ := evaluator.SubscriptionRequest(article())
	var replay map[string]any
	_ = json.Unmarshal(again, &replay)
	if replay["idempotency_key"] != plugin.requests[0]["idempotency_key"] || replay["invocation_id"] == plugin.requests[0]["invocation_id"] {
		t.Fatal("idempotency key must be stable and invocation ids unique")
	}
}

// Failures are errors, never decisions.
func TestEvaluatorFailuresAreNeverDecisions(t *testing.T) {
	for name, tc := range map[string]struct {
		answer func(map[string]any) (int, any)
		want   error
	}{
		"plugin error envelope": {func(map[string]any) (int, any) {
			return 503, map[string]any{"code": "backend_down", "message": "try later", "retryable": true}
		}, monitoring.ErrEvaluation},
		"no envelope": {func(map[string]any) (int, any) { return 502, "bad gateway" }, monitoring.ErrEvaluatorUnavailable},
		"missing decision": {func(map[string]any) (int, any) {
			return 200, map[string]any{"decisions": []any{map[string]any{"id": "e1", "decision": "no_match"}}}
		}, monitoring.ErrEvaluationInvalid},
		"unknown part key": {func(map[string]any) (int, any) {
			return 200, map[string]any{"decisions": []any{
				map[string]any{"id": "e1", "decision": "match", "evidence": map[string]any{"explanation": "x", "part_keys": []any{"nope"}}},
				map[string]any{"id": "e2", "decision": "no_match"}}}
		}, monitoring.ErrEvaluationInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			evaluator, plugin := alertsEvaluator(t)
			plugin.answer = tc.answer
			if _, err := evaluator.Evaluate(context.Background(), article()); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	// A retryable envelope is marked so the engine retries the batch whole;
	// a terminal one is not.
	for retryable, want := range map[bool]bool{true: true, false: false} {
		evaluator, plugin := alertsEvaluator(t)
		plugin.answer = func(map[string]any) (int, any) {
			return 503, map[string]any{"code": "backend", "message": "m", "retryable": retryable}
		}
		_, err := evaluator.Evaluate(context.Background(), article())
		var declared *pluginhttp.PluginError
		if errors.Is(err, monitoring.ErrEvaluationRetryable) != want || !errors.As(err, &declared) || declared.Retryable != retryable {
			t.Fatalf("retryable=%v: %v", retryable, err)
		}
	}
	evaluator, _ := alertsEvaluator(t)
	evaluator.Pin = &plugins.Pin{Manifest: evaluator.Pin.Manifest, ManifestDigest: evaluator.Pin.ManifestDigest, Endpoint: "http://127.0.0.1:1"}
	if _, err := evaluator.Evaluate(context.Background(), article()); !errors.Is(err, monitoring.ErrEvaluatorUnavailable) {
		t.Fatalf("unreachable plugin: %v", err)
	}
}

// A request over the 16 MiB bound is refused before it is sent.
func TestEvaluatorRefusesOversizeRequests(t *testing.T) {
	evaluator, plugin := alertsEvaluator(t)
	b := article()
	b.Article.Parts = []monitoring.Part{{Key: "body", Role: "body", Text: strings.Repeat("x", plugins.SubscriptionMaxRequestBytes)}}
	if _, err := evaluator.Evaluate(context.Background(), b); !errors.Is(err, monitoring.ErrRequestTooLarge) {
		t.Fatalf("oversize: %v", err)
	}
	if len(plugin.requests) != 0 {
		t.Fatal("an oversize request was sent")
	}
}

// Validation names the request member at fault and the first schema issue.
func TestEvaluatorValidatesAgainstDeclaredSchemas(t *testing.T) {
	evaluator, _ := alertsEvaluator(t)
	if err := evaluator.Validate(map[string]any{"text": "strike"}, map[string]any{"case_sensitive": true}); err != nil {
		t.Fatal(err)
	}
	err := evaluator.Validate(map[string]any{"text": ""}, map[string]any{})
	if field, message := monitoring.Field(err); !errors.Is(err, monitoring.ErrInvalidExpression) || field != "/saved_query_version_id" || !strings.Contains(message, "/expression/text") {
		t.Fatalf("expression: %v", err)
	}
	err = evaluator.Validate(map[string]any{"text": "strike"}, map[string]any{"case_sensitive": "yes"})
	if field, _ := monitoring.Field(err); !errors.Is(err, monitoring.ErrInvalidEvaluatorConfiguration) || field != "/evaluator/configuration/case_sensitive" {
		t.Fatalf("configuration: %v %q", err, field)
	}
}
