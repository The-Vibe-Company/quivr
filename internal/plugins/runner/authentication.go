package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"time"

	"github.com/The-Vibe-Company/quivr/contracts"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
)

// authentication checks the same signed discovery boundary as the engine.
// Contribution positives and invalid-body checks retain the signing context.
func (r *run) authentication(ctx context.Context) {
	raw, err := fs.ReadFile(contracts.PluginFixtures(), "authentication/cases.json")
	var fixture struct {
		Cases []struct {
			Name     string `json:"name"`
			Audience string `json:"audience"`
			Age      int64  `json:"issued_seconds_ago"`
			Body     string `json:"body"`
			Sent     string `json:"sent_body"`
			Status   int    `json:"expected_status"`
		} `json:"cases"`
	}
	if err != nil || len(plugins.ValidateDocument("authentication-fixture.schema.json", raw)) > 0 || json.Unmarshal(raw, &fixture) != nil || len(fixture.Cases) == 0 {
		r.add(Check{ID: "authentication", Title: "authentication fixtures load", Issues: []plugins.Issue{{Code: CodeInvalidFixture, Message: "authentication fixtures unavailable"}}}, time.Now())
		return
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, c := range fixture.Cases {
		started := time.Now()
		check := Check{ID: "authentication", Title: "engine request authentication: " + c.Name, Fixture: normativeFixturePrefix + "authentication/cases.json#" + c.Name}
		audience := c.Audience
		if audience == "plugin" {
			audience = r.m.ID
		} else {
			audience = r.m.ID + ".other"
		}
		issued := started.Add(-time.Duration(c.Age) * time.Second)
		ring := *r.signing
		ring.Keys = append([]plugins.SigningKey(nil), ring.Keys...)
		// An expired token is signed with the current secret even when its
		// verification window began recently; the receiver must reject expiry.
		for i := range ring.Keys {
			ring.Keys[i].NotBefore = 0
			ring.Keys[i].NotAfter = 0
		}
		token, err := plugins.EngineToken(ring, audience, "GET", "/v0/discovery", []byte(c.Body), issued, issued.Add(time.Minute))
		if c.Name == "unsigned" {
			token = ""
		}
		if c.Name == "forged" {
			token += "x"
		}
		req, requestErr := http.NewRequestWithContext(ctx, "GET", r.baseURL+"/v0/discovery", bytes.NewBufferString(c.Sent))
		if err == nil {
			err = requestErr
		}
		if err == nil {
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			resp, callErr := client.Do(req)
			err = callErr
			if err == nil {
				body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4097))
				_ = resp.Body.Close()
				var envelope struct {
					Code      string `json:"code"`
					Retryable bool   `json:"retryable"`
				}
				if resp.StatusCode != c.Status || readErr != nil || c.Status == 401 && (json.Unmarshal(body, &envelope) != nil || envelope.Code != "invalid_engine_token" || envelope.Retryable || len(plugins.ValidateDocument("error.schema.json", body)) > 0) {
					check.Issues = []plugins.Issue{{Code: CodeAcceptedInvalid, Message: "expected the authentication fixture status and terminal invalid_engine_token envelope"}}
				}
			}
		}
		if err != nil {
			check.Issues = []plugins.Issue{{Code: CodeUnavailable, Message: "authentication probe could not complete"}}
		}
		r.add(check, started)
	}
}
