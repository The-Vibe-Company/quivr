package pluginhttp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
)

// SubscriptionTimeoutCap bounds one subscription invocation: the deadline is
// min(declared timeout_ms, SubscriptionTimeoutCap).
const SubscriptionTimeoutCap = 30 * time.Second

// Evaluator is a pinned plugin's subscription Contribution installed as a
// monitoring evaluator. It speaks Plugin Protocol v0 over JSON-over-HTTP and
// judges every answer with plugins.CheckSubscriptionOutput, as the Contract
// Runner does.
type Evaluator struct {
	Pin *plugins.Pin
}

var _ monitoring.EvaluationPort = Evaluator{}

func (e Evaluator) contribution() *plugins.Subscription {
	return e.Pin.Manifest.Contributions.Subscription
}

// MaxBatch is the declared max_batch_size.
func (e Evaluator) MaxBatch() int {
	if n := e.contribution().MaxBatchSize; n > 0 {
		return n
	}
	return plugins.DefaultMaxBatchSize
}

// Validate judges a Saved Query expression and a Subscription configuration
// against the declared schemas. The first issue names the request member at
// fault: the pinned Saved Query Version for an expression, the evaluator
// configuration for a configuration. An expression of a kind the pin does not
// offer (PinConfig.Kinds) is an invalid expression too.
func (e Evaluator) Validate(expression, configuration map[string]any) error {
	if !e.Pin.Offers(expression) {
		kind, _ := expression["kind"].(string)
		return monitoring.Invalid(monitoring.ErrInvalidExpression, "/saved_query_version_id",
			fmt.Sprintf("%s@%s: /expression/kind: this installation does not offer the kind %q (offered: %s); ask the operator to enable it",
				e.Pin.Manifest.ID, e.Pin.Manifest.Version, kind, strings.Join(e.Pin.Kinds, ", ")))
	}
	expr, err := json.Marshal(expression)
	if err != nil {
		return monitoring.Invalid(monitoring.ErrInvalidExpression, "/saved_query_version_id", err.Error())
	}
	config, err := json.Marshal(configuration)
	if err != nil {
		return monitoring.Invalid(monitoring.ErrInvalidEvaluatorConfiguration, "/evaluator/configuration", err.Error())
	}
	issues := plugins.ValidateSubscriptionItem(&e.Pin.Manifest, expr, config)
	if len(issues) == 0 {
		return nil
	}
	first := issues[0]
	message := strings.TrimSpace(first.Path + ": " + first.Message)
	if len(issues) > 1 {
		message += fmt.Sprintf(" (and %d more issues)", len(issues)-1)
	}
	message = fmt.Sprintf("%s@%s: %s", e.Pin.Manifest.ID, e.Pin.Manifest.Version, message)
	if strings.HasPrefix(first.Path, "/expression") {
		return monitoring.Invalid(monitoring.ErrInvalidExpression, "/saved_query_version_id", message)
	}
	return monitoring.Invalid(monitoring.ErrInvalidEvaluatorConfiguration, "/evaluator"+first.Path, message)
}

type subscriptionPart struct {
	Key  string `json:"key"`
	Role string `json:"role"`
	Text string `json:"text"`
}

type subscriptionRecord struct {
	CorpusID        string                      `json:"corpus_id"`
	RecordID        string                      `json:"record_id"`
	RecordVersionID string                      `json:"record_version_id"`
	Enriched        bool                        `json:"enriched"`
	Parts           []subscriptionPart          `json:"parts"`
	Source          monitoring.SourceIdentity   `json:"source"`
	AcceptedAt      string                      `json:"accepted_at"`
	Provenance      monitoring.RecordProvenance `json:"provenance"`
	Extensions      map[string]any              `json:"extensions,omitempty"`
}

type subscriptionEvaluation struct {
	ID            string                       `json:"id"`
	Expression    map[string]any               `json:"expression"`
	Configuration map[string]any               `json:"configuration"`
	Subscriptions []monitoring.SubscriptionRef `json:"subscriptions"`
}

type subscriptionRequest struct {
	InvocationID   string                   `json:"invocation_id"`
	IdempotencyKey string                   `json:"idempotency_key"`
	Contribution   string                   `json:"contribution"`
	OrganizationID string                   `json:"organization_id"`
	Record         subscriptionRecord       `json:"record"`
	Evaluations    []subscriptionEvaluation `json:"evaluations"`
	Configuration  json.RawMessage          `json:"configuration"`
}

// SubscriptionRequest builds the Plugin API 0.2 subscription request of a
// batch. The idempotency key is stable for the same pinned plugin, Record
// Version and distinct evaluations, so a retry of the same batch replays the
// same logical invocation.
func (e Evaluator) SubscriptionRequest(b monitoring.Batch) ([]byte, error) {
	parts := make([]subscriptionPart, 0, len(b.Article.Parts))
	for _, p := range b.Article.Parts {
		parts = append(parts, subscriptionPart{Key: p.Key, Role: p.Role, Text: p.Text})
	}
	meta := b.Article.Metadata
	origin := meta.Provenance
	if origin.Origin == "" {
		origin.Origin = monitoring.OriginClient
	}
	record := subscriptionRecord{CorpusID: b.CorpusID, RecordID: b.RecordID, RecordVersionID: b.VersionID, Enriched: b.Enriched, Parts: parts,
		Source: meta.Source, AcceptedAt: meta.AcceptedAt.UTC().Format(time.RFC3339Nano), Provenance: origin}
	if len(meta.Extensions) > 0 {
		record.Extensions = meta.Extensions
	}
	evaluations := make([]subscriptionEvaluation, 0, len(b.Items))
	identity := []any{e.Pin.Generation(), "subscription", b.Organization, b.VersionID, b.Enriched}
	for _, item := range b.Items {
		expression, configuration := item.Expression, item.Configuration
		if expression == nil {
			expression = map[string]any{}
		}
		if configuration == nil {
			configuration = map[string]any{}
		}
		evaluations = append(evaluations, subscriptionEvaluation{ID: item.ID, Expression: expression, Configuration: configuration, Subscriptions: item.Subscriptions})
		identity = append(identity, item.ID, expression, configuration)
	}
	key, err := json.Marshal(identity)
	if err != nil {
		return nil, err
	}
	config := e.Pin.Configuration
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	return json.Marshal(subscriptionRequest{InvocationID: invocationID(), IdempotencyKey: "sk_" + content.Hash(key), Contribution: "subscription",
		OrganizationID: b.Organization, Record: record, Evaluations: evaluations, Configuration: config})
}

func invocationID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "inv_" + hex.EncodeToString(b[:])
}

// Evaluate sends one batch. Unavailability (no answer, a timeout, a non-2xx
// answer without a valid envelope, a discovery mismatch) is
// monitoring.ErrEvaluatorUnavailable; a declared plugin error is
// monitoring.ErrEvaluation; an answer CheckSubscriptionOutput refuses is
// monitoring.ErrEvaluationInvalid; a request over the 16 MiB bound is
// monitoring.ErrRequestTooLarge and is never sent. None is a decision.
func (e Evaluator) Evaluate(ctx context.Context, b monitoring.Batch) ([]monitoring.Outcome, error) {
	request, err := e.SubscriptionRequest(b)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", monitoring.ErrEvaluationInvalid, err)
	}
	if len(request) > plugins.SubscriptionMaxRequestBytes {
		return nil, fmt.Errorf("%w: %d bytes", monitoring.ErrRequestTooLarge, len(request))
	}
	if err := (Client{Pin: e.Pin}).CheckDiscovery(ctx); err != nil {
		return nil, fmt.Errorf("%w: %v", monitoring.ErrEvaluatorUnavailable, err)
	}
	view, err := plugins.ViewSubscriptionRequest(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", monitoring.ErrEvaluationInvalid, err)
	}
	timeout := time.Duration(e.contribution().TimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > SubscriptionTimeoutCap {
		timeout = SubscriptionTimeoutCap
	}
	invoke, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	check := func(body []byte) []plugins.Issue { return plugins.CheckSubscriptionOutput(body, view, &e.Pin.Manifest) }
	result, err := devhost.InvokeSubscriptionWith(invoke, e.Pin.Endpoint, request, plugins.SubscriptionMaxResponseBytes(&e.Pin.Manifest), check)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", monitoring.ErrEvaluatorUnavailable, err)
	}
	if result.Error != nil {
		declared := &PluginError{Status: result.Status, Code: result.Error.Code, Message: result.Error.Message, Retryable: result.Error.Retryable}
		if declared.Retryable {
			return nil, fmt.Errorf("%w: %w: %w", monitoring.ErrEvaluation, monitoring.ErrEvaluationRetryable, declared)
		}
		return nil, fmt.Errorf("%w: %w", monitoring.ErrEvaluation, declared)
	}
	if len(result.Issues) > 0 {
		if result.Status != 200 || result.Issues[0].Code == devhost.CodeInvalidErrorEnvelope {
			return nil, fmt.Errorf("%w: %s", monitoring.ErrEvaluatorUnavailable, describe(result.Issues))
		}
		return nil, fmt.Errorf("%w: %s", monitoring.ErrEvaluationInvalid, describe(result.Issues))
	}
	var response struct {
		Decisions []plugins.SubscriptionDecision `json:"decisions"`
	}
	if err := json.Unmarshal(result.Body, &response); err != nil {
		return nil, fmt.Errorf("%w: %v", monitoring.ErrEvaluationInvalid, err)
	}
	byID := map[string]plugins.SubscriptionDecision{}
	for _, d := range response.Decisions {
		byID[d.ID] = d
	}
	out := make([]monitoring.Outcome, len(b.Items))
	for i, item := range b.Items {
		d, ok := byID[item.ID]
		if !ok {
			return nil, errors.Join(monitoring.ErrEvaluationInvalid, fmt.Errorf("evaluation %s is not answered", item.ID))
		}
		ev := monitoring.Evaluation{Decision: monitoring.Decision(d.Decision)}
		if d.Evidence != nil {
			ev.Explanation, ev.PartKeys, ev.Details = d.Evidence.Explanation, d.Evidence.PartKeys, d.Evidence.Details
		}
		out[i] = monitoring.Outcome{Evaluation: ev}
	}
	return out, nil
}
