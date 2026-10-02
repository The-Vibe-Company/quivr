// Package backfill reprocesses a Corpus's past Versions with the active
// ingestion plugin (Spec 5): it fills vector spaces of the Corpus's routed
// generation, typically a new evaluation space, for the Versions that
// generation already serves. A dry run first reports the volume, duration
// and cost; the backfill then runs as a paced, checkpointed Operation below
// live ingestion, which the operator can pause, resume or cancel. It never
// creates a Record Version or a content event.
package backfill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
)

var (
	// ErrDryRunRequired refuses a backfill no dry run with the same key and
	// scope was recorded for.
	ErrDryRunRequired = publicerr.DryRunRequired
	// ErrCostConfirmationRequired refuses a backfill whose estimated cost
	// exceeds the deployment's threshold without confirm_cost.
	ErrCostConfirmationRequired = publicerr.CostConfirmationRequired
	// ErrRegistrationNotActive refuses a registration that is not the active
	// plan's ingestion plugin.
	ErrRegistrationNotActive = publicerr.RegistrationNotActive
	// ErrInvalid refuses target spaces or a window the backfill cannot use;
	// the error says why.
	ErrInvalid = publicerr.InvalidBackfill
	// ErrRebuildRequired refuses a Corpus whose routed generation predates
	// named vector spaces: only a rebuild can give it new spaces.
	ErrRebuildRequired = publicerr.RebuildRequired
	// ErrInProgress refuses a backfill of a Corpus another backfill has not
	// finished: both would rewrite the same segments' vectors.
	ErrInProgress = publicerr.BackfillInProgress
)

// invalid wraps ErrInvalid with the reason.
func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Request is one backfill command. The scope is a Corpus and, optionally, a
// window on when Quivr accepted its Versions: after inclusive, before
// exclusive.
type Request struct {
	Key            string
	CorpusID       string
	AcceptedAfter  *time.Time
	AcceptedBefore *time.Time
	// RegistrationID defaults to the active plan's ingestion plugin and
	// must be it.
	RegistrationID string
	// Spaces default to the registry's evaluation spaces that plugin owns.
	Spaces      []string
	DryRun      bool
	ConfirmCost bool
}

// Settings are the deployment's backfill settings.
type Settings struct {
	// Rate is how many Versions per second a backfill processes at most.
	Rate float64
	// Poll is how often a paused backfill checks whether it was resumed or
	// canceled.
	Poll time.Duration
	// MaxCostWithoutConfirmation is the estimated cost, in US dollars, above
	// which a backfill needs confirm_cost.
	MaxCostWithoutConfirmation float64
}

// Default backfill settings.
const (
	DefaultRate = 2.0
	DefaultPoll = 5 * time.Second
)

// WithDefaults fills unset settings.
func (s Settings) WithDefaults() Settings {
	if s.Rate <= 0 {
		s.Rate = DefaultRate
	}
	if s.Poll <= 0 {
		s.Poll = DefaultPoll
	}
	return s
}

// Ingestion is the active plan's ingestion plugin as a backfill sees it.
type Ingestion struct {
	RegistrationID    string
	PluginID, Version string
	// Prices are the declared price of each space the plugin owns, by space
	// key, in US dollars per million input tokens; nil when not declared.
	Prices map[string]*float64
}

// Owns reports whether the plugin declares a space key.
func (i Ingestion) Owns(space string) bool {
	_, ok := i.Prices[space]
	return ok
}

// Plans reads the active plan's ingestion plugin.
type Plans interface {
	ActiveIngestion(ctx context.Context) (Ingestion, error)
}

// RegistrationPlans resolves an explicitly selected active ingestion owner.
type RegistrationPlans interface {
	RegistrationIngestion(context.Context, string) (Ingestion, error)
}

// Size is how much of a scope a backfill has to fill: the Versions whose
// segments all hold a vector in the served space but miss one in a target
// space, their segments and the code points of those segments' text.
type Size struct {
	Versions, Segments, CodePoints int64
}

// Store keeps backfill requests and reads their scope.
type Store interface {
	// RegisteredSpaces lists the vector space registry.
	RegisteredSpaces(ctx context.Context) ([]content.RegisteredSpace, error)
	// BackfillSize measures a scope in the Corpus's routed generation; it is
	// ErrRebuildRequired when that generation predates named spaces.
	BackfillSize(ctx context.Context, org string, spec operations.Backfill, corpusID string) (Size, error)
	// RecordEstimate keeps a dry run under the request key, replacing an
	// earlier dry run of the same canonical request; another canonical
	// request under the key is operations.ErrConflict.
	RecordEstimate(ctx context.Context, org, corpusID, key string, canonical []byte, e operations.BackfillEstimate) error
	// BackfillEstimate returns the dry run recorded under the key, or
	// corpus.ErrNotFound.
	BackfillEstimate(ctx context.Context, org, corpusID, key string) ([]byte, operations.BackfillEstimate, error)
	// AcceptBackfill commits a queued backfill Operation, or returns the one
	// already accepted for the same key and canonical request; another
	// canonical request under the key is operations.ErrConflict, and a
	// Corpus another backfill has not finished is ErrInProgress. It pins the
	// backfill to the active plan, which must name spec's registration for
	// ingestion (ErrRegistrationNotActive otherwise).
	AcceptBackfill(ctx context.Context, org, corpusID, key string, canonical []byte, spec operations.Backfill) (operations.Operation, error)
	// BackfillByKey returns the backfill accepted under a key, or
	// corpus.ErrNotFound.
	BackfillByKey(ctx context.Context, org, corpusID, key string) (operations.Operation, error)
}

// Throughput reads how long one Version recently took.
type Throughput interface {
	// SecondsPerVersion is the mean seconds one Version took in recent
	// backfills, or else in the plugin's recent segment_and_embed calls, and
	// the basis it came from; 0 and "" when nothing was measured.
	SecondsPerVersion(ctx context.Context, org, pluginID, version string) (float64, string, error)
}

// Duration bases of an estimate.
const (
	BasisBackfills = "recent_backfills"
	BasisPlugin    = "recent_plugin_calls"
	BasisRate      = "rate"
)

// Service validates backfill requests, estimates them and accepts them.
type Service struct {
	Store      Store
	Plans      Plans
	Throughput Throughput
	Settings   Settings
}

// Request answers a dry run with its estimate, which it records, or accepts
// a backfill a dry run with the same key and scope preceded. The caller needs
// plugins:admin on the Corpus.
func (s Service) Request(ctx context.Context, scope corpus.Scope, r Request, prepare ...func() (Request, error)) (operations.BackfillEstimate, operations.Operation, error) {
	var none operations.BackfillEstimate
	if err := scope.Require(corpus.ActionBackfillRequest); err != nil {
		return none, operations.Operation{}, err
	}

	for _, load := range prepare {
		var err error
		r, err = load()
		if err != nil {
			return none, operations.Operation{}, err
		}
	}
	if r.CorpusID == "" || !scope.Contains(r.CorpusID) {
		return none, operations.Operation{}, corpus.ErrNotFound
	}
	if r.AcceptedAfter != nil && r.AcceptedBefore != nil && !r.AcceptedAfter.Before(*r.AcceptedBefore) {
		return none, operations.Operation{}, invalid("accepted_after must be before accepted_before")
	}
	if !r.DryRun {
		// An accepted key replays its backfill even after the plan, the
		// registry or the threshold changed.
		op, err := s.Store.BackfillByKey(ctx, scope.Organization, r.CorpusID, r.Key)
		switch {
		case err == nil && op.Backfill != nil && sameRequest(r, *op.Backfill):
			return op.Backfill.Estimate, op, nil
		case err == nil:
			return none, operations.Operation{}, operations.ErrConflict
		case !errors.Is(err, corpus.ErrNotFound):
			return none, operations.Operation{}, err
		}
	}
	spec, ingestion, err := s.resolve(ctx, r)
	if err != nil {
		return none, operations.Operation{}, err
	}
	canonical, err := canonicalRequest(r.Key, r.CorpusID, spec)
	if err != nil {
		return none, operations.Operation{}, err
	}
	settings := s.Settings.WithDefaults()
	if r.DryRun {
		size, err := s.Store.BackfillSize(ctx, scope.Organization, spec, r.CorpusID)
		if err != nil {
			return none, operations.Operation{}, err
		}
		seconds, basis := 0.0, ""
		if s.Throughput != nil {
			if seconds, basis, err = s.Throughput.SecondsPerVersion(ctx, scope.Organization, ingestion.PluginID, ingestion.Version); err != nil {
				return none, operations.Operation{}, err
			}
		}
		e := Estimate(size, spec, ingestion, seconds, basis, settings)
		if err = s.Store.RecordEstimate(ctx, scope.Organization, r.CorpusID, r.Key, canonical, e); err != nil {
			return none, operations.Operation{}, err
		}
		return e, operations.Operation{}, nil
	}
	recorded, e, err := s.Store.BackfillEstimate(ctx, scope.Organization, r.CorpusID, r.Key)
	if errors.Is(err, corpus.ErrNotFound) {
		return none, operations.Operation{}, ErrDryRunRequired
	}
	if err != nil {
		return none, operations.Operation{}, err
	}
	if string(recorded) != string(canonical) {
		return none, operations.Operation{}, operations.ErrConflict
	}
	// The threshold is the one in force now: a lower one set since the dry
	// run still asks for confirmation.
	if e.EstimatedCostUSD != nil && *e.EstimatedCostUSD > settings.MaxCostWithoutConfirmation && !r.ConfirmCost {
		return e, operations.Operation{}, ErrCostConfirmationRequired
	}
	spec.Estimate = e
	op, err := s.Store.AcceptBackfill(ctx, scope.Organization, r.CorpusID, r.Key, canonical, spec)
	return e, op, err
}

// resolve checks the registration and the target spaces against the active
// plan and the registry.
func (s Service) resolve(ctx context.Context, r Request) (operations.Backfill, Ingestion, error) {
	ingestion, err := s.Plans.ActiveIngestion(ctx)
	if r.RegistrationID != "" {
		if p, ok := s.Plans.(RegistrationPlans); ok {
			ingestion, err = p.RegistrationIngestion(ctx, r.RegistrationID)
		}
	}
	if err != nil {
		return operations.Backfill{}, ingestion, err
	}
	if r.RegistrationID != "" && r.RegistrationID != ingestion.RegistrationID {
		return operations.Backfill{}, ingestion, ErrRegistrationNotActive
	}
	registered, err := s.Store.RegisteredSpaces(ctx)
	if err != nil {
		return operations.Backfill{}, ingestion, err
	}
	roles := map[string]string{}
	for _, sp := range registered {
		roles[sp.ID] = sp.Role
	}
	spaces := slices.Clone(r.Spaces)
	if len(spaces) == 0 {
		for _, sp := range registered {
			if sp.Role == content.SpaceEvaluation && ingestion.Owns(sp.ID) {
				spaces = append(spaces, sp.ID)
			}
		}
		if len(spaces) == 0 {
			return operations.Backfill{}, ingestion, invalid("%s@%s has no evaluation vector space to fill; name the spaces to fill", ingestion.PluginID, ingestion.Version)
		}
	}
	sort.Strings(spaces)
	spaces = slices.Compact(spaces)
	for _, id := range spaces {
		switch role := roles[id]; {
		case !ingestion.Owns(id):
			return operations.Backfill{}, ingestion, invalid("vector space %s is not declared by %s@%s", id, ingestion.PluginID, ingestion.Version)
		case role != content.SpaceServed && role != content.SpaceEvaluation:
			return operations.Backfill{}, ingestion, invalid("vector space %s is not served or evaluated by this deployment; enable it in the plugin's spaces", id)
		}
	}
	return operations.Backfill{RegistrationID: ingestion.RegistrationID, Spaces: spaces, AcceptedAfter: utc(r.AcceptedAfter), AcceptedBefore: utc(r.AcceptedBefore)}, ingestion, nil
}

// sameRequest reports whether a request names the scope a backfill was
// accepted for; the registration and spaces it leaves out are the ones
// resolved then.
func sameRequest(r Request, b operations.Backfill) bool {
	sameTime := func(a, b *time.Time) bool { return (a == nil) == (b == nil) && (a == nil || a.Equal(*b)) }
	if !sameTime(r.AcceptedAfter, b.AcceptedAfter) || !sameTime(r.AcceptedBefore, b.AcceptedBefore) {
		return false
	}
	if r.RegistrationID != "" && r.RegistrationID != b.RegistrationID {
		return false
	}
	if len(r.Spaces) == 0 {
		return true
	}
	spaces := slices.Clone(r.Spaces)
	sort.Strings(spaces)
	return slices.Equal(slices.Compact(spaces), b.Spaces)
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// canonicalRequest is what idempotent replay compares: the scope, the
// registration and the spaces, never dry_run or confirm_cost.
func canonicalRequest(key, corpusID string, spec operations.Backfill) ([]byte, error) {
	format := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.Format(time.RFC3339Nano)
	}
	return json.Marshal(struct {
		Key            string   `json:"idempotency_key"`
		Corpus         string   `json:"corpus_id"`
		AcceptedAfter  string   `json:"accepted_after"`
		AcceptedBefore string   `json:"accepted_before"`
		Registration   string   `json:"registration_id"`
		Spaces         []string `json:"spaces"`
	}{key, corpusID, format(spec.AcceptedAfter), format(spec.AcceptedBefore), spec.RegistrationID, spec.Spaces})
}

// codePointsPerToken estimates input tokens from text length: about one
// token per four code points, the usual rule of thumb for subword models.
const codePointsPerToken = 4

// Estimate is the dry run of a scope: its size, the input tokens the plugin
// embeds, the cost when every priced target space declares its price, and
// how long it should take at the deployment's rate and the measured
// throughput.
func Estimate(size Size, spec operations.Backfill, ingestion Ingestion, secondsPerVersion float64, basis string, settings Settings) operations.BackfillEstimate {
	settings = settings.WithDefaults()
	e := operations.BackfillEstimate{RegistrationID: spec.RegistrationID, Spaces: spec.Spaces, Versions: size.Versions, Segments: size.Segments,
		InputTokens: (size.CodePoints + codePointsPerToken - 1) / codePointsPerToken}
	var cost float64
	priced := false
	for _, space := range spec.Spaces {
		if price := ingestion.Prices[space]; price != nil {
			priced = true
			cost += float64(e.InputTokens) * *price / 1e6
		}
	}
	if priced {
		// Cents are the finest unit an operator decides on.
		rounded := math.Ceil(cost*100) / 100
		e.EstimatedCostUSD = &rounded
		e.ConfirmationRequired = rounded > settings.MaxCostWithoutConfirmation
	}
	pace := 1 / settings.Rate
	e.DurationBasis = BasisRate
	if secondsPerVersion > pace {
		pace, e.DurationBasis = secondsPerVersion, basis
	}
	e.EstimatedSeconds = math.Ceil(float64(size.Versions) * pace)
	return e
}
