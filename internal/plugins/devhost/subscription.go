package devhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

// SubscriptionRoute is the subscription Contribution route (Plugin API 0.2).
const SubscriptionRoute = "/v0/contributions/subscription"

// IsSubscriptionFixture reports whether a fixture file is a subscription
// fixture (contracts/plugins/v0/subscription-fixture.schema.json): it has a
// top-level evaluations property. Other fixture files are invocation
// fixtures for a normalizer.
func IsSubscriptionFixture(raw []byte) bool {
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) != nil {
		return false
	}
	_, ok := probe["evaluations"]
	return ok
}

// SubscriptionBatch is one development subscription request built from a
// subscription fixture.
type SubscriptionBatch struct {
	// Index numbers the batches of a fixture from 1.
	Index int
	Body  []byte
	View  plugins.SubscriptionRequestView
	// Expect maps evaluation ids to the decision the fixture expects.
	Expect map[string]string
}

type subscriptionFixture struct {
	Configuration json.RawMessage `json:"configuration,omitempty"`
	Record        struct {
		Enriched   bool               `json:"enriched"`
		Parts      []subscriptionPart `json:"parts"`
		Source     json.RawMessage    `json:"source,omitempty"`
		AcceptedAt string             `json:"accepted_at,omitempty"`
		Provenance json.RawMessage    `json:"provenance,omitempty"`
		Extensions json.RawMessage    `json:"extensions,omitempty"`
	} `json:"record"`
	Evaluations []struct {
		Expression    json.RawMessage `json:"expression"`
		Configuration json.RawMessage `json:"configuration,omitempty"`
		Expect        string          `json:"expect,omitempty"`
	} `json:"evaluations"`
}

type subscriptionPart struct {
	Key  string `json:"key"`
	Role string `json:"role"`
	Text string `json:"text"`
}

type subscriptionRef struct {
	SubscriptionID        string `json:"subscription_id"`
	SubscriptionVersionID string `json:"subscription_version_id"`
	SavedQueryID          string `json:"saved_query_id"`
	SavedQueryVersionID   string `json:"saved_query_version_id"`
}

type subscriptionEvaluation struct {
	ID            string            `json:"id"`
	Expression    json.RawMessage   `json:"expression"`
	Configuration json.RawMessage   `json:"configuration"`
	Subscriptions []subscriptionRef `json:"subscriptions"`
}

type subscriptionRecord struct {
	CorpusID        string             `json:"corpus_id"`
	RecordID        string             `json:"record_id"`
	RecordVersionID string             `json:"record_version_id"`
	Enriched        bool               `json:"enriched"`
	Parts           []subscriptionPart `json:"parts"`
	Source          json.RawMessage    `json:"source"`
	AcceptedAt      string             `json:"accepted_at"`
	Provenance      json.RawMessage    `json:"provenance"`
	Extensions      json.RawMessage    `json:"extensions,omitempty"`
}

// DevAcceptedAt is the acceptance time of a subscription fixture that does
// not set one.
const DevAcceptedAt = "2026-01-01T00:00:00Z"

type subscriptionRequest struct {
	InvocationID   string                   `json:"invocation_id"`
	IdempotencyKey string                   `json:"idempotency_key"`
	Contribution   string                   `json:"contribution"`
	OrganizationID string                   `json:"organization_id"`
	Record         subscriptionRecord       `json:"record"`
	Evaluations    []subscriptionEvaluation `json:"evaluations"`
	Configuration  json.RawMessage          `json:"configuration"`
}

// BuildSubscriptionRequests turns a subscription fixture into development
// subscription requests. Evaluations are numbered e1, e2, ... in fixture
// order, evaluation n stands for the development Subscription
// dev-subscription-n (Version dev-subscription-version-n) of Saved Query
// dev-saved-query-n (Version dev-saved-query-version-n), and the Record
// Version ids derive from the first 16 hex digits of the SHA-256 of the
// fixture bytes. Record metadata the fixture omits defaults to source
// dev-namespace/dev-record-<digest>, origin client and DevAcceptedAt. Evaluations are split into batches of the manifest's
// max_batch_size; batch i has invocation id dev-invocation-<digest>-i and
// idempotency key dev:<sha256>:i. Issues report an invalid fixture, a
// configuration, expression or Subscription configuration the manifest
// schemas reject, or duplicate Part keys; err reports I/O failures.
func BuildSubscriptionRequests(path string, m *plugins.Manifest) ([]SubscriptionBatch, []plugins.Issue, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	if issues := plugins.ValidateDocument("subscription-fixture.schema.json", raw); len(issues) > 0 {
		return nil, issues, nil
	}
	if m == nil || m.Contributions.Subscription == nil {
		return nil, []plugins.Issue{{Code: plugins.CodeInvalidManifest, Path: "/contributions/subscription",
			Message: "this is a subscription fixture, but the manifest declares no subscription Contribution"}}, nil
	}
	var f subscriptionFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, nil, err
	}
	config := f.Configuration
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	issues := plugins.ValidateConfiguration(m, config)
	seen := map[string]bool{}
	for i, p := range f.Record.Parts {
		if seen[p.Key] {
			issues = append(issues, plugins.Issue{Code: plugins.CodeSchema, Path: fmt.Sprintf("/record/parts/%d/key", i),
				Message: fmt.Sprintf("duplicate Part key %q; the Parts of a Record Version have unique keys", p.Key)})
		}
		seen[p.Key] = true
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	short := digest[:16]
	evaluations := make([]subscriptionEvaluation, 0, len(f.Evaluations))
	expect := map[string]string{}
	for i, e := range f.Evaluations {
		n := i + 1
		evalConfig := e.Configuration
		if len(evalConfig) == 0 {
			evalConfig = json.RawMessage(`{}`)
		}
		for _, issue := range plugins.ValidateSubscriptionItem(m, e.Expression, evalConfig) {
			issue.Path = fmt.Sprintf("/evaluations/%d%s", i, issue.Path)
			issues = append(issues, issue)
		}
		id := fmt.Sprintf("e%d", n)
		if e.Expect != "" {
			expect[id] = e.Expect
		}
		evaluations = append(evaluations, subscriptionEvaluation{
			ID: id, Expression: e.Expression, Configuration: evalConfig,
			Subscriptions: []subscriptionRef{{
				SubscriptionID:        fmt.Sprintf("dev-subscription-%d", n),
				SubscriptionVersionID: fmt.Sprintf("dev-subscription-version-%d", n),
				SavedQueryID:          fmt.Sprintf("dev-saved-query-%d", n),
				SavedQueryVersionID:   fmt.Sprintf("dev-saved-query-version-%d", n),
			}},
		})
	}
	if len(issues) > 0 {
		return nil, issues, nil
	}
	parts := f.Record.Parts
	if parts == nil {
		parts = []subscriptionPart{}
	}
	record := subscriptionRecord{CorpusID: "dev-corpus", RecordID: "dev-record-" + short, RecordVersionID: "dev-version-" + short,
		Enriched: f.Record.Enriched, Parts: parts, Source: f.Record.Source, AcceptedAt: f.Record.AcceptedAt, Provenance: f.Record.Provenance, Extensions: f.Record.Extensions}
	// Metadata the fixture omits takes development defaults, as the core
	// always sends it.
	if len(record.Source) == 0 {
		record.Source, _ = json.Marshal(map[string]string{"namespace": "dev-namespace", "record_key": "dev-record-" + short})
	}
	if record.AcceptedAt == "" {
		record.AcceptedAt = DevAcceptedAt
	}
	if len(record.Provenance) == 0 {
		record.Provenance = json.RawMessage(`{"origin":"client"}`)
	}
	size := m.Contributions.Subscription.MaxBatchSize
	if size <= 0 {
		size = plugins.DefaultMaxBatchSize
	}
	var batches []SubscriptionBatch
	for start, index := 0, 1; start < len(evaluations); start, index = start+size, index+1 {
		chunk := evaluations[start:min(start+size, len(evaluations))]
		request := subscriptionRequest{
			InvocationID:   fmt.Sprintf("dev-invocation-%s-%d", short, index),
			IdempotencyKey: fmt.Sprintf("dev:%s:%d", digest, index),
			Contribution:   "subscription",
			OrganizationID: "dev-organization",
			Record:         record,
			Evaluations:    chunk,
			Configuration:  config,
		}
		body, err := json.Marshal(request)
		if err != nil {
			return nil, nil, err
		}
		if issues := plugins.ValidateDocument("subscription-request.schema.json", body); len(issues) > 0 {
			return nil, issues, nil
		}
		view, err := plugins.ViewSubscriptionRequest(body)
		if err != nil {
			return nil, nil, err
		}
		batchExpect := map[string]string{}
		for _, e := range chunk {
			if d, ok := expect[e.ID]; ok {
				batchExpect[e.ID] = d
			}
		}
		batches = append(batches, SubscriptionBatch{Index: index, Body: body, View: view, Expect: batchExpect})
	}
	return batches, nil, nil
}

// InvokeSubscriptionWith posts a request to POST /v0/contributions/subscription
// and judges the answer like InvokeNormalizerWith: at most maxResponseBytes
// are read, check judges a 200 body (normally a closure over
// plugins.CheckSubscriptionOutput), and other statuses must carry the error
// envelope.
func InvokeSubscriptionWith(ctx context.Context, baseURL string, request []byte, maxResponseBytes int, check func(body []byte) []plugins.Issue) (*Result, error) {
	return invoke(ctx, baseURL, SubscriptionRoute, request, maxResponseBytes, check)
}

// ExpectationIssues compares the decisions of a valid response with the
// decisions a fixture expects.
func ExpectationIssues(body []byte, expect map[string]string) []plugins.Issue {
	var response struct {
		Decisions []plugins.SubscriptionDecision `json:"decisions"`
	}
	if json.Unmarshal(body, &response) != nil {
		return nil
	}
	var issues []plugins.Issue
	for i, d := range response.Decisions {
		if want, ok := expect[d.ID]; ok && want != d.Decision {
			issues = append(issues, plugins.Issue{Code: CodeUnexpectedDecision, Path: fmt.Sprintf("/decisions/%d/decision", i),
				Message: fmt.Sprintf("evaluation %s answered %s; the fixture expects %s", d.ID, d.Decision, want)})
		}
	}
	return issues
}
