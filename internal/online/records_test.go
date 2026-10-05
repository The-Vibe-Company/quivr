package online_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/online"
	"github.com/The-Vibe-Company/quivr/internal/testutil/apicontract"
)

// Owns flag-to-public-client mapping and continuation output, not SQL ordering.
func TestRecordsMapsListingAndCount(t *testing.T) {
	for _, count := range []bool{false, true} {
		t.Run(map[bool]string{false: "page", true: "count"}[count], func(t *testing.T) {
			var got *http.Request
			body := `{"items":[],"next_page_cursor":"next-page"}`
			path := "/v0/records"
			if count {
				path += "/count"
				body = `{"count":7}`
			}
			server := httptest.NewServer(apicontract.Handler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(body))
			})))
			defer server.Close()
			args := []string{"records", "--corpus", "corpus_a", "--accepted-after", "2026-10-01T02:00:00+02:00", "--accepted-before", "2026-10-02T00:00:00Z"}
			want := url.Values{"corpus_id": {"corpus_a"}, "accepted_after": {"2026-10-01T02:00:00+02:00"}, "accepted_before": {"2026-10-02T00:00:00Z"}}
			if count {
				args = append(args, "--count")
			} else {
				args = append(args, "--order", "accepted_at_desc", "--page-cursor", "opaque+cursor", "--limit", "2")
				want.Set("order", "accepted_at_desc")
				want.Set("page_cursor", "opaque+cursor")
				want.Set("limit", "2")
			}
			result := run(t, map[string]string{online.EnvAPIURL: server.URL, online.EnvAPIKey: "test-key"}, args...)
			if result.code != online.ExitOK || result.stdout != body+"\n" {
				t.Fatalf("exit %d, stdout %q, stderr %q", result.code, result.stdout, result.stderr)
			}
			if got == nil || got.Method != http.MethodGet || got.URL.Path != path || got.URL.Query().Encode() != want.Encode() || got.Header.Get("Authorization") != "Bearer test-key" {
				t.Fatalf("request %+v, want GET %s?%s", got, path, want.Encode())
			}
		})
	}
}

// The CLI must reject unsupported precision before generated client formatting
// loses the original digits; the API cannot validate digits it never receives.
func TestRecordsRejectsUnsupportedBoundPrecision(t *testing.T) {
	server := httptest.NewServer(apicontract.Handler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"items":[]}`))
	})))
	defer server.Close()
	for _, flag := range []string{"--accepted-after", "--accepted-before"} {
		result := run(t, map[string]string{online.EnvAPIURL: server.URL}, "records", "--corpus", "corpus_a", flag, "2026-10-01T00:00:00.0000000001Z")
		if result.code != online.ExitUsage {
			t.Fatalf("%s unsupported precision: exit %d, stdout %q, stderr %q", flag, result.code, result.stdout, result.stderr)
		}
	}
}
