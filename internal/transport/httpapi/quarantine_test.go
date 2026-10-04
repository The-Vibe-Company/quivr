package httpapi_test

import (
	"context"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/The-Vibe-Company/quivr-v2/internal/quarantine"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

// quarantineStore lists three stuck Versions and keeps dry runs by key.
type quarantineStore struct{ estimates map[string][]byte }

func (quarantineStore) Quarantined(_ context.Context, _ string, _ []string, _ quarantine.Filter, after string, limit int) ([]quarantine.Entry, error) {
	var out []quarantine.Entry
	for _, id := range []string{"version_a", "version_b", "version_c"} {
		if id > after && len(out) < limit {
			out = append(out, quarantine.Entry{VersionID: id, RecordID: "record", CorpusID: "corpus_a", ReceiptID: "receipt", Stage: content.QuarantineIngestion,
				Reason: content.Diagnostic{Code: "ingestion_refused", Message: "refused"}, QuarantinedAt: time.Unix(0, 0).UTC()})
		}
	}
	return out, nil
}
func (quarantineStore) ReprocessSize(context.Context, string, quarantine.Filter) (operations.ReprocessEstimate, error) {
	return operations.ReprocessEstimate{Versions: 3, Stages: map[string]int64{content.QuarantineIngestion: 3}, Codes: map[string]int64{"ingestion_refused": 3}}, nil
}
func (s quarantineStore) RecordReprocessEstimate(_ context.Context, _, _, key string, canonical []byte, _ operations.ReprocessEstimate) error {
	s.estimates[key] = canonical
	return nil
}
func (s quarantineStore) ReprocessEstimate(_ context.Context, _, _, key string) ([]byte, operations.ReprocessEstimate, error) {
	canonical, ok := s.estimates[key]
	if !ok {
		return nil, operations.ReprocessEstimate{}, corpus.ErrNotFound
	}
	return canonical, operations.ReprocessEstimate{Versions: 3}, nil
}
func (quarantineStore) AcceptReprocess(_ context.Context, org, key string, _ []byte, f quarantine.Filter, e operations.ReprocessEstimate) (operations.Operation, error) {
	if key == "busy" {
		return operations.Operation{}, quarantine.ErrInProgress
	}
	return operations.Operation{ID: "operation_reprocess", Organization: org, Kind: operations.KindQuarantineReprocess, CorpusID: f.CorpusID, State: operations.StateQueued,
		Reprocess: &operations.Reprocess{Code: f.Code, PlanID: "plan", Estimate: e}}, nil
}

// The listing pages with a cursor bound to its filters, and each reprocess
// outcome has its status and public code.
func TestQuarantineRoutes(t *testing.T) {
	const operator, organization = "quarantine-admin-token-0123456789abcdef0123", "organization-token-0123456789abcdef0123456"
	keys := map[string]corpus.Scope{
		operator:     {Organization: "org_a", Actions: []string{operations.BackfillPermission}, Corpora: []string{"*"}},
		organization: {Organization: "org_a", Actions: []string{"operations:write", "corpora:write"}, Corpora: []string{"*"}},
	}
	handler, err := httpapi.New(knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"),
		httpapi.WithQuarantine(quarantine.Service{Store: quarantineStore{estimates: map[string][]byte{}}}))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(checkedAPI(t, handler))
	t.Cleanup(server.Close)

	res, page := operationCall(t, server, "GET", "/v0/admin/quarantine?code=ingestion_refused&limit=2", operator, "", "")
	items, _ := page["items"].([]any)
	cursor, _ := page["next_page_cursor"].(string)
	if res.StatusCode != 200 || len(items) != 2 || cursor == "" || items[0].(map[string]any)["reason"].(map[string]any)["code"] != "ingestion_refused" || items[0].(map[string]any)["stage"] != "ingestion" {
		t.Fatalf("first page %d %v", res.StatusCode, page)
	}
	res, page = operationCall(t, server, "GET", "/v0/admin/quarantine?code=ingestion_refused&limit=2&page_cursor="+url.QueryEscape(cursor), operator, "", "")
	if items, _ := page["items"].([]any); res.StatusCode != 200 || len(items) != 1 || items[0].(map[string]any)["version_id"] != "version_c" || page["next_page_cursor"] != nil {
		t.Fatalf("last page %d %v", res.StatusCode, page)
	}
	if res, body := operationCall(t, server, "GET", "/v0/admin/quarantine?code=other&page_cursor="+url.QueryEscape(cursor), operator, "", ""); res.StatusCode != 409 || body["code"] != "cursor_scope_changed" {
		t.Fatalf("a cursor under other filters %d %v", res.StatusCode, body)
	}

	const dry = `{"idempotency_key":"k","corpus_id":"corpus_a","code":"ingestion_refused","dry_run":true}`
	res, estimate := operationCall(t, server, "POST", "/v0/admin/quarantine/reprocess", operator, "application/json", dry)
	if res.StatusCode != 200 || estimate["versions"] != float64(3) || estimate["stages"].(map[string]any)["ingestion"] != float64(3) || estimate["codes"].(map[string]any)["ingestion_refused"] != float64(3) {
		t.Fatalf("dry run %d %v", res.StatusCode, estimate)
	}
	res, op := operationCall(t, server, "POST", "/v0/admin/quarantine/reprocess", operator, "application/json", `{"idempotency_key":"k","corpus_id":"corpus_a","code":"ingestion_refused","dry_run":false}`)
	if res.StatusCode != 202 || op["kind"] != "quarantine_reprocess" || res.Header.Get("Location") != "/v0/operations/operation_reprocess" || op["quarantine_reprocess"].(map[string]any)["plan_id"] != "plan" {
		t.Fatalf("accepted %d %v", res.StatusCode, op)
	}
	if res, _ := operationCall(t, server, "POST", "/v0/admin/quarantine/reprocess", operator, "application/json", `{"idempotency_key":"busy","corpus_id":"corpus_a","dry_run":true}`); res.StatusCode != 200 {
		t.Fatalf("dry run %d", res.StatusCode)
	}
	for _, tc := range []struct {
		name, method, path, token, body string
		status                          int
		code                            string
	}{
		{"list without plugins:admin", "GET", "/v0/admin/quarantine", organization, "", 403, "forbidden"},
		{"reprocess without plugins:admin", "POST", "/v0/admin/quarantine/reprocess", organization, dry, 403, "forbidden"},
		{"an unknown filter", "GET", "/v0/admin/quarantine?stage=ingestion", operator, "", 422, "invalid_query"},
		{"an empty limit", "GET", "/v0/admin/quarantine?limit=", operator, "", 422, "invalid_limit"},
		{"an empty page cursor", "GET", "/v0/admin/quarantine?page_cursor=", operator, "", 422, "invalid_cursor"},
		{"an empty window", "GET", "/v0/admin/quarantine?quarantined_after=2026-01-02T00:00:00Z&quarantined_before=2026-01-01T00:00:00Z", operator, "", 422, "invalid_query"},
		{"no dry run for the key", "POST", "/v0/admin/quarantine/reprocess", operator, `{"idempotency_key":"other","corpus_id":"corpus_a","dry_run":false}`, 409, "dry_run_required"},
		{"another scope under the key", "POST", "/v0/admin/quarantine/reprocess", operator, `{"idempotency_key":"k","corpus_id":"corpus_a","dry_run":false}`, 409, "idempotency_conflict"},
		{"another reprocess of the Corpus", "POST", "/v0/admin/quarantine/reprocess", operator, `{"idempotency_key":"busy","corpus_id":"corpus_a","dry_run":false}`, 409, "reprocess_in_progress"},
		{"an empty reprocess window", "POST", "/v0/admin/quarantine/reprocess", operator, `{"idempotency_key":"w","corpus_id":"corpus_a","dry_run":true,"quarantined_after":"2026-01-02T00:00:00Z","quarantined_before":"2026-01-01T00:00:00Z"}`, 422, "invalid_reprocess"},
		{"dry_run missing", "POST", "/v0/admin/quarantine/reprocess", operator, `{"idempotency_key":"k","corpus_id":"corpus_a"}`, 422, "invalid_schema"},
		{"POST on the listing", "POST", "/v0/admin/quarantine", operator, dry, 405, "method_not_allowed"},
	} {
		contentType := ""
		if tc.body != "" {
			contentType = "application/json"
		}
		res, body := operationCall(t, server, tc.method, tc.path, tc.token, contentType, tc.body)
		if res.StatusCode != tc.status || body["code"] != tc.code {
			t.Errorf("%s: %d %v, want %d %s", tc.name, res.StatusCode, body, tc.status, tc.code)
		}
	}
}
