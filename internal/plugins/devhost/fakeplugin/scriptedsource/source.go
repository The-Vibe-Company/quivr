package scriptedsource

import (
	"context"
	_ "embed"
	"encoding/json"
	"gopkg.in/yaml.v3"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

// Source is a deterministic connector for local and CI acceptance. Each run
// consumes one script step; the Acquisition Checkpoint is the next step index,
// and past the end the source is silent. A credential whose token starts with
// "fixture-revoked" (or a missing one when required) is refused as an access
// error, so tests can cut and restore access by replacing the credential.
type Source struct{}

type fixtureConfig struct {
	RequiresCredential bool `json:"requires_credential"`
	Script             []struct {
		Items []struct {
			RecordKey string `json:"record_key"`
			Text      string `json:"text"`
			Revision  string `json:"revision"`
			Withdraw  bool   `json:"withdraw"`
		} `json:"items"`
	} `json:"script"`
}

type fixtureCheckpoint struct {
	Step int `json:"step"`
}

func (Source) Kind() string                   { return "fixture" }
func (Source) DefaultInterval() time.Duration { return 5 * time.Minute }

//go:embed fixture.yaml
var Manifest []byte

var schemas = func() struct{ Config, Credential []byte } {
	var declaration struct {
		Contributions struct {
			Connector struct {
				Kinds map[string]struct {
					Config     map[string]any `yaml:"config_schema"`
					Credential map[string]any `yaml:"credential_schema"`
				} `yaml:"kinds"`
			} `yaml:"connector"`
		} `yaml:"contributions"`
	}
	if err := yaml.Unmarshal(Manifest, &declaration); err != nil {
		panic(err)
	}
	kind := declaration.Contributions.Connector.Kinds["fixture"]
	config, err := json.Marshal(kind.Config)
	if err != nil {
		panic(err)
	}
	credential, err := json.Marshal(kind.Credential)
	if err != nil {
		panic(err)
	}
	return struct{ Config, Credential []byte }{config, credential}
}()

func (Source) ConfigSchema() []byte     { return schemas.Config }
func (Source) CredentialSchema() []byte { return schemas.Credential }

func (Source) Fetch(_ context.Context, r FetchRequest) (Page, error) {
	var cfg fixtureConfig
	if err := json.Unmarshal(r.Config, &cfg); err != nil {
		return Page{}, sourceError("invalid_fixture")
	}
	if cfg.RequiresCredential || r.Credential != nil {
		var secret struct {
			Token string `json:"token"`
		}
		if r.Credential == nil || json.Unmarshal(r.Credential, &secret) != nil || strings.HasPrefix(secret.Token, "fixture-revoked") {
			return Page{}, accessError("unauthorized")
		}
	}
	var cp fixtureCheckpoint
	if len(r.Checkpoint) > 0 && json.Unmarshal(r.Checkpoint, &cp) != nil {
		return Page{}, sourceError("invalid_checkpoint")
	}
	page := Page{}
	if cp.Step < len(cfg.Script) {
		for _, item := range cfg.Script[cp.Step].Items {
			page.Items = append(page.Items, Item{RecordKey: item.RecordKey, Revision: item.Revision, Content: content.Text{Kind: "text", Text: item.Text}, Withdraw: item.Withdraw})
		}
		cp.Step++
	}
	page.Checkpoint, _ = json.Marshal(cp)
	return page, nil
}

// Request and Page are test-source data; engine adapters translate them to ports.
type FetchRequest struct{ Config, Credential, Checkpoint json.RawMessage }
type Page struct {
	Items      []Item
	Checkpoint json.RawMessage
}
type Item struct {
	RecordKey, Revision string
	Content             content.Text
	Withdraw            bool
}
type Error struct{ Class, Code string }

func (e *Error) Error() string      { return e.Class + ": " + e.Code }
func sourceError(code string) error { return &Error{Class: "source", Code: code} }
func accessError(code string) error { return &Error{Class: "access", Code: code} }
