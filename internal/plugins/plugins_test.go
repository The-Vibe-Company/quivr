package plugins_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

const fixtures = "../../contracts/plugins/v0/fixtures"

type fixtureCase struct {
	File        string   `json:"file"`
	Schema      string   `json:"schema"`
	Valid       bool     `json:"valid"`
	SchemaValid bool     `json:"schema_valid"`
	Errors      []string `json:"errors"`
}

func loadIndex(t *testing.T) []fixtureCase {
	t.Helper()
	var index struct {
		Cases []fixtureCase `json:"cases"`
	}
	b, err := os.ReadFile(filepath.Join(fixtures, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Cases) == 0 {
		t.Fatal("no normative fixtures")
	}
	return index.Cases
}

func codes(issues []plugins.Issue) []string {
	seen := map[string]bool{}
	for _, issue := range issues {
		seen[issue.Code] = true
	}
	out := []string{}
	for code := range seen {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}

func sorted(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

func TestNormativeFixtures(t *testing.T) {
	for _, c := range loadIndex(t) {
		t.Run(c.File, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(fixtures, c.File))
			if err != nil {
				t.Fatal(err)
			}
			var issues []plugins.Issue
			switch c.Schema {
			case "plugin-manifest.schema.json":
				report := plugins.Validate(raw)
				if report.Valid != c.Valid {
					t.Fatalf("valid = %v, want %v: %+v", report.Valid, c.Valid, report.Errors)
				}
				issues = report.Errors
			case "normalizer-response.schema.json":
				issues = plugins.ValidateNormalizerResponse(raw)
			default:
				issues = plugins.ValidateDocument(c.Schema, raw)
			}
			if (len(issues) == 0) != c.Valid {
				t.Fatalf("issues %+v, want valid=%v", issues, c.Valid)
			}
			if c.Errors != nil && !reflect.DeepEqual(codes(issues), sorted(c.Errors)) {
				t.Fatalf("codes %v, want %v: %+v", codes(issues), sorted(c.Errors), issues)
			}
			for _, issue := range issues {
				if issue.Message == "" || !strings.HasPrefix(issue.Path, "/") && issue.Path != "" {
					t.Fatalf("issue not actionable: %+v", issue)
				}
			}
		})
	}
}

func TestRangesMatchNormativeFixtures(t *testing.T) {
	var doc struct {
		Cases []struct {
			Range     string `json:"range"`
			Version   string `json:"version"`
			Satisfied bool   `json:"satisfied"`
		} `json:"cases"`
		Invalid []string `json:"invalid"`
	}
	b, err := os.ReadFile(filepath.Join(fixtures, "ranges.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	for _, c := range doc.Cases {
		r, err := plugins.ParseRange(c.Range)
		if err != nil {
			t.Fatalf("%q: %v", c.Range, err)
		}
		v, err := plugins.ParseVersion(c.Version)
		if err != nil {
			t.Fatalf("%q: %v", c.Version, err)
		}
		if r.Contains(v) != c.Satisfied {
			t.Errorf("%q contains %q = %v, want %v", c.Range, c.Version, !c.Satisfied, c.Satisfied)
		}
	}
	for _, raw := range doc.Invalid {
		if _, err := plugins.ParseRange(raw); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
}

func TestReportShowsEffectiveManifestAndVersions(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtures, "manifests/valid/minimal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	report := plugins.Validate(raw)
	sum := sha256.Sum256(raw)
	if report.ManifestDigest != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("digest %q", report.ManifestDigest)
	}
	if report.EngineVersion != plugins.EngineVersion || report.PluginAPIVersion != plugins.PluginAPIVersion || plugins.PluginAPIVersion != "0.1.0" {
		t.Fatalf("versions %q %q", report.EngineVersion, report.PluginAPIVersion)
	}
	n := report.Manifest.Contributions.Normalizer
	if n.TimeoutMS != 30000 || n.Retry.MaxAttempts != 3 || n.Limits.MaxResponseBytes != 4194304 || n.Limits.MaxParts != 256 {
		t.Fatalf("defaults not applied: %+v", n)
	}
	if !report.Compatibility.Engine.Compatible || !report.Compatibility.PluginAPI.Compatible {
		t.Fatalf("compatibility %+v", report.Compatibility)
	}
}

func TestIssuesAreActionable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtures, "manifests/invalid/reserved-contribution.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	report := plugins.Validate(raw)
	if len(report.Errors) != 1 {
		t.Fatalf("errors %+v", report.Errors)
	}
	issue := report.Errors[0]
	if issue.Path != "/contributions/enricher" || !strings.Contains(issue.Message, "reserved") || !strings.Contains(issue.Message, "normalizer") {
		t.Fatalf("issue %+v", issue)
	}
	dup, err := os.ReadFile(filepath.Join(fixtures, "responses/duplicate-part-keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	if issues := plugins.ValidateNormalizerResponse(dup); len(issues) != 1 || !strings.Contains(issues[0].Message, `duplicate Part key "page-1"`) {
		t.Fatalf("response issue not actionable: %+v", issues)
	}
	if got := plugins.Validate([]byte("id: [unclosed")); got.Valid || codes(got.Errors)[0] != "invalid_yaml" {
		t.Fatalf("yaml error: %+v", got.Errors)
	}
}

func TestUnparsableEngineVersionIsNeverCompatible(t *testing.T) {
	saved := plugins.EngineVersion
	defer func() { plugins.EngineVersion = saved }()
	plugins.EngineVersion = "dev"
	raw, err := os.ReadFile(filepath.Join(fixtures, "manifests/valid/minimal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	report := plugins.Validate(raw)
	if report.Valid || codes(report.Errors)[0] != plugins.CodeIncompatibleEngine || report.Compatibility.Engine.Compatible {
		t.Fatalf("report %+v", report)
	}
}
