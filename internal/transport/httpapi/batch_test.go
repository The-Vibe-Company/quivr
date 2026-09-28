package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contract "github.com/The-Vibe-Company/quivr-v2/contracts/http/v0"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const (
	adminKey  = "admin-key-0123456789abcdef0123456789"
	readerKey = "reader-key-0123456789abcdef012345678"
	scopedKey = "scoped-key-0123456789abcdef012345678"
)

// acceptancePort stands in for durable acceptance: it records each command it
// is asked to accept and answers with a Receipt derived from the entry's key.
// With cancelAfter set, it cancels the request after that many acceptances,
// as a client disconnect or an expired deadline would.
type acceptancePort struct {
	accepted    []string
	failures    map[string]error
	cancelAfter int
	cancel      context.CancelFunc
}

func (p *acceptancePort) Accept(ctx context.Context, _ corpus.Scope, c content.Command) (content.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return content.Receipt{}, err
	}
	if err := p.failures[c.Key]; err != nil {
		return content.Receipt{}, err
	}
	p.accepted = append(p.accepted, c.Key)
	if p.cancelAfter > 0 && len(p.accepted) == p.cancelAfter {
		p.cancel()
	}
	return content.Receipt{ID: "receipt_" + c.Key, State: "pending", RecordID: "record_" + c.Source.RecordKey, Source: c.Source, Processing: content.Processing{State: "queued", Phase: "materialization"}, Diagnostics: []content.Diagnostic{}}, nil
}
func (*acceptancePort) Withdraw(context.Context, corpus.Scope, content.Withdrawal) (content.Receipt, error) {
	return content.Receipt{}, errors.New("not used")
}
func (*acceptancePort) Receipt(context.Context, string, string) (content.Receipt, error) {
	return content.Receipt{}, errors.New("not used")
}
func (*acceptancePort) Record(context.Context, string, string) (content.Record, error) {
	return content.Record{}, errors.New("not used")
}
func (*acceptancePort) Version(context.Context, string, string, string) (content.StoredVersion, error) {
	return content.StoredVersion{}, errors.New("not used")
}
func (*acceptancePort) Work(context.Context, string, string) (content.Work, bool, error) {
	return content.Work{}, false, errors.New("not used")
}
func (*acceptancePort) Progress(context.Context, string, string, string, string) error {
	return errors.New("not used")
}
func (*acceptancePort) Publish(context.Context, content.Work, content.Publication) error {
	return errors.New("not used")
}

func newAPI(t *testing.T, port *acceptancePort) http.Handler {
	t.Helper()
	keys := map[string]corpus.Scope{
		adminKey:  {Organization: "org_a", Actions: []string{"content:read", "content:write"}, Corpora: []string{"*"}},
		readerKey: {Organization: "org_a", Actions: []string{"content:read"}, Corpora: []string{"*"}},
		scopedKey: {Organization: "org_a", Actions: []string{"content:read", "content:write"}, Corpora: []string{"corpus_other"}},
	}
	contents := content.Service{Repository: port, BlobSource: verifiedBlobs{}}
	handler, err := httpapi.New(nil, contents, retrieval.Service{}, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

// verifiedBlobs verifies Blob IDs prefixed "verified" and, like a real lookup,
// fails with the context error once the request is canceled.
type verifiedBlobs struct{}

func (verifiedBlobs) VerifiedBlob(ctx context.Context, _ string, id string) (content.VerifiedBlob, error) {
	if err := ctx.Err(); err != nil {
		return content.VerifiedBlob{}, err
	}
	if !strings.HasPrefix(id, "verified") {
		return content.VerifiedBlob{}, content.ErrUnverifiedBlob
	}
	return content.VerifiedBlob{ID: id, MediaType: "application/xml", Blob: content.Blob{SHA256: "checksum"}}, nil
}

func inline(key, record, text string) map[string]any {
	return map[string]any{"idempotency_key": key, "source": map[string]any{"corpus_id": "corpus_news", "namespace": "feed", "record_key": record}, "content": map[string]any{"kind": "text", "text": text}}
}

func postJSON(t *testing.T, handler http.Handler, path, key string, body any) (int, map[string]any) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return postRaw(t, handler, path, key, payload)
}

func postRaw(t *testing.T, handler http.Handler, path, key string, payload []byte) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", path, bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: undecodable response %q", path, rec.Body.String())
	}
	return rec.Code, out
}

// conforms validates an actual response body against the authoritative contract.
func conforms(t *testing.T, schema string, body any) {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(contract.OpenAPI, &doc); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("https://quivr.invalid/openapi", doc); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile("https://quivr.invalid/openapi#/components/schemas/" + schema)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(body)
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if err := compiled.Validate(instance); err != nil {
		t.Fatalf("response violates %s: %v\n%s", schema, err, b)
	}
}

func entries(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("no per-entry items: %v", body)
	}
	out := make([]map[string]any, len(raw))
	for i, item := range raw {
		out[i] = item.(map[string]any)
		if out[i]["index"] != float64(i) {
			t.Fatalf("entry %d correlated as %v", i, out[i]["index"])
		}
	}
	return out
}

func entryError(t *testing.T, item map[string]any) map[string]any {
	t.Helper()
	e, ok := item["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected an entry error, got %v", item)
	}
	return e
}

func TestBatchReturnsIndependentOutcomePerEntry(t *testing.T) {
	port := &acceptancePort{}
	handler := newAPI(t, port)
	status, body := postJSON(t, handler, "/v0/records/batch", adminKey, map[string]any{"items": []any{
		inline("batch-a", "a", "Premier texte 🌞"),
		42,
		map[string]any{"source": map[string]any{"corpus_id": "corpus_news", "namespace": "feed", "record_key": "b"}},
		inline("batch-c", "c", "Troisième texte"),
	}})
	if status != 200 {
		t.Fatalf("got %d: %v", status, body)
	}
	conforms(t, "BatchResult", body)
	items := entries(t, body)
	if len(items) != 4 {
		t.Fatalf("got %d outcomes for 4 entries", len(items))
	}
	for i, id := range map[int]string{0: "receipt_batch-a", 3: "receipt_batch-c"} {
		receipt, ok := items[i]["receipt"].(map[string]any)
		if !ok || receipt["receipt_id"] != id {
			t.Fatalf("entry %d: %v", i, items[i])
		}
	}
	for _, i := range []int{1, 2} {
		if e := entryError(t, items[i]); e["code"] != "invalid_schema" || e["retryable"] != false {
			t.Fatalf("entry %d: %v", i, e)
		}
	}
	if len(port.accepted) != 2 || port.accepted[0] != "batch-a" || port.accepted[1] != "batch-c" {
		t.Fatalf("acceptance attempts: %v", port.accepted)
	}
}

func TestBatchEnvelopeIsBoundedAndRejectedWhole(t *testing.T) {
	valid := inline("envelope-a", "a", "Texte valide")
	tooMany := make([]any, 101)
	for i := range tooMany {
		tooMany[i] = inline(fmt.Sprintf("many-%d", i), fmt.Sprintf("r%d", i), "x")
	}
	// Twelve entries just under the single-request bound exceed 10 MiB together.
	oversized := make([]any, 12)
	for i := range oversized {
		oversized[i] = inline(fmt.Sprintf("big-%d", i), fmt.Sprintf("big%d", i), strings.Repeat("a", 900<<10))
	}
	cases := []struct {
		name   string
		key    string
		body   any
		raw    string
		status int
		code   string
	}{
		{name: "more than 100 entries", key: adminKey, body: map[string]any{"items": tooMany}, status: 413, code: "batch_too_large"},
		{name: "more than 10 MiB", key: adminKey, body: map[string]any{"items": oversized}, status: 413, code: "request_too_large"},
		{name: "malformed JSON", key: adminKey, raw: `{"items":[`, status: 400, code: "malformed_json"},
		{name: "trailing document", key: adminKey, raw: `{"items":[1]} {}`, status: 400, code: "malformed_json"},
		{name: "empty items", key: adminKey, body: map[string]any{"items": []any{}}, status: 422, code: "invalid_schema"},
		{name: "items not an array", key: adminKey, body: map[string]any{"items": valid}, status: 422, code: "invalid_schema"},
		{name: "missing items", key: adminKey, body: map[string]any{}, status: 422, code: "invalid_schema"},
		{name: "unknown envelope field", key: adminKey, body: map[string]any{"items": []any{valid}, "atomic": true}, status: 422, code: "invalid_schema"},
		{name: "bare array", key: adminKey, body: []any{valid}, status: 422, code: "invalid_schema"},
		{name: "no content:write", key: readerKey, body: map[string]any{"items": []any{valid}}, status: 403, code: "forbidden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			port := &acceptancePort{}
			handler := newAPI(t, port)
			payload := []byte(tc.raw)
			if tc.body != nil {
				payload, _ = json.Marshal(tc.body)
			}
			status, body := postRaw(t, handler, "/v0/records/batch", tc.key, payload)
			if status != tc.status || body["code"] != tc.code {
				t.Fatalf("got %d code=%v with %d attempted acceptances", status, body["code"], len(port.accepted))
			}
			conforms(t, "Error", body)
			if len(port.accepted) != 0 {
				t.Fatalf("rejected envelope reached acceptance: %v", port.accepted)
			}
		})
	}
}

func TestBatchAcceptsEnvelopesAtTheirBounds(t *testing.T) {
	hundred := make([]any, 100)
	for i := range hundred {
		hundred[i] = inline(fmt.Sprintf("hundred-%d", i), fmt.Sprintf("h%d", i), "x")
	}
	// Eleven entries just under the entry bound come close to, but stay under, 10 MiB.
	wide := make([]any, 11)
	for i := range wide {
		wide[i] = inline(fmt.Sprintf("wide-%d", i), fmt.Sprintf("wide%d", i), strings.Repeat("a", 900<<10))
	}
	for name, items := range map[string][]any{"exactly 100 entries": hundred, "just under 10 MiB": wide} {
		port := &acceptancePort{}
		payload, _ := json.Marshal(map[string]any{"items": items})
		if name == "just under 10 MiB" && (len(payload) <= 9<<20 || len(payload) > 10<<20) {
			t.Fatalf("fixture is %d bytes", len(payload))
		}
		status, body := postRaw(t, newAPI(t, port), "/v0/records/batch", adminKey, payload)
		if status != 200 || len(port.accepted) != len(items) {
			t.Fatalf("%s: got %d code=%v, accepted %d", name, status, body["code"], len(port.accepted))
		}
	}
}

func TestBatchEntryBoundIsMeasuredOnRawBytes(t *testing.T) {
	port := &acceptancePort{}
	handler := newAPI(t, port)
	// ASCII-only serializers escape non-ASCII text, so the raw entry a client
	// sends is far larger than its decoded form; the raw bytes are what the
	// same entry weighs when submitted alone.
	entry := `{"idempotency_key":"escaped","source":{"corpus_id":"corpus_news","namespace":"feed","record_key":"escaped"},"content":{"kind":"text","text":"` + strings.Repeat(`\u00e9`, 180000) + `"}}`
	status, body := postRaw(t, handler, "/v0/records/batch", adminKey, []byte(`{"items":[`+entry+`]}`))
	if status != 200 {
		t.Fatalf("got %d code=%v", status, body["code"])
	}
	if e := entryError(t, entries(t, body)[0]); e["code"] != "entry_too_large" {
		t.Fatalf("raw entry above 1 MiB: %v", e)
	}
	if status, _ := postRaw(t, handler, "/v0/records", adminKey, []byte(entry)); status != 413 {
		t.Fatalf("single submission got %d", status)
	}
	if len(port.accepted) != 0 {
		t.Fatalf("accepted %v", port.accepted)
	}
}

func TestBatchCanceledMidwayLeavesRemainingEntriesRetryable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	port := &acceptancePort{cancelAfter: 2, cancel: cancel}
	handler := newAPI(t, port)
	withBlobPart := map[string]any{"idempotency_key": "cut-manifest", "source": map[string]any{"corpus_id": "corpus_news", "namespace": "feed", "record_key": "cut-manifest"}, "content": map[string]any{"kind": "manifest", "parts": []any{
		map[string]any{"key": "source", "role": "source", "content": map[string]any{"kind": "blob", "blob_id": "verified-source", "media_type": "application/xml"}},
	}}}
	payload, _ := json.Marshal(map[string]any{"items": []any{inline("cut-1", "cut-1", "Un"), inline("cut-2", "cut-2", "Deux"), inline("cut-3", "cut-3", "Trois"), withBlobPart}})
	req := httptest.NewRequest("POST", "/v0/records/batch", bytes.NewReader(payload)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+adminKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != 200 {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	conforms(t, "BatchResult", body)
	items := entries(t, body)
	for _, i := range []int{0, 1} {
		if _, ok := items[i]["receipt"]; !ok {
			t.Fatalf("entry %d accepted before the cancellation lost its Receipt: %v", i, items[i])
		}
	}
	// Entries after the cancellation, including one that must verify a Blob,
	// are reported retryable so the client replays their keys.
	for _, i := range []int{2, 3} {
		if e := entryError(t, items[i]); e["code"] != "content_unavailable" || e["retryable"] != true {
			t.Fatalf("entry %d: %v", i, e)
		}
	}
}

func TestBatchRequiresJSONMediaType(t *testing.T) {
	handler := newAPI(t, &acceptancePort{})
	req := httptest.NewRequest("POST", "/v0/records/batch", strings.NewReader(`{"items":[1]}`))
	req.Header.Set("Authorization", "Bearer "+adminKey)
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 415 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestBatchEntryAboveSingleRequestBoundFailsAlone(t *testing.T) {
	port := &acceptancePort{}
	handler := newAPI(t, port)
	oversized := inline("entry-big", "big", strings.Repeat("a", 1<<20))
	status, body := postJSON(t, handler, "/v0/records/batch", adminKey, map[string]any{"items": []any{
		inline("entry-before", "before", "Avant"),
		oversized,
		inline("entry-after", "after", "Après"),
	}})
	if status != 200 {
		t.Fatalf("got %d code=%v", status, body["code"])
	}
	conforms(t, "BatchResult", body)
	items := entries(t, body)
	if e := entryError(t, items[1]); e["code"] != "entry_too_large" || e["retryable"] != false {
		t.Fatalf("oversized entry: %v", e)
	}
	if len(port.accepted) != 2 || port.accepted[0] != "entry-before" || port.accepted[1] != "entry-after" {
		t.Fatalf("acceptance attempts: %v", port.accepted)
	}
	// The same command is not accepted alone either: a batch never admits what a
	// single submission would refuse.
	if status, single := postJSON(t, handler, "/v0/records", adminKey, oversized); status != 413 {
		t.Fatalf("single submission got %d %v", status, single["code"])
	}
}

func TestBatchEntryRejectionsMatchSingleSubmission(t *testing.T) {
	with := func(key string, change func(map[string]any)) map[string]any {
		entry := inline(key, key, "Texte")
		change(entry)
		return entry
	}
	cases := []struct {
		name, key string
		entry     map[string]any
		failure   error
		status    int
		code      string
	}{
		{name: "Corpus outside the key's scope", key: scopedKey, entry: inline("scope", "scope", "Texte"), status: 404, code: "not_found"},
		{name: "conflicting key reuse", key: adminKey, entry: inline("conflict", "conflict", "Texte"), failure: content.ErrConflict, status: 409, code: "idempotency_conflict"},
		{name: "unverified Blob", key: adminKey, entry: with("blob", func(e map[string]any) {
			e["content"] = map[string]any{"kind": "blob", "blob_id": "blob_unknown", "media_type": "text/plain"}
		}), status: 422, code: "unverified_blob"},
		{name: "NUL in text", key: adminKey, entry: inline("nul", "nul", "a\x00b"), status: 422, code: "invalid_input"},
		{name: "missing content", key: adminKey, entry: with("missing", func(e map[string]any) { delete(e, "content") }), status: 422, code: "invalid_schema"},
		{name: "unknown core field", key: adminKey, entry: with("unknown", func(e map[string]any) { e["atomic"] = true }), status: 422, code: "invalid_schema"},
		{name: "storage outage", key: adminKey, entry: inline("outage", "outage", "Texte"), failure: errors.New("connection refused"), status: 503, code: "content_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			port := &acceptancePort{failures: map[string]error{}}
			if tc.failure != nil {
				port.failures[tc.entry["idempotency_key"].(string)] = tc.failure
			}
			handler := newAPI(t, port)
			status, single := postJSON(t, handler, "/v0/records", tc.key, tc.entry)
			if status != tc.status || single["code"] != tc.code {
				t.Fatalf("single submission got %d %v", status, single)
			}
			status, body := postJSON(t, handler, "/v0/records/batch", tc.key, map[string]any{"items": []any{tc.entry}})
			if status != 200 {
				t.Fatalf("batch got %d %v", status, body)
			}
			conforms(t, "BatchResult", body)
			e := entryError(t, entries(t, body)[0])
			if e["code"] != single["code"] || e["retryable"] != single["retryable"] {
				t.Fatalf("batch entry %v differs from single submission %v", e, single)
			}
		})
	}
}
