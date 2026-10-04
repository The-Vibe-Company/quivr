package apicontract_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/testutil/apicontract"
)

func TestFakeResponseMustMatchItsOperation(t *testing.T) {
	// Observe a real handler response, not a component schema chosen by the fake.
	fake := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	})
	req := httptest.NewRequest("POST", "/v0/search", nil)
	rec := httptest.NewRecorder()
	fake.ServeHTTP(rec, req)
	if err := apicontract.Check(req.Method, req.URL.String(), rec.Code, rec.Header(), rec.Body.Bytes()); err == nil || !strings.Contains(err.Error(), "retrieval_profile") {
		t.Fatalf("invalid fake must fail for missing retrieval_profile, got %v", err)
	}
	for _, response := range []struct {
		method, path string
		status       int
		body         string
	}{
		{"POST", "/v0/search", 200, `{"items":[],"retrieval_profile":{"name":"default","version":"v1"}}`},
		{"GET", "/v0/corpora/corpus_example?limit=1", 404, `{"code":"not_found","message":"Missing corpus","retryable":false}`},
		{"POST", "/v0/connectors/connector_example/api/events/news?tag=1", 202, `{"receipts":[]}`},
	} {
		if err := apicontract.Check(response.method, response.path, response.status, http.Header{"Content-Type": {"application/json; charset=utf-8"}}, []byte(response.body)); err != nil {
			t.Fatal(err)
		}
	}
	path := "/v0/connectors/connector_example/api/events/news"
	headers := http.Header{"Content-Type": {"text/plain"}}
	if err := apicontract.Check("POST", path, 503, headers, []byte("unavailable")); err == nil {
		t.Fatal("engine failure must require the JSON Error envelope")
	}
	headers.Set("Content-Type", "application/json")
	if err := apicontract.Check("POST", path, 503, headers, []byte(`{"code":"ingestion_unavailable","message":"unavailable"}`)); err == nil {
		t.Fatal("engine Error must retain its required retryable field")
	}
	headers.Set("Content-Type", "text/plain")
	headers.Set("Quivr-Response-Origin", "plugin")
	if err := apicontract.Check("POST", path, 429, headers, []byte("provider refusal")); err != nil {
		t.Fatalf("declared plugin reply rejected: %v", err)
	}
	if err := apicontract.Check("POST", path, 503, headers, []byte("provider refusal")); err == nil {
		t.Fatal("a marked plugin reply must obey the declared status range")
	}
	if err := apicontract.Check("POST", "/v0/search", 503, headers, []byte("provider refusal")); err == nil {
		t.Fatal("a plugin header cannot bypass an engine-only operation")
	}
	challenge := "/v0/connectors/connector_example/api/challenge"
	if err := apicontract.Check("GET", challenge, 204, headers, []byte("body")); err == nil {
		t.Fatal("a plugin marker cannot permit a body on HTTP 204")
	}
	if err := apicontract.Check("GET", challenge, 204, headers, nil); err != nil {
		t.Fatal(err)
	}
}
