package acceptance

import (
	"encoding/base64"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pluginFixtures reads a plugin's fixtures folder as a registration carries
// it: each file, base64-encoded, by its path in the folder.
func pluginFixtures(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	root := filepath.Join(dir, "fixtures")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		rel, _ := filepath.Rel(root, path)
		out[filepath.ToSlash(rel)] = base64.StdEncoding.EncodeToString(data)
		return err
	})
	if err != nil || len(out) == 0 {
		t.Fatalf("fixtures of %s: %v (%d files)", dir, err, len(out))
	}
	return out
}

// TestPluginRegistrationWithItsFixtures registers, through the operator API,
// two plugins no normative fixture exercises: the pdf-text normalizer
// (application/pdf) and the RSS connector. The stack already runs both; each
// is registered at another address of the same process, with its manifest
// and its own fixtures folder. The check certifies both, as `quivr plugin
// test` does offline, and activating them makes them serve their roles.
func TestPluginRegistrationWithItsFixtures(t *testing.T) {
	operator := os.Getenv("QUIVR_TEST_OPERATOR")
	if os.Getenv("QUIVR_TEST_URL") == "" || operator == "" {
		t.Skip("make verify runs pdf-text and the RSS connector and sets the operator key")
	}
	run := time.Now().UTC().Format("20060102T150405.000000000")
	pinned := map[string]map[string]any{}
	for _, raw := range request(t, "GET", "/v0/admin/plugins", operator, nil, 200)["items"].([]any) {
		if item := raw.(map[string]any); item["state"] == "active" {
			pinned[item["plugin_id"].(string)] = item
		}
	}
	for _, p := range []struct {
		id, dir, role string
		routes        []map[string]string
	}{
		{id: "pdf-text", dir: "../../plugins/pdf-text", role: "normalizer:application/pdf", routes: []map[string]string{{"media_type": "application/pdf"}}},
		{id: "connector.rss", dir: "../../plugins/rss", role: "connector:rss"},
	} {
		if pinned[p.id] == nil {
			t.Fatalf("%s is not an active registration of the stack: %v", p.id, pinned)
		}
		manifest, err := os.ReadFile(filepath.Join(p.dir, "quivr-plugin.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		body := map[string]any{"idempotency_key": "own-fixtures-" + p.id + "-" + run, "manifest": string(manifest), "fixtures": pluginFixtures(t, p.dir),
			"endpoint": strings.Replace(pinned[p.id]["endpoint"].(string), "127.0.0.1", "localhost", 1)}
		if p.routes != nil {
			body["routes"] = p.routes
		}
		accepted := request(t, "POST", "/v0/admin/plugins", operator, body, 202)
		checked := awaitCheck(t, operator, "/v0/admin/plugins/"+accepted["registration_id"].(string))
		if checked["state"] != "validated" || checked["check"].(map[string]any)["certified"] != true {
			t.Fatalf("%s registered with its fixtures: %v, want validated", p.id, checked)
		}
		plan := request(t, "POST", "/v0/admin/plugins/"+accepted["registration_id"].(string)+"/activate", operator, map[string]any{}, 200)
		serving := ""
		for _, raw := range plan["roles"].([]any) {
			if role := raw.(map[string]any); role["role"] == p.role {
				serving = role["registration_id"].(string)
			}
		}
		if serving != accepted["registration_id"] {
			t.Fatalf("after activating %s, %s is served by %q, want %v (plan %v)", p.id, p.role, serving, accepted["registration_id"], plan)
		}
	}
}
