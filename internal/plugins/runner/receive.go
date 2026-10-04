package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost"
)

// CheckReceive judges a push kind's answers to relayed deliveries (Plugin API 0.5).
const CheckReceive = "receive"

// receiveConnector relays each receive case of a fixture and judges the
// answer with plugins.CheckReceiveOutput and the case's expectation.
func (r *run) receiveConnector(ctx context.Context, cr connectorRun) {
	for i, c := range cr.run.Receives {
		started := time.Now()
		label := fmt.Sprintf("%s#receive-%d", cr.label, i)
		title := c.Description
		if title == "" {
			title = fmt.Sprintf("%s delivery", c.Request.Method)
		}
		check := Check{ID: CheckReceive, Contribution: ContributionConnector, Fixture: label,
			Title: fmt.Sprintf("kind %s: %s answered within %d ms with a verdict the engine accepts", cr.run.Kind, strings.TrimSuffix(title, "."), r.connectorTimeoutMS())}
		result, problem := r.callReceive(ctx, cr.run.ReceiveCaseRequest(c, fmt.Sprintf("receive-%d", i)))
		check.Issues, check.Note = judgeDelivery(result, problem, c.Expect)
		r.add(check, started)
	}
}

// callReceive posts one receive within the declared deadline.
func (r *run) callReceive(ctx context.Context, body []byte) (*devhost.Result, *plugins.Issue) {
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(r.connectorTimeoutMS())*time.Millisecond)
	defer cancel()
	result, err := devhost.InvokeConnectorReceive(callCtx, r.baseURL, body, plugins.ConnectorMaxResponseBytes(r.m), func(b []byte) []plugins.Issue {
		return plugins.CheckReceiveOutput(ctx, b, r.m)
	})
	return r.connectorResult(ctx, callCtx, result, err)
}

func judgeDelivery(result *devhost.Result, problem *plugins.Issue, want *devhost.ConnectorExpectedDelivery) ([]plugins.Issue, string) {
	switch {
	case problem != nil:
		return []plugins.Issue{*problem}, ""
	case len(result.Issues) > 0:
		return result.Issues, ""
	case result.Error != nil:
		var expected *devhost.ConnectorExpectedError
		if want != nil {
			expected = want.Error
		}
		if issues := judgeConnectorError(result, expected); len(issues) > 0 {
			return issues, ""
		}
		return nil, fmt.Sprintf("answered the expected %s error %s", result.Error.Class, result.Error.Code)
	case result.Status != 200:
		return []plugins.Issue{{Code: CodeUnexpectedError, Message: fmt.Sprintf("HTTP %d; the protocol answers 200 with the verdict", result.Status)}}, ""
	}
	var d plugins.ConnectorDelivery
	_ = json.Unmarshal(result.Body, &d)
	keys := []string{}
	for _, item := range d.Items {
		keys = append(keys, item.RecordKey)
	}
	note := fmt.Sprintf("%s, HTTP %d, %d items", d.Verdict, d.Response.Status, len(keys))
	if want == nil {
		return nil, note
	}
	unexpected := func(format string, args ...any) ([]plugins.Issue, string) {
		return []plugins.Issue{{Code: CodeUnexpectedItems, Path: "/receive/expect", Message: fmt.Sprintf(format, args...)}}, note
	}
	switch {
	case want.Error != nil:
		return unexpected("the case expects a %s error; the plugin answered the verdict %s", want.Error.Class, d.Verdict)
	case want.Verdict != "" && want.Verdict != d.Verdict:
		return unexpected("the case expects the verdict %s; the plugin answered %s", want.Verdict, d.Verdict)
	case want.Status != 0 && want.Status != d.Response.Status:
		return unexpected("the case expects the source to be answered HTTP %d; the plugin answered %d", want.Status, d.Response.Status)
	case want.RecordKeys != nil && !slices.Equal(want.RecordKeys, keys):
		return unexpected("the delivery returned Record Keys %v; the case expects %v", keys, want.RecordKeys)
	case want.BodyContains != "" && !strings.Contains(d.Response.Body, want.BodyContains):
		return unexpected("the answer body does not contain %q", want.BodyContains)
	}
	return nil, note
}

// receiveCoverage fails when a push kind's fixtures relay no delivery: a
// plugin that declares push is certified on its receive answers too.
func (r *run) receiveCoverage(runs []connectorRun) {
	if !plugins.DeclaresPush(r.m) {
		return
	}
	started := time.Now()
	check := Check{ID: CheckReceive, Contribution: ContributionConnector, Title: "a push kind's fixtures relay at least one delivery"}
	cases := 0
	for _, cr := range runs {
		cases += len(cr.run.Receives)
	}
	if cases == 0 {
		check.Issues = []plugins.Issue{{Code: CodeNoFixture, Path: "/fixtures",
			Message: "a kind declares the push mode but no connector fixture has receive cases; add receive with a signed delivery, and a refused one (contracts/plugins/v0/connector-fixture.schema.json)"}}
	} else {
		check.Note = fmt.Sprintf("%d deliveries", cases)
	}
	r.add(check, started)
}

// invalidReceiveRequests sends receive requests the schema rejects, or for a
// kind the plugin does not declare. Each must be refused with the error
// envelope and retryable false.
func (r *run) invalidReceiveRequests(ctx context.Context, runs []connectorRun, normative map[string][]byte) {
	var push *devhost.ConnectorRun
	for _, cr := range runs {
		if len(cr.run.Receives) > 0 {
			push = cr.run
			break
		}
	}
	if push == nil {
		return
	}
	valid := push.ReceiveCaseRequest(push.Receives[0], "invalid")
	cases := []invalidCase{
		{label: "receive/non-json-body", reason: "the body is not JSON", body: []byte(`{"invocation_id": `)},
		{label: "receive/unknown-field", reason: "the request carries an unknown top-level field", body: withRequest(valid, func(m map[string]any) { m["unexpected_field"] = true })},
		{label: "receive/unknown-kind", reason: "the kind is not declared", body: withRequest(valid, func(m map[string]any) {
			if c, ok := m["connector"].(map[string]any); ok {
				c["kind"] = "no_such_kind"
			}
		})},
	}
	for _, name := range sortedNames(normative) {
		cases = append(cases, invalidCase{label: normativeFixturePrefix + name, reason: "the normative fixture violates connector-receive-request.schema.json", body: normative[name]})
	}
	for _, c := range cases {
		started := time.Now()
		check := Check{ID: CheckInvalidRequest, Contribution: ContributionConnector, Fixture: c.label,
			Title: "an invalid request is refused with a terminal error envelope (" + c.reason + ")"}
		result, problem := r.callReceive(ctx, c.body)
		check.Issues = judgeRefusal(result, problem, c.reason)
		r.add(check, started)
	}
}

func sortedNames(m map[string][]byte) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
