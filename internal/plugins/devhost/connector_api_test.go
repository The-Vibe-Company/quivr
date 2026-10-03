package devhost_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
)

// Owns route-fixture wire construction: losing the name, path or parsed body
// would certify a different delivery from the engine's secure API request.
func TestConnectorAPIFixture(t *testing.T) {
	report := plugins.Inspect("../../../contracts/plugins/v0/fixtures/manifests/valid/connector-api.json")
	if !report.Valid {
		t.Fatal(report.Errors)
	}
	for _, tc := range []struct {
		name, route, method, path, body string
		valid                           bool
	}{
		{"post", "push", "POST", "events/news", `{"text":"Hello"}`, true},
		{"challenge", "challenge", "GET", "challenge", "", true},
		{"unknown route", "missing", "POST", "events/news", `{}`, false},
		{"wrong method", "push", "GET", "events/news", `{}`, false},
		{"wrong path", "push", "POST", "other/news", `{}`, false},
		{"unsafe segment", "push", "POST", "events/..", `{}`, false},
		{"non JSON", "push", "POST", "events/news", "broken", false},
		{"schema mismatch", "push", "POST", "events/news", `{"text":42}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := map[string]any{"connector": map[string]any{"kind": "events", "config": map[string]any{}}, "receive": []any{map[string]any{"route": tc.route, "request": map[string]any{"method": tc.method, "path": tc.path, "body": tc.body}}}}
			raw, _ := json.Marshal(input)
			file := filepath.Join(t.TempDir(), "case.json")
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			run, issues, err := devhost.BuildConnectorRun(file, report.Manifest)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.valid {
				if len(issues) == 0 {
					t.Fatal("invalid route fixture accepted")
				}
				return
			}
			if len(issues) > 0 {
				t.Fatal(issues)
			}
			var wire map[string]any
			if err := json.Unmarshal(run.ReceiveCaseRequest(run.Receives[0], "test"), &wire); err != nil {
				t.Fatal(err)
			}
			if wire["route"] != tc.route || wire["request"].(map[string]any)["path"] != tc.path {
				t.Fatalf("route/path lost: %v", wire)
			}
			if tc.method == "GET" {
				if wire["body"] != nil {
					t.Fatalf("challenge body %v", wire["body"])
				}
			} else if wire["body"].(map[string]any)["text"] != "Hello" {
				t.Fatalf("parsed body %v", wire["body"])
			}
			if issues := plugins.ValidateDocument("connector-receive-request.schema.json", run.ReceiveCaseRequest(run.Receives[0], "test")); len(issues) > 0 {
				t.Fatal(issues)
			}
		})
	}
}
