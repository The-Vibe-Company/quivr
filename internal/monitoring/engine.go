package monitoring

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"
	"unicode/utf8"
)

// ErrNoWork reports that no evaluation intent is currently claimable.
var ErrNoWork = errors.New("no evaluation work")

// Evaluation outcomes recorded on a completed intent.
const (
	OutcomeMatched              = "matched"
	OutcomeDuplicate            = "duplicate"
	OutcomeNoMatch              = "no_match"
	OutcomeNotReady             = "not_ready"
	OutcomeIneligible           = "ineligible"
	OutcomeSubscriptionDisabled = "subscription_disabled"
)

// Intent is durable evaluation work for one Subscription Version and one
// committed trigger event naming a Record Version.
type Intent struct {
	Organization          string
	SubscriptionID        string
	SubscriptionVersionID string
	Sequence              int64
	CorpusID              string
	RecordID              string
	VersionID             string
	Attempts              int
}

// Target is the pinned configuration an intent evaluates, read before the
// evaluator runs. Commit rechecks everything that matters under the lock.
type Target struct {
	Subscription SubscriptionVersion
	Definition   Definition
	Enabled      bool
	Enriched     bool
}

// MatchEvidence is the immutable, bounded evidence stored with a Match.
type MatchEvidence struct {
	Evaluator   Evaluator      `json:"evaluator"`
	Explanation string         `json:"explanation"`
	PartKeys    []string       `json:"part_keys,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

// Backlog is the bounded diagnostic view of evaluation work.
type Backlog struct {
	Pending  int
	Erroring int
}

// EvaluationStore owns durable dispatch, claims and the atomic Match commit.
type EvaluationStore interface {
	// FanOut advances bounded dispatch checkpoints, turning committed trigger
	// events after each Subscription's activation into intents. It returns the
	// number of steps that made progress.
	FanOut(ctx context.Context) (int, error)
	// Claim leases one due pending intent or returns ErrNoWork.
	Claim(ctx context.Context, lease time.Duration) (Intent, error)
	Target(ctx context.Context, in Intent) (Target, error)
	Complete(ctx context.Context, in Intent, outcome string) error
	// Retry keeps the intent pending with a bounded error code and a delay.
	Retry(ctx context.Context, in Intent, code string, delay time.Duration) error
	// CommitMatch rechecks eligibility atomically and creates the unique Match,
	// its logical Delivery, notice, public event and outbox work. It completes
	// the intent in the same transaction and returns the outcome.
	CommitMatch(ctx context.Context, in Intent, evidence MatchEvidence) (string, error)
	Backlog(ctx context.Context) (Backlog, error)
}

// VersionReader reads the canonical text Parts of a Record Version.
type VersionReader interface {
	Parts(ctx context.Context, org, corpusID, recordID, versionID string) ([]Part, error)
}

// Engine runs evaluation with bounded concurrency. Durable state lives in the
// store; a crash loses at most a lease, after which work is reclaimed and
// converges on the same Match identity.
type Engine struct {
	Store      EvaluationStore
	Versions   VersionReader
	Evaluators map[string]EvaluationPort
	Workers    int
	Lease      time.Duration
	Poll       time.Duration
}

// EvaluatorKey identifies an installed evaluator implementation.
func EvaluatorKey(e Evaluator) string { return e.PluginID + "@" + e.Version }

const (
	maxExplanation = 4096
	maxPartKeys    = 100
	maxBackoff     = 5 * time.Minute
)

// Run dispatches and evaluates until ctx ends.
func (e Engine) Run(ctx context.Context) {
	workers, poll := max(e.Workers, 1), e.Poll
	if poll <= 0 {
		poll = 200 * time.Millisecond
	}
	var wg sync.WaitGroup
	wg.Add(workers + 1)
	go func() {
		defer wg.Done()
		e.loop(ctx, poll, func(ctx context.Context) (bool, error) {
			n, err := e.Store.FanOut(ctx)
			return n > 0, err
		}, "evaluation dispatch unavailable")
	}()
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			e.loop(ctx, poll, e.Step, "evaluation worker unavailable")
		}()
	}
	e.report(ctx)
	wg.Wait()
}

func (e Engine) loop(ctx context.Context, poll time.Duration, step func(context.Context) (bool, error), warning string) {
	for ctx.Err() == nil {
		attempt, cancel := context.WithTimeout(ctx, 30*time.Second)
		progressed, err := step(attempt)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn(warning, "error", boundedError(err))
		}
		if progressed && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(poll):
		}
	}
}

// report logs bounded backlog diagnostics periodically.
func (e Engine) report(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		read, cancel := context.WithTimeout(ctx, 5*time.Second)
		b, err := e.Store.Backlog(read)
		cancel()
		if err == nil {
			slog.Info("evaluation backlog", "pending_intents", b.Pending, "erroring_intents", b.Erroring)
		}
	}
}

// Step claims and processes one intent. It returns false when no work was due.
func (e Engine) Step(ctx context.Context) (bool, error) {
	in, err := e.Store.Claim(ctx, e.lease())
	if errors.Is(err, ErrNoWork) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	target, err := e.Store.Target(ctx, in)
	if errors.Is(err, ErrNotFound) {
		return true, e.Store.Complete(ctx, in, OutcomeIneligible)
	}
	if err != nil {
		return true, e.retry(ctx, in, "storage_unavailable")
	}
	if !target.Enabled {
		return true, e.Store.Complete(ctx, in, OutcomeSubscriptionDisabled)
	}
	evaluator, ok := e.Evaluators[EvaluatorKey(target.Subscription.Evaluator)]
	if !ok {
		return true, e.retry(ctx, in, "evaluator_unavailable")
	}
	parts, err := e.Versions.Parts(ctx, in.Organization, in.CorpusID, in.RecordID, in.VersionID)
	if err != nil {
		return true, e.retry(ctx, in, "content_unavailable")
	}
	result, err := evaluator.Evaluate(ctx, EvaluationInput{Definition: target.Definition, Evaluator: target.Subscription.Evaluator, RecordID: in.RecordID, VersionID: in.VersionID, Parts: parts, Enriched: target.Enriched})
	if err != nil {
		code := "evaluator_error"
		if errors.Is(err, ErrEvaluatorConfiguration) {
			code = "evaluator_configuration_invalid"
		}
		return true, e.retry(ctx, in, code)
	}
	switch result.Decision {
	case DecisionNoMatch:
		return true, e.Store.Complete(ctx, in, OutcomeNoMatch)
	case DecisionNotReady:
		// A later trigger (for example enrichment) creates a new intent.
		return true, e.Store.Complete(ctx, in, OutcomeNotReady)
	case DecisionMatch:
	default:
		return true, e.retry(ctx, in, "evaluation_invalid")
	}
	evidence := MatchEvidence{Evaluator: target.Subscription.Evaluator, Explanation: result.Explanation, PartKeys: result.PartKeys, Details: result.Details}
	if !validEvidence(evidence, parts) {
		return true, e.retry(ctx, in, "evaluation_invalid")
	}
	outcome, err := e.Store.CommitMatch(ctx, in, evidence)
	if err != nil {
		return true, e.retry(ctx, in, "storage_unavailable")
	}
	slog.Info("evaluation committed", "organization", in.Organization, "subscription_id", in.SubscriptionID, "record_version_id", in.VersionID, "outcome", outcome)
	return true, nil
}

func (e Engine) lease() time.Duration {
	if e.Lease <= 0 {
		return time.Minute
	}
	return e.Lease
}

// retry keeps the intent pending with jittered exponential backoff. An error
// is never recorded as a negative decision.
func (e Engine) retry(ctx context.Context, in Intent, code string) error {
	delay := time.Second << min(in.Attempts, 9)
	delay = min(delay+time.Duration(rand.Int64N(int64(delay)/2+1)), maxBackoff)
	slog.Warn("evaluation pending", "organization", in.Organization, "subscription_id", in.SubscriptionID, "record_version_id", in.VersionID, "error_code", code, "attempts", in.Attempts+1)
	return e.Store.Retry(ctx, in, code, delay)
}

// validEvidence bounds evaluator output and checks that referenced Parts exist.
func validEvidence(ev MatchEvidence, parts []Part) bool {
	if ev.Explanation == "" || utf8.RuneCountInString(ev.Explanation) > maxExplanation || len(ev.PartKeys) > maxPartKeys {
		return false
	}
	known := map[string]bool{}
	for _, p := range parts {
		known[p.Key] = true
	}
	for _, k := range ev.PartKeys {
		if !known[k] {
			return false
		}
	}
	b, err := json.Marshal(ev.Details)
	return err == nil && len(b) <= maxPinnedBytes
}

func boundedError(err error) string {
	s := err.Error()
	if len(s) > 200 {
		return s[:200]
	}
	return s
}
