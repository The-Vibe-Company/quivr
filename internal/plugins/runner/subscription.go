package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/contracts"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
)

type subscriptionRequest struct {
	label  string
	body   []byte
	view   plugins.SubscriptionRequestView
	expect map[string]string
}

// subscriptionFixtures builds requests from the plugin's own subscription
// fixtures. A subscription rule's expression shape is the plugin's own, so no
// normative fixture applies; the normative invalid requests still do.
func (r *run) subscriptionFixtures(own []ownFixture) []subscriptionRequest {
	started := time.Now()
	check := Check{ID: CheckFixtures, Contribution: ContributionSubscription, Title: "subscription fixtures build valid requests"}
	var out []subscriptionRequest
	for _, f := range own {
		if !f.subscription {
			if r.m.Contributions.Normalizer == nil {
				check.Issues = append(check.Issues, plugins.Issue{Code: CodeInvalidFixture, Path: "/contributions",
					Message: f.label + ": an invocation fixture, but the manifest declares no normalizer Contribution"})
			}
			continue
		}
		batches, issues, err := devhost.BuildSubscriptionRequests(f.path, r.m)
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
		for _, b := range batches {
			label := f.label
			if len(batches) > 1 {
				label = fmt.Sprintf("%s#batch-%d", f.label, b.Index)
			}
			out = append(out, subscriptionRequest{label: label, body: b.Body, view: b.View, expect: b.Expect})
		}
	}
	if len(out) == 0 && len(check.Issues) == 0 {
		check.Issues = []plugins.Issue{{Code: CodeNoFixture, Path: "/fixtures",
			Message: "no subscription fixture exercises the subscription Contribution; add fixtures/<name>.json with record Parts and evaluations (contracts/plugins/v0/subscription-fixture.schema.json)"}}
	}
	labels := make([]string, 0, len(out))
	for _, req := range out {
		labels = append(labels, req.label)
	}
	check.Note = "using " + orNone(labels)
	r.add(check, started)
	return out
}

func (r *run) subscriptionTimeoutMS() int { return r.m.Contributions.Subscription.TimeoutMS }

// callSubscription posts one request within the declared deadline and judges
// a 200 body with plugins.CheckSubscriptionOutput for that request.
func (r *run) callSubscription(ctx context.Context, body []byte) (*devhost.Result, *plugins.Issue) {
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(r.subscriptionTimeoutMS())*time.Millisecond)
	defer cancel()
	view, _ := plugins.ViewSubscriptionRequest(body)
	result, err := devhost.InvokeSubscriptionWith(callCtx, r.baseURL, body, plugins.SubscriptionMaxResponseBytes(r.m), func(b []byte) []plugins.Issue {
		return plugins.CheckSubscriptionOutput(b, view, r.m)
	})
	if err == nil {
		return result, nil
	}
	if issue := deadlineIssue(ctx, callCtx, ContributionSubscription, r.subscriptionTimeoutMS()); issue != nil {
		return nil, issue
	}
	return nil, &plugins.Issue{Code: CodeUnavailable, Message: err.Error()}
}

// withRequest rewrites fields of a request body.
func withRequest(body []byte, edit func(map[string]any)) []byte {
	var request map[string]any
	_ = json.Unmarshal(body, &request)
	edit(request)
	out, _ := json.Marshal(request)
	return out
}

// invokeSubscription runs the invoke, replay and batch checks on one batch.
func (r *run) invokeSubscription(ctx context.Context, req subscriptionRequest) {
	started := time.Now()
	check := Check{ID: CheckInvoke, Contribution: ContributionSubscription, Fixture: req.label,
		Title: fmt.Sprintf("a batch of %d evaluations answered within %d ms with one decision each and evidence the engine accepts", len(req.view.EvaluationIDs), r.subscriptionTimeoutMS())}
	first, problem := r.callSubscription(ctx, req.body)
	check.Issues = judgeSuccess(first, problem)
	if len(check.Issues) == 0 {
		check.Issues = devhost.ExpectationIssues(first.Body, req.expect)
	}
	r.add(check, started)

	replay := Check{ID: CheckReplay, Contribution: ContributionSubscription, Fixture: req.label, Title: "replaying the idempotency key returns the same decisions and evidence"}
	batch := Check{ID: CheckBatch, Contribution: ContributionSubscription, Fixture: req.label, Title: "each decision is the same whatever the order of the batch"}
	if len(check.Issues) > 0 {
		for _, c := range []Check{replay, batch} {
			c.Status, c.Note = Skip, "the first invocation failed"
			r.add(c, time.Now())
		}
		return
	}
	firstDecisions := decisionsByID(first.Body)

	started = time.Now()
	body := withRequest(req.body, func(m map[string]any) { m["invocation_id"] = fmt.Sprint(m["invocation_id"], "-replay") })
	second, problem := r.callSubscription(ctx, body)
	replay.Issues = judgeSuccess(second, problem)
	if len(replay.Issues) == 0 {
		if diff := firstDifference(firstDecisions, decisionsByID(second.Body), ""); diff != "" {
			replay.Issues = []plugins.Issue{{Code: CodeNondeterministic, Path: diff,
				Message: fmt.Sprintf("two invocations with the same idempotency key returned different decisions or evidence at %s (decisions keyed by evaluation id); the same key must yield the same answer", diff)}}
		}
	}
	r.add(replay, started)

	started = time.Now()
	if len(req.view.EvaluationIDs) < 2 {
		batch.Status, batch.Note = Skip, "the batch has a single evaluation"
		r.add(batch, started)
		return
	}
	body = withRequest(req.body, func(m map[string]any) {
		evaluations, _ := m["evaluations"].([]any)
		for i, j := 0, len(evaluations)-1; i < j; i, j = i+1, j-1 {
			evaluations[i], evaluations[j] = evaluations[j], evaluations[i]
		}
		m["invocation_id"] = fmt.Sprint(m["invocation_id"], "-reversed")
		m["idempotency_key"] = fmt.Sprint(m["idempotency_key"], ":reversed")
	})
	reversed, problem := r.callSubscription(ctx, body)
	batch.Issues = judgeSuccess(reversed, problem)
	if len(batch.Issues) == 0 {
		if diff := firstDifference(firstDecisions, decisionsByID(reversed.Body), ""); diff != "" {
			id := strings.SplitN(strings.TrimPrefix(diff, "/"), "/", 2)[0]
			batch.Issues = []plugins.Issue{{Code: CodeBatchDependent, Path: diff,
				Message: fmt.Sprintf("evaluation %s was decided differently when the same batch was sent in reverse order (first difference at %s, decisions keyed by evaluation id); the core batches and deduplicates evaluations freely, so a decision must depend only on the record, the expression and the configurations", id, diff)}}
		}
	}
	r.add(batch, started)
}

// decisionsByID keys the decisions of a valid response by evaluation id.
func decisionsByID(body []byte) any {
	var response struct {
		Decisions []map[string]any `json:"decisions"`
	}
	_ = json.Unmarshal(body, &response)
	out := map[string]any{}
	for _, d := range response.Decisions {
		id, _ := d["id"].(string)
		delete(d, "id")
		out[id] = d
	}
	return out
}

// invalidSubscriptionRequests sends subscription requests the protocol schema
// rejects. Each must be refused with the error envelope and retryable false.
func (r *run) invalidSubscriptionRequests(ctx context.Context, valid []byte) {
	cases := []invalidCase{
		{label: "non-json-body", reason: "the body is not JSON", body: []byte(`{"invocation_id": `)},
		{label: "unknown-field", reason: "the request carries an unknown top-level field", body: withRequest(valid, func(m map[string]any) { m["unexpected_field"] = true })},
	}
	names, _ := fs.Glob(contracts.PluginFixtures(), "requests/subscription/*.json")
	for _, name := range names {
		raw, err := fs.ReadFile(contracts.PluginFixtures(), name)
		if err != nil || len(plugins.ValidateDocument("subscription-request.schema.json", raw)) == 0 {
			continue // only the contract's invalid requests
		}
		cases = append(cases, invalidCase{label: normativeFixturePrefix + name, reason: "the normative fixture violates subscription-request.schema.json", body: raw})
	}
	for _, c := range cases {
		started := time.Now()
		check := Check{ID: CheckInvalidRequest, Contribution: ContributionSubscription, Fixture: c.label,
			Title: "an invalid request is refused with a terminal error envelope (" + c.reason + ")"}
		result, problem := r.callSubscription(ctx, c.body)
		check.Issues = judgeRefusal(result, problem, c.reason)
		r.add(check, started)
	}
}
