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
	Request     string   `json:"request"`
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
			case "subscription-response.schema.json":
				request, err := os.ReadFile(filepath.Join(fixtures, c.Request))
				if err != nil {
					t.Fatalf("subscription response fixtures name their request: %v", err)
				}
				view, err := plugins.ViewSubscriptionRequest(request)
				if err != nil {
					t.Fatal(err)
				}
				issues = plugins.CheckSubscriptionOutput(raw, view, nil)
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
	if report.EngineVersion != plugins.EngineVersion || report.PluginAPIVersion != plugins.PluginAPIVersion || plugins.PluginAPIVersion != "0.2.0" {
		t.Fatalf("versions %q %q", report.EngineVersion, report.PluginAPIVersion)
	}
	n := report.Manifest.Contributions.Normalizer
	if n.TimeoutMS != 30000 || n.Retry.MaxAttempts != 3 || n.Limits.MaxResponseBytes != 4194304 || n.Limits.MaxParts != 256 {
		t.Fatalf("defaults not applied: %+v", n)
	}
	if !report.Compatibility.Engine.Compatible || !report.Compatibility.PluginAPI.Compatible {
		t.Fatalf("compatibility %+v", report.Compatibility)
	}
	// A plugin built for Plugin API 0.1 keeps working: the engine speaks 0.1.0 to it.
	if report.Compatibility.PluginAPI.Version != "0.1.0" {
		t.Fatalf("negotiated Plugin API %q, want 0.1.0", report.Compatibility.PluginAPI.Version)
	}
}

func TestSubscriptionManifestDefaultsAndNegotiation(t *testing.T) {
	raw := []byte(`id: rule
version: 0.1.0
compatibility: {engine: ">=0.1.0 <0.2.0", plugin_api: ">=0.1.0 <0.3.0"}
contributions:
  subscription:
    expression_schema: {type: object}
`)
	report := plugins.Validate(raw)
	if !report.Valid {
		t.Fatalf("errors %+v", report.Errors)
	}
	s := report.Manifest.Contributions.Subscription
	if s.MaxBatchSize != 32 || s.TimeoutMS != 30000 || s.Retry.MaxAttempts != 3 || s.Limits.MaxResponseBytes != 4194304 || s.ConfigurationSchema != nil {
		t.Fatalf("defaults not applied: %+v", s)
	}
	if report.Compatibility.PluginAPI.Version != "0.2.0" || strings.Join(report.Manifest.Contributions.Names(), ",") != "subscription" {
		t.Fatalf("negotiated %+v, contributions %v", report.Compatibility.PluginAPI, report.Manifest.Contributions.Names())
	}
	old, err := os.ReadFile(filepath.Join(fixtures, "manifests/invalid/subscription-plugin-api-0.1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got := plugins.Validate(old)
	if len(got.Errors) != 1 || got.Errors[0].Path != "/contributions/subscription" || !strings.Contains(got.Errors[0].Message, ">=0.2.0 <0.3.0") {
		t.Fatalf("issue not actionable: %+v", got.Errors)
	}
}

func TestValidateSubscriptionItem(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtures, "manifests/valid/subscription.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := plugins.Validate(raw).Manifest
	for _, c := range []struct {
		expression, configuration string
		want                      []string
	}{
		{`{"kind": "substring", "text": "strike"}`, `{}`, []string{}},
		{`{"kind": "any_of", "terms": ["a", "b"]}`, `{"case_sensitive": true}`, []string{}},
		{`{"kind": "substring"}`, `{}`, []string{"invalid_expression"}},
		{`{"kind": "regex", "text": "a+"}`, `{"case_sensitive": "yes"}`, []string{"invalid_expression", "invalid_subscription_configuration"}},
		{`["strike"]`, `{}`, []string{"invalid_expression"}},
		{`{"kind": "substring", "text": "x"}`, `{"unknown": 1}`, []string{"invalid_subscription_configuration"}},
	} {
		issues := plugins.ValidateSubscriptionItem(m, []byte(c.expression), []byte(c.configuration))
		if !reflect.DeepEqual(codes(issues), sorted(c.want)) {
			t.Errorf("%s %s: codes %v, want %v: %+v", c.expression, c.configuration, codes(issues), c.want, issues)
		}
		for _, issue := range issues {
			if !strings.HasPrefix(issue.Path, "/expression") && !strings.HasPrefix(issue.Path, "/configuration") {
				t.Errorf("issue path %q", issue.Path)
			}
		}
	}
	if issues := plugins.ValidateSubscriptionItem(plugins.Validate(mustRead(t, "manifests/valid/minimal.yaml")).Manifest, []byte(`{}`), []byte(`{}`)); len(issues) != 1 {
		t.Fatalf("normalizer-only manifest: %+v", issues)
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
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
