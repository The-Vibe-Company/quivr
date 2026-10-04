// Package plugintest runs connector fixtures
// (contracts/plugins/v0/connector-fixture.schema.json) against a plugin in
// process, through the same HTTP handler `quivr plugin test` talks to, so a
// plugin's unit tests need no running server. `quivr plugin test` remains
// the certification: it also judges the output with the engine's rules.
package plugintest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

// Fixture is a connector fixture.
type Fixture struct {
	Description string `json:"description,omitempty"`
	Connector   struct {
		Kind   string          `json:"kind"`
		Config json.RawMessage `json:"config"`
	} `json:"connector"`
	Credential    json.RawMessage `json:"credential,omitempty"`
	Configuration json.RawMessage `json:"configuration,omitempty"`
	Checkpoint    json.RawMessage `json:"checkpoint,omitempty"`
	Now           string          `json:"now,omitempty"`
	MaxPages      int             `json:"max_pages,omitempty"`
	Expect        struct {
		Pages []struct {
			RecordKeys []string `json:"record_keys"`
			More       *bool    `json:"more,omitempty"`
		} `json:"pages,omitempty"`
		Error           *ErrorEnvelope `json:"error,omitempty"`
		CheckCredential *struct {
			Status string `json:"status,omitempty"`
			Class  string `json:"error_class,omitempty"`
			Code   string `json:"code,omitempty"`
		} `json:"check_credential,omitempty"`
	} `json:"expect"`

	short string
}

// ErrorEnvelope is a Plugin Protocol error answer.
type ErrorEnvelope struct {
	Status            int    `json:"-"`
	Code              string `json:"code"`
	Message           string `json:"message"`
	Retryable         bool   `json:"retryable"`
	Class             string `json:"error_class,omitempty"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
}

// Page is one answered fetch.
type Page struct {
	Items       []quivrplugin.Item `json:"items"`
	Checkpoint  json.RawMessage    `json:"checkpoint"`
	More        bool               `json:"more"`
	Reads       int64              `json:"reads"`
	Diagnostics map[string]any     `json:"diagnostics"`
	Notice      string             `json:"notice"`
	NotDue      bool               `json:"not_due"`
}

// RecordKeys lists the page's Record Keys in order.
func (p Page) RecordKeys() []string {
	keys := []string{}
	for _, item := range p.Items {
		keys = append(keys, item.RecordKey)
	}
	return keys
}

// Run is the outcome of running a fixture.
type Run struct {
	Pages []Page
	// Error is the error that ended the run, if any.
	Error *ErrorEnvelope
	// Checkpoint is the checkpoint after the last page.
	Checkpoint json.RawMessage
}

// Load reads a connector fixture.
func Load(path string) (*Fixture, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f Fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.Connector.Kind == "" {
		return nil, fmt.Errorf("%s: not a connector fixture (no connector.kind)", path)
	}
	sum := sha256.Sum256(raw)
	f.short = hex.EncodeToString(sum[:])[:16]
	return &f, nil
}

func (f *Fixture) defaults() (credential, configuration, checkpoint json.RawMessage, now string, maxPages int) {
	credential, configuration, checkpoint, now, maxPages = f.Credential, f.Configuration, f.Checkpoint, f.Now, f.MaxPages
	if len(credential) == 0 {
		credential = json.RawMessage("null")
	}
	if len(configuration) == 0 {
		configuration = json.RawMessage("{}")
	}
	if len(checkpoint) == 0 {
		checkpoint = json.RawMessage("null")
	}
	if now == "" {
		now = "2026-01-01T00:00:00Z"
	}
	if maxPages == 0 {
		maxPages = 10
	}
	return
}

// The instance scope of fetch requests, as `quivr plugin test` sends it
// (Plugin API 0.3.1).
const (
	DevCorpusID        = "dev-corpus"
	DevSourceNamespace = "dev-namespace"
)

func (f *Fixture) request(extra map[string]any) []byte {
	credential, configuration, _, now, _ := f.defaults()
	connector := map[string]any{"instance_id": "dev-connector-" + f.short, "kind": f.Connector.Kind, "config": f.Connector.Config}
	if _, fetch := extra["checkpoint"]; fetch {
		connector["corpus_id"], connector["source_namespace"] = DevCorpusID, DevSourceNamespace
	}
	body := map[string]any{
		"invocation_id": "dev-invocation-" + f.short, "contribution": "connector", "organization_id": "dev-organization",
		"configuration": configuration,
		"connector":     connector,
		"credential":    credential, "now": now,
	}
	for k, v := range extra {
		body[k] = v
	}
	raw, _ := json.Marshal(body)
	return raw
}

func post(h http.Handler, route string, body []byte) (int, []byte) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, route, bytes.NewReader(body)))
	return rec.Code, rec.Body.Bytes()
}

// Fetch runs the fixture's pages from its checkpoint, feeding each returned
// checkpoint back, until more is false, max_pages is reached or an error
// ends the run.
func Fetch(p *quivrplugin.Plugin, f *Fixture) (*Run, error) {
	return FetchFrom(p, f, nil)
}

// FetchFrom is Fetch from another checkpoint, such as a previous run's final
// one (nil uses the fixture's).
func FetchFrom(p *quivrplugin.Plugin, f *Fixture, checkpoint json.RawMessage) (*Run, error) {
	h, err := p.Handler()
	if err != nil {
		return nil, err
	}
	_, _, start, _, maxPages := f.defaults()
	if checkpoint == nil {
		checkpoint = start
	}
	run := &Run{Checkpoint: checkpoint}
	var reads int64
	for page := 0; page < maxPages; page++ {
		status, body := post(h, "/v0/contributions/connector/fetch", f.request(map[string]any{
			"checkpoint": run.Checkpoint, "page_in_run": page, "reads_today": reads,
		}))
		if status != 200 {
			e := &ErrorEnvelope{Status: status}
			if err := json.Unmarshal(body, e); err != nil {
				return nil, fmt.Errorf("HTTP %d without an error envelope: %s", status, body)
			}
			run.Error = e
			return run, nil
		}
		var answer Page
		if err := json.Unmarshal(body, &answer); err != nil {
			return nil, err
		}
		run.Pages = append(run.Pages, answer)
		run.Checkpoint = answer.Checkpoint
		reads += answer.Reads
		if !answer.More {
			break
		}
	}
	return run, nil
}

// CheckCredential sends the fixture's credential to check_credential; it
// returns nil for status ok.
func CheckCredential(p *quivrplugin.Plugin, f *Fixture) (*ErrorEnvelope, error) {
	h, err := p.Handler()
	if err != nil {
		return nil, err
	}
	status, body := post(h, "/v0/contributions/connector/check_credential", f.request(nil))
	if status == 200 {
		return nil, nil
	}
	e := &ErrorEnvelope{Status: status}
	if err := json.Unmarshal(body, e); err != nil {
		return nil, fmt.Errorf("HTTP %d without an error envelope: %s", status, body)
	}
	return e, nil
}

// Verify runs the fixture and compares the outcome with its expect section:
// the pages' Record Keys and more flags, the expected error class and code,
// and the credential check. It returns nil when everything matches.
func Verify(p *quivrplugin.Plugin, f *Fixture) error {
	run, err := Fetch(p, f)
	if err != nil {
		return err
	}
	switch want := f.Expect.Error; {
	case want != nil && run.Error == nil:
		return fmt.Errorf("expected a %s error, the run returned %d pages", want.Class, len(run.Pages))
	case want != nil && (run.Error.Class != want.Class || want.Code != "" && run.Error.Code != want.Code):
		return fmt.Errorf("expected a %s error %s, got %s %s: %s", want.Class, want.Code, run.Error.Class, run.Error.Code, run.Error.Message)
	case want == nil && run.Error != nil:
		return fmt.Errorf("unexpected %s error %s: %s", run.Error.Class, run.Error.Code, run.Error.Message)
	}
	if len(f.Expect.Pages) > 0 {
		if len(run.Pages) != len(f.Expect.Pages) {
			return fmt.Errorf("the run returned %d pages; the fixture expects %d", len(run.Pages), len(f.Expect.Pages))
		}
		for i, want := range f.Expect.Pages {
			got := run.Pages[i]
			if !slices.Equal(got.RecordKeys(), want.RecordKeys) || want.More != nil && *want.More != got.More {
				return fmt.Errorf("page %d returned %v (more %v); the fixture expects %v", i, got.RecordKeys(), got.More, want.RecordKeys)
			}
		}
	}
	if want := f.Expect.CheckCredential; want != nil {
		refusal, err := CheckCredential(p, f)
		if err != nil {
			return err
		}
		switch {
		case want.Status == "ok" && refusal != nil:
			return fmt.Errorf("check_credential: expected ok, got %s %s", refusal.Class, refusal.Code)
		case want.Class != "" && refusal == nil:
			return fmt.Errorf("check_credential: expected a %s error, got ok", want.Class)
		case want.Class != "" && (refusal.Class != want.Class || want.Code != "" && refusal.Code != want.Code):
			return fmt.Errorf("check_credential: expected a %s error %s, got %s %s", want.Class, want.Code, refusal.Class, refusal.Code)
		}
	}
	return nil
}
