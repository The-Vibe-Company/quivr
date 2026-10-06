package quivrplugin

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	currentSigningSecret = []byte("01234567890123456789012345678901")
	oldSigningSecret     = []byte("abcdefghijklmnopqrstuvwxyz123456")
)

const signedDiscoveryTarget = "/v0/discovery?source=engine"

// engineToken builds the wire token independently of the SDK signer. Keeping
// this helper in the HTTP-boundary tests prevents a shared signer from making
// both sides of the contract agree on the same mistake.
func engineToken(t *testing.T, secret []byte, kid, audience, pluginID, method, target string, body []byte, issued, expiry int64) string {
	t.Helper()
	header := map[string]any{"alg": "HS256", "typ": "quivr-engine+jwt", "kid": kid}
	digest := sha256.Sum256(body)
	contribution := "discovery"
	if path := strings.SplitN(target, "?", 2)[0]; strings.HasPrefix(path, "/v0/contributions/") {
		contribution = strings.Split(path, "/")[3]
	}
	claims := map[string]any{
		"aud":          audience,
		"plugin_id":    pluginID,
		"contribution": contribution,
		"method":       method,
		"target":       target,
		"iat":          issued,
		"exp":          expiry,
		"body_sha256":  hex.EncodeToString(digest[:]),
	}
	headerRaw, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	claimsRaw, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	input := encode(headerRaw) + "." + encode(claimsRaw)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(input))
	return "Bearer " + input + "." + encode(mac.Sum(nil))
}

func signingKey(id string, secret []byte, notBefore, notAfter *int64) map[string]any {
	key := map[string]any{
		"id":     id,
		"secret": base64.RawURLEncoding.EncodeToString(secret),
	}
	if notBefore != nil {
		key["not_before"] = *notBefore
	}
	if notAfter != nil {
		key["not_after"] = *notAfter
	}
	return key
}

func signingRing(t *testing.T, active string, keys ...map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"active": active, "keys": keys})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func signedHandler(t *testing.T) http.Handler {
	t.Helper()
	raw, err := os.ReadFile("testdata/quivr-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`plugin_api: ">=0.1.0 <0.14.0"`), []byte(`plugin_api: ">=0.14.0 <0.15.0"`), 1)
	path := filepath.Join(t.TempDir(), "quivr-plugin.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	fetch := func(*FetchRequest) (*Page, error) { return &Page{}, nil }
	p.MustConnector("feed", fake{fetch}).MustConnector("open", fake{fetch}).MustConnector("optional", fake{fetch})
	h, err := p.Handler()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func signingCall(h http.Handler, method, target string, body []byte, authorization string) (int, []byte) {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func assertInvalidEngineToken(t *testing.T, status int, body []byte) {
	t.Helper()
	if status != http.StatusUnauthorized || !bytes.Contains(body, []byte(`"code":"invalid_engine_token"`)) {
		t.Fatalf("got HTTP %d %s", status, body)
	}
}

func TestSignedMalformedPOSTReachesDecoder(t *testing.T) {
	now := time.Now().Unix()
	t.Setenv(EnvSigningKeys, signingRing(t, "current", signingKey("current", currentSigningSecret, nil, nil)))
	h := signedHandler(t)
	target := "/v0/contributions/connector/fetch?source=engine"
	malformed := []byte("{")
	token := engineToken(t, currentSigningSecret, "current", "sdk-test", "sdk-test", http.MethodPost, target, malformed, now-1, now+59)
	status, body := signingCall(h, http.MethodPost, target, malformed, token)
	if status != http.StatusBadRequest || !bytes.Contains(body, []byte(`"code":"invalid_request"`)) {
		t.Fatalf("signed POST dispatch: HTTP %d %s", status, body)
	}
}

func TestSigningKeyConfigurationFailsClosedWithoutEchoingSecrets(t *testing.T) {
	now := time.Now().Unix()
	token := engineToken(t, currentSigningSecret, "current", "sdk-test", "sdk-test", http.MethodGet, signedDiscoveryTarget, nil, now-1, now+59)
	secretMarker := "config-secret-marker"
	validKey := signingKey("current", currentSigningSecret, nil, nil)
	missingActiveRaw, err := json.Marshal(map[string]any{"keys": []map[string]any{validKey}})
	if err != nil {
		t.Fatal(err)
	}
	nullBounds := signingKey("current", currentSigningSecret, nil, nil)
	nullBounds["not_after"] = nil
	cases := []struct {
		name string
		ring string
	}{
		{name: "null expiry", ring: signingRing(t, "current", nullBounds)},
		{name: "missing", ring: ""},
		{name: "malformed JSON", ring: `{"active":"current","keys":`},
		{name: "missing keys", ring: `{"active":"current"}`},
		{name: "missing active", ring: string(missingActiveRaw)},
		{name: "unknown active", ring: signingRing(t, "retired", validKey)},
		{name: "malformed secret", ring: `{"active":"current","keys":[{"id":"current","secret":"` + secretMarker + `"}]}`},
		{name: "short secret", ring: `{"active":"current","keys":[{"id":"current","secret":"c2hvcnQ"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvSigningKeys, tc.ring)
			h := signedHandler(t)
			status, body := signingCall(h, http.MethodGet, signedDiscoveryTarget, nil, token)
			assertInvalidEngineToken(t, status, body)
			if bytes.Contains(body, []byte(secretMarker)) {
				t.Fatalf("response echoed key material: %s", body)
			}
		})
	}
}

func TestSigningKeyRotationAcceptsOverlapAndRejectsRetiredKeys(t *testing.T) {
	now := time.Now().Unix()
	overlapEnd := now + 600
	overlapStart := now - 600
	t.Run("overlap accepts both keys", func(t *testing.T) {
		t.Setenv(EnvSigningKeys, signingRing(t, "current",
			signingKey("old", oldSigningSecret, &overlapStart, &overlapEnd),
			signingKey("current", currentSigningSecret, nil, nil),
		))
		h := signedHandler(t)
		for _, tc := range []struct {
			id     string
			secret []byte
		}{
			{id: "old", secret: oldSigningSecret},
			{id: "current", secret: currentSigningSecret},
		} {
			token := engineToken(t, tc.secret, tc.id, "sdk-test", "sdk-test", http.MethodGet, signedDiscoveryTarget, nil, now-1, now+59)
			status, body := signingCall(h, http.MethodGet, signedDiscoveryTarget, nil, token)
			if status != http.StatusOK {
				t.Fatalf("%s key: HTTP %d %s", tc.id, status, body)
			}
		}
	})

	t.Run("retired key is rejected", func(t *testing.T) {
		retiredAt := now - 1
		t.Setenv(EnvSigningKeys, signingRing(t, "current",
			signingKey("old", oldSigningSecret, nil, &retiredAt),
			signingKey("current", currentSigningSecret, nil, nil),
		))
		h := signedHandler(t)
		token := engineToken(t, oldSigningSecret, "old", "sdk-test", "sdk-test", http.MethodGet, signedDiscoveryTarget, nil, now-1, now+59)
		status, body := signingCall(h, http.MethodGet, signedDiscoveryTarget, nil, token)
		assertInvalidEngineToken(t, status, body)
	})
}
