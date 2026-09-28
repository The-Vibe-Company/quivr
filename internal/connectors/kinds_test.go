package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

func TestValidationFailuresPointAtTheOffendingField(t *testing.T) {
	registry, err := NewRegistry(Fixture{}, RSS{}, XList{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, kind     string
		config, secret string
		want           error
		field          string
	}{
		{"pattern mismatch", "rss", `{"url":"ftp://example.org"}`, "", ErrInvalidConfig, "/config/url"},
		{"missing required property", "x_list", `{}`, "", ErrInvalidConfig, "/config/list_id"},
		{"unexpected property", "rss", `{"url":"https://example.org/feed","extra":1}`, "", ErrInvalidConfig, "/config/extra"},
		{"nested type mismatch", "fixture", `{"script":[{"items":"x"}]}`, "", ErrInvalidConfig, "/config/script/0/items"},
		{"secret branch mismatch", "rss", `{"url":"https://example.org/feed"}`, `{"username":"a"}`, ErrInvalidCredential, "/credential/secret"},
		{"secret missing property", "x_list", `{"list_id":"1"}`, `{}`, ErrInvalidCredential, "/credential/secret/bearer_token"},
	} {
		var secret json.RawMessage
		if c.secret != "" {
			secret = json.RawMessage(c.secret)
		}
		err := registry.validate(c.kind, json.RawMessage(c.config), secret, "/credential/secret")
		if !errors.Is(err, c.want) || Field(err) != c.field {
			t.Errorf("%s: %v field %q, want %v %q", c.name, err, Field(err), c.want, c.field)
		}
	}
}

func TestPointerEscapesReservedCharacters(t *testing.T) {
	if got := pointer([]string{"a/b", "c~d"}); got != "/a~1b/c~0d" {
		t.Fatalf("pointer %q", got)
	}
}

func TestRegistryDescribesEnabledKindsFromTheirSchemas(t *testing.T) {
	registry, err := NewRegistry(XList{}, Fixture{}, RSS{})
	if err != nil {
		t.Fatal(err)
	}
	kinds := registry.Describe()
	if len(kinds) != 3 || kinds[0].Kind != "fixture" || kinds[1].Kind != "rss" || kinds[2].Kind != "x_list" {
		t.Fatalf("kinds %+v", kinds)
	}
	want := map[string]string{"fixture": CredentialOptional, "rss": CredentialOptional, "x_list": CredentialRequired}
	for _, k := range kinds {
		if k.Title == "" || k.Title == k.Kind || k.Description == "" {
			t.Errorf("%s: missing title/description annotations", k.Kind)
		}
		if k.Credential != want[k.Kind] || k.CredentialSchema == nil {
			t.Errorf("%s: credential %q", k.Kind, k.Credential)
		}
		if k.DefaultInterval <= 0 || !json.Valid(k.ConfigSchema) {
			t.Errorf("%s: interval or schema", k.Kind)
		}
	}
}

func TestKindWithoutCredentialSchemaTakesNone(t *testing.T) {
	registry, err := NewRegistry(noCredential{})
	if err != nil {
		t.Fatal(err)
	}
	k := registry.Describe()[0]
	if k.Credential != CredentialNone || k.CredentialSchema != nil || k.Title != "noop" {
		t.Fatalf("%+v", k)
	}
}

type noCredential struct{ Fixture }

func (noCredential) Kind() string             { return "noop" }
func (noCredential) ConfigSchema() []byte     { return []byte(`{"type":"object"}`) }
func (noCredential) CredentialSchema() []byte { return nil }

func TestCatalogReportsCredentialDepositAvailability(t *testing.T) {
	registry, _ := NewRegistry(Fixture{})
	keyed, _ := NewSealer("service-test-credential-key-0123456789")
	keyless, _ := NewKeylessSealer("service-test-cursor-key-0123456789")
	for _, c := range []struct {
		sealer Sealer
		want   bool
	}{{keyed, true}, {keyless, false}} {
		s := Service{Store: &replayStore{}, Registry: registry, Sealer: c.sealer, MinInterval: 45 * time.Second}
		catalog, err := s.Kinds(writer)
		if err != nil || catalog.CredentialDeposits != c.want || catalog.MinInterval != 45*time.Second || len(catalog.Kinds) != 1 {
			t.Fatalf("%+v %v", catalog, err)
		}
	}
	s := Service{Store: &replayStore{}, Registry: registry, Sealer: keyed}
	if _, err := s.Kinds(corpus.Scope{Organization: "org_a", Actions: []string{"content:read"}, Corpora: []string{"*"}}); !errors.Is(err, corpus.ErrForbidden) {
		t.Fatalf("unauthorized: %v", err)
	}
}

func TestChangeScheduleValidatesAndAuthorizes(t *testing.T) {
	registry, _ := NewRegistry(Fixture{})
	sealer, _ := NewSealer("service-test-credential-key-0123456789")
	store := &replayStore{}
	s := Service{Store: store, Registry: registry, Sealer: sealer}
	inst, err := s.Create(context.Background(), writer, CreateInput{Key: "k", CorpusID: "corpus_news", Namespace: "ns", Kind: "fixture", Config: json.RawMessage(`{"script":[]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.ChangeSchedule(context.Background(), writer, inst.ID, 120); err != nil || got.Interval != 120*time.Second {
		t.Fatalf("change: %+v %v", got, err)
	}
	for _, seconds := range []int{29, 86401, 0, -1, 1<<55 + 64} {
		if _, err := s.ChangeSchedule(context.Background(), writer, inst.ID, seconds); !errors.Is(err, ErrInvalidInterval) || Field(err) != "/interval_seconds" {
			t.Errorf("%d: %v %q", seconds, err, Field(err))
		}
	}
	reader := corpus.Scope{Organization: "org_a", Actions: []string{"connectors:read"}, Corpora: []string{"*"}}
	if _, err := s.ChangeSchedule(context.Background(), reader, inst.ID, 120); !errors.Is(err, corpus.ErrForbidden) {
		t.Fatalf("reader: %v", err)
	}
	outside := corpus.Scope{Organization: "org_a", Actions: []string{"connectors:write"}, Corpora: []string{"corpus_other"}}
	if _, err := s.ChangeSchedule(context.Background(), outside, inst.ID, 120); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatalf("outside: %v", err)
	}
}
