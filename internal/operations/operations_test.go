package operations_test

import (
	"context"
	"errors"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
)

type controlStore struct {
	ops       map[string]operations.Operation
	canceled  []string
	paused    []string
	canonical string
	resolved  string
}

func (s *controlStore) AcceptRetrievalConfiguration(_ context.Context, org, corpusID, _ string, canonical, resolved []byte) (operations.Operation, error) {
	s.canonical, s.resolved = string(canonical), string(resolved)
	return operations.Operation{ID: "config", Organization: org, Kind: operations.KindRetrievalConfiguration, CorpusID: corpusID, State: operations.StateQueued}, nil
}

func (s *controlStore) AcceptRebuild(_ context.Context, org, corpusID, _ string, canonical []byte) (operations.Operation, error) {
	s.canonical = string(canonical)
	return operations.Operation{ID: "rebuild", Organization: org, Kind: operations.KindProjectionRebuild, CorpusID: corpusID, State: operations.StateQueued}, nil
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
func (s *controlStore) PauseOperation(_ context.Context, _, id string) (operations.Operation, error) {
	s.paused = append(s.paused, "pause "+id)
	return s.ops[id], nil
}
func (s *controlStore) ResumeOperation(_ context.Context, _, id string) (operations.Operation, error) {
	s.paused = append(s.paused, "resume "+id)
	return s.ops[id], nil
}
func (s *controlStore) AcceptRerun(_ context.Context, org, source, _ string, canonical []byte) (operations.Operation, error) {
	s.canonical = string(canonical)
	return operations.Operation{ID: "rerun", Organization: org, Kind: s.ops[source].Kind, PreviousID: source}, nil
}

// A rebuild needs projections:rebuild on an in-scope Corpus; its canonical
// request, which idempotent replay compares, is the key alone.
func TestRequestRebuildAuthorizesAndCanonicalizes(t *testing.T) {
	store := &controlStore{}
	service := operations.Service{Store: store}
	ctx := context.Background()
	rebuilder := corpus.Scope{Organization: "org", Actions: []string{"projections:rebuild"}, Corpora: []string{"corpus_a"}}
	for _, tc := range []struct {
		name     string
		scope    corpus.Scope
		corpusID string
		want     error
	}{
		{"missing projections:rebuild", corpus.Scope{Organization: "org", Actions: []string{"operations:write"}, Corpora: []string{"*"}}, "corpus_a", corpus.ErrForbidden},
		{"Corpus outside scope", rebuilder, "corpus_b", corpus.ErrNotFound},
		{"no Corpus", corpus.Scope{Organization: "org", Actions: []string{"projections:rebuild"}, Corpora: []string{"*"}}, "", corpus.ErrNotFound},
	} {
		if _, err := service.RequestRebuild(ctx, tc.scope, tc.corpusID, "k"); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	if store.canonical != "" {
		t.Fatalf("rejected rebuild reached the store: %q", store.canonical)
	}
	op, err := service.RequestRebuild(ctx, rebuilder, "corpus_a", "k1")
	if err != nil || op.Organization != "org" || op.CorpusID != "corpus_a" || store.canonical != `{"idempotency_key":"k1"}` {
		t.Fatalf("rebuild %+v %v canonical %s", op, err, store.canonical)
	}
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

// A configuration change needs both corpora:write and operations:write on an
// in-scope Corpus; its canonical request is the resolved configuration.
func TestConfigureRetrievalAuthorizesAndCanonicalizes(t *testing.T) {
	store := &controlStore{}
	service := operations.Service{Store: store}
	ctx := context.Background()
	cfg := corpus.Retrieval{Fields: []corpus.Field{{Name: "title", SourcePointer: "/provenance/title", Type: "string", Roles: []string{"search"}}}}
	for _, tc := range []struct {
		name  string
		scope corpus.Scope
		want  error
	}{
		{"missing operations:write", corpus.Scope{Organization: "org", Actions: []string{"corpora:write"}, Corpora: []string{"*"}}, corpus.ErrForbidden},
		{"missing corpora:write", corpus.Scope{Organization: "org", Actions: []string{"operations:write"}, Corpora: []string{"*"}}, corpus.ErrForbidden},
		{"Corpus outside scope", corpus.Scope{Organization: "org", Actions: []string{"corpora:write", "operations:write"}, Corpora: []string{"corpus_b"}}, corpus.ErrNotFound},
	} {
		if _, err := service.ConfigureRetrieval(ctx, tc.scope, "corpus_a", "k", cfg); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	if store.canonical != "" {
		t.Fatal("rejected configuration reached the store")
	}
	owner := corpus.Scope{Organization: "org", Actions: []string{"corpora:write", "operations:write"}, Corpora: []string{"corpus_a"}}
	op, err := service.ConfigureRetrieval(ctx, owner, "corpus_a", "k", cfg)
	if err != nil || op.Kind != operations.KindRetrievalConfiguration || op.CorpusID != "corpus_a" {
		t.Fatalf("configure %+v %v", op, err)
	}
	want := `{"fields":[{"name":"title","source_pointer":"/provenance/title","type":"string","roles":["search"]}]}`
	if store.resolved != want || store.canonical != `{"idempotency_key":"k","retrieval":`+want+`}` {
		t.Fatalf("canonical %s resolved %s", store.canonical, store.resolved)
	}
	// Configuration Operations are controllable: rerun needs corpora:write.
	store.ops = map[string]operations.Operation{"config": {ID: "config", Organization: "org", Kind: operations.KindRetrievalConfiguration, CorpusID: "corpus_a", State: operations.StateFailed}}
	if _, err = service.Rerun(ctx, corpus.Scope{Organization: "org", Actions: []string{"operations:write", "projections:rebuild"}, Corpora: []string{"*"}}, "config", "r"); !errors.Is(err, corpus.ErrForbidden) {
		t.Fatalf("rerun without corpora:write: %v", err)
	}
	if op, err = service.Rerun(ctx, owner, "config", "r"); err != nil || op.PreviousID != "config" {
		t.Fatalf("rerun %+v %v", op, err)
	}
	if op, err = service.Cancel(ctx, owner, "config", "c"); err != nil || op.ID != "config" {
		t.Fatalf("cancel %+v %v", op, err)
	}
}

// Only a backfill pauses and resumes, and only for a caller who holds both
// operations:write and the command's plugins:admin on its Corpus.
func TestPauseAndResumeNeedAPausableKindAndItsPermission(t *testing.T) {
	store := &controlStore{ops: map[string]operations.Operation{
		"fill":    {ID: "fill", Organization: "org", Kind: operations.KindBackfill, CorpusID: "corpus_a", State: operations.StateRunning},
		"rebuild": {ID: "rebuild", Organization: "org", Kind: operations.KindProjectionRebuild, CorpusID: "corpus_a", State: operations.StateRunning},
	}}
	service := operations.Service{Store: store}
	ctx := context.Background()
	operator := corpus.Scope{Organization: "org", Actions: []string{"operations:write", operations.BackfillPermission, "projections:rebuild"}, Corpora: []string{"*"}}
	for _, tc := range []struct {
		name  string
		scope corpus.Scope
		id    string
		want  error
	}{
		{"missing plugins:admin", corpus.Scope{Organization: "org", Actions: []string{"operations:write"}, Corpora: []string{"*"}}, "fill", corpus.ErrForbidden},
		{"missing operations:write", corpus.Scope{Organization: "org", Actions: []string{operations.BackfillPermission}, Corpora: []string{"*"}}, "fill", corpus.ErrForbidden},
		{"a rebuild cannot pause", operator, "rebuild", operations.ErrUnsupportedKind},
	} {
		if _, err := service.Pause(ctx, tc.scope, tc.id); !errors.Is(err, tc.want) {
			t.Errorf("pause %s: %v, want %v", tc.name, err, tc.want)
		}
		if _, err := service.Resume(ctx, tc.scope, tc.id); !errors.Is(err, tc.want) {
			t.Errorf("resume %s: %v, want %v", tc.name, err, tc.want)
		}
	}
	if len(store.paused) != 0 {
		t.Fatalf("rejected actions reached the store: %v", store.paused)
	}
	if _, err := service.Pause(ctx, operator, "fill"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resume(ctx, operator, "fill"); err != nil {
		t.Fatal(err)
	}
	if len(store.paused) != 2 || store.paused[0] != "pause fill" || store.paused[1] != "resume fill" {
		t.Fatalf("store calls %v", store.paused)
	}
}
