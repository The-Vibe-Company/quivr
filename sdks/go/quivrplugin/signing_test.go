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
	"testing"
	"time"
)

var (
	currentSigningSecret = []byte("01234567890123456789012345678901")
	oldSigningSecret     = []byte("abcdefghijklmnopqrstuvwxyz123456")
)

const (
	signedDiscoveryTarget = "/v0/discovery?source=engine"
	signedFetchTarget     = "/v0/contributions/connector/fetch?source=engine"
)

// engineToken builds the wire token independently of the SDK signer. Keeping
// this helper in the HTTP-boundary tests prevents a shared signer from making
// both sides of the contract agree on the same mistake.
func engineToken(t *testing.T, secret []byte, kid, audience, pluginID, method, target string, body []byte, issued, expiry int64, edit func(map[string]any, map[string]any)) string {
	t.Helper()
	header := map[string]any{"alg": "HS256", "typ": "quivr-engine+jwt", "kid": kid}
	digest := sha256.Sum256(body)
	claims := map[string]any{
		"aud":          audience,
		"plugin_id":    pluginID,
		"contribution": "discovery",
		"method":       method,
		"target":       target,
		"iat":          issued,
		"exp":          expiry,
		"body_sha256":  hex.EncodeToString(digest[:]),
	}
	if len(target) > len("/v0/contributions/") && target[:len("/v0/contributions/")] == "/v0/contributions/" {
		path := target
		for i, r := range path {
			if r == '?' {
				path = path[:i]
				break
			}
		}
		parts := bytes.Split([]byte(path), []byte("/"))
		if len(parts) >= 4 {
			claims["contribution"] = string(parts[3])
		}
	}
	if edit != nil {
		edit(header, claims)
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

func TestSignedCallsAcceptDiscoveryAndDispatchSignedPOST(t *testing.T) {
	now := time.Now().Unix()
	t.Setenv(EnvSigningKeys, signingRing(t, "current", signingKey("current", currentSigningSecret, nil, nil)))
	h := signedHandler(t)

	token := engineToken(t, currentSigningSecret, "current", "sdk-test", "sdk-test", http.MethodGet, signedDiscoveryTarget, nil, now-1, now+59, nil)
	status, body := signingCall(h, http.MethodGet, signedDiscoveryTarget, nil, token)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"plugin":{"id":"sdk-test"`)) {
		t.Fatalf("signed discovery: HTTP %d %s", status, body)
	}

	malformed := []byte("{")
	token = engineToken(t, currentSigningSecret, "current", "sdk-test", "sdk-test", http.MethodPost, signedFetchTarget, malformed, now-1, now+59, nil)
	status, body = signingCall(h, http.MethodPost, signedFetchTarget, malformed, token)
	if status != http.StatusBadRequest || !bytes.Contains(body, []byte(`"code":"invalid_request"`)) {
		t.Fatalf("signed POST dispatch: HTTP %d %s", status, body)
	}
}

func TestSignedCallsRejectInvalidTokensBeforeDispatch(t *testing.T) {
	type testCase struct {
		name   string
		make   func(now int64) string
		method string
		target string
		body   []byte
	}
	cases := []testCase{
		{
			name:   "unsigned",
			make:   func(int64) string { return "" },
			method: http.MethodGet,
			target: signedDiscoveryTarget,
		},
		{
			name: "forged",
			make: func(now int64) string {
				return engineToken(t, []byte("wrong-signing-secret-012345678901"), "current", "sdk-test", "sdk-test", http.MethodGet, signedDiscoveryTarget, nil, now-1, now+59, nil)
			},
			method: http.MethodGet,
			target: signedDiscoveryTarget,
		},
		{
			name: "expired",
			make: func(now int64) string {
				return engineToken(t, currentSigningSecret, "current", "sdk-test", "sdk-test", http.MethodGet, signedDiscoveryTarget, nil, now-120, now-60, nil)
			},
			method: http.MethodGet,
			target: signedDiscoveryTarget,
		},
		{
			name: "wrong audience",
			make: func(now int64) string {
				return engineToken(t, currentSigningSecret, "current", "other-plugin", "sdk-test", http.MethodGet, signedDiscoveryTarget, nil, now-1, now+59, nil)
			},
			method: http.MethodGet,
			target: signedDiscoveryTarget,
		},
		{
			name: "wrong plugin id",
			make: func(now int64) string {
				return engineToken(t, currentSigningSecret, "current", "sdk-test", "other-plugin", http.MethodGet, signedDiscoveryTarget, nil, now-1, now+59, nil)
			},
			method: http.MethodGet,
			target: signedDiscoveryTarget,
		},
		{
			name: "tampered body",
			make: func(now int64) string {
				return engineToken(t, currentSigningSecret, "current", "sdk-test", "sdk-test", http.MethodGet, signedDiscoveryTarget, []byte("original"), now-1, now+59, nil)
			},
			method: http.MethodGet,
			target: signedDiscoveryTarget,
			body:   []byte("tampered"),
		},
		{
			name: "wrong route",
			make: func(now int64) string {
				return engineToken(t, currentSigningSecret, "current", "sdk-test", "sdk-test", http.MethodGet, signedDiscoveryTarget, nil, now-1, now+59, nil)
			},
			method: http.MethodGet,
			target: "/v0/discovery?source=other",
		},
		{
			name: "wrong method",
			make: func(now int64) string {
				return engineToken(t, currentSigningSecret, "current", "sdk-test", "sdk-test", http.MethodPost, signedDiscoveryTarget, nil, now-1, now+59, nil)
			},
			method: http.MethodGet,
			target: signedDiscoveryTarget,
		},
		{
			name: "future issued at",
			make: func(now int64) string {
				return engineToken(t, currentSigningSecret, "current", "sdk-test", "sdk-test", http.MethodGet, signedDiscoveryTarget, nil, now+10, now+20, nil)
			},
			method: http.MethodGet,
			target: signedDiscoveryTarget,
		},
		{
			name: "lifetime over sixty seconds",
			make: func(now int64) string {
				return engineToken(t, currentSigningSecret, "current", "sdk-test", "sdk-test", http.MethodGet, signedDiscoveryTarget, nil, now-1, now+70, nil)
			},
			method: http.MethodGet,
			target: signedDiscoveryTarget,
		},
		{
			name: "algorithm confusion",
			make: func(now int64) string {
				return engineToken(t, currentSigningSecret, "current", "sdk-test", "sdk-test", http.MethodGet, signedDiscoveryTarget, nil, now-1, now+59, func(header, _ map[string]any) {
					header["alg"] = "none"
				})
			},
			method: http.MethodGet,
			target: signedDiscoveryTarget,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().Unix()
			t.Setenv(EnvSigningKeys, signingRing(t, "current", signingKey("current", currentSigningSecret, nil, nil)))
			h := signedHandler(t)
			status, body := signingCall(h, tc.method, tc.target, tc.body, tc.make(now))
			assertInvalidEngineToken(t, status, body)
		})
	}
}

func TestSigningKeyConfigurationFailsClosedWithoutEchoingSecrets(t *testing.T) {
	now := time.Now().Unix()
	token := engineToken(t, currentSigningSecret, "current", "sdk-test", "sdk-test", http.MethodGet, signedDiscoveryTarget, nil, now-1, now+59, nil)
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
			token := engineToken(t, tc.secret, tc.id, "sdk-test", "sdk-test", http.MethodGet, signedDiscoveryTarget, nil, now-1, now+59, nil)
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
		token := engineToken(t, oldSigningSecret, "old", "sdk-test", "sdk-test", http.MethodGet, signedDiscoveryTarget, nil, now-1, now+59, nil)
		status, body := signingCall(h, http.MethodGet, signedDiscoveryTarget, nil, token)
		assertInvalidEngineToken(t, status, body)
	})
}
