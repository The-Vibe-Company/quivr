package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/contracts"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
)

// CheckEmbedQuery certifies embed_query for one fixture and one space.
const CheckEmbedQuery = "embed_query"

// CheckSegmentsOnly certifies a segment_and_embed request with no space
// (Plugin API 0.8): the same segments, without vectors.
const CheckSegmentsOnly = "segments_only"

// ContributionIngestion is the ingestion Contribution (Plugin API 0.6).
const ContributionIngestion = "ingestion"

type ingestionRun struct {
	label string
	run   *devhost.IngestionRun
}

// ingestion runs the ingestion checks: fixtures, invoke and replay per
// fixture, embed_query per fixture and space, invalid requests on both
// routes, and the secrets check.
func (r *run) ingestion(ctx context.Context, own []ownFixture) {
	runs := r.ingestionFixtures(own)
	for _, ir := range runs {
		r.invokeIngestion(ctx, ir)
		r.embedQueries(ctx, ir)
	}
	if len(runs) > 0 {
		r.invalidIngestionRequests(ctx, runs[0].run)
	}
	r.secretsCheck(ContributionIngestion)
}

// ingestionFixtures builds requests from the normative ingestion fixtures
// (contracts/plugins/v0/fixtures/ingestion) whose configuration the plugin
// accepts, plus the plugin's own ingestion fixtures.
func (r *run) ingestionFixtures(own []ownFixture) []ingestionRun {
	started := time.Now()
	check := Check{ID: CheckFixtures, Contribution: ContributionIngestion, Title: "ingestion fixtures build valid requests"}
	var out []ingestionRun
	var notes []string
	normative, _ := fs.Glob(contracts.PluginFixtures(), "ingestion/*.json")
	for _, name := range normative {
		raw, err := fs.ReadFile(contracts.PluginFixtures(), name)
		if err != nil {
			continue
		}
		label := normativeFixturePrefix + name
		built, issues := devhost.BuildIngestionRun(raw, r.m)
		if len(issues) > 0 {
			notes = append(notes, fmt.Sprintf("%s skipped: %s", label, issues[0].Message))
			continue
		}
		out = append(out, ingestionRun{label: label, run: built})
	}
	for _, f := range own {
		if f.contribution != ContributionIngestion {
			if !r.declared(f.contribution) {
				check.Issues = append(check.Issues, foreignFixture(f))
			}
			continue
		}
		raw, err := os.ReadFile(f.path)
		if err != nil {
			check.Issues = append(check.Issues, plugins.Issue{Code: CodeInvalidFixture, Message: f.label + ": " + err.Error()})
			continue
		}
		built, issues := devhost.BuildIngestionRun(raw, r.m)
		for _, issue := range issues {
			issue.Message = f.label + ": " + issue.Message
			check.Issues = append(check.Issues, issue)
		}
		if built != nil {
			out = append(out, ingestionRun{label: f.label, run: built})
		}
	}
	if len(out) == 0 && len(check.Issues) == 0 {
		check.Issues = []plugins.Issue{{Code: CodeNoFixture, Path: "/fixtures",
			Message: "no ingestion fixture applies: the normative ones need a configuration your schema rejects; add fixtures/<name>.json with Parts and a configuration (contracts/plugins/v0/ingestion-fixture.schema.json)"}}
	}
	labels := make([]string, 0, len(out))
	for _, ir := range out {
		labels = append(labels, ir.label)
	}
	check.Note = strings.Join(append([]string{"using " + orNone(labels)}, notes...), "; ")
	r.add(check, started)
	return out
}

// callSegmentAndEmbed posts one request within timeout_ms and judges a 200
// body with plugins.CheckSegmentAndEmbedOutput for that request.
func (r *run) callSegmentAndEmbed(ctx context.Context, body []byte) (*devhost.Result, *plugins.Issue) {
	timeoutMS := r.m.Contributions.Ingestion.TimeoutMS
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	view, _ := plugins.ViewIngestionRequest(body)
	result, err := devhost.InvokeSegmentAndEmbed(callCtx, r.baseURL, body, plugins.IngestionMaxResponseBytes(r.m), func(b []byte) []plugins.Issue {
		return plugins.CheckSegmentAndEmbedOutput(b, view, r.m)
	})
	return r.ingestionResult(ctx, callCtx, result, err, timeoutMS)
}

// callEmbedQuery posts one embed_query within query_timeout_ms and judges a
// 200 body with plugins.CheckEmbedQueryOutput for the requested space.
func (r *run) callEmbedQuery(ctx context.Context, body []byte) (*devhost.Result, *plugins.Issue) {
	timeoutMS := r.m.Contributions.Ingestion.QueryTimeoutMS
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	var request struct {
		Space string `json:"space"`
	}
	_ = json.Unmarshal(body, &request)
	result, err := devhost.InvokeEmbedQuery(callCtx, r.baseURL, body, func(b []byte) []plugins.Issue {
		return plugins.CheckEmbedQueryOutput(b, request.Space, r.m)
	})
	return r.ingestionResult(ctx, callCtx, result, err, timeoutMS)
}

func (r *run) ingestionResult(ctx, callCtx context.Context, result *devhost.Result, err error, timeoutMS int) (*devhost.Result, *plugins.Issue) {
	if err != nil {
		if issue := deadlineIssue(ctx, callCtx, ContributionIngestion, timeoutMS); issue != nil {
			return nil, issue
		}
		return nil, &plugins.Issue{Code: CodeUnavailable, Message: err.Error()}
	}
	r.seen = append(r.seen, result.Body)
	return result, nil
}

// invokeIngestion runs the invoke and replay checks on one fixture.
func (r *run) invokeIngestion(ctx context.Context, ir ingestionRun) {
	started := time.Now()
	check := Check{ID: CheckInvoke, Contribution: ContributionIngestion, Fixture: ir.label,
		Title: fmt.Sprintf("segments of %d Parts, each with a vector in %d spaces, answered within %d ms and accepted by the engine", len(ir.run.View.Parts), len(ir.run.View.Spaces), r.m.Contributions.Ingestion.TimeoutMS)}
	first, problem := r.callSegmentAndEmbed(ctx, ir.run.Request)
	check.Issues = judgeSuccess(first, problem)
	if len(check.Issues) == 0 {
		check.Issues = devhost.SegmentExpectationIssues(first.Body, ir.run.Expect)
		if answer, err := plugins.DecodeSegmentAndEmbed(first.Body); err == nil {
			check.Note = fmt.Sprintf("%d segments", len(answer.Segments))
		}
	}
	r.add(check, started)
	replay := Check{ID: CheckReplay, Contribution: ContributionIngestion, Fixture: ir.label, Title: "replaying the idempotency key returns the same segments, vectors, lexical text and provenance"}
	if len(check.Issues) > 0 {
		replay.Status, replay.Note = Skip, "the first invocation failed"
		r.add(replay, time.Now())
		return
	}
	started = time.Now()
	second, problem := r.callSegmentAndEmbed(ctx, ir.run.WithInvocation("replay"))
	replay.Issues = judgeSuccess(second, problem)
	if len(replay.Issues) == 0 {
		if diff := firstDifference(decoded(first.Body), decoded(second.Body), ""); diff != "" {
			replay.Issues = []plugins.Issue{{Code: CodeNondeterministic, Path: diff,
				Message: fmt.Sprintf("two invocations with the same idempotency key differ at %s; the same key must yield the same answer, or rebuilds would change what search serves", diff)}}
		}
	}
	r.add(replay, started)
	if len(replay.Issues) == 0 && plugins.SegmentsOnly(r.m) {
		r.segmentsOnly(ctx, ir, first.Body)
	}
}

// segmentsOnly asks for the fixture's segments without any space, as the
// core does before embedding a Version, and compares them with the segments
// of the full answer: the core stores the first and refuses a Version whose
// later answer differs.
func (r *run) segmentsOnly(ctx context.Context, ir ingestionRun, full []byte) {
	started := time.Now()
	check := Check{ID: CheckSegmentsOnly, Contribution: ContributionIngestion, Fixture: ir.label,
		Title: "a request with no space returns the same segments, lexical text and provenance, without vectors"}
	result, problem := r.callSegmentAndEmbed(ctx, ir.run.SegmentsOnlyRequest())
	check.Issues = judgeSuccess(result, problem)
	if len(check.Issues) == 0 {
		want, got := withoutVectors(full), withoutVectors(result.Body)
		if diff := firstDifference(want, got, ""); diff != "" {
			check.Issues = []plugins.Issue{{Code: CodeNondeterministic, Path: diff,
				Message: fmt.Sprintf("the segments without spaces differ from the segments with spaces at %s; the core segments a Version first and embeds it later, and refuses a Version whose segments change", diff)}}
		}
	}
	r.add(check, started)
}

// withoutVectors decodes an answer and drops every segment's vectors.
func withoutVectors(body []byte) any {
	doc, _ := decoded(body).(map[string]any)
	segments, _ := doc["segments"].([]any)
	for _, s := range segments {
		if segment, ok := s.(map[string]any); ok {
			delete(segment, "vectors")
		}
	}
	return doc
}

func decoded(body []byte) any {
	var doc any
	_ = json.Unmarshal(body, &doc)
	return doc
}

// embedQueries encodes the fixture's queries in every requested space: each
// answer has the space's dimensions, and the same query yields the same
// vector twice.
func (r *run) embedQueries(ctx context.Context, ir ingestionRun) {
	for _, space := range ir.run.View.Spaces {
		started := time.Now()
		declared, _ := plugins.DeclaredSpace(r.m, space)
		check := Check{ID: CheckEmbedQuery, Contribution: ContributionIngestion, Fixture: ir.label,
			Title: fmt.Sprintf("embed_query encodes %d queries in %s (%d dimensions) within %d ms, the same vector each time", len(ir.run.Queries), space, declared.Dimensions, r.m.Contributions.Ingestion.QueryTimeoutMS)}
		for i, query := range ir.run.Queries {
			first, problem := r.callEmbedQuery(ctx, ir.run.QueryRequest(space, query, fmt.Sprintf("%s-%d", space, i)))
			issues := judgeSuccess(first, problem)
			if len(issues) == 0 {
				second, problem := r.callEmbedQuery(ctx, ir.run.QueryRequest(space, query, fmt.Sprintf("%s-%d-replay", space, i)))
				issues = judgeSuccess(second, problem)
				if len(issues) == 0 {
					if diff := firstDifference(decoded(first.Body), decoded(second.Body), ""); diff != "" {
						issues = []plugins.Issue{{Code: CodeNondeterministic, Path: diff,
							Message: fmt.Sprintf("query %d encoded twice in %s gave different vectors; search would rank differently from one query to the next", i+1, space)}}
					}
				}
			}
			for _, issue := range issues {
				issue.Message = fmt.Sprintf("query %d: %s", i+1, issue.Message)
				check.Issues = append(check.Issues, issue)
			}
			if len(issues) > 0 {
				break
			}
		}
		r.add(check, started)
	}
}

// invalidIngestionRequests sends requests each route must refuse with a
// terminal error envelope: a non-JSON body, an unknown field, a space the
// manifest does not declare, and the normative invalid requests.
func (r *run) invalidIngestionRequests(ctx context.Context, ir *devhost.IngestionRun) {
	undeclared := r.m.ID + ".quivr_contract_undeclared"
	query := ir.QueryRequest(ir.View.Spaces[0], "contract runner", "invalid")
	type route struct {
		name  string
		valid []byte
		call  func(context.Context, []byte) (*devhost.Result, *plugins.Issue)
		space func(map[string]any)
	}
	routes := []route{
		{"segment_and_embed", ir.Request, r.callSegmentAndEmbed, func(m map[string]any) { m["spaces"] = []string{undeclared} }},
		{"embed_query", query, r.callEmbedQuery, func(m map[string]any) { m["space"] = undeclared }},
	}
	names, _ := fs.Glob(contracts.PluginFixtures(), "requests/ingestion/*.json")
	for _, rt := range routes {
		cases := []invalidCase{
			{label: rt.name + ":non-json-body", reason: "the body is not JSON", body: []byte(`{"invocation_id": `)},
			{label: rt.name + ":unknown-field", reason: "the request carries an unknown top-level field", body: withRequest(rt.valid, func(m map[string]any) { m["unexpected_field"] = true })},
			{label: rt.name + ":undeclared-space", reason: "the request names a space the manifest does not declare", body: withRequest(rt.valid, rt.space)},
		}
		schema := "ingestion-segment-and-embed-request.schema.json"
		prefix := "requests/ingestion/segment-and-embed"
		if rt.name == "embed_query" {
			schema, prefix = "ingestion-embed-query-request.schema.json", "requests/ingestion/embed-query"
		}
		for _, name := range names {
			raw, err := fs.ReadFile(contracts.PluginFixtures(), name)
			if err != nil || !strings.HasPrefix(name, prefix) || len(plugins.ValidateDocument(schema, raw)) == 0 {
				continue // only the contract's invalid requests for this route
			}
			cases = append(cases, invalidCase{label: normativeFixturePrefix + name, reason: "the normative fixture violates " + schema, body: raw})
		}
		for _, c := range cases {
			started := time.Now()
			check := Check{ID: CheckInvalidRequest, Contribution: ContributionIngestion, Fixture: c.label,
				Title: "an invalid request is refused with a terminal error envelope (" + c.reason + ")"}
			result, problem := rt.call(ctx, c.body)
			check.Issues = judgeRefusal(result, problem, c.reason)
			r.add(check, started)
		}
	}
}
