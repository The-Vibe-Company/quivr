package pluginhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/call"
)

// SubscriptionTimeoutCap bounds one subscription invocation: the deadline is
// min(declared timeout_ms, SubscriptionTimeoutCap).
const SubscriptionTimeoutCap = plugins.SubscriptionTimeoutCap

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

func (e Evaluator) WantsVectors() bool {
	vectors := e.contribution().Vectors
	return vectors != nil && (vectors.Parts || vectors.Query)
}

func (e Evaluator) QueryText(expression map[string]any) string {
	vectors := e.contribution().Vectors
	if vectors == nil || !vectors.Query || !e.Pin.Offers(expression) {
		return ""
	}
	raw, err := json.Marshal(expression)
	if err != nil || !plugins.SubscriptionQueryVectorApplies(&e.Pin.Manifest, raw) {
		return ""
	}
	var value any = expression
	for _, token := range strings.Split(strings.TrimPrefix(vectors.QueryTextPointer, "/"), "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		switch object := value.(type) {
		case map[string]any:
			value = object[token]
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(object) || strconv.Itoa(index) != token {
				return ""
			}
			value = object[index]
		default:
			return ""
		}
	}
	text, _ := value.(string)
	return strings.TrimSpace(text)
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

// SubscriptionRequest builds the Plugin API 0.2 subscription request of a
// batch. The idempotency key is stable for the same pinned plugin, Record
// Version and distinct evaluations, so a retry of the same batch replays the
// same logical invocation.
func (e Evaluator) SubscriptionRequest(b monitoring.Batch) ([]byte, error) {
	var encodeErr error
	raw := func(value any) json.RawMessage {
		out, err := json.Marshal(value)
		if err != nil && encodeErr == nil {
			encodeErr = err
		}
		return out
	}
	parts := make([]plugins.SubscriptionPart, 0, len(b.Article.Parts))
	for _, p := range b.Article.Parts {
		parts = append(parts, plugins.SubscriptionPart{Key: p.Key, Role: p.Role, Text: p.Text})
	}
	meta := b.Article.Metadata
	origin := meta.Provenance
	if origin.Origin == "" {
		origin.Origin = monitoring.OriginClient
	}
	record := plugins.SubscriptionRecord{CorpusID: b.CorpusID, RecordID: b.RecordID, RecordVersionID: b.VersionID, Enriched: b.Enriched, Parts: parts,
		Source: raw(meta.Source), AcceptedAt: meta.AcceptedAt.UTC().Format(time.RFC3339Nano), Provenance: raw(origin)}
	if len(meta.Extensions) > 0 {
		record.Extensions = raw(meta.Extensions)
	}
	wants := e.contribution().Vectors
	if wants != nil && (wants.Parts || wants.Query) {
		ready := false
		record.VectorsReady = &ready
		if vectors := b.Article.Vectors; vectors != nil {
			record.VectorSpaceID = vectors.SpaceID
			ready = vectors.Ready
			if wants.Parts {
				for index := range record.Parts {
					if values := vectors.Parts[record.Parts[index].Key]; len(values) > 0 {
						record.Parts[index].Vectors = raw(values)
					}
				}
			}
		}
	}
	evaluations := make([]plugins.SubscriptionEvaluation, 0, len(b.Items))

	for _, item := range b.Items {
		expression, configuration := item.Expression, item.Configuration
		if expression == nil {
			expression = map[string]any{}
		}
		if configuration == nil {
			configuration = map[string]any{}
		}
		refs := make([]plugins.SubscriptionRef, len(item.Subscriptions))
		if item.Subscriptions == nil {
			refs = nil
		}
		for i, v := range item.Subscriptions {
			refs[i] = plugins.SubscriptionRef(v)
		}
		evaluation := plugins.SubscriptionEvaluation{ID: item.ID, Expression: raw(expression), Configuration: raw(configuration), Subscriptions: refs}
		if wants != nil && wants.Query {
			for _, vector := range item.QueryVectors {
				if vector.SpaceID == record.VectorSpaceID {
					copy := vector
					evaluation.QueryVector = raw(copy)
					break
				}
			}
		}
		evaluations = append(evaluations, evaluation)

	}
	if encodeErr != nil {
		return nil, encodeErr
	}
	key := plugins.SubscriptionKey(plugins.SubscriptionKeyInput{Generation: e.Pin.Generation(), OrganizationID: b.Organization, VersionID: b.VersionID, Enriched: b.Enriched, Evaluations: evaluations, IncludeVectors: wants != nil, VectorSpaceID: record.VectorSpaceID, VectorsReady: record.VectorsReady})
	config := e.Pin.Configuration
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	return plugins.BuildSubscriptionRequest(plugins.SubscriptionRequest{InvocationID: plugins.InvocationID(), IdempotencyKey: key, Contribution: "subscription",
		OrganizationID: b.Organization, Record: record, Evaluations: evaluations, Configuration: config})
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
	started := time.Now()
	view, err := plugins.ViewSubscriptionRequest(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", monitoring.ErrEvaluationInvalid, err)
	}
	result, err := call.Invoke(ctx, e.Pin, call.EvaluateSubscription, call.Bytes(request), func(ctx context.Context, body []byte) []plugins.Issue {
		return plugins.CheckSubscriptionOutput(body, view, &e.Pin.Manifest)
	}, nil)
	observe(e.Pin, b.Organization, OpEvaluateSubscription, started, result, err)
	if err != nil {
		return nil, err
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
