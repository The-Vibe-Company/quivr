package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

// Fixture is a deterministic connector for local and CI acceptance. Each run
// consumes one script step; the Acquisition Checkpoint is the next step index,
// and past the end the source is silent. A credential whose token starts with
// "fixture-revoked" (or a missing one when required) is refused as an access
// error, so tests can cut and restore access by replacing the credential.
type Fixture struct{}

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

func (Fixture) Kind() string                   { return "fixture" }
func (Fixture) DefaultInterval() time.Duration { return 5 * time.Minute }
func (Fixture) ConfigSchema() []byte {
	return []byte(`{"type":"object","additionalProperties":false,"required":["script"],"properties":{
"requires_credential":{"type":"boolean"},
"script":{"type":"array","maxItems":100,"items":{"type":"object","additionalProperties":false,"required":["items"],"properties":{
"items":{"type":"array","maxItems":100,"items":{"type":"object","additionalProperties":false,"required":["record_key"],"properties":{
"record_key":{"type":"string","minLength":1},"text":{"type":"string"},"revision":{"type":"string"},"withdraw":{"type":"boolean"}}}}}}}}}`)
}
func (Fixture) CredentialSchema() []byte {
	return []byte(`{"type":"object","additionalProperties":false,"required":["token"],"properties":{"token":{"type":"string","minLength":1}}}`)
}

func (Fixture) Fetch(_ context.Context, r FetchRequest) (Page, error) {
	var cfg fixtureConfig
	if err := json.Unmarshal(r.Config, &cfg); err != nil {
		return Page{}, SourceError("invalid_fixture")
	}
	if cfg.RequiresCredential || r.Credential != nil {
		var secret struct {
			Token string `json:"token"`
		}
		if r.Credential == nil || json.Unmarshal(r.Credential, &secret) != nil || strings.HasPrefix(secret.Token, "fixture-revoked") {
			return Page{}, AccessError("unauthorized")
		}
	}
	var cp fixtureCheckpoint
	if len(r.Checkpoint) > 0 && json.Unmarshal(r.Checkpoint, &cp) != nil {
		return Page{}, SourceError("invalid_checkpoint")
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

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
