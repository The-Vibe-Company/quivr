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
	registry, err := NewRegistry(Fixture{}, bearerSource{})
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
		{"pattern mismatch", "bearer_source", `{"list_id":"list"}`, "", ErrInvalidConfig, "/config/list_id"},
		{"missing required property", "bearer_source", `{}`, "", ErrInvalidConfig, "/config/list_id"},
		{"unexpected property", "bearer_source", `{"list_id":"1","extra":1}`, "", ErrInvalidConfig, "/config/extra"},
		{"reserved characters are escaped", "bearer_source", `{"list_id":"1","a/b~c":1}`, "", ErrInvalidConfig, "/config/a~1b~0c"},
		{"nested type mismatch", "fixture", `{"script":[{"items":"x"}]}`, "", ErrInvalidConfig, "/config/script/0/items"},
		{"secret missing property", "bearer_source", `{"list_id":"1"}`, `{}`, ErrInvalidCredential, "/credential/secret/bearer_token"},
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
	registry, err := NewRegistry(bearerSource{}, Fixture{})
	if err != nil {
		t.Fatal(err)
	}
	kinds := registry.Describe()
	if len(kinds) != 2 || kinds[0].Kind != "bearer_source" || kinds[1].Kind != "fixture" {
		t.Fatalf("kinds %+v", kinds)
	}
	want := map[string]string{"fixture": CredentialOptional, "bearer_source": CredentialRequired}
	interval := map[string]time.Duration{"fixture": 5 * time.Minute, "bearer_source": 2 * time.Minute}
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

// bearerSource is a kind that needs a credential, as plugin kinds such as
// x_list do.
type bearerSource struct{ Fixture }

func (bearerSource) Kind() string                   { return "bearer_source" }
func (bearerSource) DefaultInterval() time.Duration { return 2 * time.Minute }
func (bearerSource) CredentialRequired() bool       { return true }
func (bearerSource) ConfigSchema() []byte {
	return []byte(`{"title":"Bearer source","description":"A source that needs a bearer token.","type":"object","additionalProperties":false,"required":["list_id"],"properties":{"list_id":{"type":"string","pattern":"^[0-9]+$"}}}`)
}
func (bearerSource) CredentialSchema() []byte {
	return []byte(`{"type":"object","required":["bearer_token"],"properties":{"bearer_token":{"type":"string","writeOnly":true}}}`)
}

func (noCredential) Kind() string             { return "noop" }
func (noCredential) ConfigSchema() []byte     { return []byte(`{"type":"object"}`) }
func (noCredential) CredentialSchema() []byte { return nil }
