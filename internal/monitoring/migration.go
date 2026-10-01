package monitoring

import (
	"context"
	"errors"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
)

// MigrationAction is the operator permission a migration needs: the plugin
// registry's (registry.Action).
const MigrationAction = "plugins:admin"

const (
	// DefaultMigrationLimit and MaxMigrationLimit bound the Subscriptions one
	// migration call looks at.
	DefaultMigrationLimit = 100
	MaxMigrationLimit     = 500
)

var (
	// ErrInvalidMigration refuses a migration from the version new
	// Subscription Versions already pin.
	ErrInvalidMigration = publicerr.New("invalid_migration")
	// ErrSubscriptionChanged leaves a Subscription that got another current
	// Version while the migration ran; running it again looks at it again.
	ErrSubscriptionChanged = publicerr.New("subscription_changed")
)

// EvaluatorMoves lists and moves the Subscriptions pinning an evaluator.
type EvaluatorMoves interface {
	// PinningSubscriptions lists up to limit Subscriptions of org that are
	// not deleted, enabled or not, whose current Version pins the evaluator
	// pluginID@version, with an ID after after, in ID order. A non-nil
	// corpora keeps only those whose every pinned Corpus it contains.
	PinningSubscriptions(ctx context.Context, org, pluginID, version string, corpora []string, after string, limit int) ([]Subscription, error)
	// MoveEvaluator makes current a new Version of from's Subscription that
	// keeps from's Saved Query Version, configuration and destination and
	// pins evaluator, with edit semantics: it judges only changes committed
	// after it, and Matches keep the Version that produced them. It returns
	// ErrConflict when from is no longer the current Version, and
	// ErrSubscriptionDeleted for a deleted Subscription.
	MoveEvaluator(ctx context.Context, org string, from SubscriptionVersion, evaluator Evaluator) (SubscriptionVersion, error)
}

// EvaluatorMigrationInput moves an Organization's Subscriptions from one
// version of an alert-rule plugin to the version the active Pipeline Plan
// serves.
type EvaluatorMigrationInput struct {
	PluginID    string
	FromVersion string
	// DryRun validates and reports without moving anything.
	DryRun bool
	// Limit bounds the Subscriptions looked at (DefaultMigrationLimit when 0).
	Limit int
	// After continues from the Next of a previous call.
	After string
}

// EvaluatorMigration is what a migration call did, or would do in a dry run.
type EvaluatorMigration struct {
	PluginID    string
	FromVersion string
	ToVersion   string
	DryRun      bool
	Moved       []MovedSubscription
	Refused     []RefusedSubscription
	// Next continues the migration past the Subscriptions looked at; "" once
	// none is left.
	Next string
}

// MovedSubscription is a Subscription the migration moved, or would move.
type MovedSubscription struct {
	SubscriptionID string
	FromVersionID  string
	// VersionID is the new current Version; "" in a dry run.
	VersionID string
}

// RefusedSubscription is a Subscription the migration leaves on its version,
// with the reason: its expression or configuration is invalid for the
// target version, or it changed during the migration.
type RefusedSubscription struct {
	SubscriptionID string
	VersionID      string
	Code           string
	Pointer        string
	Message        string
}

// MigrateEvaluator moves the Subscriptions of the key's Organization whose
// current Version pins pluginID@FromVersion to the version of pluginID the
// active plan serves. Each one's Saved Query expression and configuration
// must validate against that version's schemas first; one that does not is
// refused and stays where it is. A moved Subscription gets a new Version, as
// an edit would make it. Only Subscriptions whose every Corpus the key grants
// are looked at. Calls are bounded by Limit and continue with Next; running
// one again converges, since a moved Subscription no longer pins FromVersion.
func (s Service) MigrateEvaluator(ctx context.Context, scope corpus.Scope, in EvaluatorMigrationInput) (EvaluatorMigration, error) {
	if !scope.Allows(MigrationAction) {
		return EvaluatorMigration{}, ErrForbidden
	}
	if s.Moves == nil || s.Evaluators == nil {
		return EvaluatorMigration{}, ErrNotFound
	}
	to, ok := s.Evaluators.Serving(in.PluginID)
	if !ok {
		return EvaluatorMigration{}, ErrUnsupportedEvaluator
	}
	if to == in.FromVersion {
		return EvaluatorMigration{}, publicerr.WithDetail(ErrInvalidMigration, "%s@%s is the version new alerts already use", in.PluginID, to)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = DefaultMigrationLimit
	}
	limit = min(limit, MaxMigrationLimit)
	var corpora []string
	if !scope.AllCorpora() {
		corpora = append([]string{}, scope.Corpora...)
	}
	subs, err := s.Moves.PinningSubscriptions(ctx, scope.Organization, in.PluginID, in.FromVersion, corpora, in.After, limit)
	if err != nil {
		return EvaluatorMigration{}, err
	}
	out := EvaluatorMigration{PluginID: in.PluginID, FromVersion: in.FromVersion, ToVersion: to, DryRun: in.DryRun, Moved: []MovedSubscription{}, Refused: []RefusedSubscription{}}
	if len(subs) == limit {
		out.Next = subs[len(subs)-1].ID
	}
	for _, sub := range subs {
		if !covers(scope, sub.Current.CorpusIDs) || !covers(scope, sub.PinnedCorpusIDs) {
			continue
		}
		from := sub.Current
		target := Evaluator{PluginID: in.PluginID, Version: to, Configuration: from.Evaluator.Configuration}
		refuse := func(err error) {
			code, _ := publicerr.Code(err)
			pointer, message := Field(err)
			out.Refused = append(out.Refused, RefusedSubscription{SubscriptionID: sub.ID, VersionID: from.VersionID, Code: code, Pointer: pointer, Message: message})
		}
		query, err := s.Store.SavedQueryVersion(ctx, scope.Organization, from.SavedQueryID, from.SavedQueryVersionID)
		if err != nil {
			return out, err
		}
		if err = s.validPair(target, query); err != nil {
			if _, coded := publicerr.Code(err); !coded {
				return out, err
			}
			refuse(err)
			continue
		}
		moved := MovedSubscription{SubscriptionID: sub.ID, FromVersionID: from.VersionID}
		if !in.DryRun {
			v, err := s.Moves.MoveEvaluator(ctx, scope.Organization, from, target)
			switch {
			case errors.Is(err, ErrConflict):
				refuse(Invalid(ErrSubscriptionChanged, "", "the Subscription got another Version during the migration; run the migration again to move it"))
				continue
			case errors.Is(err, ErrSubscriptionDeleted):
				continue
			case err != nil:
				return out, err
			}
			moved.VersionID = v.VersionID
		}
		out.Moved = append(out.Moved, moved)
	}
	return out, nil
}
