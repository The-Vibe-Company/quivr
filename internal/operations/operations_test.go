package operations_test

import (
	"context"
	"errors"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
)

type controlStore struct {
	ops       map[string]operations.Operation
	canceled  []string
	canonical string
}

func (s *controlStore) AcceptRebuild(context.Context, string, string, string, []byte) (operations.Operation, error) {
	return operations.Operation{}, errors.New("unused")
}
func (s *controlStore) Operation(_ context.Context, org, id string) (operations.Operation, error) {
	if op, ok := s.ops[id]; ok && op.Organization == org {
		return op, nil
	}
	return operations.Operation{}, corpus.ErrNotFound
}
func (s *controlStore) CancelOperation(_ context.Context, _, id string) (operations.Operation, error) {
	s.canceled = append(s.canceled, id)
	return s.ops[id], nil
}
func (s *controlStore) AcceptRerun(_ context.Context, org, source, _ string, canonical []byte) (operations.Operation, error) {
	s.canonical = string(canonical)
	return operations.Operation{ID: "rerun", Organization: org, Kind: s.ops[source].Kind, PreviousID: source}, nil
}

// Control actions revalidate the caller's current permission and Corpus scope
// on every request; rerun also needs the originating command's permission.
func TestCancelAndRerunRevalidateScopeAndPermission(t *testing.T) {
	store := &controlStore{ops: map[string]operations.Operation{
		"op_a":      {ID: "op_a", Organization: "org", Kind: operations.KindProjectionRebuild, CorpusID: "corpus_a", State: operations.StateSucceeded},
		"op_future": {ID: "op_future", Organization: "org", Kind: "cold_restoration", CorpusID: "corpus_a", State: operations.StateSucceeded},
	}}
	service := operations.Service{Store: store}
	ctx := context.Background()
	operator := corpus.Scope{Organization: "org", Actions: []string{"operations:write", "projections:rebuild"}, Corpora: []string{"*"}}
	for _, tc := range []struct {
		name  string
		scope corpus.Scope
		id    string
		want  error
	}{
		{"missing operations:write", corpus.Scope{Organization: "org", Actions: []string{"projections:rebuild", "operations:read"}, Corpora: []string{"*"}}, "op_a", corpus.ErrForbidden},
		{"Corpus outside scope", corpus.Scope{Organization: "org", Actions: []string{"operations:write", "projections:rebuild"}, Corpora: []string{"corpus_b"}}, "op_a", corpus.ErrNotFound},
		{"foreign Organization", corpus.Scope{Organization: "other", Actions: []string{"operations:write", "projections:rebuild"}, Corpora: []string{"*"}}, "op_a", corpus.ErrNotFound},
		{"absent Operation", operator, "op_missing", corpus.ErrNotFound},
		{"kind without control support", operator, "op_future", operations.ErrUnsupportedKind},
	} {
		if _, err := service.Cancel(ctx, tc.scope, tc.id, "k"); !errors.Is(err, tc.want) {
			t.Errorf("cancel %s: %v, want %v", tc.name, err, tc.want)
		}
		if _, err := service.Rerun(ctx, tc.scope, tc.id, "k"); !errors.Is(err, tc.want) {
			t.Errorf("rerun %s: %v, want %v", tc.name, err, tc.want)
		}
	}
	if len(store.canceled) != 0 || store.canonical != "" {
		t.Fatalf("rejected actions reached the store: %v %q", store.canceled, store.canonical)
	}
	// Cancel needs only operations:write; rerun revalidates projections:rebuild.
	writer := corpus.Scope{Organization: "org", Actions: []string{"operations:write"}, Corpora: []string{"corpus_a"}}
	if op, err := service.Cancel(ctx, writer, "op_a", "k"); err != nil || op.ID != "op_a" {
		t.Fatalf("cancel %+v %v", op, err)
	}
	if _, err := service.Rerun(ctx, writer, "op_a", "k"); !errors.Is(err, corpus.ErrForbidden) {
		t.Fatalf("rerun without command permission: %v", err)
	}
	op, err := service.Rerun(ctx, operator, "op_a", "again")
	if err != nil || op.PreviousID != "op_a" || store.canonical != `{"idempotency_key":"again","source_operation_id":"op_a"}` {
		t.Fatalf("rerun %+v %v canonical %s", op, err, store.canonical)
	}
}
