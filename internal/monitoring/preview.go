package monitoring

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
)

// Preview bounds. Each previewed Record Version costs one evaluator call, and
// a call may cost money (a classifier behind the plugin), so the engine caps
// the Records one preview judges whatever the caller asks.
const (
	// MaxPreviewRecords is the most Record Versions one preview judges.
	MaxPreviewRecords = 50
	// DefaultPreviewRecords applies when the request names no limit.
	DefaultPreviewRecords = 20
	// defaultPreviewBudget leaves response headroom inside the HTTP request's
	// five-second deadline. It includes preparation and storage reads.
	defaultPreviewBudget = 4 * time.Second
	// previewCalls bounds the evaluator calls one preview runs at once.
	previewCalls = 8
	// PreviewID names the synthetic Subscription, Version and evaluation a
	// preview sends: a preview judges for no Subscription.
	PreviewID = "preview"
)

var (
	// ErrPreviewUnavailable fails a preview whose evaluator could not be
	// reached or declared a transient failure. Retrying may succeed.
	ErrPreviewUnavailable = publicerr.EvaluatorUnavailable
	// ErrPreviewFailed fails a preview whose evaluator refused or broke an
	// evaluation. A partial preview would claim that articles do not match.
	ErrPreviewFailed = publicerr.EvaluatorError
	// ErrPreviewTimeout reports a deadline before evaluation could produce a
	// partial result, or a caller deadline that expired first.
	ErrPreviewTimeout = publicerr.PreviewDeadlineExceeded
)

// PreviewInput asks how a proposed Subscription would have judged the most
// recent Records of its Corpora. It names either an inline Saved Query
// Definition or an existing Saved Query Version, and the evaluator to pin.
type PreviewInput struct {
	Definition          *Definition `json:"definition,omitempty"`
	SavedQueryID        string      `json:"saved_query_id,omitempty"`
	SavedQueryVersionID string      `json:"saved_query_version_id,omitempty"`
	Evaluator           Evaluator   `json:"evaluator"`
	// Limit is the most Record Versions to judge, newest first: 0 means
	// DefaultPreviewRecords, and it is capped at MaxPreviewRecords.
	Limit int `json:"limit,omitempty"`
	// AcceptedAfter, when set, keeps only revisions accepted after it.
	AcceptedAfter *time.Time `json:"accepted_after,omitempty"`
}

// RecentVersion is one current, eligible Record Version a preview may judge.
type RecentVersion struct {
	CorpusID   string
	RecordID   string
	VersionID  string
	AcceptedAt time.Time
	Enriched   bool
}

// RecentReader lists what a preview judges.
type RecentReader interface {
	// Recent lists up to limit current eligible Record Versions of the
	// Corpora, the most recently accepted first, accepted after after unless
	// it is zero. Eligibility is the one evaluation applies.
	Recent(ctx context.Context, org string, corpora []string, after time.Time, limit int) ([]RecentVersion, error)
}

// PreviewMatch is a Record Version the proposed Subscription would have matched.
type PreviewMatch struct {
	RecentVersion
	Evidence MatchEvidence
}

// PreviewResult is what a preview found. It is computed on request and never
// stored: a preview creates no Match, Delivery or journal event.
type PreviewResult struct {
	// Evaluated counts the Record Versions the evaluator decided. It is below
	// the limit when fewer Records exist, or when the time budget ran out.
	Evaluated int
	NotReady  int
	// Matches are the positive decisions, most recent first.
	Matches []PreviewMatch
	// Oldest is when the oldest decided Record Version was accepted (zero
	// when none was decided): the preview covers everything accepted since.
	Oldest time.Time
	// Complete reports that every listed Record Version was decided in time.
	Complete bool
}

// Preview runs a proposed Subscription's evaluator on the most recent eligible
// Record Versions of its Corpora and returns what it would have matched. The
// pair is validated as a Subscription creation validates it. Nothing is
// written: a preview has no Subscription, so it creates no Match, Delivery or
// event. Evaluator calls run under a time budget; Records still undecided when
// it runs out are left out, and an evaluator error fails the whole preview.
func (s Service) Preview(ctx context.Context, scope corpus.Scope, in PreviewInput, prepare ...func() (PreviewInput, error)) (result PreviewResult, err error) {
	if err := scope.Require(corpus.ActionMonitoringPreview); err != nil {
		return PreviewResult{}, err
	}
	for _, load := range prepare {
		var err error
		in, err = load()
		if err != nil {
			return PreviewResult{}, err
		}
	}
	if s.Recent == nil || s.Versions == nil {
		return PreviewResult{}, ErrNotFound
	}
	parent := ctx
	budget := s.PreviewBudget
	if budget <= 0 {
		budget = defaultPreviewBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	defer func() {
		if err != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded)) {
			err = ErrPreviewTimeout
		}
	}()
	query, err := s.previewQuery(ctx, scope, in)
	if err != nil {
		return PreviewResult{}, err
	}
	if !s.Evaluators.Serves(EvaluatorKey(in.Evaluator)) {
		return PreviewResult{}, ErrUnsupportedEvaluator
	}
	if tooLarge(in.Evaluator.Configuration) {
		return PreviewResult{}, ErrTooLarge
	}
	if err = s.validPair(in.Evaluator, query); err != nil {
		return PreviewResult{}, err
	}
	if in.Definition != nil && s.QueryEncoder != nil {
		query.QueryVectors, err = s.QueryEncoder.EncodeSavedQuery(ctx, scope.Organization, query.Definition)
		if err != nil {
			return PreviewResult{}, err
		}
	}
	limit := in.Limit
	if limit <= 0 {
		limit = DefaultPreviewRecords
	}
	limit = min(limit, MaxPreviewRecords)
	var after time.Time
	if in.AcceptedAfter != nil {
		after = *in.AcceptedAfter
	}
	recent, err := s.Recent.Recent(ctx, scope.Organization, query.Definition.CorpusIDs, after, limit)
	if err != nil {
		return PreviewResult{}, err
	}
	item := BatchItem{ID: PreviewID, Expression: query.Definition.Expression, Configuration: in.Evaluator.Configuration, QueryVectors: query.QueryVectors,
		Subscriptions: []SubscriptionRef{{SubscriptionID: PreviewID, SubscriptionVersionID: PreviewID, SavedQueryID: query.SavedQueryID, SavedQueryVersionID: query.VersionID}}}
	if item.Expression == nil {
		item.Expression = map[string]any{}
	}
	if item.Configuration == nil {
		item.Configuration = map[string]any{}
	}
	evaluator, _ := s.Evaluators.Evaluator(EvaluatorKey(in.Evaluator))
	decided := make([]*Evaluation, len(recent))
	errs := make([]error, len(recent))
	var wg sync.WaitGroup
	slots := make(chan struct{}, previewCalls)
	for i, r := range recent {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-slots }()
			decided[i], errs[i] = s.previewOne(ctx, evaluator, scope.Organization, r, item)
		}()
	}
	wg.Wait()
	out := PreviewResult{Complete: true}
	for i, r := range recent {
		if errs[i] != nil {
			// Out of time is not an evaluator failure: the Record is left out.
			if errors.Is(errs[i], context.DeadlineExceeded) && parent.Err() == nil {
				out.Complete = false
				continue
			}
			return PreviewResult{}, errs[i]
		}
		if decided[i] == nil {
			out.Complete = false
			continue
		}
		out.Evaluated++
		if out.Oldest.IsZero() || r.AcceptedAt.Before(out.Oldest) {
			out.Oldest = r.AcceptedAt
		}
		switch decided[i].Decision {
		case DecisionNotReady:
			out.NotReady++
		case DecisionMatch:
			out.Matches = append(out.Matches, PreviewMatch{RecentVersion: r, Evidence: MatchEvidence{
				Evaluator: in.Evaluator, Explanation: decided[i].Explanation, PartKeys: decided[i].PartKeys, Details: decided[i].Details}})
		}
	}
	if parent.Err() != nil {
		return PreviewResult{}, parent.Err()
	}
	return out, nil
}

// previewQuery is the Saved Query Version a preview judges with: the inline
// Definition, validated as a new Saved Query Version is, or an existing
// Version the key may pin.
func (s Service) previewQuery(ctx context.Context, scope corpus.Scope, in PreviewInput) (SavedQueryVersion, error) {
	inline := in.Definition != nil
	if inline == (in.SavedQueryID != "" || in.SavedQueryVersionID != "") {
		return SavedQueryVersion{}, ErrUnknownSavedQuery
	}
	if !inline {
		return s.pinnable(ctx, scope, in.SavedQueryID, in.SavedQueryVersionID)
	}
	if err := s.validDefinition(ctx, scope, *in.Definition); err != nil {
		return SavedQueryVersion{}, err
	}
	return SavedQueryVersion{SavedQueryID: PreviewID, VersionID: PreviewID, Definition: *in.Definition}, nil
}

// previewOne asks the evaluator about one Record Version. It returns nil and
// no error when the budget ran out first.
func (s Service) previewOne(ctx context.Context, evaluator EvaluationPort, org string, r RecentVersion, item BatchItem) (*Evaluation, error) {
	article, err := s.Versions.Article(ctx, org, r.CorpusID, r.RecordID, r.VersionID)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if err := loadVectors(ctx, evaluator, s.Versions, org, r.CorpusID, r.VersionID, &article); err != nil {
		return nil, err
	}
	outcomes, err := evaluator.Evaluate(ctx, Batch{Organization: org, CorpusID: r.CorpusID, RecordID: r.RecordID, VersionID: r.VersionID,
		Enriched: r.Enriched, Article: article, Items: []BatchItem{item}})
	if err == nil && len(outcomes) != 1 {
		err = ErrEvaluationInvalid
	}
	if err == nil {
		err = outcomes[0].Err
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, previewError(err)
	}
	ev := outcomes[0].Evaluation
	switch ev.Decision {
	case DecisionMatch:
		if !validEvidence(MatchEvidence{Explanation: ev.Explanation, PartKeys: ev.PartKeys, Details: ev.Details}, article.Parts) {
			return nil, publicerr.WithDetail(ErrPreviewFailed, "invalid evidence")
		}
	case DecisionNoMatch, DecisionNotReady:
	default:
		return nil, publicerr.WithDetail(ErrPreviewFailed, "unknown decision")
	}
	return &ev, nil
}

// previewError maps an evaluation error to the public failure of a preview.
func previewError(err error) error {
	if errors.Is(err, ErrEvaluatorUnavailable) || errors.Is(err, ErrEvaluationRetryable) {
		return publicerr.WithDetail(ErrPreviewUnavailable, "%s", boundedError(err))
	}
	return publicerr.WithDetail(ErrPreviewFailed, "%s", boundedError(err))
}
