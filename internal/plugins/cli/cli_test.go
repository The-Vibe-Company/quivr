package cli_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/cli"
)

const fixtures = "../../../contracts/plugins/v0/fixtures/manifests/"

func run(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := cli.Run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestInspectSummarizesASubscriptionContribution(t *testing.T) {
	code, out, errOut := run("inspect", fixtures+"valid/subscription.yaml")
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	for _, want := range []string{
		"example-alerts 0.3.0", "Plugin API 0.2.0", "Contribution subscription",
		"16 evaluations per request", "5000 ms", "2 attempts", "1048576 bytes",
		"kinds", "substring, any_of", "case_sensitive",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
}

func TestInspectPrintsAHumanSummary(t *testing.T) {
	code, out, errOut := run("inspect", fixtures+"valid/full.yaml")
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	for _, want := range []string{
		"example-pdf 1.2.0-rc.1+build.7", "valid",
		">=0.1.0 <0.2.0", "engine 0.2.0", "Plugin API 0.1.0",
		"normalizer", "application/pdf", "60000 ms", "4 attempts", "8388608 bytes", "200",
		"max_pages", "required",
		"EXAMPLE_PDF_LICENSE_KEY", "optional",
		"example-pdf.document", "example-pdf.page",
		"python -m example_pdf", "sha256:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
}

func TestInspectAcceptsAPluginDirectory(t *testing.T) {
	dir := t.TempDir()
	raw, _ := readFixture(t, "valid/minimal.yaml")
	writeFile(t, dir+"/"+plugins.ManifestFile, raw)
	if code, out, _ := run("inspect", dir); code != 0 || !strings.Contains(out, "example-minimal") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestInspectReportsInvalidManifestsWithExitOne(t *testing.T) {
	code, out, _ := run("inspect", fixtures+"invalid/foreign-namespace.yaml")
	if code != 1 || !strings.Contains(out, "foreign_namespace") || !strings.Contains(out, "/extensions/example-pdfx.document") || !strings.Contains(out, "invalid") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if code, out, _ := run("inspect", fixtures+"missing.yaml"); code != 1 || !strings.Contains(out, plugins.CodeUnreadable) {
		t.Fatalf("missing file: exit %d:\n%s", code, out)
	}
}

func TestInspectJSONReport(t *testing.T) {
	for _, c := range []struct {
		file  string
		code  int
		valid bool
	}{{"valid/full.yaml", 0, true}, {"invalid/incompatible-engine.yaml", 1, false}} {
		code, out, _ := run("inspect", "--json", fixtures+c.file)
		if code != c.code {
			t.Fatalf("%s: exit %d", c.file, code)
		}
		var report plugins.Report
		if err := json.Unmarshal([]byte(out), &report); err != nil {
			t.Fatalf("%s: not JSON: %v\n%s", c.file, err, out)
		}
		if report.Valid != c.valid || report.EngineVersion != plugins.EngineVersion || report.PluginAPIVersion != plugins.PluginAPIVersion || report.Compatibility.Engine == nil {
			t.Fatalf("%s: report %+v", c.file, report)
		}
	}
	// Flags after the path are accepted too.
	if code, out, _ := run("inspect", fixtures+"valid/minimal.yaml", "--json"); code != 0 || !strings.HasPrefix(out, "{") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	for _, args := range [][]string{{}, {"inspect"}, {"inspect", "a", "b"}, {"unknown"}, {"inspect", "--yaml", "x"}} {
		if code, _, errOut := run(args...); code != 2 || !strings.Contains(errOut, "usage") {
			t.Errorf("%v: exit %d, stderr %q", args, code, errOut)
		}
	}
}
