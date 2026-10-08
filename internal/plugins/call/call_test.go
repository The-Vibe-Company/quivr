package call_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/call"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

// A different running manifest must never receive work intended for the pin.
// This guards the common invocation seam; adding a Contribution cannot skip it.
func TestInvocationRefusesDiscoveryMismatch(t *testing.T) {
	for _, mismatch := range []string{"digest", "version"} {
		t.Run(mismatch, func(t *testing.T) {
			posts := 0
			pin := policyPin()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts++
					_, _ = w.Write([]byte(`{}`))
					return
				}
				digest, version := pin.ManifestDigest, pin.Manifest.Version
				if mismatch == "digest" {
					digest = "sha256:" + strings.Repeat("0", 64)
				} else {
					version = "2.0.0"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"plugin_api": pin.PluginAPI(), "plugin": map[string]string{"id": pin.Manifest.ID, "version": version}, "manifest_digest": digest, "contributions": pin.Manifest.Contributions.Names()})
			}))
			defer server.Close()
			pin.Endpoint = server.URL
			for _, operation := range operations {
				_, err := call.Invoke(context.Background(), pin, operation, call.Bytes([]byte(`{}`)), nil, nil)
				if err == nil || posts != 0 {
					t.Fatalf("%s with %s mismatch: error %v, posts %d; want refusal and no POST", operation, mismatch, err, posts)
				}
			}
		})
	}

}

// A single stopped-work guard protects every operation, including secondary
// calls such as encoding queries, receive and attachment exchange.
func TestCancelledAndStoppedWorkNeverReachesPeer(t *testing.T) {
	pin := policyPin()
	set, err := plugins.NewPinSet([]*plugins.Pin{pin})
	if err != nil {
		t.Fatal(err)
	}
	live, err := plugins.NewLive("old", set)
	if err != nil {
		t.Fatal(err)
	}
	work, err := live.Pin(context.Background(), plugins.Work{Kind: plugins.WorkIngestion, Organization: "org", ID: "receipt", Plan: "old", Stopped: true}, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := live.Store("new", nil); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, operation := range operations {
		for _, ctx := range []context.Context{work, cancelled} {
			_, err := call.Invoke(ctx, pin, operation, func(context.Context, string) ([]byte, error) {
				t.Fatal("cancelled/stopped work built a request")
				return nil, nil
			}, nil, nil)
			if err == nil {
				t.Fatalf("%s: expected stopped/cancelled refusal", operation)
			}
		}
	}
}

// Deadlines are applied before preparing a request and never extend an outer
// activity or search deadline. No test waits for a timer to expire.
func TestInvocationOwnsDeadlineInsideOuterEnvelope(t *testing.T) {
	pin := policyPin()
	server := peer(t, pin, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	defer server.Close()
	pin.Endpoint = server.URL
	for _, operation := range operations {
		t.Run(operation, func(t *testing.T) {
			var deadline bool
			_, err := call.Invoke(context.Background(), pin, operation, func(ctx context.Context, _ string) ([]byte, error) {
				end, ok := ctx.Deadline()
				deadline = ok
				if !ok || time.Until(end) > 2*time.Second {
					t.Errorf("%s deadline %v, want <= declared 2s", operation, end)
				}
				return nil, errors.New("request construction ended")
			}, nil, nil)
			if err == nil || !deadline {
				t.Fatalf("%s: missing invocation deadline (%v)", operation, err)
			}
			end := time.Now().Add(time.Second)
			outer, cancel := context.WithDeadline(context.Background(), end)
			defer cancel()
			_, _ = call.Invoke(outer, pin, operation, func(ctx context.Context, _ string) ([]byte, error) {
				actual, _ := ctx.Deadline()
				if actual != end {
					t.Errorf("outer deadline changed: %v, want %v", actual, end)
				}
				return nil, errors.New("done")
			}, nil, nil)
		})
	}
	// Virtual time exercises expiry in every phase without wall-clock waits.
	// The transport supplies wire bytes; real discovery checks and policy run.
	for _, operation := range []string{call.Normalize, call.SegmentAndEmbed} {
		for _, phase := range []string{"discovery", "build", "response", "parent cancellation", "outer deadline"} {
			t.Run("expiry/"+operation+"/"+phase, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					pin := policyPin()
					parent, cancel := context.WithCancel(context.Background())
					if phase == "outer deadline" {
						parent, cancel = context.WithTimeout(context.Background(), time.Second)
					}
					defer cancel()
					discovery, err := json.Marshal(map[string]any{"plugin_api": pin.PluginAPI(), "plugin": map[string]string{"id": pin.Manifest.ID, "version": pin.Manifest.Version}, "manifest_digest": pin.ManifestDigest, "contributions": pin.Manifest.Contributions.Names()})
					if err != nil {
						t.Fatal(err)
					}
					previous := http.DefaultTransport
					defer func() { http.DefaultTransport = previous }()
					http.DefaultTransport = roundTrip(func(r *http.Request) (*http.Response, error) {
						body := "{}"
						if r.URL.Path == "/v0/discovery" {
							if phase == "discovery" {
								<-r.Context().Done()
								return nil, r.Context().Err()
							}
							body = string(discovery)
						}
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
					})
					_, err = call.Invoke(parent, pin, operation, func(ctx context.Context, _ string) ([]byte, error) {
						if phase == "build" {
							<-ctx.Done()
							return nil, ctx.Err()
						}
						return []byte("{}"), nil
					}, func(ctx context.Context, _ []byte) []plugins.Issue {
						if phase == "response" || phase == "outer deadline" {
							<-ctx.Done()
						}
						if phase == "parent cancellation" {
							cancel()
						}
						return nil
					}, nil)
					deadline := phase != "parent cancellation" && phase != "outer deadline"
					if !errors.Is(err, plugins.ErrUnavailable) || errors.Is(err, plugins.ErrCallDeadline) != deadline {
						t.Fatalf("%s: %v; want unavailable with inner deadline=%t", phase, err, deadline)
					}
				})
			})
		}
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

var operations = []string{call.Normalize, call.SegmentAndEmbed, call.EmbedQuery, call.SearchRound, call.EvaluateSubscription, call.ConnectorFetch, call.CheckCredential, call.ConnectorReceive, call.DescribeAttachment, call.UploadAttachment}

func policyPin() *plugins.Pin {
	return &plugins.Pin{Endpoint: "http://127.0.0.1:1", ManifestDigest: "sha256:" + strings.Repeat("a", 64), Configuration: json.RawMessage(`{}`), Manifest: plugins.Manifest{ID: "test.calls", Version: "1.0.0", Compatibility: plugins.Compatibility{PluginAPI: ">=0.6.0 <0.14.0"}, Contributions: plugins.Contributions{
		Normalizer: &plugins.Normalizer{TimeoutMS: 2000}, Ingestion: &plugins.Ingestion{TimeoutMS: 2000, QueryTimeoutMS: 2000}, Subscription: &plugins.Subscription{TimeoutMS: 2000}, Connector: &plugins.Connector{TimeoutMS: 2000, Attachments: &plugins.ConnectorAttachments{TimeoutMS: 2000}}, Retrieval: &plugins.Retrieval{},
	}}}
}
func peer(t *testing.T, pin *plugins.Pin, post http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v0/discovery" {
			_ = json.NewEncoder(w).Encode(map[string]any{"plugin_api": pin.PluginAPI(), "plugin": map[string]string{"id": pin.Manifest.ID, "version": pin.Manifest.Version}, "manifest_digest": pin.ManifestDigest, "contributions": pin.Manifest.Contributions.Names()})
			return
		}
		post(w, r)
	}))
}

// Result classes belong to the invocation module. The HTTP peer supplies wire
// envelopes and schema-invalid answers; it never supplies the asserted errors.
func TestInvocationClassifiesWireOutcomes(t *testing.T) {
	cases := []struct {
		name, operation string
		status          int
		body            string
		want            error
		declared        bool
		class           connectors.ErrorClass
		code            string
		secrets         []string
	}{
		{name: "success", operation: call.EmbedQuery, status: 200, body: `{"vector":[1,0]}`},
		{name: "retryable", operation: call.Normalize, status: 503, body: `{"code":"busy","message":"later","retryable":true}`, declared: true},
		{name: "terminal", operation: call.SegmentAndEmbed, status: 422, body: `{"code":"bad_input","message":"refused","retryable":false}`, want: content.ErrIngestionRefused, declared: true},
		{name: "query limit", operation: call.EmbedQuery, status: 422, body: `{"code":"query_too_long","message":"128 tokens","retryable":false}`, want: retrieval.ErrQueryTooLong},
		{name: "query refused", operation: call.EmbedQuery, status: 422, body: `{"code":"unsupported","message":"refused","retryable":false}`, want: content.ErrInvalid},
		{name: "search refused", operation: call.SearchRound, status: 422, body: `{"code":"unsupported","message":"refused","retryable":false}`, want: content.ErrInvalid},
		{name: "evaluation retryable", operation: call.EvaluateSubscription, status: 503, body: `{"code":"busy","message":"later","retryable":true}`, want: monitoring.ErrEvaluationRetryable, declared: true},
		{name: "evaluation terminal", operation: call.EvaluateSubscription, status: 422, body: `{"code":"bad_input","message":"refused","retryable":false}`, want: monitoring.ErrEvaluation, declared: true},
		{name: "malformed envelope", operation: call.EmbedQuery, status: 502, body: `bad gateway`, want: plugins.ErrUnavailable},
		{name: "invalid output", operation: call.EmbedQuery, status: 200, body: `{}`, want: content.ErrIngestionRefused},
		{name: "connector access", operation: call.ConnectorReceive, status: 401, body: `{"code":"denied","message":"no","retryable":false,"error_class":"access"}`, class: connectors.ClassAccess, code: "denied"},
		{name: "connector retryable", operation: call.ConnectorFetch, status: 503, body: `{"code":"busy","message":"later","retryable":true,"error_class":"transient"}`, class: connectors.ClassTransient, code: "busy"},
		{name: "connector source", operation: call.DescribeAttachment, status: 422, body: `{"code":"gone","message":"removed","retryable":false,"error_class":"source"}`, class: connectors.ClassSource, code: "gone"},
		{name: "contradictory connector error", operation: call.CheckCredential, status: 422, body: `{"code":"bad","message":"no","retryable":true,"error_class":"source"}`, class: connectors.ClassSource, code: "plugin_invalid_error"},
		{name: "credential leak", operation: call.UploadAttachment, status: 200, body: `{"uploaded":true,"secret":"credential_value"}`, class: connectors.ClassSource, code: "credential_leak"},
		// An empty string is in every body; it holds no secret (THE-1314).
		{name: "empty secret", operation: call.UploadAttachment, status: 200, body: `{"status":"uploaded"}`, secrets: []string{"credential_value", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pin := policyPin()
			server := peer(t, pin, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			defer server.Close()
			pin.Endpoint = server.URL
			var check call.Check
			if tc.operation == call.EmbedQuery {
				check = func(_ context.Context, body []byte) []plugins.Issue {
					return plugins.ValidateDocument("ingestion-embed-query-response.schema.json", body)
				}
			}
			secrets := tc.secrets
			if secrets == nil {
				secrets = []string{"credential_value"}
			}
			result, err := call.Invoke(context.Background(), pin, tc.operation, call.Bytes([]byte(`{}`)), check, secrets)
			if result == nil {
				t.Fatalf("expected a wire result: %v", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("got %v; want %v", err, tc.want)
			}
			if tc.declared {
				var declared *plugins.PluginError
				if !errors.As(err, &declared) {
					t.Fatalf("%v has no declared envelope", err)
				}
				if (tc.name == "retryable" || tc.name == "evaluation retryable") && !declared.Retryable {
					t.Fatal("retryable envelope lost")
				}
			}
			if tc.class != "" {
				var e *connectors.Error
				if !errors.As(err, &e) || e.Class != tc.class || e.Code != tc.code {
					t.Fatalf("got %v; want %s %s", err, tc.class, tc.code)
				}
			}
			if tc.want == nil && !tc.declared && tc.class == "" && err != nil {
				t.Fatalf("success: %v", err)
			}
		})
	}
	// A transport outage has no envelope and remains retryable unavailability.
	pin := policyPin()
	_, err := call.Invoke(context.Background(), pin, call.Normalize, call.Bytes([]byte(`{}`)), nil, nil)
	if !errors.Is(err, plugins.ErrUnavailable) || errors.Is(err, plugins.ErrCallDeadline) {
		t.Fatalf("outage: %v; want unavailability without plugin deadline", err)
	}
}

// Retries converge on the pinned generation and immutable input, regardless
// of an attempt id or a stale key supplied by a caller.
func TestInvocationAttachesStableKey(t *testing.T) {
	pin := policyPin()
	pin.KeyIdentity = "generation"
	var keys, ids []string
	server := peer(t, pin, func(w http.ResponseWriter, r *http.Request) {
		var request plugins.SegmentAndEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		keys = append(keys, request.IdempotencyKey)
		ids = append(ids, request.InvocationID)
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"code":"busy","message":"retry","retryable":true}`))
	})
	defer server.Close()
	pin.Endpoint = server.URL
	for _, id := range []string{"attempt-1", "attempt-2"} {
		body, err := plugins.BuildSegmentAndEmbedRequest(plugins.SegmentAndEmbedRequest{InvocationID: id, IdempotencyKey: "stale", OrganizationID: "org", Version: plugins.IngestionVersion{RecordVersionID: "version"}, Spaces: []string{"z", "a"}})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = call.Invoke(context.Background(), pin, call.SegmentAndEmbed, call.Bytes(body), nil, nil)
	}
	const want = "ingestion_2cc460cd25884d0b0c45c20882e1b8b7829a87798d8824ebe22262cff4be6bc6"
	if len(keys) != 2 || keys[0] != want || keys[1] != want || ids[0] == ids[1] {
		t.Fatalf("keys %v, ids %v; want same immutable key %s and distinct attempts", keys, ids, want)
	}
}
