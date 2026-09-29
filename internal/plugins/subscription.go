package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Evidence bounds of a subscription decision. They mirror the bounds the
// monitoring engine applies to Match evidence (internal/monitoring), so a
// certified plugin never produces evidence the engine refuses.
const (
	MaxExplanationRunes     = 4096
	MaxEvidencePartKeys     = 100
	MaxEvidenceDetailsBytes = 16 << 10
)

// Subscription decisions.
const (
	DecisionMatch    = "match"
	DecisionNoMatch  = "no_match"
	DecisionNotReady = "not_ready"
)

// Issue codes for subscription requests and output.
const (
	CodeMissingDecision                  = "missing_decision"
	CodeDuplicateDecision                = "duplicate_decision"
	CodeUnknownDecision                  = "unknown_decision"
	CodeMissingEvidence                  = "missing_evidence"
	CodeUnknownPartKey                   = "unknown_part_key"
	CodeEvidenceTooLarge                 = "evidence_too_large"
	CodeDetailsTooLarge                  = "details_too_large"
	CodeInvalidEvidence                  = "invalid_evidence"
	CodeInvalidExpression                = "invalid_expression"
	CodeInvalidSubscriptionConfiguration = "invalid_subscription_configuration"
)

// SubscriptionMaxResponseBytes is the response bound for a subscription
// Contribution: the declared max_response_bytes (or its default), capped by
// EngineMaxResponseBytes.
func SubscriptionMaxResponseBytes(m *Manifest) int {
	limit := DefaultMaxResponseBytes
	if m != nil && m.Contributions.Subscription != nil && m.Contributions.Subscription.Limits.MaxResponseBytes > 0 {
		limit = m.Contributions.Subscription.Limits.MaxResponseBytes
	}
	return min(limit, EngineMaxResponseBytes)
}

// SubscriptionRequestView is what output validation needs from a subscription
// request: the evaluation ids and the Part keys of the evaluated Version.
type SubscriptionRequestView struct {
	EvaluationIDs []string
	PartKeys      []string
}

// ViewSubscriptionRequest reads a subscription request body.
func ViewSubscriptionRequest(request []byte) (SubscriptionRequestView, error) {
	var r struct {
		Record struct {
			Parts []struct {
				Key string `json:"key"`
			} `json:"parts"`
		} `json:"record"`
		Evaluations []struct {
			ID string `json:"id"`
		} `json:"evaluations"`
	}
	if err := json.Unmarshal(request, &r); err != nil {
		return SubscriptionRequestView{}, err
	}
	var view SubscriptionRequestView
	for _, p := range r.Record.Parts {
		view.PartKeys = append(view.PartKeys, p.Key)
	}
	for _, e := range r.Evaluations {
		view.EvaluationIDs = append(view.EvaluationIDs, e.ID)
	}
	return view, nil
}

// SubscriptionDecision is one decoded decision of a valid response.
type SubscriptionDecision struct {
	ID       string                `json:"id"`
	Decision string                `json:"decision"`
	Evidence *SubscriptionEvidence `json:"evidence,omitempty"`
}

// SubscriptionEvidence is the bounded evidence of a decision.
type SubscriptionEvidence struct {
	Explanation string         `json:"explanation"`
	PartKeys    []string       `json:"part_keys,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

// CheckSubscriptionOutput judges a 200 subscription response exactly as the
// engine does before it records any decision: the response bound, the
// evidence bounds (explanation at most 4096 runes, at most 100 Part keys that
// exist in the request, details at most 16 KiB of JSON, no NUL character),
// the response schema (unknown fields are rejected), then exactly one
// decision per requested evaluation, with evidence for every match.
func CheckSubscriptionOutput(raw []byte, request SubscriptionRequestView, m *Manifest) []Issue {
	if limit := SubscriptionMaxResponseBytes(m); len(raw) > limit {
		return []Issue{{Code: CodeResponseTooLarge, Message: fmt.Sprintf("the response is %d bytes; the limit is %d (declared max_response_bytes, capped by the engine at %d)", len(raw), limit, EngineMaxResponseBytes)}}
	}
	instance, err := decodeInstance(raw)
	if err != nil {
		return []Issue{{Code: CodeSchema, Message: "not JSON: " + err.Error()}}
	}
	semantic := evidenceIssues(instance, request)
	schemaIssues, err := validateAgainst("subscription-response.schema.json", instance)
	if err != nil {
		return []Issue{{Code: CodeSchema, Message: err.Error()}}
	}
	issues := append(semantic, withoutCovered(schemaIssues, semantic)...)
	if len(issues) > 0 {
		return issues
	}
	var response struct {
		Decisions []SubscriptionDecision `json:"decisions"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return []Issue{{Code: CodeSchema, Path: "/decisions", Message: err.Error()}}
	}
	requested := map[string]bool{}
	for _, id := range request.EvaluationIDs {
		requested[id] = true
	}
	answered := map[string]bool{}
	for i, d := range response.Decisions {
		path := fmt.Sprintf("/decisions/%d", i)
		switch {
		case !requested[d.ID]:
			issues = append(issues, Issue{Code: CodeUnknownDecision, Path: path + "/id",
				Message: fmt.Sprintf("decision for evaluation %q, which the request does not carry (requested: %v)", d.ID, request.EvaluationIDs)})
		case answered[d.ID]:
			issues = append(issues, Issue{Code: CodeDuplicateDecision, Path: path + "/id",
				Message: fmt.Sprintf("evaluation %q is answered more than once; answer each requested id exactly once", d.ID)})
		}
		answered[d.ID] = true
		if d.Decision == DecisionMatch && d.Evidence == nil {
			issues = append(issues, Issue{Code: CodeMissingEvidence, Path: path,
				Message: fmt.Sprintf("evaluation %q is a match without evidence; a match carries evidence with an explanation, which the engine stores with the Match", d.ID)})
		}
	}
	for _, id := range request.EvaluationIDs {
		if !answered[id] {
			issues = append(issues, Issue{Code: CodeMissingDecision, Path: "/decisions",
				Message: fmt.Sprintf("evaluation %q is not answered; return exactly one decision per requested id", id)})
		}
	}
	return issues
}

// evidenceIssues applies the evidence bounds JSON Schema cannot express, or
// expresses without a specific code. It reads the generic document
// defensively because it runs before, and independently of, the schema.
func evidenceIssues(doc any, request SubscriptionRequestView) []Issue {
	root, _ := doc.(map[string]any)
	decisions, _ := root["decisions"].([]any)
	known := map[string]bool{}
	for _, k := range request.PartKeys {
		known[k] = true
	}
	var issues []Issue
	for i, raw := range decisions {
		decision, _ := raw.(map[string]any)
		evidence, ok := decision["evidence"].(map[string]any)
		if !ok {
			continue
		}
		path := fmt.Sprintf("/decisions/%d/evidence", i)
		if explanation, ok := evidence["explanation"].(string); ok {
			if n := utf8.RuneCountInString(explanation); n > MaxExplanationRunes {
				issues = append(issues, Issue{Code: CodeEvidenceTooLarge, Path: path + "/explanation",
					Message: fmt.Sprintf("the explanation has %d code points; the engine stores at most %d", n, MaxExplanationRunes)})
			}
		}
		if keys, ok := evidence["part_keys"].([]any); ok {
			if len(keys) > MaxEvidencePartKeys {
				issues = append(issues, Issue{Code: CodeEvidenceTooLarge, Path: path + "/part_keys",
					Message: fmt.Sprintf("%d Part keys; the engine stores at most %d", len(keys), MaxEvidencePartKeys)})
			} else {
				for j, k := range keys {
					if key, ok := k.(string); ok && !known[key] {
						issues = append(issues, Issue{Code: CodeUnknownPartKey, Path: fmt.Sprintf("%s/part_keys/%d", path, j),
							Message: fmt.Sprintf("Part key %q is not a Part of the evaluated Record Version (Parts: %v)", key, request.PartKeys)})
					}
				}
			}
		}
		if details, ok := evidence["details"]; ok {
			if b, err := json.Marshal(details); err == nil && len(b) > MaxEvidenceDetailsBytes {
				issues = append(issues, Issue{Code: CodeDetailsTooLarge, Path: path + "/details",
					Message: fmt.Sprintf("details serialize to %d bytes of JSON; the engine stores at most %d", len(b), MaxEvidenceDetailsBytes)})
			}
		}
		// The engine stores evidence as JSON text, which cannot hold NUL.
		if containsNUL(evidence) {
			issues = append(issues, Issue{Code: CodeInvalidEvidence, Path: path,
				Message: "the evidence contains a NUL character, which the engine cannot store"})
		}
	}
	return issues
}

// ValidateSubscriptionItem validates a Saved Query expression and a
// Subscription evaluator configuration (JSON objects) against the schemas the
// manifest's subscription Contribution declares, as the core does when it
// pins them. Issue paths start with /expression or /configuration.
func ValidateSubscriptionItem(m *Manifest, expression, configuration []byte) []Issue {
	var sub *Subscription
	if m != nil {
		sub = m.Contributions.Subscription
	}
	if sub == nil {
		return []Issue{{Code: CodeInvalidManifest, Path: "/contributions/subscription", Message: "the manifest declares no subscription Contribution"}}
	}
	var issues []Issue
	for _, target := range []struct {
		name, code string
		value      []byte
		schema     json.RawMessage
	}{
		{"expression", CodeInvalidExpression, expression, sub.ExpressionSchema},
		{"configuration", CodeInvalidSubscriptionConfiguration, configuration, sub.ConfigurationSchema},
	} {
		issues = append(issues, validateObject(target.name, target.code, target.value, target.schema)...)
	}
	return issues
}

func validateObject(name, code string, value []byte, schemaRaw json.RawMessage) []Issue {
	path := "/" + name
	instance, err := decodeInstance(value)
	if err != nil {
		return []Issue{{Code: code, Path: path, Message: "not JSON: " + err.Error()}}
	}
	if _, ok := instance.(map[string]any); !ok {
		return []Issue{{Code: code, Path: path, Message: name + " must be a JSON object"}}
	}
	if len(schemaRaw) == 0 {
		return nil
	}
	schemaValue, err := decodeInstance(schemaRaw)
	var schema *jsonschema.Schema
	if err == nil {
		schema, err = compileUserSchema(schemaValue)
	}
	if err != nil {
		return []Issue{{Code: code, Path: path, Message: "the declared schema does not compile: " + err.Error()}}
	}
	err = schema.Validate(instance)
	var verr *jsonschema.ValidationError
	if errors.As(err, &verr) {
		return leafIssues(verr, code, path)
	}
	if err != nil {
		return []Issue{{Code: code, Path: path, Message: err.Error()}}
	}
	return nil
}
