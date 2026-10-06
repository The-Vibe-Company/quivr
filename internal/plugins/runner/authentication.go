package runner

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/The-Vibe-Company/quivr/contracts"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
)

type authenticationCase struct {
	Name      string            `json:"name"`
	Audience  string            `json:"audience"`
	Age       int64             `json:"issued_seconds_ago"`
	Body      string            `json:"body"`
	Sent      string            `json:"sent_body"`
	Status    int               `json:"expected_status"`
	Claims    map[string]string `json:"claims"`
	Lifetime  int64             `json:"lifetime_seconds"`
	Algorithm string            `json:"algorithm"`
	Encoding  string            `json:"json_encoding"`
}

// token edits a valid engine token and re-signs its exact bytes. Each refusal
// can therefore exercise one verifier guard without failing HMAC first.
func (c authenticationCase) token(ring plugins.SigningKeys, pluginID string, started time.Time) (string, error) {
	if c.Name == "unsigned" {
		return "", nil
	}
	issued := started.Add(-time.Duration(c.Age) * time.Second)
	for i := range ring.Keys {
		// Expired/future probes still use the active secret.
		ring.Keys[i].NotBefore, ring.Keys[i].NotAfter = 0, 0
	}
	token, err := plugins.EngineToken(ring, pluginID, "GET", "/v0/discovery?source=engine", []byte(c.Body), issued, issued.Add(time.Minute))
	if err != nil {
		return "", err
	}
	parts := strings.Split(token, ".")
	decode := func(part string) map[string]any {
		raw, _ := base64.RawURLEncoding.DecodeString(part)
		var object map[string]any
		_ = json.Unmarshal(raw, &object)
		return object
	}
	header, claims := decode(parts[0]), decode(parts[1])
	if c.Audience != "plugin" {
		claims["aud"] = pluginID + ".other"
	}
	for name, value := range c.Claims {
		if name == "plugin_id" && value == "other-plugin" {
			value = pluginID + ".other"
		}
		claims[name] = value
	}
	if c.Lifetime != 0 {
		claims["exp"] = issued.Unix() + c.Lifetime
	}
	if c.Algorithm != "" {
		header["alg"] = c.Algorithm
	}
	encode := func(object map[string]any) string {
		raw, _ := json.Marshal(object)
		if c.Encoding == "utf-16" {
			encoded := []byte{0xff, 0xfe}
			for _, word := range utf16.Encode([]rune(string(raw))) {
				encoded = append(encoded, byte(word), byte(word>>8))
			}
			raw = encoded
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	input := encode(header) + "." + encode(claims)
	for _, key := range ring.Keys {
		if key.ID == ring.Active {
			secret, _ := base64.RawURLEncoding.DecodeString(key.Secret)
			mac := hmac.New(sha256.New, secret)
			_, _ = mac.Write([]byte(input))
			signature := mac.Sum(nil)
			if c.Name == "forged" {
				signature[0] ^= 1
			}
			return input + "." + base64.RawURLEncoding.EncodeToString(signature), nil
		}
	}
	return "", plugins.ErrSigningKeys
}

// authentication checks the same signed discovery boundary as the engine.
// Contribution positives and invalid-body checks retain the signing context.
func (r *run) authentication(ctx context.Context) {
	raw, err := fs.ReadFile(contracts.PluginFixtures(), "authentication/cases.json")
	var fixture struct {
		Cases []authenticationCase `json:"cases"`
	}
	if err != nil || len(plugins.ValidateDocument("authentication-fixture.schema.json", raw)) > 0 || json.Unmarshal(raw, &fixture) != nil || len(fixture.Cases) == 0 {
		r.add(Check{ID: "authentication", Title: "authentication fixtures load", Issues: []plugins.Issue{{Code: CodeInvalidFixture, Message: "authentication fixtures unavailable"}}}, time.Now())
		return
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, c := range fixture.Cases {
		started := time.Now()
		check := Check{ID: "authentication", Title: "engine request authentication: " + c.Name, Fixture: normativeFixturePrefix + "authentication/cases.json#" + c.Name}
		ring := *r.signing
		ring.Keys = append([]plugins.SigningKey(nil), ring.Keys...)
		token, err := c.token(ring, r.m.ID, started)
		req, requestErr := http.NewRequestWithContext(ctx, "GET", r.baseURL+"/v0/discovery?source=engine", bytes.NewBufferString(c.Sent))
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
