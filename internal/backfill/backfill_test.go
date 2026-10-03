package backfill_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/backfill"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
)

func price(usd float64) *float64 { return &usd }

// The estimate prices only the target spaces that declare a price, rounds up
// to the cent, asks for confirmation above the threshold, and never promises
// a pace faster than the configured rate.
func TestEstimate(t *testing.T) {
	ingestion := backfill.Ingestion{RegistrationID: "reg", Prices: map[string]*float64{"p.small@1": nil, "p.large@1": price(0.02)}}
	size := backfill.Size{Versions: 10, Segments: 40, CodePoints: 4_000_001}
	for _, tc := range []struct {
		name      string
		spaces    []string
		measured  float64
		threshold float64
		cost      *float64
		confirm   bool
		seconds   float64
		basis     string
	}{
		{"unpriced space: cost unknown", []string{"p.small@1"}, 0, 0, nil, false, 5, backfill.BasisRate},
		// 1,000,001 tokens at $0.02 per million is $0.02000002, rounded up.
		{"priced space above the threshold", []string{"p.large@1", "p.small@1"}, 0, 0.02, price(0.03), true, 5, backfill.BasisRate},
		{"priced space under the threshold", []string{"p.large@1"}, 0, 0.05, price(0.03), false, 5, backfill.BasisRate},
		{"measured throughput slower than the rate", []string{"p.small@1"}, 1.5, 0, nil, false, 15, backfill.BasisBackfills},
		{"measured throughput faster than the rate", []string{"p.small@1"}, 0.1, 0, nil, false, 5, backfill.BasisRate},
	} {
		e := backfill.Estimate(size, operations.Backfill{RegistrationID: "reg", Spaces: tc.spaces}, ingestion, tc.measured, backfill.BasisBackfills, backfill.Settings{Rate: 2, MaxCostWithoutConfirmation: tc.threshold})
		if e.InputTokens != 1_000_001 || e.Versions != 10 || e.Segments != 40 {
			t.Errorf("%s: size %+v", tc.name, e)
		}
		if (e.EstimatedCostUSD == nil) != (tc.cost == nil) || (tc.cost != nil && *e.EstimatedCostUSD != *tc.cost) {
			t.Errorf("%s: cost %v, want %v", tc.name, e.EstimatedCostUSD, tc.cost)
		}
		if e.ConfirmationRequired != tc.confirm || e.EstimatedSeconds != tc.seconds || e.DurationBasis != tc.basis {
			t.Errorf("%s: confirm %v seconds %v basis %q", tc.name, e.ConfirmationRequired, e.EstimatedSeconds, e.DurationBasis)
		}
	}
}

type fakePlans struct{ ingestion backfill.Ingestion }

func (p fakePlans) ActiveIngestion(context.Context) (backfill.Ingestion, error) {
	return p.ingestion, nil
}

type estimateKey struct{ corpus, key string }

type fakeStore struct {
	spaces    []content.RegisteredSpace
	estimates map[estimateKey][]byte
	recorded  map[estimateKey]operations.BackfillEstimate
	accepted  []operations.Backfill
	byKey     map[estimateKey]operations.Operation
}

func (s *fakeStore) BackfillByKey(_ context.Context, _, corpusID, key string) (operations.Operation, error) {
	if op, ok := s.byKey[estimateKey{corpusID, key}]; ok {
		return op, nil
	}
	return operations.Operation{}, corpus.ErrNotFound
}

func (s *fakeStore) RegisteredSpaces(context.Context) ([]content.RegisteredSpace, error) {
	return s.spaces, nil
}
func (s *fakeStore) BackfillSize(context.Context, string, operations.Backfill, string) (backfill.Size, error) {
	return backfill.Size{Versions: 3, Segments: 6, CodePoints: 400_000_000}, nil
}
func (s *fakeStore) RecordEstimate(_ context.Context, _, corpusID, key string, canonical []byte, e operations.BackfillEstimate) error {
	k := estimateKey{corpusID, key}
	if prior, ok := s.estimates[k]; ok && string(prior) != string(canonical) {
		return operations.ErrConflict
	}
	s.estimates[k], s.recorded[k] = canonical, e
	return nil
}
func (s *fakeStore) BackfillEstimate(_ context.Context, _, corpusID, key string) ([]byte, operations.BackfillEstimate, error) {
	k := estimateKey{corpusID, key}
	canonical, ok := s.estimates[k]
	if !ok {
		return nil, operations.BackfillEstimate{}, corpus.ErrNotFound
	}
	return canonical, s.recorded[k], nil
}
func (s *fakeStore) AcceptBackfill(_ context.Context, org, corpusID, key string, _ []byte, spec operations.Backfill) (operations.Operation, error) {
	s.accepted = append(s.accepted, spec)
	op := operations.Operation{ID: "op", Organization: org, Kind: operations.KindBackfill, CorpusID: corpusID, State: operations.StateQueued, Backfill: &spec}
	s.byKey[estimateKey{corpusID, key}] = op
	return op, nil
}

// A backfill starts only after a dry run of the same scope under the same
// key, and past the cost threshold only with confirm_cost; its targets
// default to the evaluation spaces the active plugin owns.
func TestRequestNeedsADryRunAndCostConfirmation(t *testing.T) {
	store := &fakeStore{estimates: map[estimateKey][]byte{}, recorded: map[estimateKey]operations.BackfillEstimate{}, byKey: map[estimateKey]operations.Operation{}, spaces: []content.RegisteredSpace{
		{VectorSpace: content.VectorSpace{ID: "p.small@1"}, Role: content.SpaceServed},
		{VectorSpace: content.VectorSpace{ID: "p.large@1"}, Role: content.SpaceEvaluation},
		{VectorSpace: content.VectorSpace{ID: "p.old@1"}, Role: content.SpaceRetired},
		{VectorSpace: content.VectorSpace{ID: "other.space@1"}, Role: content.SpaceEvaluation},
	}}
	ingestion := backfill.Ingestion{RegistrationID: "reg", PluginID: "p", Version: "1.0.0", Prices: map[string]*float64{"p.small@1": nil, "p.large@1": price(0.02), "p.old@1": nil}}
	service := backfill.Service{Store: store, Registry: store, Plans: fakePlans{ingestion}, Settings: backfill.Settings{MaxCostWithoutConfirmation: 1}}
	ctx := context.Background()
	operator := corpus.Scope{Organization: "org", Actions: []string{operations.BackfillPermission}, Corpora: []string{"*"}}
	after := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	request := backfill.Request{Key: "k", CorpusID: "corpus_a", AcceptedAfter: &after}

	for _, tc := range []struct {
		name  string
		scope corpus.Scope
		edit  func(*backfill.Request)
		want  error
	}{
		{"without plugins:admin", corpus.Scope{Organization: "org", Actions: []string{"operations:write"}, Corpora: []string{"*"}}, nil, corpus.ErrForbidden},
		{"Corpus outside the key", corpus.Scope{Organization: "org", Actions: []string{operations.BackfillPermission}, Corpora: []string{"corpus_b"}}, nil, corpus.ErrNotFound},
		{"no dry run yet", operator, nil, backfill.ErrDryRunRequired},
		{"another registration", operator, func(r *backfill.Request) { r.RegistrationID = "reg_other" }, backfill.ErrRegistrationNotActive},
		{"a space of another plugin", operator, func(r *backfill.Request) { r.Spaces = []string{"other.space@1"} }, backfill.ErrInvalid},
		{"a retired space", operator, func(r *backfill.Request) { r.Spaces = []string{"p.old@1"} }, backfill.ErrInvalid},
		{"an empty window", operator, func(r *backfill.Request) { before := after; r.AcceptedBefore = &before }, backfill.ErrInvalid},
	} {
		r := request
		if tc.edit != nil {
			tc.edit(&r)
		}
		if _, _, err := service.Request(ctx, tc.scope, r); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}

	dry := request
	dry.DryRun = true
	e, _, err := service.Request(ctx, operator, dry)
	// 100,000,000 tokens at $0.02 per million: $2, above the $1 threshold.
	if err != nil || len(e.Spaces) != 1 || e.Spaces[0] != "p.large@1" || e.EstimatedCostUSD == nil || *e.EstimatedCostUSD != 2 || !e.ConfirmationRequired {
		t.Fatalf("dry run %+v %v", e, err)
	}
	if _, _, err = service.Request(ctx, operator, request); !errors.Is(err, backfill.ErrCostConfirmationRequired) {
		t.Fatalf("unconfirmed: %v", err)
	}
	other := request
	other.AcceptedAfter = nil
	if _, _, err = service.Request(ctx, operator, other); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("another scope under the key: %v", err)
	}
	if len(store.accepted) != 0 {
		t.Fatalf("refused requests reached the store: %+v", store.accepted)
	}
	confirmed := request
	confirmed.ConfirmCost = true
	_, op, err := service.Request(ctx, operator, confirmed)
	if err != nil || op.Kind != operations.KindBackfill || len(store.accepted) != 1 {
		t.Fatalf("confirmed %+v %v", op, err)
	}
	if spec := store.accepted[0]; spec.RegistrationID != "reg" || spec.AcceptedAfter == nil || !spec.AcceptedAfter.Equal(after) || spec.Estimate.Versions != 3 {
		t.Fatalf("accepted spec %+v", spec)
	}
	// Once accepted, the key replays its backfill even after the active
	// plugin changed, and refuses another scope.
	service.Plans = fakePlans{backfill.Ingestion{RegistrationID: "reg_next", Prices: ingestion.Prices}}
	if _, replay, err := service.Request(ctx, operator, confirmed); err != nil || replay.ID != op.ID || len(store.accepted) != 1 {
		t.Fatalf("replay after a plan change %+v %v", replay, err)
	}
	if _, _, err = service.Request(ctx, operator, other); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("another scope under an accepted key: %v", err)
	}
}
