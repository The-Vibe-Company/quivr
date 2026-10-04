// Package plugincontract runs the Plugin Contract Runner (`quivr plugin test`)
// against the test plugins in this directory. Each subdirectory holds a
// quivr-plugin.yaml whose run.command starts the Go fake plugin in one mode
// (so no Python is needed) and an expect.json: either certified, or the check
// id and issue code the runner must report. Every JSON report is validated
// against contracts/plugins/v0/reports/contract-report.schema.json.
package plugincontract_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/cli"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost/fakeplugin"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestMain(m *testing.M) {
	fakeplugin.MaybeRun()
	os.Exit(m.Run())
}

type expectation struct {
	Certified bool   `json:"certified"`
	Check     string `json:"check"`
	Code      string `json:"code"`
}

type report struct {
	Certified bool                  `json:"certified"`
	Target    struct{ Mode string } `json:"target"`
	Checks    []struct {
		ID           string          `json:"id"`
		Contribution string          `json:"contribution"`
		Status       string          `json:"status"`
		Issues       []plugins.Issue `json:"issues"`
	} `json:"checks"`
}

// fakeOnPath exposes this test binary as quivr-fake-plugin, the command the
// test manifests declare.
func fakeOnPath(t *testing.T) {
	t.Helper()
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(self, filepath.Join(bin, "quivr-fake-plugin")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(fakeplugin.EnvEnable, "1")
}

func reportSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	schema, err := jsonschema.NewCompiler().Compile("../../contracts/plugins/v0/reports/contract-report.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func runTest(t *testing.T, args ...string) (int, string, report) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "report.json")
	var out, errOut bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	code := cli.RunContext(ctx, append([]string{"test", "--report", path}, args...), &out, &errOut)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no report (exit %d): %v\n%s%s", code, err, out.String(), errOut.String())
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := reportSchema(t).Validate(instance); err != nil {
		t.Fatalf("report violates contract-report.schema.json: %v\n%s", err, raw)
	}
	var r report
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return code, out.String(), r
}

func TestContractRunnerCertifiesOnlyWellBehavedPlugins(t *testing.T) {
	fakeOnPath(t)
	manifests, err := filepath.Glob(filepath.Join("*", plugins.ManifestFile))
	if err != nil || len(manifests) < 17 {
		t.Fatalf("test plugins: %v %v", manifests, err)
	}
	for _, manifest := range manifests {
		dir := filepath.Dir(manifest)
		t.Run(dir, func(t *testing.T) {
			var want expectation
			raw, err := os.ReadFile(filepath.Join(dir, "expect.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatal(err)
			}
			code, out, r := runTest(t, dir)
			if want.Certified {
				if code != cli.ExitOK || !r.Certified || !strings.Contains(out, "CERTIFIED") {
					t.Fatalf("want certified, exit %d:\n%s", code, out)
				}
				return
			}
			if code != cli.ExitInvalid || r.Certified || !strings.Contains(out, "NOT CERTIFIED") {
				t.Fatalf("want not certified with exit 1, got exit %d:\n%s", code, out)
			}
			for _, c := range r.Checks {
				if c.ID != want.Check || c.Status != "fail" {
					continue
				}
				for _, issue := range c.Issues {
					if issue.Code == want.Code && issue.Message != "" && strings.Contains(out, want.Code) {
						return
					}
				}
			}
			t.Fatalf("no failed %s check with issue %s:\n%s", want.Check, want.Code, out)
		})
	}
}

func TestContractRunnerCertifiesASubscriptionPlugin(t *testing.T) {
	fakeOnPath(t)
	code, out, r := runTest(t, "subscription-valid")
	if code != cli.ExitOK || !r.Certified {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	passed := map[string]int{}
	for _, c := range r.Checks {
		if c.Contribution == "normalizer" {
			t.Errorf("normalizer check %s on a subscription-only plugin", c.ID)
		}
		if c.Status == "pass" {
			passed[c.Contribution+"/"+c.ID]++
		}
	}
	for _, want := range []string{"subscription/fixtures", "subscription/invoke", "subscription/replay", "subscription/batch", "subscription/invalid_request", "/discovery"} {
		if passed[want] == 0 {
			t.Errorf("no passing %s check:\n%s", want, out)
		}
	}
	// Two unknown-field and non-JSON probes plus the normative invalid subscription requests.
	if passed["subscription/invalid_request"] < 5 {
		t.Errorf("invalid requests probed: %d\n%s", passed["subscription/invalid_request"], out)
	}
}

// TestContractRunnerJudgesConnectors owns the connector rules of the Contract
// Runner: the well-behaved static source in connector-valid is certified with
// every connector check passing, and each broken mode of the same fake plugin
// fails exactly the check that owns its rule.
func TestContractRunnerJudgesConnectors(t *testing.T) {
	fakeOnPath(t)
	code, out, r := runTest(t, "connector-valid")
	if code != cli.ExitOK || !r.Certified {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	passed := map[string]int{}
	for _, c := range r.Checks {
		if c.Status == "pass" {
			passed[c.Contribution+"/"+c.ID]++
		}
	}
	for _, want := range []string{"connector/fixtures", "connector/invoke", "connector/resume", "connector/check_credential", "connector/invalid_request", "connector/credentials", "connector/attachments", "connector/receive", "/discovery"} {
		if passed[want] == 0 {
			t.Errorf("no passing %s check:\n%s", want, out)
		}
	}
	for mode, want := range map[string]expectation{
		"connector-credential-leak":         {Check: "credentials", Code: "credential_leak"},
		"connector-stalled-checkpoint":      {Check: "invoke", Code: "stalled_checkpoint"},
		"connector-ignores-checkpoint":      {Check: "resume", Code: "checkpoint_not_honoured"},
		"connector-wrong-error-class":       {Check: "invoke", Code: "wrong_error_class"},
		"connector-blob-part":               {Check: "invoke", Code: "blob_part_not_allowed"},
		"connector-too-many-items":          {Check: "invoke", Code: "too_many_items"},
		"connector-attachment-mismatch":     {Check: "attachments", Code: "attachment_mismatch"},
		"connector-receive-invalid-verdict": {Check: "receive", Code: "invalid_verdict"},
		"accept-invalid":                    {Check: "invalid_request", Code: "accepted_invalid_request"},
	} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(fakeplugin.EnvMode, mode)
			code, out, r := runTest(t, "connector-valid")
			if code != cli.ExitInvalid || r.Certified {
				t.Fatalf("want not certified, exit %d:\n%s", code, out)
			}
			for _, c := range r.Checks {
				if c.ID == want.Check && c.Contribution == "connector" && c.Status == "fail" {
					for _, issue := range c.Issues {
						if issue.Code == want.Code {
							return
						}
					}
				}
			}
			t.Fatalf("no failed connector %s check with issue %s:\n%s", want.Check, want.Code, out)
		})
	}
}

// TestContractRunnerJudgesIngestion owns the ingestion rules of the Contract
// Runner: the well-behaved plugin in ingestion-valid is certified with every
// ingestion check passing, and each broken mode of the same fake plugin fails
// exactly the check that owns its rule.
func TestContractRunnerJudgesIngestion(t *testing.T) {
	fakeOnPath(t)
	t.Setenv("QUIVR_TEST_EMBEDDER_KEY", "embedder-test-key-0123")
	code, out, r := runTest(t, "ingestion-valid")
	if code != cli.ExitOK || !r.Certified {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	passed := map[string]int{}
	for _, c := range r.Checks {
		if c.Status == "pass" {
			passed[c.Contribution+"/"+c.ID]++
		}
	}
	// The normative article and the plugin's own memo; two spaces for the article, one for the memo.
	for want, n := range map[string]int{"ingestion/fixtures": 1, "ingestion/invoke": 2, "ingestion/replay": 2, "ingestion/segments_only": 2, "ingestion/embed_query": 3, "ingestion/credentials": 1, "/discovery": 1} {
		if passed[want] < n {
			t.Errorf("%d passing %s checks, want %d:\n%s", passed[want], want, n, out)
		}
	}
	// Non-JSON, unknown field and undeclared space on both routes, plus the normative invalid requests.
	if passed["ingestion/invalid_request"] < 9 {
		t.Errorf("invalid requests probed: %d\n%s", passed["ingestion/invalid_request"], out)
	}
	for mode, want := range map[string]expectation{
		"ingestion-offset":                 {Check: "invoke", Code: "offset_out_of_range"},
		"ingestion-dimensions":             {Check: "invoke", Code: "dimension_mismatch"},
		"ingestion-missing-vector":         {Check: "invoke", Code: "missing_vector"},
		"ingestion-split":                  {Check: "invoke", Code: "unexpected_segments"},
		"ingestion-nondeterministic":       {Check: "replay", Code: "nondeterministic_output"},
		"ingestion-segments-only-split":    {Check: "segments_only", Code: "nondeterministic_output"},
		"ingestion-query-dimensions":       {Check: "embed_query", Code: "dimension_mismatch"},
		"ingestion-query-nondeterministic": {Check: "embed_query", Code: "nondeterministic_output"},
		"ingestion-secret-leak":            {Check: "credentials", Code: "credential_leak"},
		"accept-invalid":                   {Check: "invalid_request", Code: "accepted_invalid_request"},
	} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(fakeplugin.EnvMode, mode)
			code, out, r := runTest(t, "ingestion-valid")
			if code != cli.ExitInvalid || r.Certified {
				t.Fatalf("want not certified, exit %d:\n%s", code, out)
			}
			for _, c := range r.Checks {
				if c.ID == want.Check && c.Contribution == "ingestion" && c.Status == "fail" {
					for _, issue := range c.Issues {
						if issue.Code == want.Code {
							return
						}
					}
				}
			}
			t.Fatalf("no failed ingestion %s check with issue %s:\n%s", want.Check, want.Code, out)
		})
	}
}

func TestContractRunnerReportsABrokenSubscriptionFixture(t *testing.T) {
	fakeOnPath(t)
	bad := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(bad, []byte(`{"record": `), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, r := runTest(t, "--fixture", bad, "subscription-valid")
	if code != cli.ExitInvalid || r.Certified || strings.Contains(out, "declares no normalizer") || !strings.Contains(out, "not JSON") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestContractRunnerTargetsARunningEndpoint(t *testing.T) {
	fakeOnPath(t)
	dir := "valid"
	proc, err := devhost.Start(devhost.Options{Dir: dir, Command: []string{"quivr-fake-plugin", "ok"}, Manifest: filepath.Join(dir, plugins.ManifestFile)})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Stop(5 * time.Second)
	code, out, r := runTest(t, "--endpoint", proc.BaseURL+"/", dir)
	if code != cli.ExitOK || !r.Certified || r.Target.Mode != "endpoint" {
		t.Fatalf("exit %d mode %q:\n%s", code, r.Target.Mode, out)
	}
	// A dead endpoint is reported, not certified.
	port, err := devhost.FreePort("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	code, out, _ = runTest(t, "--startup-timeout", "300ms", "--endpoint", "http://127.0.0.1:"+strconv.Itoa(port), dir)
	if code != cli.ExitInvalid || !strings.Contains(out, "unhealthy") {
		t.Fatalf("dead endpoint: exit %d:\n%s", code, out)
	}
}

func TestContractRunnerUsage(t *testing.T) {
	for _, args := range [][]string{{"test", "--endpoint"}, {"test", "--endpoint", "ftp://x"}, {"test", "--bogus"}, {"test", "a", "b"}, {"test", "--startup-timeout", "soon"}} {
		var out, errOut bytes.Buffer
		if code := cli.RunContext(context.Background(), args, &out, &errOut); code != cli.ExitUsage || !strings.Contains(errOut.String(), "usage: quivr plugin test") {
			t.Errorf("%v: exit %d %s", args, code, errOut.String())
		}
	}
}

// TestContractRunnerJudgesRetrieval owns the retrieval rules of the Contract
// Runner: the well-behaved plugin in retrieval-valid is certified offline,
// candidates served from fixtures, and each broken mode of the same fake
// plugin fails exactly the check that owns its rule.
func TestContractRunnerJudgesRetrieval(t *testing.T) {
	fakeOnPath(t)
	t.Setenv("QUIVR_TEST_RERANKER_KEY", "reranker-test-key-0123")
	code, out, r := runTest(t, "retrieval-valid")
	if code != cli.ExitOK || !r.Certified {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	passed := map[string]int{}
	for _, c := range r.Checks {
		if c.Status == "pass" {
			passed[c.Contribution+"/"+c.ID]++
		}
	}
	// The normative newsroom and the plugin's own wire fixture, each under two profiles.
	for want, n := range map[string]int{"retrieval/fixtures": 1, "retrieval/invoke": 4, "retrieval/replay": 2, "retrieval/invalid_request": 5, "retrieval/credentials": 1} {
		if passed[want] < n {
			t.Errorf("%d passing %s checks, want %d:\n%s", passed[want], want, n, out)
		}
	}
	for mode, want := range map[string]expectation{
		"retrieval-unserved":          {Check: "invoke", Code: "unserved_candidate"},
		"retrieval-endless":           {Check: "invoke", Code: "too_many_rounds"},
		"retrieval-too-many-requests": {Check: "invoke", Code: "too_many_requests"},
		"retrieval-over-budget":       {Check: "invoke", Code: "over_budget"},
		"retrieval-slow":              {Check: "invoke", Code: "deadline_exceeded"},
		"retrieval-nondeterministic":  {Check: "replay", Code: "nondeterministic_output"},
		"retrieval-secret-leak":       {Check: "credentials", Code: "credential_leak"},
		"accept-invalid":              {Check: "invalid_request", Code: "accepted_invalid_request"},
	} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(fakeplugin.EnvMode, mode)
			code, out, r := runTest(t, "retrieval-valid")
			if code != cli.ExitInvalid || r.Certified {
				t.Fatalf("want not certified, exit %d:\n%s", code, out)
			}
			for _, c := range r.Checks {
				if c.ID == want.Check && c.Contribution == "retrieval" && c.Status == "fail" {
					for _, issue := range c.Issues {
						if issue.Code == want.Code {
							return
						}
					}
				}
			}
			t.Fatalf("no failed retrieval %s check with issue %s:\n%s", want.Check, want.Code, out)
		})
	}
}
