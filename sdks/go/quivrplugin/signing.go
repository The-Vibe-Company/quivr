package quivrplugin

import (
	"bytes"
	"crypto/hmac"
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

// EnvSigningKeys contains this plugin's HS256 verification ring. It is never
// part of the public manifest or discovery document.
const EnvSigningKeys = "QUIVR_PLUGIN_SIGNING_KEYS"

type verificationKey struct {
	ID        string `json:"id"`
	Secret    string `json:"secret"`
	NotBefore int64  `json:"not_before"`
	NotAfter  int64  `json:"not_after"`
}

func decodeTokenPart(value string) ([]byte, error) {
	if value == "" {
		return nil, errors.New("invalid base64url")
	}
	for _, c := range value {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return nil, errors.New("invalid base64url")
		}
	}
	return base64.RawURLEncoding.Strict().DecodeString(value)
}

type engineClaims struct {
	Audience     string `json:"aud"`
	PluginID     string `json:"plugin_id"`
	Contribution string `json:"contribution"`
	Method       string `json:"method"`
	Target       string `json:"target"`
	Issued       int64  `json:"iat"`
	Expires      int64  `json:"exp"`
	Digest       string `json:"body_sha256"`
}

// jsonObject rejects duplicate members, so all verifiers interpret signed
// claims identically, and refuses trailing input.
func jsonObject(raw []byte, target any) bool {
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	object = map[string]json.RawMessage{}
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return false
		}
		if _, exists := object[key]; exists {
			return false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
		object[key] = value
	}
	if _, err = decoder.Token(); err != nil {
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		return false
	}
	return json.Unmarshal(raw, target) == nil
}

func verifyEngineToken(authorization, audience, method, target string, body []byte, now int64) bool {
	if !strings.HasPrefix(authorization, "Bearer ") || len(authorization) > 8192 {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(authorization, "Bearer "), ".")
	if len(parts) != 3 {
		return false
	}
	decode := decodeTokenPart
	raw, err := decode(parts[0])
	if err != nil {
		return false
	}
	var header map[string]string
	if !jsonObject(raw, &header) || len(header) != 3 || header["alg"] != "HS256" || header["typ"] != "quivr-engine+jwt" || header["kid"] == "" {
		return false
	}
	var ring struct {
		Active string            `json:"active"`
		Keys   []json.RawMessage `json:"keys"`
	}
	if !jsonObject([]byte(os.Getenv(EnvSigningKeys)), &ring) || len(ring.Keys) == 0 || len(ring.Keys) > 16 {
		return false
	}
	seen := map[string]bool{}
	var selected []byte
	for _, rawKey := range ring.Keys {
		var key verificationKey
		var fields map[string]json.RawMessage
		if !jsonObject(rawKey, &fields) || bytes.Equal(bytes.TrimSpace(fields["not_before"]), []byte("null")) || bytes.Equal(bytes.TrimSpace(fields["not_after"]), []byte("null")) {
			return false
		}
		if !jsonObject(rawKey, &key) {
			return false
		}
		secret, err := decode(key.Secret)
		if err != nil || len(secret) < 32 || key.ID == "" || seen[key.ID] || key.NotBefore < 0 || key.NotAfter < 0 || key.NotAfter != 0 && key.NotAfter <= key.NotBefore {
			return false
		}
		seen[key.ID] = true
		if key.ID == header["kid"] && now >= key.NotBefore && (key.NotAfter == 0 || now < key.NotAfter) {
			selected = secret
		}
	}
	if selected == nil || !seen[ring.Active] {
		return false
	}
	signature, err := decode(parts[2])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, selected)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return false
	}
	raw, err = decode(parts[1])
	if err != nil {
		return false
	}
	var claims engineClaims
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 8 {
		return false
	}
	for _, name := range []string{"aud", "plugin_id", "contribution", "method", "target", "iat", "exp", "body_sha256"} {
		if _, ok := fields[name]; !ok {
			return false
		}
	}
	if !jsonObject(raw, &claims) || claims.Issued < 0 || claims.Issued > now || claims.Expires <= now || claims.Expires <= claims.Issued || claims.Expires-claims.Issued > 60 {
		return false
	}
	contribution := "discovery"
	path := strings.SplitN(target, "?", 2)[0]
	if fields := strings.Split(path, "/"); len(fields) >= 4 && fields[1] == "v0" && fields[2] == "contributions" {
		contribution = fields[3]
	}
	digest := sha256.Sum256(body)
	return claims.Audience == audience && claims.PluginID == audience && claims.Contribution == contribution && claims.Method == method && claims.Target == target && claims.Digest == hex.EncodeToString(digest[:])
}

func (p *Plugin) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/health" && r.Method == http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
		if err != nil {
			writeJSON(w, 413, envelope{Code: "request_too_large", Message: "request body exceeds the SDK limit"})
			return
		}
		if len(r.Header.Values("Authorization")) != 1 || !verifyEngineToken(r.Header.Get("Authorization"), p.m.ID, r.Method, r.URL.RequestURI(), body, time.Now().Unix()) {
			writeJSON(w, 401, envelope{Code: "invalid_engine_token", Message: "engine request authentication failed"})
			return
		}
		r.Body = &authenticatedBody{Reader: bytes.NewReader(body), data: body}
		next.ServeHTTP(w, r)
	})
}

// authenticatedBody lets SDK decoders reuse the bytes whose digest was verified.
type authenticatedBody struct {
	*bytes.Reader
	data []byte
}

func (*authenticatedBody) Close() error { return nil }
func requestBody(r *http.Request) ([]byte, error) {
	if body, ok := r.Body.(*authenticatedBody); ok {
		return body.data, nil
	}
	return io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
}
