package scaffold_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost"
	"github.com/The-Vibe-Company/quivr/internal/plugins/scaffold"
)

// TestTemplatesAreValidPlugins owns the content of every template kind: a
// valid manifest naming the plugin and its module, a sample fixture the
// manifest accepts, and no placeholder left. scripts/plugin_sdk.sh runs each
// template end to end.
func TestTemplatesAreValidPlugins(t *testing.T) {
	for _, kind := range scaffold.Kinds {
		t.Run(kind, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "my-plugin")
			files, err := scaffold.Write(dir, "my-plugin", kind)
			if err != nil {
				t.Fatal(err)
			}
			// Dotfiles and the module directory are embedded too (all:).
			if !slices.Contains(files, ".gitignore") || !slices.Contains(files, "my_plugin/__main__.py") {
				t.Fatalf("template files %v", files)
			}
			report := plugins.Inspect(dir)
			if !report.Valid {
				t.Fatalf("template fails inspect: %+v", report.Errors)
			}
			m := report.Manifest
			if m.ID != "my-plugin" || strings.Join(m.Run.Command, " ") != "python3 -m my_plugin" || strings.Join(m.Contributions.Names(), ",") != kind {
				t.Fatalf("manifest %+v", m)
			}
			fixture := filepath.Join(dir, "fixtures", "sample.json")
			switch kind {
			case scaffold.KindConnector:
				for _, name := range []string{"sample.json", "revoked.json"} {
					if run, issues, err := devhost.BuildConnectorRun(filepath.Join(dir, "fixtures", name), m); err != nil || len(issues) != 0 || run == nil {
						t.Fatalf("%s fixture: %+v %v", name, issues, err)
					}
				}
			case scaffold.KindNormalizer:
				if _, issues, err := devhost.BuildFixtureRequest(fixture, m); err != nil || len(issues) != 0 {
					t.Fatalf("sample fixture: %+v %v", issues, err)
				}
			case scaffold.KindSubscription:
				if batches, issues, err := devhost.BuildSubscriptionRequests(fixture, m); err != nil || len(issues) != 0 || len(batches) != 1 {
					t.Fatalf("sample fixture: %d batches, %+v %v", len(batches), issues, err)
				}
				// The expression schema discriminates alert kinds on "kind".
				if issues := plugins.ValidateSubscriptionItem(m, []byte(`{"kind": "keywords", "text": "x"}`), []byte(`{}`)); len(issues) == 0 {
					t.Fatal("an unknown kind was accepted")
				}
			default:
				t.Fatalf("no fixture check for template kind %s", kind)
			}
			noPlaceholders(t, dir)
		})
	}
}

func noPlaceholders(t *testing.T, dir string) {
	t.Helper()
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(path, "__PLUGIN") {
			t.Errorf("placeholder left in a path: %s", path)
		}
		if !d.IsDir() {
			b, _ := os.ReadFile(path)
			if strings.Contains(string(b), "__PLUGIN") {
				t.Errorf("placeholder left in %s", path)
			}
		}
		return nil
	})
}

func TestWriteRefusesANonEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := scaffold.Write(dir, "demo", scaffold.KindNormalizer); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("err %v", err)
	}
	if _, err := scaffold.Write(t.TempDir(), "demo", scaffold.KindNormalizer); err != nil {
		t.Fatalf("an existing empty directory is fine: %v", err)
	}
}

func TestCheckName(t *testing.T) {
	for _, name := range []string{"demo", "my-plugin", "pdf2text"} {
		if err := scaffold.CheckName(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"", "Demo", "1demo", "my_plugin", "a.b", "trailing-", "class", "json", "tests", "quivr-plugin", strings.Repeat("a", 65)} {
		if err := scaffold.CheckName(name); err == nil {
			t.Errorf("%q accepted", name)
		}
	}
}
