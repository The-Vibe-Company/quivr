package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

// recordingStore keeps created instances by ID and counts every call that
// would persist something, so a test can prove a refusal never reached it.
type recordingStore struct {
	calls     int
	created   map[string]Instance
	deposited []CredentialDeposit
}

func (s *recordingStore) CreateConnector(_ context.Context, n NewInstance) (Instance, error) {
	s.calls++
	if s.created == nil {
		s.created = map[string]Instance{}
	}
	s.created[n.ID] = n.Instance
	return n.Instance, nil
}

func (s *recordingStore) ReadConnector(_ context.Context, org, id string) (Instance, error) {
	if in, ok := s.created[id]; ok && in.Organization == org {
		return in, nil
	}
	return Instance{}, corpus.ErrNotFound
}

func (s *recordingStore) ListConnectors(context.Context, corpus.Scope, string, string, int) ([]Instance, error) {
	return nil, nil
}

func (s *recordingStore) DisableConnector(ctx context.Context, org, id string) (Instance, error) {
	return s.ReadConnector(ctx, org, id)
}

func (s *recordingStore) PauseConnector(ctx context.Context, org, id string) (Instance, error) {
	return s.ReadConnector(ctx, org, id)
}

func (s *recordingStore) ResumeConnector(ctx context.Context, org, id string) (Instance, error) {
	return s.ReadConnector(ctx, org, id)
}

func (s *recordingStore) ReplaceCredential(ctx context.Context, org, id string, d CredentialDeposit) (Instance, error) {
	s.calls++
	s.deposited = append(s.deposited, d)
	return s.ReadConnector(ctx, org, id)
}

func (s *recordingStore) ChangeSchedule(ctx context.Context, org, id string, _ time.Duration) (Instance, error) {
	return s.ReadConnector(ctx, org, id)
}

func (s *recordingStore) RequestRun(context.Context, string, string, time.Duration) (time.Time, error) {
	return time.Time{}, nil
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
	store := &recordingStore{}
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
