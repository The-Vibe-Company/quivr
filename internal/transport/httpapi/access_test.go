package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/changes"
	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
	"github.com/The-Vibe-Company/quivr/internal/logging"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
)

// Owns access-event completeness at the real handler boundary. Existing API
// response tests cannot catch missing logs, leaked request data or wrong sizes.
func TestAccessEventsDescribeFinalResponsesWithoutRequestSecrets(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	logger, err := logging.New(&logs, logging.Options{})
	if err != nil {
		t.Fatal(err)
	}
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(previous) })
	handler, err := httpapi.New(knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{}, map[string]corpus.Scope{
		catalogReader: {Organization: "org_a", Actions: []string{"content:read", content.ObservabilityRead}, Corpora: []string{"*"}},
	}, catalogCursorKey, httpapi.WithActivity(content.Activities{Store: memoryActivity{}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, path, key, route, code string
		status                               int
	}{
		{"success", "GET", "/v0/admin/documents", catalogReader, "/v0/admin/documents", "", 200},
		{"unauthorized", "GET", "/v0/corpora/corpus-secret-path", "secret-unknown-key", "/v0/corpora/{corpus_id}", "invalid_api_key", 401},
		{"forbidden", "GET", "/v0/corpora/corpus-secret-path", catalogReader, "/v0/corpora/{corpus_id}", "forbidden", 403},
		{"alias", "GET", "/v0/admin/plugins/", catalogReader, "/v0/admin/plugins", "forbidden", 403},
		{"unknown", "GET", "/secret-unknown-path", catalogReader, "unmatched", "not_found", 404},
		{"method", "DELETE", "/v0/search", catalogReader, "/v0/search", "not_found", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			path := tc.path
			if tc.name != "success" {
				path += "?credential=secret-query"
			}
			req := httptest.NewRequest(tc.method, path, strings.NewReader("secret-body"))
			req.RemoteAddr = "192.0.2.7:4321"
			req.Header.Set("Authorization", "Bearer "+tc.key)
			req.Header.Set("X-Forwarded-For", "198.51.100.9")
			req.Header.Set("X-Request-ID", "secret-client-id")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("response status: want %d got %d", tc.status, rec.Code)
			}
			var event map[string]any
			dec := json.NewDecoder(&logs)
			if err := dec.Decode(&event); err != nil {
				t.Fatal(err)
			}
			if err := dec.Decode(new(any)); err != io.EOF {
				t.Fatal("more than one access event")
			}
			if event["route"] != tc.route || event["status"] != float64(rec.Code) || event["method"] != tc.method || event["response_size"] != float64(rec.Body.Len()) || event["client_ip"] != "192.0.2.7" || event["error_code"] != tc.code || event["request_id"] != rec.Header().Get("X-Request-ID") || event["request_id"] == "" || event["duration_ms"] == nil {
				t.Fatalf("incomplete access event for response %d (%d bytes): %+v", rec.Code, rec.Body.Len(), event)
			}
			if tc.key == catalogReader && (event["api_key_id"] == nil || event["api_key_id"] == "") {
				t.Fatal("authenticated key identifier missing")
			}
			encoded, _ := json.Marshal(event)
			for _, secret := range []string{tc.key, "secret-query", "secret-body", "corpus-secret-path", "secret-unknown-path", "secret-client-id"} {
				if bytes.Contains(encoded, []byte(secret)) {
					t.Fatalf("access event leaked %q", secret)
				}
			}
		})
	}
}

type cancelingStreamWriter struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w cancelingStreamWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(b)
	w.cancel()
	return n, err
}

// A real SSE handler requires flush/deadline forwarding and emits several
// request diagnostics before the one final access event. Cancellation follows
// its first actual write, without waiting for a polling interval.
func TestStreamingAccessEventCountsBytesAndCorrelatesDiagnostics(t *testing.T) {
	var logs bytes.Buffer
	logger, err := logging.New(&logs, logging.Options{})
	if err != nil {
		t.Fatal(err)
	}
	previous := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(previous) })
	handler, err := httpapi.New(knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{},
		map[string]corpus.Scope{catalogReader: {Organization: "org_a", Actions: []string{"changes:read"}, Corpora: []string{"*"}}}, catalogCursorKey,
		httpapi.WithChanges(changes.Service{Journal: &memoryJournal{}, Key: catalogCursorKey}, 0))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/changes/stream?corpus_id=corpus_a", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+catalogReader)
	handler.ServeHTTP(cancelingStreamWriter{rec, cancel}, req)
	dec := json.NewDecoder(&logs)
	accesses, diagnostics := 0, 0
	for {
		var event map[string]any
		if err := dec.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if event["request_id"] != rec.Header().Get("X-Request-ID") {
			t.Fatalf("uncorrelated stream event: %+v", event)
		}
		if event["msg"] == "http request" {
			accesses++
			if event["status"] != float64(200) || event["response_size"] != float64(rec.Body.Len()) || event["route"] != "/v0/changes/stream" {
				t.Fatalf("stream access event: %+v", event)
			}
		} else {
			diagnostics++
		}
	}
	if accesses != 1 || diagnostics != 2 || !rec.Flushed || rec.Body.Len() == 0 {
		t.Fatalf("stream: accesses=%d diagnostics=%d flushed=%t bytes=%d", accesses, diagnostics, rec.Flushed, rec.Body.Len())
	}
}

type failingPushAudit struct {
	stopProcess func()
	canceled    *bool
}

func (a failingPushAudit) ProtectPush(_ context.Context, _ connectors.PushAttempt, invoke func() (connectors.RelayAnswer, error)) (connectors.RelayAnswer, error) {
	return invoke()
}

func (a failingPushAudit) RecordPush(ctx context.Context, _ string, _ bool) error {
	if a.stopProcess != nil {
		a.stopProcess()
		*a.canceled = ctx.Err() != nil
	}
	return errors.New("sentinel-audit-secret")
}

type auditedPush struct{ echoPush }

func (p auditedPush) Descriptor() connectors.Descriptor {
	d := p.echoPush.Descriptor()
	d.APIRoutes = []connectors.APIRoute{{Name: "events", Method: "POST", Path: "events", Auth: "quivr_key", RequestSchema: json.RawMessage(`{"type":"object"}`)}}
	d.Receiver = p
	return d
}

// Push auditing may replace the plugin's successful response after it runs.
// The access event must describe that final refusal, including early lookup errors.
func TestAccessEventsIncludeEarlyAndFinalPushAuditRefusals(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	logger, err := logging.New(&logs, logging.Options{})
	if err != nil {
		t.Fatal(err)
	}
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, early := range []bool{true, false} {
		group := lifecycle.New()
		defer group.Close()
		budgetCanceled := false
		logs.Reset()
		var seen []connectors.ReceiveRequest
		registry, err := connectors.NewRegistry(auditedPush{echoPush{seen: &seen}})
		if err != nil {
			t.Fatal(err)
		}
		store := &onePushInstance{target: connectors.Target{Instance: connectors.Instance{Organization: "org_a", ID: "connector_push", CorpusID: "corpus_news", Namespace: "echo", Kind: "echo", Config: json.RawMessage(`{}`), Enabled: true}}}
		if early {
			store.fail = errors.New("sentinel-lookup-secret")
		}
		handler, err := httpapi.New(nil, content.Service{}, retrieval.Service{}, uploads.Service{}, map[string]corpus.Scope{"push-key": {Organization: "org_a", Actions: []string{"connector:push"}, Corpora: []string{"corpus_news"}}}, catalogCursorKey,
			httpapi.WithLifecycle(group),
			httpapi.WithRelay(connectors.Relay{Store: store, Registry: registry, Protection: failingPushAudit{stopProcess: group.Close, canceled: &budgetCanceled}}))
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		path := "/v0/connector-webhooks/connector_push"
		if !early {
			path = "/v0/connectors/connector_push/api/events"
		}
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer push-key")
		handler.ServeHTTP(rec, request)
		if rec.Code != 503 {
			t.Fatalf("early=%t: final status %d", early, rec.Code)
		}
		if !early && len(seen) != 1 {
			t.Fatalf("push plugin was not invoked: %d deliveries", len(seen))
		}
		if !early && !budgetCanceled {
			t.Fatal("push audit escaped the process shutdown budget")
		}
		dec := json.NewDecoder(&logs)
		accesses := 0
		for {
			var event map[string]any
			if err := dec.Decode(&event); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if event["msg"] != "http request" {
				continue
			}
			accesses++
			if event["status"] != float64(503) || event["response_size"] != float64(rec.Body.Len()) || event["error_code"] != "connectors_unavailable" || event["request_id"] != rec.Header().Get("X-Request-ID") {
				t.Fatalf("early=%t: final access event %+v", early, event)
			}
		}
		if accesses != 1 {
			t.Fatalf("early=%t: want one access event, got %d", early, accesses)
		}
	}
}
