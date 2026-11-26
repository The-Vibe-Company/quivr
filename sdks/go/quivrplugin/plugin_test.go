package quivrplugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const secret = "tok-super-secret-value"

// fake is a connector whose Fetch answer each test chooses.
type fake struct {
	fetch func(*FetchRequest) (*Page, error)
}

func (f fake) Fetch(_ context.Context, r *FetchRequest) (*Page, error) { return f.fetch(r) }
func (f fake) CheckCredential(context.Context, *CredentialRequest) (*CredentialStatus, error) {
	expires := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	return &CredentialStatus{ExpiresAt: &expires}, nil
}

func newTestPlugin(t *testing.T, fetch func(*FetchRequest) (*Page, error)) (http.Handler, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	p, err := New("testdata/quivr-plugin.yaml", WithLogger(slog.New(slog.NewTextHandler(logs, nil))))
	if err != nil {
		t.Fatal(err)
	}
	p.MustConnector("feed", fake{fetch}).MustConnector("open", fake{fetch}).MustConnector("optional", fake{fetch})
	h, err := p.Handler()
	if err != nil {
		t.Fatal(err)
	}
	return h, logs
}

func fetchBody(edit func(map[string]any)) []byte {
	body := map[string]any{
		"invocation_id": "inv-1", "contribution": "connector", "organization_id": "org-1", "configuration": map[string]any{},
		"connector":  map[string]any{"instance_id": "c-1", "kind": "feed", "config": map[string]any{"url": "https://feeds.example.com"}},
		"credential": map[string]any{"token": secret}, "checkpoint": map[string]any{"offset": 4},
		"now": "2026-09-29T09:00:00Z", "page_in_run": 0, "reads_today": 0,
	}
	if edit != nil {
		edit(body)
	}
	raw, _ := json.Marshal(body)
	return raw
}

func call(h http.Handler, route string, body []byte) (int, map[string]any, string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, route, bytes.NewReader(body)))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func TestDiscoveryServesTheNegotiatedVersionAndDigest(t *testing.T) {
	h, _ := newTestPlugin(t, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v0/discovery", nil))
	raw, _ := os.ReadFile("testdata/quivr-plugin.yaml")
	sum := sha256.Sum256(raw)
	if err := validate("plugins/v0/discovery.schema.json", rec.Body.Bytes()); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		PluginAPI      string   `json:"plugin_api"`
		ManifestDigest string   `json:"manifest_digest"`
		Contributions  []string `json:"contributions"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &doc)
	if doc.PluginAPI != "0.13.0" || doc.ManifestDigest != "sha256:"+hex.EncodeToString(sum[:]) || fmt.Sprint(doc.Contributions) != "[connector]" {
		t.Fatalf("discovery %+v", doc)
	}
}

func TestRequestsTheContractRefusesAreTerminalAndNeverQuoteTheCredential(t *testing.T) {
	h, _ := newTestPlugin(t, func(*FetchRequest) (*Page, error) { t.Fatal("an invalid request reached Fetch"); return nil, nil })
	for name, tc := range map[string]struct {
		body []byte
		code string
	}{
		"not json":           {[]byte(`{"invocation_id": `), "invalid_request"},
		"unknown field":      {fetchBody(func(b map[string]any) { b["extra"] = secret }), "invalid_request"},
		"unknown kind":       {fetchBody(func(b map[string]any) { b["connector"].(map[string]any)["kind"] = "other" }), "unknown_kind"},
		"config schema":      {fetchBody(func(b map[string]any) { b["connector"].(map[string]any)["config"] = map[string]any{"url": "ftp://x"} }), "invalid_config"},
		"credential schema":  {fetchBody(func(b map[string]any) { b["credential"] = map[string]any{"token": "leaked-" + secret} }), "invalid_credential"},
		"missing credential": {fetchBody(func(b map[string]any) { b["credential"] = nil }), "invalid_credential"},
		"unexpected credential": {fetchBody(func(b map[string]any) {
			b["connector"].(map[string]any)["kind"] = "open"
		}), "invalid_credential"},
	} {
		t.Run(name, func(t *testing.T) {
			status, out, raw := call(h, "/v0/contributions/connector/fetch", tc.body)
			if status != 400 || out["code"] != tc.code || out["retryable"] != false {
				t.Fatalf("HTTP %d %s", status, raw)
			}
			if strings.Contains(raw, "super-secret") {
				t.Fatalf("the refusal quotes the credential: %s", raw)
			}
		})
	}
}

func TestErrorsMapToClassifiedEnvelopes(t *testing.T) {
	for name, tc := range map[string]struct {
		err        error
		status     int
		class      string
		retryable  bool
		retryAfter float64
		code       string
	}{
		"access":                  {AccessError("token_rejected", "refused "+secret), 403, "access", false, 0, "token_rejected"},
		"transient":               {TransientError("rate_limited", "wait").WithRetryAfter(90 * time.Second), 503, "transient", true, 90, "rate_limited"},
		"source":                  {fmt.Errorf("wrapped: %w", SourceError("bad_feed", "unparsable")), 422, "source", false, 0, "bad_feed"},
		"unclassified":            {errors.New("dial tcp: " + secret), 503, "transient", true, 0, "unexpected_error"},
		"panic":                   {nil, 500, "source", false, 0, "internal_error"},
		"invalid page":            {nil, 500, "source", false, 0, "invalid_response"},
		"huge checkpoint":         {nil, 500, "source", false, 0, "invalid_response"},
		"unsupported concurrency": {nil, 500, "source", false, 0, "invalid_response"},
		"empty manifest":          {nil, 500, "source", false, 0, "invalid_response"},
	} {
		t.Run(name, func(t *testing.T) {
			h, logs := newTestPlugin(t, func(r *FetchRequest) (*Page, error) {
				switch name {
				case "panic":
					panic("boom with " + secret)
				case "invalid page":
					return &Page{Items: []Item{{RecordKey: "a", Content: Text("a")}, {RecordKey: "b", Content: Text("b")}, {RecordKey: "c", Content: Text("c")}}}, nil
				case "empty manifest":
					return &Page{Items: []Item{{RecordKey: "a", Content: NewManifest()}}}, nil
				case "unsupported concurrency":
					return &Page{SubmissionConcurrency: 2}, nil
				case "huge checkpoint":
					return &Page{Checkpoint: strings.Repeat("x", MaxCheckpointBytes)}, nil
				}
				return nil, tc.err
			})
			status, out, raw := call(h, "/v0/contributions/connector/fetch", fetchBody(nil))
			if status != tc.status || out["error_class"] != tc.class || out["retryable"] != tc.retryable || out["code"] != tc.code {
				t.Fatalf("HTTP %d %s", status, raw)
			}
			if tc.retryAfter > 0 && out["retry_after_seconds"] != tc.retryAfter {
				t.Fatalf("retry_after_seconds: %s", raw)
			}
			if err := validate("plugins/v0/error.schema.json", []byte(raw)); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(raw, "super-secret") || strings.Contains(logs.String(), "super-secret") || strings.Contains(raw, "boom") {
				t.Fatalf("the credential or panic text leaked:\n%s\n%s", raw, logs.String())
			}
		})
	}
}

// credential_required: false lets an instance without a credential run, and
// a credential that is sent must still match the schema.
func TestAnOptionalCredentialMayBeNull(t *testing.T) {
	h, _ := newTestPlugin(t, func(r *FetchRequest) (*Page, error) {
		if !r.Credential.IsNull() {
			t.Fatal("the null credential did not reach Fetch as null")
		}
		return &Page{Checkpoint: map[string]any{}}, nil
	})
	optional := func(credential any) []byte {
		return fetchBody(func(b map[string]any) {
			b["connector"].(map[string]any)["kind"] = "optional"
			b["credential"] = credential
		})
	}
	if status, _, raw := call(h, "/v0/contributions/connector/fetch", optional(nil)); status != 200 {
		t.Fatalf("a null optional credential: HTTP %d %s", status, raw)
	}
	if status, out, raw := call(h, "/v0/contributions/connector/fetch", optional(map[string]any{"token": "wrong"})); status != 400 || out["code"] != "invalid_credential" {
		t.Fatalf("an invalid optional credential: HTTP %d %s", status, raw)
	}
}

func TestNotDueEchoesTheRequestCheckpoint(t *testing.T) {
	h, _ := newTestPlugin(t, func(*FetchRequest) (*Page, error) { return nil, ErrNotDue })
	status, out, raw := call(h, "/v0/contributions/connector/fetch", fetchBody(nil))
	if status != 200 || out["not_due"] != true || fmt.Sprint(out["checkpoint"]) != "map[offset:4]" || out["more"] != false {
		t.Fatalf("HTTP %d %s", status, raw)
	}
}

// Page is the public handler result, distinct from the internal wire model
// exercised by normative fixture round trips: encoding must carry its hint.
func TestTypedPageCarriesNewConnectorFieldsOnSupportedAPI(t *testing.T) {
	raw, err := os.ReadFile("testdata/quivr-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`plugin_api: ">=0.1.0 <0.14.0"`), []byte(`plugin_api: ">=0.15.0 <0.16.0"`), 1)
	raw = bytes.Replace(raw, []byte("    limits:"), []byte("    attachments: {max_bytes: 1048576}\n    limits:"), 1)
	path := filepath.Join(t.TempDir(), "quivr-plugin.yaml")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	plugin, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	body, problem := plugin.encodePage(&Page{SubmissionConcurrency: 32, Items: []Item{{RecordKey: "member.xml", Content: NewManifest(), Attachments: []Attachment{{Key: "source", Role: "source", MediaType: "application/xml", Ref: "member:1"}}}}})
	if problem != "" {
		t.Fatal(problem)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["submission_concurrency"] != float64(32) {
		t.Fatalf("encoded handler page lost its submission hint: %s", body)
	}
	for _, concurrency := range []int{-1, 33} {
		_, problem := plugin.encodePage(&Page{SubmissionConcurrency: concurrency})
		if !strings.Contains(problem, "1 to 32") {
			t.Fatalf("submission_concurrency %d has no actionable bounds diagnostic: %q", concurrency, problem)
		}
	}
	invalid, err := filepath.Glob(filepath.Join(fixtures, "responses/connector/attachment-only-invalid-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(invalid) == 0 {
		t.Fatal("no attachment-only invalid fixtures found; the SDK semantic guard would be untested")
	}
	for _, path := range invalid {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var page pageJSON
			if err := json.Unmarshal(raw, &page); err != nil {
				t.Fatal(err)
			}
			_, problem := plugin.encodePage(&Page{Items: page.Items, Checkpoint: page.Checkpoint, More: page.More})
			if !strings.Contains(problem, "attachment-only input") {
				t.Fatalf("SDK failed to reject ambiguous raw input at its semantic guard: %q", problem)
			}
		})
	}
}

func TestFetchOrderedRecordFlagIsOptionalVersionGatedAndStillValidatesItems(t *testing.T) {
	pageFromFixture := func(t *testing.T, name string) Page {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(fixtures, "responses", "connector", name))
		if err != nil {
			t.Fatal(err)
		}
		var wire pageJSON
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		return Page{
			AllowRepeatedRecordKeys: wire.AllowRepeatedRecordKeys,
			Items:                   wire.Items,
			Checkpoint:              wire.Checkpoint,
			More:                    wire.More,
		}
	}
	pluginFromManifest := func(t *testing.T, name string) *Plugin {
		t.Helper()
		p, err := New(filepath.Join(fixtures, "manifests", "valid", name))
		if err != nil {
			t.Fatal(err)
		}
		p.MustConnector("feed", fake{})
		return p
	}

	cases := []struct {
		name     string
		manifest string
		response string
		want     string
	}{
		{"new API omits flag", "connector-ordered-019.yaml", "ordered-revisions-absent.json", "record key is duplicated"},
		{"new API false rejects duplicates", "connector-ordered-019.yaml", "ordered-revisions-false.json", "record key is duplicated"},
		{"new API true preserves source order", "connector-ordered-019.yaml", "ordered-revisions.json", ""},
		{"old API omits flag", "connector-ordered-018.yaml", "ordered-revisions-absent.json", "record key is duplicated"},
		{"old API false rejects duplicates", "connector-ordered-018.yaml", "ordered-old-false.json", "record key is duplicated"},
		{"old API true is refused", "connector-ordered-018.yaml", "ordered-old-true.json", "requires Plugin API 0.19.0"},
		{"repeated item is still validated", "connector-ordered-019.yaml", "ordered-invalid-revision.json", "content and withdraw"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, problem := pluginFromManifest(t, tc.manifest).encodePage(func() *Page {
				page := pageFromFixture(t, tc.response)
				return &page
			}())
			if tc.want != "" {
				if problem == "" || !strings.Contains(problem, tc.want) {
					t.Fatalf("expected %q, got body %s and problem %q", tc.want, body, problem)
				}
				return
			}
			if problem != "" {
				t.Fatal(problem)
			}
			var wire map[string]any
			if err := json.Unmarshal(body, &wire); err != nil {
				t.Fatal(err)
			}
			if wire["allow_repeated_record_keys"] != true {
				t.Fatalf("the enabled flag was not encoded: %s", body)
			}
		})
	}

	old := pluginFromManifest(t, "connector-ordered-018.yaml")
	page := Page{Items: []Item{{RecordKey: "unique", Content: Text("body")}}}
	body, problem := old.encodePage(&page)
	if problem != "" {
		t.Fatal(problem)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if _, present := wire["allow_repeated_record_keys"]; present {
		t.Fatalf("false default must omit the optional flag: %s", body)
	}
}

func TestAPageAndACredentialCheckMatchTheResponseSchemas(t *testing.T) {
	h, _ := newTestPlugin(t, func(r *FetchRequest) (*Page, error) {
		var at struct{ Offset int }
		if err := r.DecodeCheckpoint(&at); err != nil || r.FirstRun() || at.Offset != 4 {
			t.Fatalf("checkpoint %s", r.Checkpoint)
		}
		return &Page{Items: []Item{{RecordKey: "a", Revision: "1", Content: NewManifest(TextPart("body", "body", "text"))}, {RecordKey: "gone", Withdraw: true}},
			Checkpoint: map[string]int{"offset": 6}, More: true, Reads: 2}, nil
	})
	status, _, raw := call(h, "/v0/contributions/connector/fetch", fetchBody(nil))
	if status != 200 {
		t.Fatalf("HTTP %d %s", status, raw)
	}
	if err := validate("plugins/v0/connector-fetch-response.schema.json", []byte(raw)); err != nil {
		t.Fatal(err)
	}
	credential := fetchBody(func(b map[string]any) {
		for _, k := range []string{"checkpoint", "page_in_run", "reads_today"} {
			delete(b, k)
		}
	})
	status, out, raw := call(h, "/v0/contributions/connector/check_credential", credential)
	if status != 200 || out["expires_at"] != "2027-01-01T00:00:00Z" {
		t.Fatalf("HTTP %d %s", status, raw)
	}
	if err := validate("plugins/v0/connector-check-credential-response.schema.json", []byte(raw)); err != nil {
		t.Fatal(err)
	}
}

func TestACredentialNeverPrints(t *testing.T) {
	c := newCredential(json.RawMessage(`{"token": "` + secret + `", "nested": {"key": "second-secret"}}`))
	type holder struct{ Credential Credential }
	var logs bytes.Buffer
	logger := slog.New(redactingHandler{next: slog.NewJSONHandler(&logs, nil), credential: c})
	logger.Info("using "+secret, "credential", c, "token", secret, "group", slog.GroupValue(slog.String("inner", "second-secret")), "any", []string{secret})
	encoded, _ := json.Marshal(holder{c})
	printed := fmt.Sprintf("%v %+v %#v %s %v", c, holder{c}, holder{c}, c, c.Redact("header "+secret))
	for _, out := range []string{printed, string(encoded), logs.String()} {
		if strings.Contains(out, "secret") || !strings.Contains(out, Redacted) {
			t.Fatalf("credential printed: %s", out)
		}
	}
	var decoded struct{ Token string }
	if err := c.Decode(&decoded); err != nil || decoded.Token != secret {
		t.Fatalf("decode: %v %q", err, decoded.Token)
	}
}

func TestEveryDeclaredKindNeedsAnImplementation(t *testing.T) {
	p, err := New("testdata/quivr-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Connector("undeclared", fake{}); err == nil {
		t.Fatal("registered an undeclared kind")
	}
	p.MustConnector("feed", fake{})
	if _, err := p.Handler(); err == nil || !strings.Contains(err.Error(), "open") {
		t.Fatalf("served without the open kind: %v", err)
	}
}

func TestServeReadyNotifiesOnlyAfterBind(t *testing.T) {
	p, err := New("testdata/quivr-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p.MustConnector("feed", fake{}).MustConnector("open", fake{}).MustConnector("optional", fake{})

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()

	called := false
	if err := p.serveAddr(t.Context(), occupied.Addr().String(), func(string) { called = true }); err == nil {
		t.Fatal("expected bind failure on occupied port")
	}
	if called {
		t.Fatal("onReady must not run when bind fails")
	}

	ctx, cancel := context.WithCancel(t.Context())
	var bound string
	if err := p.serveAddr(ctx, "127.0.0.1:0", func(addr string) {
		bound = addr
		cancel()
	}); err != nil {
		t.Fatalf("serveAddr failed: %v", err)
	}
	if !strings.HasPrefix(bound, "127.0.0.1:") || bound == "127.0.0.1:0" {
		t.Fatalf("bound address = %q, want resolved loopback port", bound)
	}
}

func TestWithHealthCheck(t *testing.T) {
	newPlugin := func(t *testing.T, fn func(context.Context) error) (http.Handler, *bytes.Buffer, string) {
		t.Helper()
		logs := &bytes.Buffer{}
		opts := []Option{WithLogger(slog.New(slog.NewTextHandler(logs, nil)))}
		if fn != nil {
			opts = append(opts, WithHealthCheck(fn))
		}
		p, err := New("testdata/quivr-plugin.yaml", opts...)
		if err != nil {
			t.Fatal(err)
		}
		p.MustConnector("feed", fake{}).MustConnector("open", fake{}).MustConnector("optional", fake{})
		h, err := p.Handler()
		if err != nil {
			t.Fatal(err)
		}
		return h, logs, p.m.digest
	}

	t.Run("default and passing check return 200 ok", func(t *testing.T) {
		called := false
		for _, fn := range []func(context.Context) error{
			nil,
			func(ctx context.Context) error {
				called = true
				if ctx == nil {
					return errors.New("nil context")
				}
				return nil
			},
		} {
			h, _, digest := newPlugin(t, fn)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v0/health", nil))
			if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"status":"ok"}` {
				t.Fatalf("HTTP %d %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get(ManifestDigestHeader); got != digest {
				t.Fatalf("manifest digest header = %q, want %q", got, digest)
			}
		}
		if !called {
			t.Fatal("configured health check was not invoked")
		}
	})

	cases := []struct {
		name           string
		fn             func(context.Context) error
		wantCode       string
		wantMessage    string
		wantClass      string
		wantRetryAfter float64
		forbidInBody   string
		expectLog      string
	}{
		{
			name:        "retryable contribution error",
			fn:          func(context.Context) error { return RetryableError("warming_up", "loading models") },
			wantCode:    "warming_up",
			wantMessage: "loading models",
		},
		{
			name:           "transient connector error with retry after",
			fn:             func(context.Context) error { return TransientError("backend_busy", "waiting").WithRetryAfter(30 * time.Second) },
			wantCode:       "backend_busy",
			wantMessage:    "waiting",
			wantClass:      "transient",
			wantRetryAfter: 30,
		},
		{
			name:        "source connector error stays retryable without conflicting class",
			fn:          func(context.Context) error { return SourceError("not_ready", "index loading") },
			wantCode:    "not_ready",
			wantMessage: "index loading",
		},
		{
			name:         "unclassified error hides detail from response and logs it",
			fn:           func(context.Context) error { return errors.New("dial tcp 10.0.0.1:5432: connection refused") },
			wantCode:     "unhealthy",
			forbidInBody: "10.0.0.1:5432",
			expectLog:    "health check failed",
		},
		{
			name:         "panic is recovered, hidden from response, and logged",
			fn:           func(context.Context) error { panic("boom internal pool state") },
			wantCode:     "unhealthy",
			forbidInBody: "boom",
			expectLog:    "health check panicked",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, logs, digest := newPlugin(t, tc.fn)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v0/health", nil))
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("HTTP %d %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get(ManifestDigestHeader); got != digest {
				t.Fatalf("manifest digest header = %q, want %q", got, digest)
			}
			raw := rec.Body.Bytes()
			if err := validate("plugins/v0/error.schema.json", raw); err != nil {
				t.Fatalf("health error envelope failed schema: %v: %s", err, raw)
			}
			var out map[string]any
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			if out["code"] != tc.wantCode || out["retryable"] != true {
				t.Fatalf("unexpected envelope: %s", raw)
			}
			if tc.wantMessage != "" && out["message"] != tc.wantMessage {
				t.Fatalf("message = %v, want %q", out["message"], tc.wantMessage)
			}
			if tc.wantClass != "" {
				if out["error_class"] != tc.wantClass {
					t.Fatalf("error_class = %v, want %q", out["error_class"], tc.wantClass)
				}
			} else if _, has := out["error_class"]; has {
				t.Fatalf("unexpected error_class in %s", raw)
			}
			if tc.wantRetryAfter > 0 && out["retry_after_seconds"] != tc.wantRetryAfter {
				t.Fatalf("retry_after_seconds = %v, want %v", out["retry_after_seconds"], tc.wantRetryAfter)
			}
			if tc.forbidInBody != "" && strings.Contains(string(raw), tc.forbidInBody) {
				t.Fatalf("response leaked internal detail: %s", raw)
			}
			if tc.expectLog != "" && !strings.Contains(logs.String(), tc.expectLog) {
				t.Fatalf("expected log %q, got %q", tc.expectLog, logs.String())
			}
		})
	}

	t.Run("declared secret is redacted from health responses and logs", func(t *testing.T) {
		rawManifest, err := os.ReadFile("testdata/quivr-plugin.yaml")
		if err != nil {
			t.Fatal(err)
		}
		rawManifest = append(rawManifest, []byte("\nsecrets:\n  - name: TEST_HEALTH_SECRET\n    description: backend token\n  - name: TEST_SHORT_SECRET\n    description: short token\n")...)
		manifestPath := filepath.Join(t.TempDir(), "quivr-plugin.yaml")
		if err := os.WriteFile(manifestPath, rawManifest, 0600); err != nil {
			t.Fatal(err)
		}
		const shortSecret = "k9z"
		const codeSecret = "tok_code_secret"
		t.Setenv("TEST_HEALTH_SECRET", codeSecret)
		t.Setenv("TEST_SHORT_SECRET", shortSecret)
		// Malformed signing ring with duplicate "secret" keys and a syntax error.
		t.Setenv(EnvSigningKeys, `{malformed, "secret": "dup_first_ring_secret", "secret": "dup_second_ring_secret"`)

		for _, tc := range []struct {
			name      string
			fn        func(context.Context) error
			forbid    []string
			wantCode  string
			expectLog bool
		}{
			{
				name:      "unclassified error scrubs declared, short, and malformed ring secrets",
				fn:        func(context.Context) error { return errors.New("dial: " + codeSecret + " " + shortSecret + " dup_first_ring_secret dup_second_ring_secret") },
				forbid:    []string{codeSecret, shortSecret, "dup_first_ring_secret", "dup_second_ring_secret"},
				wantCode:  "unhealthy",
				expectLog: true,
			},
			{
				name:      "panic scrubs declared, short, and malformed ring secrets",
				fn:        func(context.Context) error { panic("boom " + codeSecret + " " + shortSecret + " dup_first_ring_secret") },
				forbid:    []string{codeSecret, shortSecret, "dup_first_ring_secret"},
				wantCode:  "unhealthy",
				expectLog: true,
			},
			{
				name:     "classified ingest error scrubs secret in code and message",
				fn:       func(context.Context) error { return RetryableError(codeSecret, "using "+shortSecret+" dup_first_ring_secret") },
				forbid:   []string{codeSecret, shortSecret, "dup_first_ring_secret"},
				wantCode: "unhealthy",
			},
			{
				name:     "classified connector error scrubs secret in code and message",
				fn:       func(context.Context) error { return TransientError("err_"+shortSecret, "using "+codeSecret) },
				forbid:   []string{codeSecret, shortSecret},
				wantCode: "unhealthy",
			},
			{
				name:     "classified search error scrubs secret in code and message",
				fn:       func(context.Context) error { return &SearchError{Code: codeSecret, Message: "using " + shortSecret} },
				forbid:   []string{codeSecret, shortSecret},
				wantCode: "unhealthy",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				logs := &bytes.Buffer{}
				p, err := New(manifestPath, WithLogger(slog.New(slog.NewTextHandler(logs, nil))), WithHealthCheck(tc.fn))
				if err != nil {
					t.Fatal(err)
				}
				p.MustConnector("feed", fake{}).MustConnector("open", fake{}).MustConnector("optional", fake{})
				h, err := p.Handler()
				if err != nil {
					t.Fatal(err)
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v0/health", nil))
				if rec.Code != http.StatusServiceUnavailable {
					t.Fatalf("HTTP %d %s", rec.Code, rec.Body.String())
				}
				raw := rec.Body.Bytes()
				if err := validate("plugins/v0/error.schema.json", raw); err != nil {
					t.Fatalf("health error envelope failed schema: %v: %s", err, raw)
				}
				var out map[string]any
				if err := json.Unmarshal(raw, &out); err != nil {
					t.Fatal(err)
				}
				if out["code"] != tc.wantCode || out["retryable"] != true {
					t.Fatalf("unexpected envelope: %s", raw)
				}
				for _, forbidden := range tc.forbid {
					if strings.Contains(string(raw), forbidden) || strings.Contains(logs.String(), forbidden) {
						t.Fatalf("secret %q leaked in health response or logs:\nbody=%s\nlogs=%s", forbidden, raw, logs.String())
					}
				}
				if tc.expectLog && !strings.Contains(logs.String(), Redacted) {
					t.Fatalf("expected redacted marker in logs, got %q", logs.String())
				}
			})
		}
	})
}

func TestManifestDigestHeaderOnAllV0Responses(t *testing.T) {
	p, err := New("testdata/quivr-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p.MustConnector("feed", fake{func(*FetchRequest) (*Page, error) {
		return &Page{Checkpoint: map[string]any{}}, nil
	}}).MustConnector("open", fake{}).MustConnector("optional", fake{})
	h, err := p.Handler()
	if err != nil {
		t.Fatal(err)
	}
	digest := p.m.digest

	for _, tc := range []struct {
		method, path string
		body         []byte
		wantStatus   int
		wantHeader   string
	}{
		{http.MethodGet, "/v0/discovery", nil, http.StatusOK, digest},
		{http.MethodGet, "/v0/health", nil, http.StatusOK, digest},
		{http.MethodPost, "/v0/contributions/connector/fetch", fetchBody(nil), http.StatusOK, digest},
		{http.MethodPost, "/v0/contributions/connector/fetch", []byte("{"), http.StatusBadRequest, digest},
		{http.MethodGet, "/v0/unknown", nil, http.StatusNotFound, digest},
		{http.MethodGet, "/v0/contributions/connector/fetch", nil, http.StatusMethodNotAllowed, digest},
		{http.MethodGet, "/not-v0", nil, http.StatusNotFound, ""},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, bytes.NewReader(tc.body)))
			if rec.Code != tc.wantStatus {
				t.Fatalf("HTTP %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got := rec.Header().Get(ManifestDigestHeader); got != tc.wantHeader {
				t.Fatalf("%s = %q, want %q", ManifestDigestHeader, got, tc.wantHeader)
			}
		})
	}

	// Signed API 0.14.0 401 refusal also carries X-Quivr-Manifest-Digest.
	raw, err := os.ReadFile("testdata/quivr-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`plugin_api: ">=0.1.0 <0.14.0"`), []byte(`plugin_api: ">=0.14.0 <0.15.0"`), 1)
	manifestPath := filepath.Join(t.TempDir(), "quivr-plugin.yaml")
	if err := os.WriteFile(manifestPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	signed, err := New(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	signed.MustConnector("feed", fake{}).MustConnector("open", fake{}).MustConnector("optional", fake{})
	sh, err := signed.Handler()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	sh.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v0/discovery", nil))
	if rec.Code != http.StatusUnauthorized || rec.Header().Get(ManifestDigestHeader) != signed.m.digest {
		t.Fatalf("signed 401: status=%d header=%q want=%q", rec.Code, rec.Header().Get(ManifestDigestHeader), signed.m.digest)
	}
}

func TestServerTimeoutsAndGracefulShutdown(t *testing.T) {
	p, err := New("testdata/quivr-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if p.shutdownTimeout != 30*time.Second {
		t.Fatalf("default shutdown timeout = %v, want 30s", p.shutdownTimeout)
	}
	pIgnore, err := New("testdata/quivr-plugin.yaml", WithShutdownTimeout(0), WithShutdownTimeout(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if pIgnore.shutdownTimeout != 30*time.Second {
		t.Fatalf("non-positive shutdown timeout overwrote default: %v", pIgnore.shutdownTimeout)
	}
	pCustom, err := New("testdata/quivr-plugin.yaml", WithShutdownTimeout(45*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if pCustom.shutdownTimeout != 45*time.Second {
		t.Fatalf("custom shutdown timeout = %v, want 45s", pCustom.shutdownTimeout)
	}
	srv := p.newHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if srv.IdleTimeout != 120*time.Second || srv.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("server timeouts: IdleTimeout=%v ReadHeaderTimeout=%v", srv.IdleTimeout, srv.ReadHeaderTimeout)
	}

	t.Run("drains in-flight request within shutdown timeout", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		plugin, err := New("testdata/quivr-plugin.yaml", WithHealthCheck(func(ctx context.Context) error {
			close(entered)
			<-release
			return nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		plugin.MustConnector("feed", fake{}).MustConnector("open", fake{}).MustConnector("optional", fake{})
		h, err := plugin.Handler()
		if err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		serveDone := make(chan error, 1)
		go func() { serveDone <- plugin.serveListener(ctx, ln, h) }()

		respDone := make(chan *http.Response, 1)
		errDone := make(chan error, 1)
		client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
		go func() {
			resp, err := client.Get("http://" + ln.Addr().String() + "/v0/health")
			if err != nil {
				errDone <- err
				return
			}
			respDone <- resp
		}()

		<-entered
		cancel()
		// Wait until server.Shutdown closes the listener before releasing the in-flight request.
		closed := false
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			conn, err := net.DialTimeout("tcp", ln.Addr().String(), 50*time.Millisecond)
			if err != nil {
				closed = true
				break
			}
			_ = conn.Close()
		}
		if !closed {
			close(release)
			t.Fatal("listener remained open after shutdown began")
		}
		close(release)

		select {
		case err := <-errDone:
			t.Fatalf("in-flight request failed during graceful shutdown: %v", err)
		case resp := <-respDone:
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK || resp.Header.Get(ManifestDigestHeader) != plugin.m.digest {
				t.Fatalf("unexpected response: status=%d digest=%q", resp.StatusCode, resp.Header.Get(ManifestDigestHeader))
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for in-flight response")
		}
		select {
		case err := <-serveDone:
			if err != nil {
				t.Fatalf("serveListener returned error on clean drain: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for serveListener to exit")
		}
	})

	t.Run("enforces WithShutdownTimeout when request blocks past deadline", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		plugin, err := New(
			"testdata/quivr-plugin.yaml",
			WithShutdownTimeout(time.Nanosecond),
			WithHealthCheck(func(ctx context.Context) error {
				close(entered)
				<-release
				return nil
			}),
		)
		if err != nil {
			t.Fatal(err)
		}
		plugin.MustConnector("feed", fake{}).MustConnector("open", fake{}).MustConnector("optional", fake{})
		h, err := plugin.Handler()
		if err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		serveDone := make(chan error, 1)
		go func() { serveDone <- plugin.serveListener(ctx, ln, h) }()

		clientDone := make(chan struct{})
		client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
		go func() {
			defer close(clientDone)
			if resp, err := client.Get("http://" + ln.Addr().String() + "/v0/health"); err == nil {
				_ = resp.Body.Close()
			}
		}()

		<-entered
		cancel()
		select {
		case err := <-serveDone:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("serveListener error = %v, want context.DeadlineExceeded", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for serveListener shutdown timeout")
		}
		close(release)
		<-clientDone
	})
}
