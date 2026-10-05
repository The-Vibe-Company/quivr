// Package runner is the Plugin Contract Runner behind `quivr plugin test`. It
// launches a plugin through its manifest's run command (or targets a running
// endpoint), talks only the public Plugin Protocol v0, and judges every answer
// with the engine's own validation (package plugins, content.CheckManifest), so
// a certified plugin is one the engine accepts. It imports no plugin or SDK
// code.
package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/contracts"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost"
)

// ReportVersion identifies the JSON report shape described by
// contracts/plugins/v0/reports/contract-report.schema.json.
const ReportVersion = "1"

// Check identifiers, in report order.
const (
	CheckManifest       = "manifest"
	CheckCompatibility  = "compatibility"
	CheckHealth         = "health"
	CheckDiscovery      = "discovery"
	CheckFixtures       = "fixtures"
	CheckInvoke         = "invoke"
	CheckReplay         = "replay"
	CheckInvalidRequest = "invalid_request"
	CheckBatch          = "batch"
	CheckResume         = "resume"
	CheckCredential     = "check_credential"
	CheckCredentials    = "credentials"
)

// Contributions a check can concern.
const (
	ContributionNormalizer   = "normalizer"
	ContributionSubscription = "subscription"
	ContributionConnector    = "connector"
)

// Issue codes the runner adds to those of packages plugins and devhost.
const (
	CodeNoRunCommand       = "no_run_command"
	CodeUnhealthy          = "unhealthy"
	CodeNoFixture          = "no_fixture"
	CodeInvalidFixture     = "invalid_fixture"
	CodeDeadlineExceeded   = "deadline_exceeded"
	CodeUnavailable        = "plugin_unavailable"
	CodeUnexpectedError    = "unexpected_error"
	CodeNondeterministic   = "nondeterministic_output"
	CodeAcceptedInvalid    = "accepted_invalid_request"
	CodeWrongErrorClass    = "wrong_error_class"
	CodeBatchDependent     = "batch_dependent_decision"
	CodeUnexpectedDecision = devhost.CodeUnexpectedDecision
	CodeUnexpectedItems    = "unexpected_items"
	CodeStalledCheckpoint  = "stalled_checkpoint"
	CodeCheckpointIgnored  = "checkpoint_not_honoured"
	CodeCredentialLeak     = "credential_leak"
	normativeFixturePrefix = "contracts:"
)

// Status of one check.
type Status string

const (
	Pass Status = "pass"
	Fail Status = "fail"
	Skip Status = "skip"
)

// Check is one verdict in the report.
type Check struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Contribution is the Contribution the check exercises; empty for
	// checks of the whole plugin.
	Contribution string          `json:"contribution,omitempty"`
	Status       Status          `json:"status"`
	Fixture      string          `json:"fixture,omitempty"`
	DurationMS   int64           `json:"duration_ms"`
	Note         string          `json:"note,omitempty"`
	Issues       []plugins.Issue `json:"issues"`
}

// Plugin identifies the certified plugin.
type Plugin struct {
	ID             string `json:"id,omitempty"`
	Version        string `json:"version,omitempty"`
	ManifestPath   string `json:"manifest_path"`
	ManifestDigest string `json:"manifest_digest,omitempty"`
}

// Target is how the runner reached the plugin.
type Target struct {
	Mode    string `json:"mode"` // "launch" or "endpoint"
	BaseURL string `json:"base_url,omitempty"`
}

// Summary counts checks by status.
type Summary struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

// Report is the compatibility report of one run.
type Report struct {
	ReportVersion    string                      `json:"report_version"`
	Certified        bool                        `json:"certified"`
	Plugin           Plugin                      `json:"plugin"`
	Target           Target                      `json:"target"`
	EngineVersion    string                      `json:"engine_version"`
	PluginAPIVersion string                      `json:"plugin_api_version"`
	Compatibility    plugins.CompatibilityReport `json:"compatibility"`
	Checks           []Check                     `json:"checks"`
	Summary          Summary                     `json:"summary"`
}

// Options configure a run.
type Options struct {
	// Dir is the plugin directory or its quivr-plugin.yaml.
	Dir string
	// Endpoint targets an already running plugin instead of launching one.
	Endpoint string
	// Fixtures replace the plugin's own fixtures (<dir>/fixtures/*.json).
	Fixtures []string
	// StartupTimeout bounds the wait for GET /v0/health; default 30s.
	StartupTimeout time.Duration
	// Output receives the launched plugin's stdout and stderr; nil discards.
	Output io.Writer
	// Configuration, when set, is the installation's plugin configuration:
	// the normative ingestion and retrieval fixtures run with it instead of
	// their own, as the engine calls a registered plugin (Spec 5).
	Configuration json.RawMessage
}

// configured returns a normative fixture whose section runs with the
// installation's configuration, when the run has one.
func (r *run) configured(raw []byte, section string) []byte {
	if len(r.opts.Configuration) == 0 {
		return raw
	}
	var doc map[string]json.RawMessage
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil || json.Unmarshal(doc[section], &body) != nil || body == nil {
		return raw
	}
	body["configuration"] = r.opts.Configuration
	doc[section], _ = json.Marshal(body)
	out, err := json.Marshal(doc)
	if err != nil {
		return raw
	}
	return out
}

type run struct {
	opts    Options
	report  Report
	m       *plugins.Manifest
	api     plugins.API
	baseURL string
	// pluginAPI is the Plugin API version discovery serves.
	pluginAPI string
	tmp       string // extracted normative fixtures
	// logs captures the launched plugin's output for the credential check.
	logs *lockedBuffer
	// seen collects every connector answer body for the credential check.
	seen    [][]byte
	signing *plugins.SigningKeys
}

func (r *run) add(c Check, started time.Time) {
	c.DurationMS = time.Since(started).Milliseconds()
	if c.Issues == nil {
		c.Issues = []plugins.Issue{}
	}
	if c.Status == "" {
		c.Status = Pass
		if len(c.Issues) > 0 {
			c.Status = Fail
		}
	}
	r.report.Checks = append(r.report.Checks, c)
}

// Run certifies one plugin and returns its report. The report is complete even
// when an early check fails; later checks are then absent or skipped.
func Run(ctx context.Context, opts Options) Report {
	if opts.Dir == "" {
		opts.Dir = "."
	}
	if opts.StartupTimeout <= 0 {
		opts.StartupTimeout = 30 * time.Second
	}
	r := &run{opts: opts}
	r.report = Report{ReportVersion: ReportVersion, Target: Target{Mode: "launch"},
		EngineVersion: plugins.EngineVersion, PluginAPIVersion: plugins.PluginAPIVersion, Checks: []Check{}}
	if opts.Endpoint != "" {
		r.report.Target = Target{Mode: "endpoint", BaseURL: strings.TrimRight(opts.Endpoint, "/")}
	}
	r.execute(ctx)
	for _, c := range r.report.Checks {
		switch c.Status {
		case Pass:
			r.report.Summary.Passed++
		case Fail:
			r.report.Summary.Failed++
		case Skip:
			r.report.Summary.Skipped++
		}
	}
	r.report.Certified = r.report.Summary.Failed == 0 && r.report.Summary.Passed > 0
	return r.report
}

func (r *run) execute(ctx context.Context) {
	if !r.inspect() {
		return
	}
	if r.api.Speaks(plugins.FeatureSignedCalls) {
		ring, err := plugins.NewSigningKeys()
		if r.opts.Endpoint != "" {
			ring, err = plugins.EngineSigningKeys(r.m.ID)
		}
		if err != nil {
			r.add(Check{ID: "authentication", Title: "engine signing keys are available", Issues: []plugins.Issue{{Code: "invalid_engine_token", Message: plugins.ErrSigningKeys.Error()}}}, time.Now())
			return
		}
		r.signing = &ring
		ctx = plugins.WithRequestSigning(ctx, r.m.ID, ring)
	}
	defer func() {
		if r.tmp != "" {
			_ = os.RemoveAll(r.tmp)
		}
	}()
	proc, ok := r.reach(ctx)
	if proc != nil {
		defer proc.Stop(5 * time.Second)
	}
	if !ok {
		return
	}
	started := time.Now()
	served, issues, err := devhost.Discover(ctx, r.baseURL, plugins.Report{Path: r.report.Plugin.ManifestPath, ManifestDigest: r.report.Plugin.ManifestDigest, Manifest: r.m})
	if err != nil {
		issues = append(issues, plugins.Issue{Code: CodeUnavailable, Path: "/v0/discovery", Message: err.Error()})
	}
	r.pluginAPI = served
	r.add(Check{ID: CheckDiscovery, Title: "GET /v0/discovery matches quivr-plugin.yaml", Issues: issues}, started)
	if r.signing != nil {
		r.authentication(ctx)
	}

	own := r.ownFixtures()
	if r.m.Contributions.Normalizer != nil {
		requests := r.fixtures(own)
		for _, req := range requests {
			r.invoke(ctx, req)
		}
		if len(requests) > 0 {
			r.invalidRequests(ctx, requests[0].body)
		}
	}
	if r.m.Contributions.Subscription != nil {
		batches := r.subscriptionFixtures(own)
		for _, b := range batches {
			r.invokeSubscription(ctx, b)
		}
		if len(batches) > 0 {
			r.invalidSubscriptionRequests(ctx, batches[0].body)
		}
	}
	if r.m.Contributions.Connector != nil {
		r.connector(ctx, own)
	}
	if r.m.Contributions.Ingestion != nil {
		r.ingestion(ctx, own)
	}
	if r.m.Contributions.Retrieval != nil {
		r.retrieval(ctx, own)
	}
}

type ownFixture struct {
	label, path string
	// contribution is the Contribution the fixture exercises: normalizer
	// (an invocation fixture), subscription or connector.
	contribution string
}

// declared reports whether the manifest declares a Contribution.
func (r *run) declared(contribution string) bool {
	return slices.Contains(r.m.Contributions.Names(), contribution)
}

// foreignFixture reports a fixture for a Contribution the manifest does not
// declare.
func foreignFixture(f ownFixture) plugins.Issue {
	kind := map[string]string{ContributionNormalizer: "an invocation fixture"}[f.contribution]
	if kind == "" {
		kind = "a " + f.contribution + " fixture"
	}
	return plugins.Issue{Code: CodeInvalidFixture, Path: "/contributions",
		Message: fmt.Sprintf("%s: %s, but the manifest declares no %s Contribution", f.label, kind, f.contribution)}
}

// ownFixtures lists the plugin's own fixture files (<dir>/fixtures/*.json or
// --fixture), each classified as a subscription, connector or invocation
// fixture.
func (r *run) ownFixtures() []ownFixture {
	files := r.opts.Fixtures
	if len(files) == 0 {
		files, _ = filepath.Glob(filepath.Join(filepath.Dir(r.report.Plugin.ManifestPath), "fixtures", "*.json"))
		sort.Strings(files)
	}
	base := filepath.Dir(r.report.Plugin.ManifestPath)
	var out []ownFixture
	for _, file := range files {
		label := file
		if rel, err := filepath.Rel(base, file); err == nil && !strings.HasPrefix(rel, "..") {
			label = filepath.ToSlash(rel)
		}
		raw, err := os.ReadFile(file)
		contribution := ContributionNormalizer
		switch {
		case err != nil || !json.Valid(raw):
			// Unreadable or not JSON: let a declared Contribution report the real error.
			contribution = r.m.Contributions.Names()[0]
		case devhost.IsSubscriptionFixture(raw):
			contribution = ContributionSubscription
		case devhost.IsConnectorFixture(raw):
			contribution = ContributionConnector
		case devhost.IsIngestionFixture(raw):
			contribution = ContributionIngestion
		case devhost.IsRetrievalFixture(raw):
			contribution = ContributionRetrieval
		}
		out = append(out, ownFixture{label: label, path: file, contribution: contribution})
	}
	return out
}

// inspect runs the manifest and compatibility checks with the engine's
// manifest validation.
func (r *run) inspect() bool {
	started := time.Now()
	inspected := plugins.Inspect(r.opts.Dir)
	r.report.Plugin.ManifestPath = inspected.Path
	r.report.Plugin.ManifestDigest = inspected.ManifestDigest
	r.report.Compatibility = inspected.Compatibility
	var manifestIssues, compatIssues []plugins.Issue
	for _, issue := range inspected.Errors {
		if issue.Code == plugins.CodeIncompatibleEngine || issue.Code == plugins.CodeIncompatiblePluginAPI {
			compatIssues = append(compatIssues, issue)
		} else {
			manifestIssues = append(manifestIssues, issue)
		}
	}
	if m := inspected.Manifest; m != nil {
		r.report.Plugin.ID, r.report.Plugin.Version = m.ID, m.Version
	}
	r.add(Check{ID: CheckManifest, Title: "quivr-plugin.yaml is valid (quivr plugin inspect)", Issues: manifestIssues}, started)
	compat := Check{ID: CheckCompatibility, Issues: compatIssues,
		Title: fmt.Sprintf("engine %s and Plugin API %s satisfy the declared ranges", plugins.EngineVersion, plugins.PluginAPIVersion)}
	if inspected.Compatibility.Engine == nil || inspected.Compatibility.PluginAPI == nil {
		if len(compatIssues) == 0 {
			compat.Status = Skip
			compat.Note = "a compatibility range is missing or invalid; see the manifest check"
		}
	}
	r.add(compat, started)
	if len(manifestIssues) > 0 || len(compatIssues) > 0 || inspected.Manifest == nil {
		return false
	}
	r.m = inspected.Manifest
	r.api = plugins.ResolveAPI((&plugins.Pin{Manifest: *r.m}).PluginAPI())
	return true
}

// reach launches the plugin (or uses the endpoint) and waits for health.
func (r *run) reach(ctx context.Context) (*devhost.Process, bool) {
	started := time.Now()
	check := Check{ID: CheckHealth, Title: "GET /v0/health answers 200"}
	var proc *devhost.Process
	healthCtx, cancel := context.WithTimeout(ctx, r.opts.StartupTimeout)
	defer cancel()
	var err error
	if r.opts.Endpoint != "" {
		r.baseURL = r.report.Target.BaseURL
		err = devhost.WaitHealthyAt(healthCtx, r.baseURL)
	} else if r.m.Run == nil || len(r.m.Run.Command) == 0 {
		check.Issues = []plugins.Issue{{Code: CodeNoRunCommand, Path: "/run/command",
			Message: "the manifest declares no run.command; add one or pass --endpoint <url> of a running plugin"}}
		r.add(check, started)
		return nil, false
	} else {
		dir := filepath.Dir(r.report.Plugin.ManifestPath)
		output := r.opts.Output
		if r.m.Contributions.Connector != nil || r.m.Contributions.Ingestion != nil || r.m.Contributions.Retrieval != nil {
			r.logs = &lockedBuffer{}
			if output == nil {
				output = r.logs
			} else {
				output = io.MultiWriter(output, r.logs)
			}
		}
		var env []string
		if r.signing != nil {
			raw, _ := json.Marshal(r.signing)
			env = []string{plugins.EnvPluginSigningKeys + "=" + string(raw)}
		}
		proc, err = devhost.Start(devhost.Options{Dir: dir, Command: r.m.Run.Command, Manifest: r.report.Plugin.ManifestPath, Output: output, Env: env})
		if err == nil {
			r.baseURL = proc.BaseURL
			r.report.Target.BaseURL = proc.BaseURL
			check.Note = "launched " + strings.Join(r.m.Run.Command, " ")
			err = proc.WaitHealthy(healthCtx)
		}
	}
	if err != nil {
		check.Issues = []plugins.Issue{{Code: CodeUnhealthy, Path: "/v0/health", Message: err.Error()}}
	}
	r.add(check, started)
	return proc, err == nil
}

type fixtureRequest struct {
	label string
	body  []byte
}

// fixtures builds requests from the normative invocation fixtures that apply
// to the plugin's media types and configuration, plus the plugin's own.
func (r *run) fixtures(own []ownFixture) []fixtureRequest {
	started := time.Now()
	check := Check{ID: CheckFixtures, Contribution: ContributionNormalizer, Title: "invocation fixtures build valid requests"}
	var out []fixtureRequest
	var notes []string
	declared := map[string]bool{}
	for _, mt := range r.m.Contributions.Normalizer.MediaTypes {
		declared[mt] = true
	}
	// The normative inputs must outlive every invocation: requests reference
	// them through file:// URLs. execute removes the directory.
	tmp, err := os.MkdirTemp("", "quivr-plugin-test-")
	if err == nil {
		r.tmp = tmp
		err = os.CopyFS(tmp, contracts.PluginFixtures())
	}
	if err != nil {
		check.Issues = append(check.Issues, plugins.Issue{Code: CodeInvalidFixture, Message: "extract normative fixtures: " + err.Error()})
	}
	normative, _ := fs.Glob(contracts.PluginFixtures(), "invocations/*.json")
	for _, name := range normative {
		label := normativeFixturePrefix + name
		file := filepath.Join(tmp, filepath.FromSlash(name))
		raw, err := os.ReadFile(file)
		if err != nil || len(plugins.ValidateDocument("plugin-fixture.schema.json", raw)) > 0 {
			continue // negative fixtures of the contract itself
		}
		var f struct {
			Input struct {
				MediaType string `json:"media_type"`
			} `json:"input"`
		}
		_ = json.Unmarshal(raw, &f)
		if !declared[f.Input.MediaType] {
			continue
		}
		body, issues, err := devhost.BuildFixtureRequest(file, r.m)
		switch {
		case err != nil:
			notes = append(notes, fmt.Sprintf("%s skipped: %v", label, err))
		case len(issues) > 0:
			notes = append(notes, fmt.Sprintf("%s skipped: the plugin configuration schema rejects its configuration (%s)", label, issues[0].Message))
		default:
			out = append(out, fixtureRequest{label: label, body: body})
		}
	}
	for _, f := range own {
		label, file := f.label, f.path
		if f.contribution != ContributionNormalizer {
			if !r.declared(f.contribution) {
				check.Issues = append(check.Issues, foreignFixture(f))
			}
			continue
		}
		body, issues, err := devhost.BuildFixtureRequest(file, r.m)
		if err != nil {
			issues = []plugins.Issue{{Code: CodeInvalidFixture, Message: err.Error()}}
		}
		if len(issues) > 0 {
			for _, issue := range issues {
				issue.Message = label + ": " + issue.Message
				check.Issues = append(check.Issues, issue)
			}
			continue
		}
		out = append(out, fixtureRequest{label: label, body: body})
	}
	if len(out) == 0 && len(check.Issues) == 0 {
		check.Issues = []plugins.Issue{{Code: CodeNoFixture, Path: "/fixtures",
			Message: fmt.Sprintf("no invocation fixture exercises the normalizer: no normative fixture matches the declared media types %v; add fixtures/<name>.json (contracts/plugins/v0/plugin-fixture.schema.json)", r.m.Contributions.Normalizer.MediaTypes)}}
	}
	labels := make([]string, 0, len(out))
	for _, req := range out {
		labels = append(labels, req.label)
	}
	check.Note = strings.Join(append([]string{"using " + orNone(labels)}, notes...), "; ")
	r.add(check, started)
	return out
}

func orNone(labels []string) string {
	if len(labels) == 0 {
		return "no fixture"
	}
	return strings.Join(labels, ", ")
}

func (r *run) timeout() time.Duration {
	return time.Duration(r.m.Contributions.Normalizer.TimeoutMS) * time.Millisecond
}

// deadlineIssue reports an invocation abandoned at the declared deadline, or
// nil when callCtx did not expire on its own.
func deadlineIssue(ctx, callCtx context.Context, contribution string, timeoutMS int) *plugins.Issue {
	if errors.Is(callCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return &plugins.Issue{Code: CodeDeadlineExceeded, Path: "/contributions/" + contribution + "/timeout_ms",
			Message: fmt.Sprintf("no complete answer within the declared timeout_ms %d; the engine abandons the invocation at this deadline", timeoutMS)}
	}
	return nil
}

// call posts one request within the declared deadline and judges a 200 body
// with plugins.CheckNormalizerOutput for that request's input Blob.
func (r *run) call(ctx context.Context, body []byte) (*devhost.Result, *plugins.Issue) {
	callCtx, cancel := context.WithTimeout(ctx, r.timeout())
	defer cancel()
	input, _ := plugins.InputFromRequest(body)
	oc := plugins.OutputContext{Manifest: r.m, Input: input, VerifyBlob: verifyInput(input, body)}
	result, err := devhost.InvokeNormalizerWith(callCtx, r.baseURL, body, plugins.MaxResponseBytes(r.m), func(b []byte) []plugins.Issue {
		return plugins.CheckNormalizerOutput(ctx, b, oc)
	})
	if err == nil {
		return result, nil
	}
	if issue := deadlineIssue(ctx, callCtx, ContributionNormalizer, r.m.Contributions.Normalizer.TimeoutMS); issue != nil {
		return nil, issue
	}
	return nil, &plugins.Issue{Code: CodeUnavailable, Message: err.Error()}
}

// verifyInput resolves the input Blob the way the engine's Blob store would:
// a file reference is re-read and hashed, so a Blob Part is verified against
// the bytes actually served, not against the checksum the request claims.
func verifyInput(input plugins.InputBlob, request []byte) func(context.Context, string) (plugins.VerifiedBlob, error) {
	return func(_ context.Context, id string) (plugins.VerifiedBlob, error) {
		if id != input.BlobID {
			return plugins.VerifiedBlob{}, fmt.Errorf("unknown Blob %s", id)
		}
		var r struct {
			Input struct {
				Reference struct {
					Kind string `json:"kind"`
					URL  string `json:"url"`
				} `json:"reference"`
			} `json:"input"`
		}
		_ = json.Unmarshal(request, &r)
		verified := plugins.VerifiedBlob{ID: id, MediaType: input.MediaType, SHA256: input.SHA256}
		if r.Input.Reference.Kind != "file" {
			return verified, nil
		}
		u, err := url.Parse(r.Input.Reference.URL)
		if err != nil {
			return plugins.VerifiedBlob{}, err
		}
		data, err := os.ReadFile(filepath.FromSlash(u.Path))
		if err != nil {
			return plugins.VerifiedBlob{}, err
		}
		sum := sha256.Sum256(data)
		verified.SHA256 = hex.EncodeToString(sum[:])
		return verified, nil
	}
}

func (r *run) invoke(ctx context.Context, req fixtureRequest) {
	started := time.Now()
	check := Check{ID: CheckInvoke, Contribution: ContributionNormalizer, Fixture: req.label,
		Title: fmt.Sprintf("valid invocation within %d ms returns output the engine accepts", r.m.Contributions.Normalizer.TimeoutMS)}
	first, problem := r.call(ctx, req.body)
	check.Issues = judgeSuccess(first, problem)
	r.add(check, started)
	replay := Check{ID: CheckReplay, Contribution: ContributionNormalizer, Fixture: req.label, Title: "replaying the idempotency key returns the same logical output"}
	if len(check.Issues) > 0 {
		replay.Status = Skip
		replay.Note = "the first invocation failed"
		r.add(replay, time.Now())
		return
	}
	started = time.Now()
	var request map[string]any
	_ = json.Unmarshal(req.body, &request)
	request["invocation_id"] = fmt.Sprint(request["invocation_id"], "-replay")
	body, _ := json.Marshal(request)
	second, problem := r.call(ctx, body)
	replay.Issues = judgeSuccess(second, problem)
	if len(replay.Issues) == 0 {
		if diff := logicalDifference(first.Body, second.Body); diff != "" {
			replay.Issues = []plugins.Issue{{Code: CodeNondeterministic, Path: diff,
				Message: fmt.Sprintf("two invocations with idempotency key %v returned different logical output at %s; the same key must yield the same Manifest, extensions and language", request["idempotency_key"], diff)}}
		}
	}
	r.add(replay, started)
}

func judgeSuccess(result *devhost.Result, problem *plugins.Issue) []plugins.Issue {
	switch {
	case problem != nil:
		return []plugins.Issue{*problem}
	case len(result.Issues) > 0:
		return result.Issues
	case result.Error != nil:
		class := "terminal"
		if result.Error.Retryable {
			class = "retryable"
		}
		return []plugins.Issue{{Code: CodeUnexpectedError,
			Message: fmt.Sprintf("a valid fixture was answered HTTP %d with a %s error %s: %s", result.Status, class, result.Error.Code, result.Error.Message)}}
	case result.Status != 200:
		return []plugins.Issue{{Code: CodeUnexpectedError, Message: fmt.Sprintf("a valid fixture was answered HTTP %d; the protocol answers 200", result.Status)}}
	}
	return nil
}

// logicalDifference returns the JSON Pointer of the first difference between
// the logical outputs (manifest, extensions, language) of two responses, or "".
func logicalDifference(a, b []byte) string {
	pick := func(raw []byte) any {
		var doc map[string]any
		_ = json.Unmarshal(raw, &doc)
		return map[string]any{"manifest": doc["manifest"], "extensions": doc["extensions"], "language": doc["language"]}
	}
	return firstDifference(pick(a), pick(b), "")
}

func firstDifference(a, b any, at string) string {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			return orRoot(at)
		}
		keys := map[string]bool{}
		for k := range av {
			keys[k] = true
		}
		for k := range bv {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			if d := firstDifference(av[k], bv[k], at+"/"+strings.NewReplacer("~", "~0", "/", "~1").Replace(k)); d != "" {
				return d
			}
		}
		return ""
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return orRoot(at)
		}
		for i := range av {
			if d := firstDifference(av[i], bv[i], fmt.Sprintf("%s/%d", at, i)); d != "" {
				return d
			}
		}
		return ""
	}
	if !reflect.DeepEqual(a, b) {
		return orRoot(at)
	}
	return ""
}

func orRoot(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

type invalidCase struct {
	label, reason string
	body          []byte
}

// invalidRequests sends requests the protocol schema rejects. Each must be
// refused with the error envelope and retryable false: retrying an invalid
// request cannot succeed.
func (r *run) invalidRequests(ctx context.Context, valid []byte) {
	var withUnknown map[string]any
	_ = json.Unmarshal(valid, &withUnknown)
	withUnknown["unexpected_field"] = true
	unknown, _ := json.Marshal(withUnknown)
	cases := []invalidCase{
		{label: "non-json-body", reason: "the body is not JSON", body: []byte(`{"invocation_id": `)},
		{label: "unknown-field", reason: "the request carries an unknown top-level field", body: unknown},
	}
	names, _ := fs.Glob(contracts.PluginFixtures(), "requests/*.json")
	for _, name := range names {
		raw, err := fs.ReadFile(contracts.PluginFixtures(), name)
		if err != nil || len(plugins.ValidateDocument("normalizer-request.schema.json", raw)) == 0 {
			continue // only the contract's invalid requests
		}
		cases = append(cases, invalidCase{label: normativeFixturePrefix + name, reason: "the normative fixture violates normalizer-request.schema.json", body: raw})
	}
	for _, c := range cases {
		started := time.Now()
		check := Check{ID: CheckInvalidRequest, Contribution: ContributionNormalizer, Fixture: c.label,
			Title: "an invalid request is refused with a terminal error envelope (" + c.reason + ")"}
		result, problem := r.call(ctx, c.body)
		check.Issues = judgeRefusal(result, problem, c.reason)
		r.add(check, started)
	}
}

// judgeRefusal requires an invalid request to be refused with the error
// envelope and retryable false.
func judgeRefusal(result *devhost.Result, problem *plugins.Issue, reason string) []plugins.Issue {
	switch {
	case problem != nil:
		return []plugins.Issue{*problem}
	case result.Status >= 200 && result.Status < 300, result.Error == nil && len(result.Issues) == 0:
		return []plugins.Issue{{Code: CodeAcceptedInvalid, Message: fmt.Sprintf("HTTP %d for an invalid request (%s); answer 4xx with the error envelope and retryable false", result.Status, reason)}}
	case len(result.Issues) > 0:
		return result.Issues
	case result.Error.Retryable:
		return []plugins.Issue{{Code: CodeWrongErrorClass, Path: "/retryable",
			Message: fmt.Sprintf("HTTP %d %s with retryable true for an invalid request (%s); the engine would retry a request that can never succeed; answer retryable false", result.Status, result.Error.Code, reason)}}
	}
	return nil
}

// WriteJSON writes the report as indented JSON.
func WriteJSON(w io.Writer, report Report) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(report); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}
