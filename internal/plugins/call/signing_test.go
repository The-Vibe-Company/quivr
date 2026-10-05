package call_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/call"
)

// This transport owner proves every operation signs the final body, including
// the stable idempotency key added by the engine. SDK tests own verification.
func TestEngineSignsDiscoveryAndEveryContribution(t *testing.T) {
	secret := []byte("independent-fixture-key-32-bytes!!")
	ring := plugins.SigningKeys{Active: "current", Keys: []plugins.SigningKey{{ID: "current", Secret: base64.RawURLEncoding.EncodeToString(secret)}}}
	raw, _ := json.Marshal(map[string]plugins.SigningKeys{"test.calls": ring})
	t.Setenv(plugins.EnvSigningKeys, string(raw))
	pin := policyPin()
	pin.Manifest.Compatibility.PluginAPI = ">=0.14.0 <0.15.0"
	var received atomic.Int64
	inspect := func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		body, _ := io.ReadAll(r.Body)
		parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
		if len(parts) != 3 {
			t.Errorf("unsigned %s %s", r.Method, r.URL.Path)
			return
		}
		signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
		if !hmac.Equal(signature, mac.Sum(nil)) {
			t.Error("signature does not match the independently provisioned key")
		}
		claimsRaw, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var claims map[string]any
		_ = json.Unmarshal(claimsRaw, &claims)
		sum := sha256.Sum256(body)
		now := float64(time.Now().Unix())
		if claims["aud"] != pin.Manifest.ID || claims["plugin_id"] != pin.Manifest.ID || claims["target"] != r.URL.RequestURI() || claims["method"] != r.Method || claims["body_sha256"] != hex.EncodeToString(sum[:]) || claims["iat"].(float64) > now || claims["exp"].(float64) <= now || claims["exp"].(float64)-claims["iat"].(float64) > 60 {
			t.Errorf("request binding or lifetime missing for %s", r.URL.Path)
		}
		if r.Method == "GET" {
			if claims["contribution"] != "discovery" {
				t.Error("discovery scope missing")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"plugin_api": pin.PluginAPI(), "plugin": map[string]string{"id": pin.Manifest.ID, "version": pin.Manifest.Version}, "manifest_digest": pin.ManifestDigest, "contributions": pin.Manifest.Contributions.Names()})
		} else {
			if claims["contribution"] != strings.Split(r.URL.Path, "/")[3] {
				t.Error("contribution scope missing")
			}
			_, _ = w.Write([]byte(`{}`))
		}
	}
	server := httptest.NewServer(http.HandlerFunc(inspect))
	defer server.Close()
	pin.Endpoint = server.URL
	for _, op := range operations {
		if result, err := call.Invoke(context.Background(), pin, op, call.Bytes([]byte(`{}`)), nil, nil); result == nil {
			t.Fatalf("%s did not reach the wire: %v", op, err)
		}
	}
	if received.Load() != int64(2*len(operations)) {
		t.Errorf("received %d signed requests, want %d", received.Load(), 2*len(operations))
	}
	t.Setenv(plugins.EnvSigningKeys, "")
	before := received.Load()
	if _, err := call.Discover(context.Background(), pin); err == nil || received.Load() != before {
		t.Fatal("missing key reached the peer or was accepted")
	}
}
