package monitoring

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
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
	// OutcomeVersionSuperseded: a later Subscription Version was already
	// effective at the trigger's position, so this Version does not judge it.
	OutcomeVersionSuperseded = "subscription_version_superseded"
	// OutcomeNoLongerMatches: a negative decision on a correction committed a
	// match.no_longer_matches notice for the prior positive Match.
	OutcomeNoLongerMatches = "no_longer_matches"
	// OutcomeWithdrawalNotified: a withdrawal intent committed its match.withdrawn notice.
	OutcomeWithdrawalNotified = "withdrawal_notified"
)

// Intent kinds. An evaluation intent runs the pinned evaluator on one Record
// Version; a withdrawal intent consumes a committed record.withdrawn event and
// runs no evaluator.
const (
	IntentEvaluation = "evaluation"
	IntentWithdrawal = "withdrawal"
)

// Intent is durable monitoring work for one Subscription Version and one
// committed trigger event. An evaluation intent names the evaluated Record
// Version; a withdrawal intent names the Record's latest matched Version.
type Intent struct {
	Kind                  string
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
	QueryVectors []QueryVector
	Subscription SubscriptionVersion
	Definition   Definition
	Enabled      bool
	// Superseded reports a later Subscription Version effective at the
	// intent's trigger position.
	Superseded bool
	Enriched   bool
	// Decided reports another intent of the same Subscription Version that
	// already decided the Record Version: this one completes as a duplicate.
	Decided bool
}

// MatchEvidence is the immutable, bounded evidence stored with a Match.
type MatchEvidence struct {
	Evaluator   Evaluator      `json:"evaluator"`
	Explanation string         `json:"explanation"`
	PartKeys    []string       `json:"part_keys,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

// MatchCommit is one validated positive decision in a Record Version group.
type MatchCommit struct {
	Intent   Intent
	Evidence MatchEvidence
}

// MatchBatchStore optionally commits ordered positive decisions for one
// Organization, Record and Record Version atomically. Outcomes follow input
// order; an error commits none of the group.
type MatchBatchStore interface {
	CommitMatches(ctx context.Context, matches []MatchCommit) ([]string, error)
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
	// ClaimRelated leases up to limit more due pending evaluation intents of
	// first's Record Version whose Subscription Version pins the same
	// evaluator plugin id and version, so one step can batch them.
	ClaimRelated(ctx context.Context, first Intent, evaluator Evaluator, limit int, lease time.Duration) ([]Intent, error)
	Target(ctx context.Context, in Intent) (Target, error)
	Complete(ctx context.Context, in Intent, outcome string) error
	// Retry keeps the intent pending with a bounded error code and a delay.
	Retry(ctx context.Context, in Intent, code string, delay time.Duration) error
	// CommitMatch rechecks eligibility atomically and creates the unique Match,
	// its logical Delivery, notice, public event and outbox work. It completes
	// the intent in the same transaction and returns the outcome.
	CommitMatch(ctx context.Context, in Intent, evidence MatchEvidence) (string, error)
	// CommitNoMatch records a completed negative decision. When the evaluated
	// Version is an eligible correction of a Record with a prior positive
	// Match, it atomically commits a match.no_longer_matches notice for that
	// Match; it never creates a Match. It completes the intent.
	CommitNoMatch(ctx context.Context, in Intent) (string, error)
	// CommitWithdrawal rechecks the Tombstone and the Subscription's Corpus
	// scope and idempotently commits the match.withdrawn notice for the
	// Record's latest positive Match, whether or not the Subscription is
	// enabled (admission parks it while disabled). It completes the intent.
	CommitWithdrawal(ctx context.Context, in Intent) (string, error)
	Backlog(ctx context.Context) (Backlog, error)
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
