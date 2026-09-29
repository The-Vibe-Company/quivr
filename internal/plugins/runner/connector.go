package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/contracts"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
)

// minSecretLength is the shortest credential string the credentials check
// looks for: shorter values ("en", "true") appear in ordinary output.
const minSecretLength = 8

type connectorRun struct {
	label string
	run   *devhost.ConnectorRun
}

// connector runs every connector check: fixtures, then per fixture the page
// loop (invoke), the resumed run (resume) and the credential check, then the
// invalid requests, then the credentials check over everything the plugin
// answered or logged.
func (r *run) connector(ctx context.Context, own []ownFixture) {
	runs := r.connectorFixtures(own)
	for _, cr := range runs {
		r.invokeConnector(ctx, cr)
		r.checkConnectorCredential(ctx, cr)
	}
	if len(runs) > 0 {
		r.invalidConnectorRequests(ctx, runs[0].run)
		r.credentialsCheck(runs)
	}
}

// connectorFixtures builds runs from the plugin's own connector fixtures. The
// configuration and credential shapes are the plugin's own, so no normative
// fixture applies; the normative invalid requests still do.
func (r *run) connectorFixtures(own []ownFixture) []connectorRun {
	started := time.Now()
	check := Check{ID: CheckFixtures, Contribution: ContributionConnector, Title: "connector fixtures build valid requests"}
	var out []connectorRun
	for _, f := range own {
		if f.contribution != ContributionConnector {
			if !r.declared(f.contribution) {
				check.Issues = append(check.Issues, foreignFixture(f))
			}
			continue
		}
		run, issues, err := devhost.BuildConnectorRun(f.path, r.m)
		if err != nil {
			issues = []plugins.Issue{{Code: CodeInvalidFixture, Message: err.Error()}}
		}
		if len(issues) > 0 {
			for _, issue := range issues {
				issue.Message = f.label + ": " + issue.Message
				check.Issues = append(check.Issues, issue)
			}
			continue
		}
		out = append(out, connectorRun{label: f.label, run: run})
	}
	if len(out) == 0 && len(check.Issues) == 0 {
		check.Issues = []plugins.Issue{{Code: CodeNoFixture, Path: "/fixtures",
			Message: "no connector fixture exercises the connector Contribution; add fixtures/<name>.json with a connector kind, config and credential (contracts/plugins/v0/connector-fixture.schema.json)"}}
	}
	labels := make([]string, 0, len(out))
	for _, cr := range out {
		labels = append(labels, cr.label)
	}
	check.Note = "using " + orNone(labels)
	r.add(check, started)
	return out
}

func (r *run) connectorTimeoutMS() int { return r.m.Contributions.Connector.TimeoutMS }

// callFetch posts one fetch within the declared deadline and judges a 200
// body with plugins.CheckConnectorOutput for that request's checkpoint.
func (r *run) callFetch(ctx context.Context, body []byte) (*devhost.Result, *plugins.Issue) {
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(r.connectorTimeoutMS())*time.Millisecond)
	defer cancel()
	var request struct {
		Checkpoint json.RawMessage `json:"checkpoint"`
	}
	_ = json.Unmarshal(body, &request)
	result, err := devhost.InvokeConnectorFetch(callCtx, r.baseURL, body, plugins.ConnectorMaxResponseBytes(r.m), func(b []byte) []plugins.Issue {
		return plugins.CheckConnectorOutput(ctx, b, request.Checkpoint, r.m)
	})
	return r.connectorResult(ctx, callCtx, result, err)
}

// callCheckCredential posts one check_credential within the declared deadline.
func (r *run) callCheckCredential(ctx context.Context, body []byte) (*devhost.Result, *plugins.Issue) {
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(r.connectorTimeoutMS())*time.Millisecond)
	defer cancel()
	result, err := devhost.InvokeCheckCredential(callCtx, r.baseURL, body)
	return r.connectorResult(ctx, callCtx, result, err)
}

func (r *run) connectorResult(ctx, callCtx context.Context, result *devhost.Result, err error) (*devhost.Result, *plugins.Issue) {
	if err == nil {
		if len(result.Body) > 0 {
			r.seen = append(r.seen, result.Body)
		}
		return result, nil
	}
	if issue := deadlineIssue(ctx, callCtx, ContributionConnector, r.connectorTimeoutMS()); issue != nil {
		return nil, issue
	}
	return nil, &plugins.Issue{Code: CodeUnavailable, Message: err.Error()}
}

// judgeConnectorError requires a connector error answering a valid request to
// carry a class that agrees with retryable, and, when the fixture expects an
// error, that class and code.
func judgeConnectorError(result *devhost.Result, want *devhost.ConnectorExpectedError) []plugins.Issue {
	if issues := plugins.ConnectorErrorIssues(result.Error.Class, result.Error.Retryable); len(issues) > 0 {
		for i := range issues {
			issues[i].Message = fmt.Sprintf("HTTP %d %s: %s", result.Status, result.Error.Code, issues[i].Message)
		}
		return issues
	}
	if want == nil {
		return []plugins.Issue{{Code: CodeUnexpectedError,
			Message: fmt.Sprintf("a valid fixture was answered HTTP %d with a %s error %s: %s", result.Status, result.Error.Class, result.Error.Code, result.Error.Message)}}
	}
	if result.Error.Class != want.Class || want.Code != "" && result.Error.Code != want.Code {
		return []plugins.Issue{{Code: CodeUnexpectedError,
			Message: fmt.Sprintf("the fixture expects a %s error %s; the plugin answered a %s error %s: %s", want.Class, orAny(want.Code), result.Error.Class, result.Error.Code, result.Error.Message)}}
	}
	return nil
}

func orAny(code string) string {
	if code == "" {
		return "(any code)"
	}
	return code
}

type fetchedItem struct {
	key, revision string
	content       json.RawMessage
}

// invokeConnector runs the invoke check (the page loop from the fixture
// checkpoint, feeding each returned checkpoint back) and the resume check (a
// new run from the final checkpoint returns nothing already returned).
func (r *run) invokeConnector(ctx context.Context, cr connectorRun) {
	started := time.Now()
	run := cr.run
	check := Check{ID: CheckInvoke, Contribution: ContributionConnector, Fixture: cr.label,
		Title: fmt.Sprintf("kind %s: pages answered within %d ms with items the engine accepts, each checkpoint fed back", run.Kind, r.connectorTimeoutMS())}
	resume := Check{ID: CheckResume, Contribution: ContributionConnector, Fixture: cr.label,
		Title: "a new run from the final checkpoint returns no item already returned"}
	checkpoint := run.Checkpoint
	var reads int64
	var pages [][]string
	var fetched []fetchedItem
	complete := false
	for page := 0; page < run.MaxPages; page++ {
		result, problem := r.callFetch(ctx, run.FetchRequest(checkpoint, page, reads, fmt.Sprintf("page-%d", page)))
		switch {
		case problem != nil:
			check.Issues = []plugins.Issue{*problem}
		case len(result.Issues) > 0:
			check.Issues = result.Issues
		case result.Error != nil:
			if check.Issues = judgeConnectorError(result, run.Expect.Error); len(check.Issues) == 0 {
				check.Note = fmt.Sprintf("page %d answered the expected %s error %s", page, result.Error.Class, result.Error.Code)
			}
		case result.Status != 200:
			check.Issues = []plugins.Issue{{Code: CodeUnexpectedError, Message: fmt.Sprintf("a valid fixture was answered HTTP %d; the protocol answers 200", result.Status)}}
		case run.Expect.Error != nil:
			check.Issues = []plugins.Issue{{Code: CodeUnexpectedError, Path: "/expect/error",
				Message: fmt.Sprintf("the fixture expects a %s error; page %d was answered 200", run.Expect.Error.Class, page)}}
		}
		if len(check.Issues) > 0 || result != nil && result.Error != nil {
			break
		}
		var answer plugins.ConnectorPage
		_ = json.Unmarshal(result.Body, &answer)
		keys := []string{}
		for _, item := range answer.Items {
			keys = append(keys, item.RecordKey)
			fetched = append(fetched, fetchedItem{key: item.RecordKey, revision: item.Revision, content: item.Content})
		}
		pages = append(pages, keys)
		reads += answer.Reads
		if answer.More && plugins.SameJSON(answer.Checkpoint, checkpoint) {
			check.Issues = []plugins.Issue{{Code: CodeStalledCheckpoint, Path: "/checkpoint",
				Message: fmt.Sprintf("page %d answered more: true with the request's checkpoint unchanged; the core would fetch the same page forever. Move the checkpoint past the returned items", page)}}
			break
		}
		checkpoint = answer.Checkpoint
		if len(run.Expect.Pages) > page {
			want := run.Expect.Pages[page]
			if !slices.Equal(keys, want.RecordKeys) || want.More != nil && *want.More != answer.More {
				check.Issues = []plugins.Issue{{Code: CodeUnexpectedItems, Path: fmt.Sprintf("/expect/pages/%d", page),
					Message: fmt.Sprintf("page %d returned Record Keys %v with more %v; the fixture expects %v%s", page, keys, answer.More, want.RecordKeys, moreText(want.More))}}
				break
			}
		}
		if !answer.More {
			complete = true
			break
		}
	}
	if len(check.Issues) == 0 && run.Expect.Error == nil && len(run.Expect.Pages) > 0 && len(pages) != len(run.Expect.Pages) {
		check.Issues = []plugins.Issue{{Code: CodeUnexpectedItems, Path: "/expect/pages",
			Message: fmt.Sprintf("the run returned %d pages; the fixture expects %d", len(pages), len(run.Expect.Pages))}}
	}
	if len(check.Issues) == 0 && check.Note == "" {
		check.Note = fmt.Sprintf("%d pages, %d items", len(pages), len(fetched))
		if !complete {
			check.Note += fmt.Sprintf("; stopped at max_pages %d with more: true", run.MaxPages)
		}
	}
	r.add(check, started)

	started = time.Now()
	switch {
	case len(check.Issues) > 0:
		resume.Status, resume.Note = Skip, "the run failed"
	case run.Expect.Error != nil:
		resume.Status, resume.Note = Skip, "the fixture expects an error"
	case !complete:
		resume.Status, resume.Note = Skip, "the run stopped at max_pages before more: false"
	}
	if resume.Status == Skip {
		r.add(resume, started)
		return
	}
	result, problem := r.callFetch(ctx, run.FetchRequest(checkpoint, 0, reads, "resume"))
	resume.Issues = judgeSuccess(result, problem)
	if len(resume.Issues) == 0 {
		var answer plugins.ConnectorPage
		_ = json.Unmarshal(result.Body, &answer)
		for _, item := range answer.Items {
			for _, before := range fetched {
				if item.RecordKey == before.key && sameRevision(item, before) && !item.Withdraw {
					resume.Issues = append(resume.Issues, plugins.Issue{Code: CodeCheckpointIgnored, Path: "/items",
						Message: fmt.Sprintf("a new run from the final checkpoint returned %q again with the same revision; the checkpoint must resume after the items already returned", item.RecordKey)})
					break
				}
			}
		}
	}
	r.add(resume, started)
}

func moreText(more *bool) string {
	if more == nil {
		return ""
	}
	return fmt.Sprintf(" with more %v", *more)
}

func sameRevision(item plugins.ConnectorItem, before fetchedItem) bool {
	if item.Revision != "" || before.revision != "" {
		return item.Revision == before.revision
	}
	return plugins.SameJSON(item.Content, before.content)
}

// checkConnectorCredential sends the fixture's credential to check_credential
// and compares the answer with the fixture's expectation, if any.
func (r *run) checkConnectorCredential(ctx context.Context, cr connectorRun) {
	started := time.Now()
	run := cr.run
	check := Check{ID: CheckCredential, Contribution: ContributionConnector, Fixture: cr.label,
		Title: fmt.Sprintf("kind %s: check_credential answers ok or a classified error", run.Kind)}
	result, problem := r.callCheckCredential(ctx, run.CheckCredentialRequest("check-credential"))
	want := run.Expect.CheckCredential
	switch {
	case problem != nil:
		check.Issues = []plugins.Issue{*problem}
	case len(result.Issues) > 0:
		check.Issues = result.Issues
	case result.Error != nil:
		var expected *devhost.ConnectorExpectedError
		if want != nil && want.Class != "" {
			expected = &devhost.ConnectorExpectedError{Class: want.Class, Code: want.Code}
		}
		if want == nil {
			// Without an expectation, a classified refusal is a valid answer.
			check.Issues = plugins.ConnectorErrorIssues(result.Error.Class, result.Error.Retryable)
			check.Note = fmt.Sprintf("answered a %s error %s", result.Error.Class, result.Error.Code)
		} else {
			check.Issues = judgeConnectorError(result, expected)
		}
	case result.Status != 200:
		check.Issues = []plugins.Issue{{Code: CodeUnexpectedError, Message: fmt.Sprintf("HTTP %d; the protocol answers 200", result.Status)}}
	case want != nil && want.Class != "":
		check.Issues = []plugins.Issue{{Code: CodeUnexpectedError, Path: "/expect/check_credential",
			Message: fmt.Sprintf("the fixture expects a %s error; check_credential answered 200", want.Class)}}
	}
	r.add(check, started)
}

// invalidConnectorRequests sends requests the protocol schemas reject, or for
// a kind the plugin does not declare, to both routes. Each must be refused
// with the error envelope and retryable false.
func (r *run) invalidConnectorRequests(ctx context.Context, run *devhost.ConnectorRun) {
	valid := run.FetchRequest(run.Checkpoint, 0, 0, "invalid")
	credential := run.CheckCredentialRequest("invalid")
	unknownKind := func(m map[string]any) {
		if c, ok := m["connector"].(map[string]any); ok {
			c["kind"] = "no_such_kind"
		}
	}
	type connectorCase struct {
		invalidCase
		fetch bool
	}
	cases := []connectorCase{
		{invalidCase{label: "fetch/non-json-body", reason: "the body is not JSON", body: []byte(`{"invocation_id": `)}, true},
		{invalidCase{label: "fetch/unknown-field", reason: "the request carries an unknown top-level field", body: withRequest(valid, func(m map[string]any) { m["unexpected_field"] = true })}, true},
		{invalidCase{label: "fetch/unknown-kind", reason: "the kind is not declared", body: withRequest(valid, unknownKind)}, true},
		{invalidCase{label: "check_credential/non-json-body", reason: "the body is not JSON", body: []byte(`{"invocation_id": `)}, false},
		{invalidCase{label: "check_credential/unknown-kind", reason: "the kind is not declared", body: withRequest(credential, unknownKind)}, false},
	}
	names, _ := fs.Glob(contracts.PluginFixtures(), "requests/connector/*.json")
	sort.Strings(names)
	for _, name := range names {
		raw, err := fs.ReadFile(contracts.PluginFixtures(), name)
		if err != nil {
			continue
		}
		schema, fetch := "connector-fetch-request.schema.json", true
		if strings.HasPrefix(name, "requests/connector/check-credential") {
			schema, fetch = "connector-check-credential-request.schema.json", false
		}
		if len(plugins.ValidateDocument(schema, raw)) == 0 {
			continue // only the contract's invalid requests
		}
		cases = append(cases, connectorCase{invalidCase{label: normativeFixturePrefix + name, reason: "the normative fixture violates " + schema, body: raw}, fetch})
	}
	for _, c := range cases {
		started := time.Now()
		check := Check{ID: CheckInvalidRequest, Contribution: ContributionConnector, Fixture: c.label,
			Title: "an invalid request is refused with a terminal error envelope (" + c.reason + ")"}
		var result *devhost.Result
		var problem *plugins.Issue
		if c.fetch {
			result, problem = r.callFetch(ctx, c.body)
		} else {
			result, problem = r.callCheckCredential(ctx, c.body)
		}
		check.Issues = judgeRefusal(result, problem, c.reason)
		r.add(check, started)
	}
}

// credentialsCheck looks for every credential string of the fixtures in every
// connector answer and, when the runner launched the plugin, in its output.
func (r *run) credentialsCheck(runs []connectorRun) {
	started := time.Now()
	check := Check{ID: CheckCredentials, Contribution: ContributionConnector,
		Title: "no credential value appears in a response, an error envelope or the plugin's output"}
	secrets := map[string]bool{}
	for _, cr := range runs {
		var credential any
		_ = json.Unmarshal(cr.run.Credential, &credential)
		collectSecrets(credential, secrets)
	}
	if len(secrets) == 0 {
		check.Status, check.Note = Skip, fmt.Sprintf("no fixture credential has a string of at least %d characters to look for", minSecretLength)
		r.add(check, started)
		return
	}
	sources := map[string][]byte{}
	for i, body := range r.seen {
		sources[fmt.Sprintf("answer %d", i+1)] = body
	}
	if r.logs != nil {
		sources["plugin output"] = r.logs.Bytes()
	} else {
		check.Note = "the plugin's output was not checked: the runner did not launch it (--endpoint)"
	}
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, secret := range slices.Sorted(maps.Keys(secrets)) {
		for _, name := range names {
			if bytes.Contains(sources[name], []byte(secret)) || bytes.Contains(sources[name], jsonEscaped(secret)) {
				check.Issues = append(check.Issues, plugins.Issue{Code: CodeCredentialLeak,
					Message: fmt.Sprintf("a fixture credential value (%d characters) appears in %s; never echo, log or return a credential", len(secret), name)})
				break
			}
		}
	}
	r.add(check, started)
}

func collectSecrets(v any, into map[string]bool) {
	switch v := v.(type) {
	case string:
		if len(v) >= minSecretLength {
			into[v] = true
		}
	case map[string]any:
		for _, e := range v {
			collectSecrets(e, into)
		}
	case []any:
		for _, e := range v {
			collectSecrets(e, into)
		}
	}
}

func jsonEscaped(s string) []byte {
	b, _ := json.Marshal(s)
	return bytes.Trim(b, `"`)
}

// lockedBuffer collects the plugin's output from its stdout and stderr copiers.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buf.Bytes())
}
