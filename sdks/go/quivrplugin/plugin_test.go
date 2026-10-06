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
