package monitoring

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
)

// Decision is an evaluator's answer for one pinned Subscription Version and
// one eligible Record Version. An evaluation that could not complete is an
// error, never a Decision.
type Decision string

const (
	DecisionMatch    Decision = "match"
	DecisionNoMatch  Decision = "no_match"
	DecisionNotReady Decision = "not_ready"
)

var (
	// ErrEvaluation reports that an evaluator could not complete.
	ErrEvaluation = errors.New("evaluator_error")
	// ErrEvaluatorConfiguration reports a pinned configuration the evaluator cannot interpret.
	ErrEvaluatorConfiguration = errors.New("evaluator_configuration_invalid")
	// ErrEvaluatorUnavailable reports an evaluator that cannot be reached or
	// did not answer in time. It is retried, never a decision.
	ErrEvaluatorUnavailable = errors.New("evaluator_unavailable")
	// ErrEvaluationInvalid reports an answer the engine refuses (schema,
	// completeness or evidence bounds).
	ErrEvaluationInvalid = errors.New("evaluation_invalid")
	// ErrEvaluationRetryable marks an ErrEvaluation the evaluator declared
	// transient (a backend down): it fails every evaluation alike, so the
	// engine retries the batch without splitting it.
	ErrEvaluationRetryable = errors.New("evaluator_error_retryable")
	// ErrRequestTooLarge reports a batch whose request would exceed the
	// protocol's request bound; the engine splits it.
	ErrRequestTooLarge = errors.New("evaluation_request_too_large")

	// ErrInvalidExpression refuses a Subscription Version whose pinned Saved
	// Query expression the evaluator's declared schema rejects.
	ErrInvalidExpression = publicerr.New("invalid_expression")
	// ErrInvalidEvaluatorConfiguration refuses a Subscription Version whose
	// evaluator configuration the evaluator's declared schema rejects.
	ErrInvalidEvaluatorConfiguration = publicerr.New("invalid_subscription_configuration")
)

// fieldError points a refused command at the request member that caused it.
type fieldError struct {
	err     error
	pointer string
	message string
}

func (e *fieldError) Error() string { return e.err.Error() + " " + e.pointer + ": " + e.message }
func (e *fieldError) Unwrap() error { return e.err }

// Invalid wraps a validation sentinel with the JSON Pointer of the request
// member at fault and a human explanation.
func Invalid(sentinel error, pointer, message string) error {
	return &fieldError{err: sentinel, pointer: pointer, message: message}
}

// Field returns the JSON Pointer and explanation attached by Invalid.
func Field(err error) (pointer, message string) {
	var f *fieldError
	if errors.As(err, &f) {
		return f.pointer, f.message
	}
	return "", ""
}

// Part is one canonical text Part of the evaluated Record Version.
type Part struct {
	Key, Role, Text string
}

// SourceIdentity is where the evaluated Record Version comes from.
type SourceIdentity struct {
	Namespace string `json:"namespace"`
	RecordKey string `json:"record_key"`
	// Position is the Source Position of the accepted revision, when the
	// producer sent one.
	Position string `json:"position,omitempty"`
}

// Origins of a Record Version.
const (
	OriginClient    = "client"
	OriginConnector = "connector"
)

// RecordProvenance is who produced the evaluated Record Version.
type RecordProvenance struct {
	// Origin is "connector" for a revision a Connector Instance acquired and
	// "client" for one an API client submitted.
	Origin          string               `json:"origin"`
	Producer        string               `json:"producer,omitempty"`
	ProducerVersion string               `json:"producer_version,omitempty"`
	Connector       *ConnectorOrigin     `json:"connector,omitempty"`
	Normalization   *NormalizationOrigin `json:"normalization,omitempty"`
}

// ConnectorOrigin names the Connector Instance that acquired a revision.
type ConnectorOrigin struct {
	InstanceID string `json:"instance_id"`
	Kind       string `json:"kind"`
}

// NormalizationOrigin names the external normalizer whose output a Version publishes.
type NormalizationOrigin struct {
	PluginID      string `json:"plugin_id"`
	PluginVersion string `json:"plugin_version"`
	Contribution  string `json:"contribution"`
	Fallback      bool   `json:"fallback,omitempty"`
}

// RecordMetadata is what a rule may test besides the text: source identity,
// acceptance time, provenance and the Version's extensions.
type RecordMetadata struct {
	Source     SourceIdentity   `json:"source"`
	AcceptedAt time.Time        `json:"accepted_at"`
	Provenance RecordProvenance `json:"provenance"`
	Extensions map[string]any   `json:"extensions,omitempty"`
}

// Article is the evaluated Record Version: its text Parts and metadata.
type Article struct {
	Parts    []Part
	Metadata RecordMetadata
}

// SubscriptionRef names a Subscription Version an evaluation stands for.
type SubscriptionRef struct {
	SubscriptionID        string `json:"subscription_id"`
	SubscriptionVersionID string `json:"subscription_version_id"`
	SavedQueryID          string `json:"saved_query_id"`
	SavedQueryVersionID   string `json:"saved_query_version_id"`
	// Owner is the Subscription Owner, absent for a global Subscription.
	Owner string `json:"owner,omitempty"`
}

// BatchItem is one distinct evaluation: a Saved Query expression and an
// evaluator configuration, shared by every Subscription it stands for.
type BatchItem struct {
	ID            string
	Expression    map[string]any
	Configuration map[string]any
	Subscriptions []SubscriptionRef
}

// Batch is the logical evaluation seam: one eligible Record Version and the
// distinct evaluations pinned to one evaluator.
type Batch struct {
	Organization string
	CorpusID     string
	RecordID     string
	VersionID    string
	// Enriched reports that the Version has embedding coverage in the active generation.
	Enriched bool
	Article  Article
	Items    []BatchItem
}

// Evaluation is a completed decision. Explanation, PartKeys and Details become
// the Match evidence when the Decision is DecisionMatch.
type Evaluation struct {
	Decision    Decision
	Explanation string
	PartKeys    []string
	Details     map[string]any
}

// Outcome is the answer to one BatchItem: a completed Evaluation or the
// error that kept this item from completing.
type Outcome struct {
	Evaluation
	Err error
}

// EvaluationPort is an installed evaluator. Implementations return an error,
// never a negative Decision, when they cannot decide.
type EvaluationPort interface {
	// MaxBatch is the most evaluations one Evaluate call accepts.
	MaxBatch() int
	// Validate checks a Saved Query expression and an evaluator configuration
	// when a Subscription Version pins them. It returns ErrInvalidExpression
	// or ErrInvalidEvaluatorConfiguration, wrapped by Invalid.
	Validate(expression, configuration map[string]any) error
	// Evaluate decides every item of the batch: one Outcome per item, in
	// order. An error fails the whole batch.
	Evaluate(ctx context.Context, b Batch) ([]Outcome, error)
}

// Evaluators are the installed evaluators keyed by EvaluatorKey.
type Evaluators map[string]EvaluationPort

// FixtureEvaluators installs only the deterministic fixture evaluator.
func FixtureEvaluators() Evaluators {
	return Evaluators{EvaluatorKey(Evaluator{PluginID: FixtureEvaluator, Version: FixtureEvaluatorVersion}): Fixture{}}
}

// Fixture is the deterministic test evaluator for notification mechanics; it
// is not a relevance algorithm, and it is installed only where a deployment
// enables it for tests. Its pinned configuration is
// {"decisions": {"<marker>": "<decision>", ..., "default": "<decision>"}}.
// Markers are literal substrings of Part text, checked in sorted order; the
// first present marker decides, otherwise "default" (absent: no_match).
// Decisions are match, no_match and not_ready, plus two fixture-only,
// test-oriented values: "error" fails the evaluation, and
// "match_after_enrichment" is not_ready until the Version is enriched.
type Fixture struct{}

// MaxBatch accepts the protocol's largest batch.
func (Fixture) MaxBatch() int { return 256 }

// Validate accepts any expression; the fixture reads only its configuration.
func (Fixture) Validate(map[string]any, map[string]any) error { return nil }

func (f Fixture) Evaluate(_ context.Context, b Batch) ([]Outcome, error) {
	out := make([]Outcome, len(b.Items))
	for i, item := range b.Items {
		ev, err := fixtureDecide(item.Configuration, b.Article.Parts, b.Enriched)
		out[i] = Outcome{Evaluation: ev, Err: err}
	}
	return out, nil
}

func fixtureDecide(configuration map[string]any, parts []Part, enriched bool) (Evaluation, error) {
	raw, ok := configuration["decisions"]
	if !ok {
		raw = map[string]any{}
	}
	decisions, ok := raw.(map[string]any)
	if !ok {
		return Evaluation{}, fmt.Errorf("%w: decisions must be an object", ErrEvaluatorConfiguration)
	}
	markers := make([]string, 0, len(decisions))
	for marker := range decisions {
		if marker != "default" && marker != "" {
			markers = append(markers, marker)
		}
	}
	sort.Strings(markers)
	chosen, value := "default", any("no_match")
	if v, ok := decisions["default"]; ok {
		value = v
	}
	var keys []string
	for _, marker := range markers {
		for _, p := range parts {
			if strings.Contains(p.Text, marker) {
				keys = append(keys, p.Key)
			}
		}
		if len(keys) > 0 {
			chosen, value = marker, decisions[marker]
			break
		}
	}
	decision, _ := value.(string)
	switch decision {
	case "error":
		return Evaluation{}, fmt.Errorf("%w: fixture error decision", ErrEvaluation)
	case "match_after_enrichment":
		if !enriched {
			decision = string(DecisionNotReady)
		} else {
			decision = string(DecisionMatch)
		}
	case string(DecisionMatch), string(DecisionNoMatch), string(DecisionNotReady):
	default:
		return Evaluation{}, fmt.Errorf("%w: unknown decision", ErrEvaluatorConfiguration)
	}
	return Evaluation{
		Decision:    Decision(decision),
		Explanation: fmt.Sprintf("Fixture evaluator decided %s from marker %q.", decision, truncate(chosen, 256)),
		PartKeys:    keys,
		Details:     map[string]any{"marker": truncate(chosen, 256), "decision": decision},
	}, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
