package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

// replayStore persists by idempotency key and compares request digests the
// way the PostgreSQL adapter does, recording every call it receives.
type replayStore struct {
	calls     int
	created   map[string]NewInstance
	deposited []CredentialDeposit
}

func (s *replayStore) CreateConnector(_ context.Context, n NewInstance) (Instance, error) {
	s.calls++
	if prior, ok := s.created[n.RequestKey]; ok {
		if !bytes.Equal(prior.RequestDigest, n.RequestDigest) {
			return Instance{}, ErrConflict
		}
		return prior.Instance, nil
	}
	if s.created == nil {
		s.created = map[string]NewInstance{}
	}
	s.created[n.RequestKey] = n
	return n.Instance, nil
}

func (s *replayStore) ReadConnector(_ context.Context, org, id string) (Instance, error) {
	for _, n := range s.created {
		if n.Organization == org && n.ID == id {
			return n.Instance, nil
		}
	}
	return Instance{}, corpus.ErrNotFound
}

func (s *replayStore) ListConnectors(context.Context, corpus.Scope, string, string, int) ([]Instance, error) {
	return nil, nil
}

func (s *replayStore) DisableConnector(_ context.Context, org, id string) (Instance, error) {
	return s.ReadConnector(context.Background(), org, id)
}

func (s *replayStore) ReplaceCredential(_ context.Context, org, id string, d CredentialDeposit) (Instance, error) {
	s.calls++
	s.deposited = append(s.deposited, d)
	return s.ReadConnector(context.Background(), org, id)
}

var writer = corpus.Scope{Organization: "org_a", Actions: []string{"connectors:read", "connectors:write"}, Corpora: []string{"corpus_news"}}

func keylessService(t *testing.T, store Store) Service {
	t.Helper()
	registry, err := NewRegistry(Fixture{})
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := NewKeylessSealer(testCursorKey)
	if err != nil {
		t.Fatal(err)
	}
	return Service{Store: store, Registry: registry, Sealer: sealer}
}

func fixtureInput(key string, secret json.RawMessage) CreateInput {
	return CreateInput{Key: key, CorpusID: "corpus_news", Namespace: "wire", Kind: "fixture", Config: json.RawMessage(`{"script":[]}`), Secret: secret}
}

func TestKeylessServiceRefusesCredentialDepositsBeforeStoringAnything(t *testing.T) {
	store := &replayStore{}
	svc := keylessService(t, store)
	ctx := context.Background()
	if _, err := svc.Create(ctx, writer, fixtureInput("c1", json.RawMessage(`{"token":"fixture-test-secret-keyless"}`))); !errors.Is(err, ErrCredentialsUnavailable) {
		t.Fatalf("create with secret: %v", err)
	}
	// Refusal precedes validation: even a schema-invalid secret is refused as unavailable.
	if _, err := svc.Create(ctx, writer, fixtureInput("c2", json.RawMessage(`{"password":"x"}`))); !errors.Is(err, ErrCredentialsUnavailable) {
		t.Fatalf("create with invalid secret: %v", err)
	}
	if store.calls != 0 {
		t.Fatalf("store called %d times for refused deposits", store.calls)
	}
	inst, err := svc.Create(ctx, writer, fixtureInput("c3", nil))
	if err != nil {
		t.Fatal(err)
	}
	calls := store.calls
	if _, err = svc.ReplaceCredential(ctx, writer, inst.ID, CredentialInput{Key: "r1", Secret: json.RawMessage(`{"token":"fixture-test-secret-keyless"}`)}); !errors.Is(err, ErrCredentialsUnavailable) {
		t.Fatalf("rotation: %v", err)
	}
	if store.calls != calls || len(store.deposited) != 0 {
		t.Fatal("refused rotation reached the store")
	}
	// Authorization still comes first: an unknown instance stays not found.
	if _, err = svc.ReplaceCredential(ctx, writer, "connector_unknown", CredentialInput{Key: "r2", Secret: json.RawMessage(`{"token":"x"}`)}); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatalf("unknown instance rotation: %v", err)
	}
	if _, err = svc.Create(ctx, corpus.Scope{Organization: "org_a", Actions: []string{"connectors:read"}, Corpora: []string{"*"}}, fixtureInput("c4", json.RawMessage(`{"token":"x"}`))); !errors.Is(err, corpus.ErrForbidden) {
		t.Fatalf("unauthorized create: %v", err)
	}
}

func TestKeylessServiceReplaysSecretFreeCreatesUnderAKeyedDigest(t *testing.T) {
	store := &replayStore{}
	svc := keylessService(t, store)
	ctx := context.Background()
	first, err := svc.Create(ctx, writer, fixtureInput("c1", nil))
	if err != nil {
		t.Fatal(err)
	}
	stored := store.created["c1"]
	if len(stored.RequestDigest) == 0 || stored.Credential != nil {
		t.Fatalf("stored digest %x credential %v", stored.RequestDigest, stored.Credential)
	}
	replay, err := svc.Create(ctx, writer, fixtureInput("c1", nil))
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay: %v %s != %s", err, replay.ID, first.ID)
	}
	changed := fixtureInput("c1", nil)
	changed.Namespace = "other"
	if _, err = svc.Create(ctx, writer, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed body under the same key: %v", err)
	}
}
