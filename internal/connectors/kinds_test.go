package connectors

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// validate owns the JSON Pointer of a schema failure and the registry's
// acceptance: every row is one registry call as Create and rotation make it.
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
		{"valid config and secret", "fixture", `{"script":[]}`, `{"token":"fixture-test-token"}`, nil, ""},
		{"kind not enabled here", "m365_mail", `{}`, "", ErrUnsupportedKind, ""},
		{"pattern mismatch", "rss", `{"url":"ftp://example.org"}`, "", ErrInvalidConfig, "/config/url"},
		{"missing required property", "x_list", `{}`, "", ErrInvalidConfig, "/config/list_id"},
		{"unexpected property", "rss", `{"url":"https://example.org/feed","extra":1}`, "", ErrInvalidConfig, "/config/extra"},
		{"reserved characters are escaped", "rss", `{"url":"https://example.org/feed","a/b~c":1}`, "", ErrInvalidConfig, "/config/a~1b~0c"},
		{"nested type mismatch", "fixture", `{"script":[{"items":"x"}]}`, "", ErrInvalidConfig, "/config/script/0/items"},
		{"secret branch mismatch", "rss", `{"url":"https://example.org/feed"}`, `{"username":"a"}`, ErrInvalidCredential, "/credential/secret"},
		{"secret missing property", "x_list", `{"list_id":"1"}`, `{}`, ErrInvalidCredential, "/credential/secret/bearer_token"},
	} {
		var secret json.RawMessage
		if c.secret != "" {
			secret = json.RawMessage(c.secret)
		}
		err := registry.validate(c.kind, json.RawMessage(c.config), secret, "/credential/secret")
		if (c.want == nil) != (err == nil) || !errors.Is(err, c.want) || Field(err) != c.field {
			t.Errorf("%s: %v field %q, want %v %q", c.name, err, Field(err), c.want, c.field)
		}
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
	// Documented per-kind defaults (docs/connectors/rss.md, x.md).
	interval := map[string]time.Duration{"fixture": 5 * time.Minute, "rss": 5 * time.Minute, "x_list": 2 * time.Minute}
	for _, k := range kinds {
		if k.Title == "" || k.Title == k.Kind || k.Description == "" {
			t.Errorf("%s: missing title/description annotations", k.Kind)
		}
		if k.Credential != want[k.Kind] || k.CredentialSchema == nil {
			t.Errorf("%s: credential %q", k.Kind, k.Credential)
		}
		if k.DefaultInterval != interval[k.Kind] || !json.Valid(k.ConfigSchema) {
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
