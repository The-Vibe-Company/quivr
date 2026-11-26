package call_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/call"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost"
)

// This transport owner proves every operation signs the final body, including
// the stable idempotency key added by the engine. SDK tests own verification.
func TestEngineSignsDiscoveryAndEveryContribution(t *testing.T) {
	t.Setenv("QUIVR_PLUGIN_DISCOVERY_TTL", "")
	secret := []byte("independent-fixture-key-32-bytes!!")
	ring := plugins.SigningKeys{Active: "current", Keys: []plugins.SigningKey{{ID: "current", Secret: base64.RawURLEncoding.EncodeToString(secret)}}}
	raw, _ := json.Marshal(map[string]plugins.SigningKeys{"test.calls": ring})
	path := filepath.Join(t.TempDir(), "engine-signing-rings")
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(plugins.EnvSigningKeys, "")
	t.Setenv(plugins.EnvSigningKeys+"_FILE", path)
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
	if received.Load() != int64(1+len(operations)) {
		t.Errorf("received %d signed requests, want one discovery and %d Contribution calls", received.Load(), len(operations))
	}
	t.Setenv(plugins.EnvSigningKeys+"_FILE", "")
	before := received.Load()
	if _, err := call.Discover(context.Background(), pin); err == nil || received.Load() != before {
		t.Fatal("missing key reached the peer or was accepted")
	}
}

func TestContributionSigningFailureKeepsVerifiedDiscovery(t *testing.T) {
	t.Setenv("QUIVR_PLUGIN_DISCOVERY_TTL", "")
	pin := policyPin()
	pin.Endpoint = freshEndpoint()
	pin.Manifest.Compatibility.PluginAPI = ">=0.14.0 <0.15.0"
	document := discoveryDocument(t, pin)
	ring, err := plugins.NewSigningKeys()
	if err != nil {
		t.Fatal(err)
	}
	setRing := func() {
		raw, err := json.Marshal(map[string]plugins.SigningKeys{pin.Manifest.ID: ring})
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv(plugins.EnvSigningKeys, string(raw))
	}
	setRing()
	gets, posts := 0, 0
	t.Cleanup(func() { devhost.SetTransport(nil) })
	devhost.SetTransport(roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			gets++
			return wireResponse(http.StatusOK, document), nil
		}
		posts++
		return wireResponse(http.StatusOK, "{}"), nil
	}))
	_, err = call.Invoke(t.Context(), pin, call.EmbedQuery, func(context.Context, string) ([]byte, error) {
		ring.Keys[0].NotAfter = 1
		setRing()
		return []byte("{}"), nil
	}, nil, nil)
	if !errors.Is(err, plugins.ErrSigningKeys) || gets != 1 || posts != 0 {
		t.Fatalf("local signing failure: error=%v GETs=%d POSTs=%d, want a signing error before sending the POST", err, gets, posts)
	}
	ring.Keys[0].NotAfter = 0
	setRing()
	_, err = call.Invoke(t.Context(), pin, call.EmbedQuery, call.Bytes([]byte("{}")), nil, nil)
	if err != nil || gets != 1 || posts != 1 {
		t.Fatalf("retry after restoring signing key: error=%v GETs=%d POSTs=%d, want one discovery and one successful POST", err, gets, posts)
	}
}
