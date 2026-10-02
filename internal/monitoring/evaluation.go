package monitoring

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
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
	Vectors  *ArticleVectors
}

type SegmentVector struct {
	SegmentID string    `json:"segment_id"`
	Vector    []float32 `json:"vector"`
}

type QueryVector struct {
	SpaceID string    `json:"vector_space_id"`
	Vector  []float32 `json:"vector"`
}

type ArticleVectors struct {
	SpaceID string
	Ready   bool
	Parts   map[string][]SegmentVector
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
	QueryVectors  []QueryVector
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

// EvaluatorSet resolves the installed evaluators by EvaluatorKey.
type EvaluatorSet interface {
	// Evaluator returns an installed evaluator, to judge the Subscription
	// Versions that pin it.
	Evaluator(key string) (EvaluationPort, bool)
	// Serves reports whether a new Subscription Version may pin key: an
	// evaluator kept only for the Subscription Versions that already pin it
	// is installed but not served.
	Serves(key string) bool
	// Serving returns the version of an alert-rule plugin that new
	// Subscription Versions pin.
	Serving(pluginID string) (version string, ok bool)
}

// Evaluator returns the evaluator installed under key.
func (e Evaluators) Evaluator(key string) (EvaluationPort, bool) {
	port, ok := e[key]
	return port, ok
}

// Serves reports whether key is installed: a plain set serves all it has.
func (e Evaluators) Serves(key string) bool {
	_, ok := e[key]
	return ok
}

// Serving returns the version installed for pluginID; when several are,
// the greatest key, so the answer does not depend on map order.
func (e Evaluators) Serving(pluginID string) (string, bool) {
	best := ""
	for key := range e {
		if version, ok := strings.CutPrefix(key, pluginID+"@"); ok && !strings.Contains(version, "@") && version > best {
			best = version
		}
	}
	return best, best != ""
}

// PlanEvaluators are the evaluators of a Pipeline Plan (Spec 5): Served are
// those its alert-rule roles name, which new Subscription Versions pin;
// Retained are other versions of those plugins that earlier plans named,
// kept installed so that the Subscription Versions pinning them are still
// judged by the version they recorded until an operator migrates them.
type PlanEvaluators struct {
	Served   Evaluators
	Retained Evaluators
}

func (p PlanEvaluators) Evaluator(key string) (EvaluationPort, bool) {
	if port, ok := p.Served[key]; ok {
		return port, true
	}
	port, ok := p.Retained[key]
	return port, ok
}

func (p PlanEvaluators) Serves(key string) bool { return p.Served.Serves(key) }

func (p PlanEvaluators) Serving(pluginID string) (string, bool) { return p.Served.Serving(pluginID) }

// LiveEvaluators are the evaluators of the plan a running api or worker
// follows, swapped whole when the plan changes.
type LiveEvaluators struct {
	current atomic.Pointer[PlanEvaluators]
}

// Store swaps in the evaluators of a new plan.
func (l *LiveEvaluators) Store(p PlanEvaluators) { l.current.Store(&p) }

// Load returns the current evaluators; none before the first Store.
func (l *LiveEvaluators) Load() PlanEvaluators {
	if p := l.current.Load(); p != nil {
		return *p
	}
	return PlanEvaluators{}
}

func (l *LiveEvaluators) Evaluator(key string) (EvaluationPort, bool) { return l.Load().Evaluator(key) }

func (l *LiveEvaluators) Serves(key string) bool { return l.Load().Serves(key) }

func (l *LiveEvaluators) Serving(pluginID string) (string, bool) { return l.Load().Serving(pluginID) }
