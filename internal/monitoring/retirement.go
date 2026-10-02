package monitoring

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
)

const OutcomeEvaluatorRetired = "evaluator_retired"

var ErrInvalidRetirement = publicerr.InvalidInput

type EvaluationCounts struct {
	PluginID    string `json:"plugin_id"`
	Version     string `json:"version"`
	Pending     int    `json:"pending"`
	Erroring    int    `json:"erroring"`
	Unavailable int    `json:"unavailable"`
	Retired     int    `json:"retired"`
}

type EvaluationRetirementInput struct {
	Key      string `json:"key"`
	PluginID string `json:"plugin_id"`
	Version  string `json:"version"`
	Reason   string `json:"reason"`
	DryRun   bool   `json:"dry_run"`
	Limit    int    `json:"limit"`
}

type RetiredEvaluation struct {
	SubscriptionID        string `json:"subscription_id"`
	SubscriptionVersionID string `json:"subscription_version_id"`
	Sequence              int64  `json:"sequence"`
	CorpusID              string `json:"corpus_id"`
	RecordID              string `json:"record_id"`
	RecordVersionID       string `json:"record_version_id"`
	EventID               string `json:"event_id,omitempty"`
}

type EvaluationRetirement struct {
	EvaluationRetirementInput
	ID        string              `json:"retirement_id"`
	CreatedAt *time.Time          `json:"created_at,omitempty"`
	Outcome   string              `json:"outcome"`
	Items     []RetiredEvaluation `json:"items"`
	Remaining int                 `json:"remaining"`
	Leased    int                 `json:"leased"`
}

type EvaluationBacklogPage struct {
	Items []EvaluationCounts `json:"items"`
	Next  string             `json:"next_after,omitempty"`
}

type EvaluationAdministration interface {
	EvaluationBacklog(context.Context, string, []string, string, int) ([]EvaluationCounts, error)
	RetireEvaluations(context.Context, string, []string, EvaluationRetirementInput) (EvaluationRetirement, error)
	EvaluationRetirement(context.Context, string, []string, string) (EvaluationRetirement, error)
}

func evaluationScope(scope corpus.Scope) []string {
	if scope.AllCorpora() {
		return nil
	}
	corpora := append([]string{}, scope.Corpora...)
	sort.Strings(corpora)
	return slices.Compact(corpora)
}

func (s Service) EvaluationBacklog(ctx context.Context, scope corpus.Scope, after string, limit int) (EvaluationBacklogPage, error) {
	if !scope.Allows(MigrationAction) {
		return EvaluationBacklogPage{}, ErrForbidden
	}
	if s.Evaluations == nil {
		return EvaluationBacklogPage{}, ErrNotFound
	}
	if limit <= 0 {
		limit = DefaultMigrationLimit
	}
	limit = min(limit, MaxMigrationLimit)
	items, err := s.Evaluations.EvaluationBacklog(ctx, scope.Organization, evaluationScope(scope), after, limit+1)
	if err != nil {
		return EvaluationBacklogPage{}, err
	}
	page := EvaluationBacklogPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[limit-1]
		page.Next = EvaluatorKey(Evaluator{PluginID: last.PluginID, Version: last.Version})
	}
	return page, nil
}

func (s Service) RetireEvaluations(ctx context.Context, scope corpus.Scope, in EvaluationRetirementInput) (EvaluationRetirement, error) {
	if !scope.Allows(MigrationAction) {
		return EvaluationRetirement{}, ErrForbidden
	}
	if s.Evaluations == nil {
		return EvaluationRetirement{}, ErrNotFound
	}
	if strings.TrimSpace(in.Key) == "" || utf8.RuneCountInString(in.Key) > 128 || strings.TrimSpace(in.PluginID) == "" || utf8.RuneCountInString(in.PluginID) > 128 || strings.TrimSpace(in.Version) == "" || utf8.RuneCountInString(in.Version) > 128 || strings.TrimSpace(in.Reason) == "" || utf8.RuneCountInString(in.Reason) > 1024 || in.Limit < 0 || in.Limit > MaxMigrationLimit {
		return EvaluationRetirement{}, ErrInvalidRetirement
	}
	if in.Limit == 0 {
		in.Limit = DefaultMigrationLimit
	}
	return s.Evaluations.RetireEvaluations(ctx, scope.Organization, evaluationScope(scope), in)
}

func (s Service) EvaluationRetirement(ctx context.Context, scope corpus.Scope, id string) (EvaluationRetirement, error) {
	if !scope.Allows(MigrationAction) {
		return EvaluationRetirement{}, ErrForbidden
	}
	if s.Evaluations == nil {
		return EvaluationRetirement{}, ErrNotFound
	}
	return s.Evaluations.EvaluationRetirement(ctx, scope.Organization, evaluationScope(scope), id)
}
