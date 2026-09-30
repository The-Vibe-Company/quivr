package registry_test

import (
	"context"
	"os"
	"testing"
	"time"

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

// TestRegistrationCheckRunsTheContractRunner owns the registration check: the
// Contract Runner runs in process against the address the operator gave, with
// the manifest the registration carries, and certifies a well-behaved
// ingestion plugin; the same plugin answering vectors of the wrong size is
// not certified, and the report lists the failing check.
func TestRegistrationCheckRunsTheContractRunner(t *testing.T) {
	const path = "../../../tests/plugin-contract/ingestion-valid/quivr-plugin.yaml"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	check := func(mode string) registry.CheckReport {
		t.Helper()
		proc, err := devhost.Start(devhost.Options{Command: fakeplugin.Command(), Manifest: path, Env: []string{fakeplugin.EnvEnable + "=1", fakeplugin.EnvMode + "=" + mode}})
		if err != nil {
			t.Fatal(err)
		}
		defer proc.Stop(5 * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := proc.WaitHealthy(ctx); err != nil {
			t.Fatal(err)
		}
		pin, err := plugins.LoadPinManifest(raw, "test", plugins.PinConfig{Endpoint: proc.BaseURL, Spaces: map[string]string{"certified.ingestion-valid.small": plugins.SpaceServed, "certified.ingestion-valid.large": plugins.SpaceEvaluation}})
		if err != nil {
			t.Fatal(err)
		}
		set, err := plugins.NewPinSet([]*plugins.Pin{pin})
		if err != nil {
			t.Fatal(err)
		}
		return registry.RunCheck(ctx, registry.FromPins(set).Registrations[0])
	}

	if ok := check("ok"); !ok.Certified || ok.Failed != 0 || ok.Passed == 0 {
		t.Fatalf("a well-behaved plugin: %+v, want certified", ok)
	}
	broken := check("ingestion-dimensions")
	failing := map[string]bool{}
	for _, c := range broken.Checks {
		if c.Status == string(runner.Fail) && len(c.Issues) > 0 {
			failing[c.ID] = true
		}
	}
	if broken.Certified || broken.Failed == 0 || !failing[runner.CheckInvoke] {
		t.Fatalf("vectors of the wrong size: %+v, want not certified with the invoke check failing", broken)
	}
}
