package runner

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
)

// The runner must judge the advertised capability over HTTP: ordinary
// responses alone cannot certify a plugin which ignores continuation.
func TestIngestionCertificationExercisesPaging(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/plugins/v0/fixtures/manifests/valid/ingestion-paged.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pin, err := plugins.LoadPinManifest(raw, "paged", plugins.PinConfig{Endpoint: "http://127.0.0.1:9"})
	if err != nil {
		t.Fatal(err)
	}
	for _, ignoresCursor := range []bool{false, true} {
		t.Run(map[bool]string{false: "continuation", true: "ignores-cursor"}[ignoresCursor], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				body, _ := io.ReadAll(req.Body)
				schema := "ingestion-segment-and-embed-request.schema.json"
				if req.URL.Path == plugins.EmbedQueryRoute {
					schema = "ingestion-embed-query-request.schema.json"
				}
				if len(plugins.ValidateDocument(schema, body)) != 0 || strings.Contains(string(body), "quivr_contract_undeclared") {
					w.WriteHeader(400)
					_ = json.NewEncoder(w).Encode(map[string]any{"code": "invalid_request", "message": "invalid request", "retryable": false})
					return
				}
				if req.URL.Path == plugins.EmbedQueryRoute {
					_ = json.NewEncoder(w).Encode(map[string]any{"vector": []float64{1, 0, 0, 0}})
					return
				}
				var r plugins.SegmentAndEmbedRequest
				_ = json.Unmarshal(body, &r)
				segments := []any{}
				response := map[string]any{"segments": segments}
				for _, p := range r.Parts {
					n := len([]rune(p.Text))
					start, end := 0, n
					if r.Page != nil && !ignoresCursor {
						start = r.Page.Start
						end = min(n, start+2)
						if end < n {
							response["next_start"] = end
						}
					}
					if start == end {
						continue
					}
					vectors := map[string]any{}
					for _, space := range r.Spaces {
						vectors[space] = []float64{1, 0, 0, 0}
					}
					segments = append(segments, map[string]any{"part_key": p.Key, "start": start, "end": end, "vectors": vectors, "lexical_text": strings.ToLower(string([]rune(p.Text)[start:end]))})
				}
				response["segments"] = segments
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			r := &run{m: &pin.Manifest, api: plugins.ResolveAPI(pin.PluginAPI()), baseURL: server.URL}
			r.ingestion(t.Context(), nil)
			found := false
			for _, check := range r.report.Checks {
				if check.ID == "ingestion_pages" {
					found = true
					if ignoresCursor {
						if check.Status != Fail || len(check.Issues) == 0 || check.Issues[0].Code != "invalid_ingestion_page" {
							t.Fatalf("ignored cursor accepted: %+v", check)
						}
					} else if check.Status != Pass {
						t.Fatalf("valid continuation rejected: %+v", check)
					}
				}
				if !ignoresCursor && check.Status == Fail {
					t.Fatalf("valid plugin rejected: %+v", check)
				}
			}
			if !found {
				t.Fatal("advertised paging was never exercised")
			}
		})
	}
}
