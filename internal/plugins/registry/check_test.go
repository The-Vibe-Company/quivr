package registry_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost/fakeplugin"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/runner"
)

func TestMain(m *testing.M) {
	fakeplugin.MaybeRun()
	os.Exit(m.Run())
}

// fixtureFolder reads a plugin's fixtures folder as a registration carries
// it: each file by its path in the folder.
func fixtureFolder(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if files[e.Name()], err = os.ReadFile(filepath.Join(dir, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	return files
}

// TestRegistrationCheckRunsTheContractRunner owns the registration check: the
// Contract Runner runs in process against the address the operator gave, with
// the manifest and the fixtures the registration carries, as `quivr plugin
// test` runs the plugin's folder. A normalizer for a media type no normative
// fixture covers and a connector are certified with their own fixtures; a
// plugin that fails one of them is not, and the report names the failing
// check and its fixture.
func TestRegistrationCheckRunsTheContractRunner(t *testing.T) {
	const contract = "../../../tests/plugin-contract/"
	// The baseline normalizer narrowed to text/plain: no normative fixture
	// applies, so only its own note.json exercises it.
	raw, err := os.ReadFile(contract + "valid/quivr-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(t.TempDir(), plugins.ManifestFile)
	if err = os.WriteFile(plain, []byte(strings.Replace(string(raw), "[text/markdown, text/plain]", "[text/plain]", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	plainRoute := []plugins.RouteConfig{{MediaType: "text/plain"}}
	ingestionSpaces := map[string]string{"certified.ingestion-valid.small": plugins.SpaceServed, "certified.ingestion-valid.large": plugins.SpaceEvaluation}
	for _, tc := range []struct {
		name, manifest, mode string
		spaces               map[string]string
		routes               []plugins.RouteConfig
		fixtures             map[string][]byte
		// failing is the check, and its fixture, that must fail; "" wants
		// the plugin certified.
		failing, fixture string
	}{
		{name: "ingestion", manifest: contract + "ingestion-valid/quivr-plugin.yaml", mode: "ok", spaces: ingestionSpaces},
		{name: "ingestion vectors of the wrong size", manifest: contract + "ingestion-valid/quivr-plugin.yaml", mode: "ingestion-dimensions", spaces: ingestionSpaces, failing: runner.CheckInvoke},
		{name: "text/plain normalizer", manifest: plain, mode: "ok", routes: plainRoute, fixtures: fixtureFolder(t, contract+"valid/fixtures")},
		{name: "text/plain normalizer failing its fixture", manifest: plain, mode: "malformed-part", routes: plainRoute, fixtures: fixtureFolder(t, contract+"valid/fixtures"), failing: runner.CheckInvoke, fixture: "fixtures/note.json"},
		{name: "connector", manifest: contract + "connector-valid/quivr-plugin.yaml", mode: "ok", fixtures: fixtureFolder(t, contract+"connector-valid/fixtures")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest, err := os.ReadFile(tc.manifest)
			if err != nil {
				t.Fatal(err)
			}
			proc, err := devhost.Start(devhost.Options{Command: fakeplugin.Command(), Manifest: tc.manifest, Env: []string{fakeplugin.EnvEnable + "=1", fakeplugin.EnvMode + "=" + tc.mode}})
			if err != nil {
				t.Fatal(err)
			}
			defer proc.Stop(5 * time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := proc.WaitHealthy(ctx); err != nil {
				t.Fatal(err)
			}
			pin, err := plugins.LoadPinManifest(manifest, "test", plugins.PinConfig{Endpoint: proc.BaseURL, Spaces: tc.spaces, Routes: tc.routes})
			if err != nil {
				t.Fatal(err)
			}
			set, err := plugins.NewPinSet([]*plugins.Pin{pin})
			if err != nil {
				t.Fatal(err)
			}
			r := registry.FromPins(set).Registrations[0]
			r.Fixtures = tc.fixtures
			report := registry.RunCheck(ctx, r)
			if tc.failing == "" {
				if !report.Certified || report.Failed != 0 || report.Passed == 0 {
					t.Fatalf("report %+v, want certified", report)
				}
				return
			}
			for _, c := range report.Checks {
				if c.ID == tc.failing && c.Status == string(runner.Fail) && len(c.Issues) > 0 && (tc.fixture == "" || c.Fixture == tc.fixture) && !report.Certified {
					return
				}
			}
			t.Fatalf("report %+v, want not certified with the %s check failing on %q", report, tc.failing, tc.fixture)
		})
	}
}

// TestRegistrationRefusesFixturesOutsideTheirFolder owns the bounds of the
// fixtures a registration carries: the check writes each one under the
// plugin's fixtures folder, so a path that would leave it is refused before
// anything is stored.
func TestRegistrationRefusesFixturesOutsideTheirFolder(t *testing.T) {
	manifest, err := os.ReadFile("../../../tests/plugin-contract/valid/quivr-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	routes := []plugins.RouteConfig{{MediaType: "text/plain"}}
	scope := corpus.Scope{Organization: "org_ops", Actions: []string{registry.Action}, Corpora: []string{"*"}}
	for _, name := range []string{"../escape.json", "/etc/escape.json", "inputs/../../escape.json", ".", "a\\b.json"} {
		_, err := registry.Service{}.Register(context.Background(), scope, registry.Request{Key: "k", Manifest: manifest, Endpoint: "http://127.0.0.1:9900", Routes: routes, Fixtures: map[string][]byte{"note.json": []byte("{}"), name: []byte("{}")}})
		var issues *registry.IssueError
		if !errors.Is(err, registry.ErrInvalid) || !errors.As(err, &issues) || len(issues.Issues) != 1 || !strings.HasPrefix(issues.Issues[0].Path, "/fixtures/") {
			t.Errorf("fixture %q: %v, want invalid_plugin naming that fixture alone", name, err)
		}
	}
	big := map[string][]byte{"a.bin": make([]byte, registry.MaxFixtureBytes/2+1), "b.bin": make([]byte, registry.MaxFixtureBytes/2)}
	if _, err := (registry.Service{}).Register(context.Background(), scope, registry.Request{Key: "k", Manifest: manifest, Endpoint: "http://127.0.0.1:9900", Routes: routes, Fixtures: big}); !errors.Is(err, registry.ErrInvalid) {
		t.Errorf("fixtures over %d bytes: %v, want invalid_plugin", registry.MaxFixtureBytes, err)
	}
}
