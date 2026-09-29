package scaffold_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/scaffold"
)

func TestTemplatePassesInspect(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-plugin")
	files, err := scaffold.Write(dir, "my-plugin", scaffold.KindNormalizer)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"quivr-plugin.yaml", "pyproject.toml", "README.md", ".gitignore",
		"my_plugin/__init__.py", "my_plugin/__main__.py", "my_plugin/normalizer.py",
		"fixtures/sample.json", "fixtures/sample.md", "tests/test_normalizer.py",
	} {
		if !contains(files, want) {
			t.Errorf("template lacks %s: %v", want, files)
		}
	}
	report := plugins.Inspect(dir)
	if !report.Valid {
		t.Fatalf("template fails inspect: %+v", report.Errors)
	}
	m := report.Manifest
	if m.ID != "my-plugin" || strings.Join(m.Run.Command, " ") != "python3 -m my_plugin" ||
		strings.Join(m.Contributions.Normalizer.MediaTypes, ",") != "text/markdown" {
		t.Fatalf("manifest %+v", m)
	}
	if _, issues, err := devhost.BuildFixtureRequest(filepath.Join(dir, "fixtures", "sample.json"), m); err != nil || len(issues) != 0 {
		t.Fatalf("sample fixture: %+v %v", issues, err)
	}
	noPlaceholders(t, dir)
}

func TestSubscriptionTemplatePassesInspect(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-alerts")
	files, err := scaffold.Write(dir, "my-alerts", scaffold.KindSubscription)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"quivr-plugin.yaml", "pyproject.toml", "README.md", ".gitignore",
		"my_alerts/__init__.py", "my_alerts/__main__.py", "my_alerts/rule.py",
		"fixtures/sample.json", "tests/test_rule.py",
	} {
		if !contains(files, want) {
			t.Errorf("template lacks %s: %v", want, files)
		}
	}
	report := plugins.Inspect(dir)
	if !report.Valid {
		t.Fatalf("template fails inspect: %+v", report.Errors)
	}
	m := report.Manifest
	if m.Contributions.Normalizer != nil || m.Contributions.Subscription == nil || report.Compatibility.PluginAPI.Version != "0.2.0" {
		t.Fatalf("manifest %+v, compatibility %+v", m.Contributions, report.Compatibility)
	}
	batches, issues, err := devhost.BuildSubscriptionRequests(filepath.Join(dir, "fixtures", "sample.json"), m)
	if err != nil || len(issues) != 0 || len(batches) != 1 || len(batches[0].Expect) != 3 {
		t.Fatalf("sample fixture: %d batches, %+v %v", len(batches), issues, err)
	}
	// The expression schema discriminates alert kinds on "kind".
	if issues := plugins.ValidateSubscriptionItem(m, []byte(`{"kind": "keywords", "text": "x"}`), []byte(`{}`)); len(issues) == 0 {
		t.Fatal("an unknown kind was accepted")
	}
	noPlaceholders(t, dir)
}

func TestWriteRefusesAnUnknownKind(t *testing.T) {
	if _, err := scaffold.Write(t.TempDir(), "demo", "connector"); err == nil || !strings.Contains(err.Error(), "normalizer or subscription") {
		t.Fatalf("err %v", err)
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

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
