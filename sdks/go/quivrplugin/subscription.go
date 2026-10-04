package quivrplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Subscriber decides one Record Version against distinct saved query evaluations.
type Subscriber interface {
	Evaluate(context.Context, *SubscriptionRequest) (*SubscriptionResponse, error)
}

type SubscriptionContribution struct {
	ExpressionSchema    json.RawMessage      `json:"expression_schema"`
	ConfigurationSchema json.RawMessage      `json:"configuration_schema,omitempty"`
	MaxBatchSize        int                  `json:"max_batch_size"`
	TimeoutMS           int                  `json:"timeout_ms"`
	Vectors             *SubscriptionVectors `json:"vectors,omitempty"`
	Limits              struct {
		MaxResponseBytes int `json:"max_response_bytes"`
	} `json:"limits"`
}

// SubscriptionVectors opts in to canonical Part and saved-query vectors (API 0.10).
type SubscriptionVectors struct {
	Parts                 bool            `json:"parts,omitempty"`
	Query                 bool            `json:"query,omitempty"`
	QueryTextPointer      string          `json:"query_text_pointer,omitempty"`
	QueryExpressionSchema json.RawMessage `json:"query_expression_schema,omitempty"`
}

type SubscriptionRequest struct {
	InvocationID   string                   `json:"invocation_id"`
	IdempotencyKey string                   `json:"idempotency_key"`
	Contribution   string                   `json:"contribution"`
	OrganizationID string                   `json:"organization_id"`
	Record         SubscriptionRecord       `json:"record"`
	Evaluations    []SubscriptionEvaluation `json:"evaluations"`
	Configuration  json.RawMessage          `json:"configuration"`
	logger         *slog.Logger
}

func (r *SubscriptionRequest) Logger() *slog.Logger { return r.logger }

type SubscriptionRecord struct {
	CorpusID        string               `json:"corpus_id"`
	RecordID        string               `json:"record_id"`
	RecordVersionID string               `json:"record_version_id"`
	Enriched        bool                 `json:"enriched"`
	Parts           []SubscriptionPart   `json:"parts"`
	Source          json.RawMessage      `json:"source,omitempty"`
	AcceptedAt      string               `json:"accepted_at,omitempty"`
	Provenance      json.RawMessage      `json:"provenance,omitempty"`
	Extensions      map[string]Extension `json:"extensions,omitempty"`
	VectorSpaceID   string               `json:"vector_space_id,omitempty"`
	VectorsReady    *bool                `json:"vectors_ready,omitempty"`
}
type SubscriptionPart struct {
	Key     string               `json:"key"`
	Role    string               `json:"role"`
	Text    string               `json:"text"`
	Vectors []SubscriptionVector `json:"vectors,omitempty"`
}
type SubscriptionVector struct {
	SegmentID string    `json:"segment_id"`
	Vector    []float64 `json:"vector"`
}
type SubscriptionQueryVector struct {
	VectorSpaceID string    `json:"vector_space_id"`
	Vector        []float64 `json:"vector"`
}
type SubscriptionEvaluation struct {
	ID            string                   `json:"id"`
	Expression    json.RawMessage          `json:"expression"`
	Configuration json.RawMessage          `json:"configuration"`
	Subscriptions []SubscriptionRef        `json:"subscriptions"`
	QueryVector   *SubscriptionQueryVector `json:"query_vector,omitempty"`
}
type SubscriptionRef struct {
	SubscriptionID        string `json:"subscription_id"`
	SubscriptionVersionID string `json:"subscription_version_id"`
	SavedQueryID          string `json:"saved_query_id"`
	SavedQueryVersionID   string `json:"saved_query_version_id"`
	Owner                 string `json:"owner,omitempty"`
}
type SubscriptionResponse struct {
	Decisions []Decision `json:"decisions"`
}
type Decision struct {
	ID       string    `json:"id"`
	Decision string    `json:"decision"`
	Evidence *Evidence `json:"evidence,omitempty"`
}
type Evidence struct {
	Explanation string         `json:"explanation"`
	PartKeys    []string       `json:"part_keys,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

func Match(id, explanation string, keys ...string) Decision {
	return Decision{ID: id, Decision: "match", Evidence: &Evidence{Explanation: explanation, PartKeys: keys}}
}
func NoMatch(id string) Decision  { return Decision{ID: id, Decision: "no_match"} }
func NotReady(id string) Decision { return Decision{ID: id, Decision: "not_ready"} }

func (p *Plugin) Subscription(impl Subscriber) error {
	if p.m.Subscription == nil {
		return fmt.Errorf("the manifest declares no subscription Contribution")
	}
	p.subscriber = impl
	return nil
}

func (p *Plugin) serveSubscription(w http.ResponseWriter, r *http.Request) {
	var req SubscriptionRequest
	if !p.readIngestion(w, r, "plugins/v0/subscription-request.schema.json", &req) {
		return
	}
	s := p.m.Subscription
	if len(req.Evaluations) > s.MaxBatchSize {
		refuse(w, 400, "batch_too_large", "evaluations exceed max_batch_size", Credential{})
		return
	}
	ids := map[string]bool{}
	for _, e := range req.Evaluations {
		if ids[e.ID] {
			refuse(w, 400, "invalid_request", "duplicate evaluation id", Credential{})
			return
		}
		ids[e.ID] = true
		for _, schema := range []struct {
			raw, value json.RawMessage
			code       string
		}{
			{s.ExpressionSchema, e.Expression, "invalid_expression"}, {s.ConfigurationSchema, e.Configuration, "invalid_subscription_configuration"},
		} {
			if len(schema.raw) > 0 {
				compiled, err := compileDeclared(schema.raw)
				if err == nil {
					err = validateWith(compiled, schema.value)
				}
				if err != nil {
					refuse(w, 400, schema.code, err.Error(), Credential{})
					return
				}
			}
		}
	}
	req.logger = p.logger.With("invocation_id", req.InvocationID, "idempotency_key", req.IdempotencyKey)
	defer p.ingestPanic(w, req.logger)
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(s.TimeoutMS)*time.Millisecond)
	defer cancel()
	out, err := p.subscriber.Evaluate(ctx, &req)
	if err != nil {
		p.ingestFail(w, req.logger, err)
		return
	}
	if problem := subscriptionProblem(&req, out); problem != "" {
		refuse(w, 500, "invalid_response", problem, Credential{})
		return
	}
	p.contributionResponse(w, out, "subscription-response.schema.json", s.Limits.MaxResponseBytes)
}

func subscriptionProblem(req *SubscriptionRequest, out *SubscriptionResponse) string {
	if out == nil {
		return "no subscription response"
	}
	pending := map[string]bool{}
	for _, e := range req.Evaluations {
		pending[e.ID] = true
	}
	parts := map[string]bool{}
	for _, part := range req.Record.Parts {
		parts[part.Key] = true
	}
	for _, d := range out.Decisions {
		if !pending[d.ID] {
			return "unknown or duplicate decision id"
		}
		delete(pending, d.ID)
		if d.Decision == "match" && d.Evidence == nil {
			return "match requires evidence"
		}
		if e := d.Evidence; e != nil {
			for _, key := range e.PartKeys {
				if !parts[key] {
					return "evidence references an unknown Part"
				}
			}
			if problem := jsonValueProblem(e.Details, 16<<10); problem != "" {
				return "evidence details: " + problem
			}
			if problem := jsonValueProblem(e, 0); problem != "" {
				return "evidence: " + problem
			}
		}
	}
	if len(pending) > 0 {
		return "missing decision"
	}
	return ""
}
