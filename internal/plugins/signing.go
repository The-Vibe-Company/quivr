package plugins

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// EnvSigningKeys maps plugin ids to independent key rings. Secrets are
// deployment environment values, never registration metadata.
const EnvSigningKeys = "QUIVR_ENGINE_PLUGIN_KEYS"
const EnvPluginSigningKeys = "QUIVR_PLUGIN_SIGNING_KEYS"

var ErrSigningKeys = errors.New("plugin signing keys unavailable or invalid")

// NewSigningKeys provisions an ephemeral secret for a locally launched plugin.
func NewSigningKeys() (SigningKeys, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return SigningKeys{}, ErrSigningKeys
	}
	return SigningKeys{Active: "local", Keys: []SigningKey{{ID: "local", Secret: base64.RawURLEncoding.EncodeToString(secret)}}}, nil
}

// SigningKey is one HS256 key with an optional bounded verification window.
type SigningKey struct {
	ID        string `json:"id"`
	Secret    string `json:"secret"`
	NotBefore int64  `json:"not_before,omitempty"`
	NotAfter  int64  `json:"not_after,omitempty"`
}

// SigningKeys selects the engine's active key. Plugins verify every key whose
// validity window includes the current instant, allowing rolling rotation.
type SigningKeys struct {
	Active string       `json:"active"`
	Keys   []SigningKey `json:"keys"`
}

func (k SigningKey) validAt(now int64) bool {
	return now >= k.NotBefore && (k.NotAfter == 0 || now < k.NotAfter)
}

// ParseSigningKeys deliberately returns a constant error: malformed secret
// configuration must never leak into logs or public diagnostics.
func ParseSigningKeys(raw []byte) (SigningKeys, error) {
	var ring SigningKeys
	if !uniqueJSON(raw) || json.Unmarshal(raw, &ring) != nil || len(ring.Keys) == 0 || len(ring.Keys) > 16 || ring.Active == "" {
		return SigningKeys{}, ErrSigningKeys
	}
	seen := map[string]bool{}
	var fields struct {
		Keys []map[string]json.RawMessage `json:"keys"`
	}
	if json.Unmarshal(raw, &fields) != nil {
		return SigningKeys{}, ErrSigningKeys
	}
	for i, key := range ring.Keys {
		if bytes.Equal(bytes.TrimSpace(fields.Keys[i]["not_before"]), []byte("null")) || bytes.Equal(bytes.TrimSpace(fields.Keys[i]["not_after"]), []byte("null")) {
			return SigningKeys{}, ErrSigningKeys
		}
		secret, err := base64.RawURLEncoding.Strict().DecodeString(key.Secret)
		if err != nil || base64.RawURLEncoding.EncodeToString(secret) != key.Secret || len(secret) < 32 || key.ID == "" || seen[key.ID] || key.NotBefore < 0 || key.NotAfter < 0 || key.NotAfter != 0 && key.NotAfter <= key.NotBefore {
			return SigningKeys{}, ErrSigningKeys
		}
		seen[key.ID] = true
	}
	if !seen[ring.Active] {
		return SigningKeys{}, ErrSigningKeys
	}
	return ring, nil
}

// EngineSigningKeys resolves one plugin's secret ring, including registry pins.
func EngineSigningKeys(id string) (SigningKeys, error) {
	var all map[string]json.RawMessage
	raw := []byte(os.Getenv(EnvSigningKeys))
	if !uniqueJSON(raw) || json.Unmarshal(raw, &all) != nil {
		return SigningKeys{}, ErrSigningKeys
	}
	return ParseSigningKeys(all[id])
}

// EngineToken binds exact request bytes and the operation to a plugin audience.
// Tokens last at most 60 seconds and are issued separately for every request.
func EngineToken(ring SigningKeys, pluginID, method, target string, body []byte, now time.Time, expiry time.Time) (string, error) {
	if expiry.After(now.Add(time.Minute)) {
		expiry = now.Add(time.Minute)
	}
	if expiry.Unix() <= now.Unix() {
		return "", ErrSigningKeys
	}
	var selected SigningKey
	for _, key := range ring.Keys {
		if key.ID == ring.Active && key.validAt(now.Unix()) {
			selected = key
			break
		}
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(selected.Secret)
	if err != nil || len(secret) < 32 {
		return "", ErrSigningKeys
	}
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "quivr-engine+jwt", "kid": selected.ID})
	contribution := "discovery"
	path := strings.SplitN(target, "?", 2)[0]
	if parts := strings.Split(path, "/"); len(parts) >= 4 && parts[1] == "v0" && parts[2] == "contributions" {
		contribution = parts[3]
	}
	sum := sha256.Sum256(body)
	claims, _ := json.Marshal(map[string]any{"aud": pluginID, "plugin_id": pluginID, "contribution": contribution, "method": method, "target": target, "iat": now.Unix(), "exp": expiry.Unix(), "body_sha256": hex.EncodeToString(sum[:])})
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(input))
	return input + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

type signingContextKey struct{}
type requestSigning struct {
	id   string
	ring SigningKeys
}

// WithRequestSigning scopes signing to one peer and to this call tree only.
func WithRequestSigning(ctx context.Context, id string, ring SigningKeys) context.Context {
	return context.WithValue(ctx, signingContextKey{}, requestSigning{id, ring})
}

// SignHTTPRequest is shared by discovery and every Contribution transport.
func SignHTTPRequest(req *http.Request, body []byte) error {
	signer, ok := req.Context().Value(signingContextKey{}).(requestSigning)
	if !ok {
		return nil
	}
	now := time.Now()
	expiry := now.Add(time.Minute)
	token, err := EngineToken(signer.ring, signer.id, req.Method, req.URL.RequestURI(), body, now, expiry)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// SigningContext preserves the unsigned compatibility path only for a pin
// whose declared API cannot negotiate signed_calls.
func SigningContext(ctx context.Context, pin *Pin) (context.Context, error) {
	if !pin.Speaks(FeatureSignedCalls) {
		return ctx, nil
	}
	ring, err := EngineSigningKeys(pin.Manifest.ID)
	if err != nil {
		return ctx, err
	}
	return WithRequestSigning(ctx, pin.Manifest.ID, ring), nil
}

// uniqueJSON refuses ambiguous operator key configuration at every object level.
func uniqueJSON(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	var value func() bool
	value = func() bool {
		token, err := d.Token()
		if err != nil {
			return false
		}
		switch token {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				token, err := d.Token()
				name, ok := token.(string)
				if err != nil || !ok || seen[name] {
					return false
				}
				seen[name] = true
				if !value() {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		case json.Delim('['):
			for d.More() {
				if !value() {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		default:
			return true
		}
	}
	if !value() {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}
