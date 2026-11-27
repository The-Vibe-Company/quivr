package scaffold_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost"
	"github.com/The-Vibe-Company/quivr/internal/plugins/runner"
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
			// Dotfiles and the module directory are embedded too (all:); Go
			// sources and go.mod lose their .tmpl suffix.
			entry, command := "my_plugin/__main__.py", "python3 -m my_plugin"
			if kind == scaffold.KindRetrieval {
				entry, command = "go.mod", "go run ."
			}
			if !slices.Contains(files, ".gitignore") || !slices.Contains(files, entry) || slices.ContainsFunc(files, func(f string) bool { return strings.HasSuffix(f, ".tmpl") }) {
				t.Fatalf("template files %v", files)
			}
			report := plugins.Inspect(dir)
			if !report.Valid {
				t.Fatalf("template fails inspect: %+v", report.Errors)
			}
			m := report.Manifest
			if m.ID != "my-plugin" || strings.Join(m.Run.Command, " ") != command || strings.Join(m.Contributions.Names(), ",") != kind {
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
			case scaffold.KindRetrieval:
				// One fixture per profile, each expecting that profile's ranking.
				for _, profile := range []string{"default", "composed"} {
					raw, err := os.ReadFile(filepath.Join(dir, "fixtures", profile+".json"))
					if err != nil {
						t.Fatal(err)
					}
					if run, issues := devhost.BuildRetrievalRun(raw, m); len(issues) != 0 || !slices.Equal(run.Profiles, []string{profile}) || len(run.Top) == 0 {
						t.Fatalf("%s fixture: %+v %+v", profile, issues, run)
					}
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

// TestRetrievalTemplateIsCertified owns the Go retrieval template end to end:
// the generated module vets and passes its own tests, and `quivr plugin test`
// certifies it under both profiles, the composed one through a profile
// request. The module builds against this checkout's SDK, offline, with the
// template's go.sum: a missing entry means the template's SDK version needs
// a bump (go get the new SDK version in a scaffolded plugin, go mod tidy, and
// copy go.mod and go.sum back).
func TestRetrievalTemplateIsCertified(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("the retrieval template needs the go command: %v", err)
	}
	sdk, err := filepath.Abs(filepath.Join("..", "..", "..", "sdks", "go"))
	if err != nil {
		t.Fatal(err)
	}
	// Named after a standard library package: the plugin id must not become
	// a module path the go command resolves to the standard library.
	dir := filepath.Join(t.TempDir(), "sort")
	if _, err := scaffold.Write(dir, "sort", scaffold.KindRetrieval); err != nil {
		t.Fatal(err)
	}
	// The plugin process inherits this environment through `go run .`.
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOFLAGS", "-mod=readonly")
	for _, args := range [][]string{
		{"mod", "edit", "-replace", "github.com/The-Vibe-Company/quivr/sdks/go=" + sdk},
		{"vet", "./..."},
		// Also compiles the plugin, so the runner's startup wait covers only the start.
		{"test", "./..."},
	} {
		cmd := exec.Command(gobin, args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	var logs strings.Builder
	report := runner.Run(t.Context(), runner.Options{Dir: dir, StartupTimeout: 2 * time.Minute, Output: &logs})
	if !report.Certified {
		t.Fatalf("not certified: %+v\nplugin output:\n%s", report.Checks, logs.String())
	}
	// Certified alone could pass on the normative fixture: each own fixture
	// must have ranked as it expects under its profile.
	for _, profile := range []string{"default", "composed"} {
		if !slices.ContainsFunc(report.Checks, func(c runner.Check) bool {
			return c.ID == runner.CheckInvoke && c.Status == runner.Pass && strings.HasSuffix(c.Fixture, profile+".json") && strings.HasPrefix(c.Title, "profile "+profile+" ")
		}) {
			t.Errorf("no passing invoke check of fixtures/%s.json under profile %s: %+v", profile, profile, report.Checks)
		}
	}
}
