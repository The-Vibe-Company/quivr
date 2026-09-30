package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/contracts"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
)

// ContributionRetrieval is the retrieval Contribution.
const ContributionRetrieval = "retrieval"

// CodeUnexpectedRanking is a ranking whose first hits differ from what a
// retrieval fixture expects.
const CodeUnexpectedRanking = "unexpected_ranking"

type retrievalRun struct {
	label string
	run   *devhost.RetrievalRun
}

// retrieval runs the retrieval checks: fixtures, invoke per fixture and
// profile, replay per fixture, invalid requests and the secrets check.
func (r *run) retrieval(ctx context.Context, own []ownFixture) {
	runs := r.retrievalFixtures(own)
	for _, rr := range runs {
		r.invokeRetrieval(ctx, rr)
	}
	if len(runs) > 0 {
		r.invalidRetrievalRequests(ctx, runs[0].run)
	}
	r.secretsCheck(ContributionRetrieval)
}

// retrievalFixtures builds searches from the normative retrieval fixtures
// (contracts/plugins/v0/fixtures/retrieval) whose configuration the plugin
// accepts, plus the plugin's own retrieval fixtures.
func (r *run) retrievalFixtures(own []ownFixture) []retrievalRun {
	started := time.Now()
	check := Check{ID: CheckFixtures, Contribution: ContributionRetrieval, Title: "retrieval fixtures build valid searches"}
	var out []retrievalRun
	var notes []string
	normative, _ := fs.Glob(contracts.PluginFixtures(), "retrieval/*.json")
	for _, name := range normative {
		raw, err := fs.ReadFile(contracts.PluginFixtures(), name)
		if err != nil {
			continue
		}
		label := normativeFixturePrefix + name
		built, issues := devhost.BuildRetrievalRun(r.configured(raw, "retrieval"), r.m)
		if len(issues) > 0 {
			notes = append(notes, fmt.Sprintf("%s skipped: %s", label, issues[0].Message))
			continue
		}
		out = append(out, retrievalRun{label: label, run: built})
	}
	for _, f := range own {
		if f.contribution != ContributionRetrieval {
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
		built, issues := devhost.BuildRetrievalRun(raw, r.m)
		for _, issue := range issues {
			issue.Message = f.label + ": " + issue.Message
			check.Issues = append(check.Issues, issue)
		}
		if built != nil {
			out = append(out, retrievalRun{label: f.label, run: built})
		}
	}
	if len(out) == 0 && len(check.Issues) == 0 {
		check.Issues = []plugins.Issue{{Code: CodeNoFixture, Path: "/fixtures",
			Message: "no retrieval fixture applies: the normative ones need a configuration your schema rejects; add fixtures/<name>.json with a query and candidates (contracts/plugins/v0/retrieval-fixture.schema.json)"}}
	}
	labels := make([]string, 0, len(out))
	for _, rr := range out {
		labels = append(labels, rr.label)
	}
	check.Note = strings.Join(append([]string{"using " + orNone(labels)}, notes...), "; ")
	r.add(check, started)
	return out
}

// search drives one whole search under a profile within its max_latency_ms,
// serving candidates from the fixture. Certification holds a plugin to its
// latency objective on its own fixtures; the engine stops a search only at
// the looser hard bound (plugins.RetrievalProfile.Deadline).
func (r *run) search(ctx context.Context, rr retrievalRun, profile, suffix string) (*devhost.SearchOutcome, []plugins.Issue) {
	budget := r.m.Contributions.Retrieval.Profiles[profile].MaxLatencyMS
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(budget)*time.Millisecond)
	defer cancel()
	outcome, err := devhost.DriveSearch(callCtx, r.baseURL, r.m, rr.run.Request(profile, suffix), rr.run.Serve)
	if err != nil {
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, []plugins.Issue{{Code: CodeDeadlineExceeded, Path: "/contributions/retrieval/profiles/" + profile + "/max_latency_ms",
				Message: fmt.Sprintf("the search did not end within profile %q's max_latency_ms %d, its latency objective", profile, budget)}}
		}
		return nil, []plugins.Issue{{Code: CodeUnavailable, Message: err.Error()}}
	}
	if outcome.Result != nil {
		r.seen = append(r.seen, outcome.Result.Body)
	}
	if len(outcome.Issues) > 0 {
		return outcome, outcome.Issues
	}
	if outcome.Ranking == nil {
		return outcome, judgeSuccess(outcome.Result, nil)
	}
	return outcome, nil
}

func segmentIDs(hits []plugins.RankedHit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.SegmentID
	}
	return out
}

// invokeRetrieval runs the invoke check per profile and the replay check on
// one fixture.
func (r *run) invokeRetrieval(ctx context.Context, rr retrievalRun) {
	var first *devhost.SearchOutcome
	for _, profile := range rr.run.Profiles {
		started := time.Now()
		p := r.m.Contributions.Retrieval.Profiles[profile]
		check := Check{ID: CheckInvoke, Contribution: ContributionRetrieval, Fixture: rr.label,
			Title: fmt.Sprintf("profile %s ranks served candidates within %d ms and %.4g cents, in at most %d rounds the engine accepts", profile, p.MaxLatencyMS, p.MaxCostCents, plugins.RetrievalMaxRounds(r.m))}
		outcome, issues := r.search(ctx, rr, profile, profile)
		check.Issues = issues
		if len(issues) == 0 {
			check.Note = fmt.Sprintf("%d hits in %d rounds", len(outcome.Ranking), outcome.Rounds)
			if top := rr.run.Top; top != nil {
				got := segmentIDs(outcome.Ranking)
				if len(got) < len(top) || fmt.Sprint(got[:len(top)]) != fmt.Sprint(top) {
					check.Issues = []plugins.Issue{{Code: CodeUnexpectedRanking, Path: "/ranking/hits",
						Message: fmt.Sprintf("the ranking starts %v; the fixture expects %v", got, top)}}
				}
			}
			if first == nil {
				first = outcome
			}
		}
		r.add(check, started)
	}
	replay := Check{ID: CheckReplay, Contribution: ContributionRetrieval, Fixture: rr.label, Title: "the same search twice returns the same ranking"}
	if first == nil {
		replay.Status, replay.Note = Skip, "no search succeeded"
		r.add(replay, time.Now())
		return
	}
	started := time.Now()
	profile := rr.run.Profiles[0]
	second, issues := r.search(ctx, rr, profile, profile+"-replay")
	replay.Issues = issues
	if len(issues) == 0 {
		a, _ := json.Marshal(first.Ranking)
		b, _ := json.Marshal(second.Ranking)
		if diff := firstDifference(decoded(a), decoded(b), ""); diff != "" {
			replay.Issues = []plugins.Issue{{Code: CodeNondeterministic, Path: "/ranking/hits" + diff,
				Message: fmt.Sprintf("two identical searches differ at /ranking/hits%s; evaluation compares rankings run to run, so the same search must rank the same way", diff)}}
		}
	}
	r.add(replay, started)
}

// invalidRetrievalRequests sends searches the plugin must refuse with a
// terminal error envelope: a non-JSON body, an unknown field, a profile the
// manifest does not declare, and the normative invalid requests.
func (r *run) invalidRetrievalRequests(ctx context.Context, rr *devhost.RetrievalRun) {
	valid, _ := json.Marshal(rr.Request(rr.Profiles[0], "invalid"))
	cases := []invalidCase{
		{label: "search:non-json-body", reason: "the body is not JSON", body: []byte(`{"invocation_id": `)},
		{label: "search:unknown-field", reason: "the request carries an unknown top-level field", body: withRequest(valid, func(m map[string]any) { m["unexpected_field"] = true })},
		{label: "search:undeclared-profile", reason: "the request names a profile the manifest does not declare", body: withRequest(valid, func(m map[string]any) { m["profile"] = "quivr_contract_undeclared" })},
	}
	names, _ := fs.Glob(contracts.PluginFixtures(), "requests/retrieval/*.json")
	for _, name := range names {
		raw, err := fs.ReadFile(contracts.PluginFixtures(), name)
		if err != nil || len(plugins.ValidateDocument("retrieval-search-request.schema.json", raw)) == 0 {
			continue // only the contract's invalid requests
		}
		cases = append(cases, invalidCase{label: normativeFixturePrefix + name, reason: "the normative fixture violates retrieval-search-request.schema.json", body: raw})
	}
	budget := r.m.Contributions.Retrieval.Profiles[rr.Profiles[0]].MaxLatencyMS
	for _, c := range cases {
		started := time.Now()
		check := Check{ID: CheckInvalidRequest, Contribution: ContributionRetrieval, Fixture: c.label,
			Title: "an invalid search is refused with a terminal error envelope (" + c.reason + ")"}
		callCtx, cancel := context.WithTimeout(ctx, time.Duration(budget)*time.Millisecond)
		result, err := devhost.InvokeSearch(callCtx, r.baseURL, c.body, plugins.RetrievalMaxResponseBytes(r.m), func([]byte) []plugins.Issue { return nil })
		var problem *plugins.Issue
		if err != nil {
			problem = &plugins.Issue{Code: CodeUnavailable, Message: err.Error()}
		} else {
			r.seen = append(r.seen, result.Body)
		}
		cancel()
		check.Issues = judgeRefusal(result, problem, c.reason)
		r.add(check, started)
	}
}
