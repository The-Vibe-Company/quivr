package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/logging"
)

// Owns external JSON key compatibility, environment precedence, conflict checks
// and secret redaction for admin_listen and admin_keys.
func TestAdminListenerConfigValidationAndRedaction(t *testing.T) {
	previousLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	const (
		funcToken  = "functional-token-0123456789abcdef01234567"
		adminToken = "dedicated-admin-token-0123456789abcdef012"
	)
	// Omitted admin_listen/admin_keys preserves combined mode.
	var omitted Config
	if err := json.Unmarshal([]byte(`{"listen":"127.0.0.1:8080","probe_listen":"127.0.0.1:8081"}`), &omitted); err != nil {
		t.Fatal(err)
	}
	separated, shareProbe, err := omitted.validateAdminListener()
	if err != nil || separated || shareProbe {
		t.Fatalf("omitted config: separated=%t shareProbe=%t err=%v", separated, shareProbe, err)
	}

	// Literal external keys configure a dedicated internal admin listener.
	var dedicated Config
	raw := `{"listen":"0.0.0.0:8080","probe_listen":"0.0.0.0:8081","admin_listen":"0.0.0.0:8082","keys":{"` + funcToken + `":{"organization":"org_a","actions":["corpora:read"],"corpora":["*"]}},"admin_keys":{"` + adminToken + `":{"organization":"org_a","actions":["plugins:admin"],"corpora":["*"]}}}`
	if err := json.Unmarshal([]byte(raw), &dedicated); err != nil {
		t.Fatal(err)
	}
	separated, shareProbe, err = dedicated.validateAdminListener()
	if err != nil || !separated || shareProbe {
		t.Fatalf("dedicated admin listener: separated=%t shareProbe=%t err=%v", separated, shareProbe, err)
	}

	// Sharing probe_listen is allowed when both addresses match identically.
	shared := dedicated
	shared.AdminListen = "0.0.0.0:8081"
	separated, shareProbe, err = shared.validateAdminListener()
	if err != nil || !separated || !shareProbe {
		t.Fatalf("shared probe/admin listener: separated=%t shareProbe=%t err=%v", separated, shareProbe, err)
	}

	// Environment override QUIVR_ADMIN_LISTEN overrides file config, and "off" clears
	// both admin_listen and admin_keys so combined mode starts cleanly.
	t.Setenv("QUIVR_ADMIN_LISTEN", "127.0.0.1:8085")
	overridden := dedicated
	if _, err := overridden.configureProcess("api"); err != nil {
		t.Fatal(err)
	}
	if overridden.AdminListen != "127.0.0.1:8085" {
		t.Fatalf("QUIVR_ADMIN_LISTEN override: got %q", overridden.AdminListen)
	}
	t.Setenv("QUIVR_ADMIN_LISTEN", "off")
	disabled := dedicated
	if _, err := disabled.configureProcess("api"); err != nil {
		t.Fatal(err)
	}
	if disabled.AdminListen != "" || len(disabled.AdminKeys) != 0 {
		t.Fatalf("QUIVR_ADMIN_LISTEN=off: listen=%q adminKeys=%d", disabled.AdminListen, len(disabled.AdminKeys))
	}
	if sep, sharedProbe, err := disabled.validateAdminListener(); err != nil || sep || sharedProbe {
		t.Fatalf("QUIVR_ADMIN_LISTEN=off validation: sep=%t shareProbe=%t err=%v", sep, sharedProbe, err)
	}
	t.Setenv("QUIVR_ADMIN_LISTEN", "")

	// Failure matrix: every conflict, missing-key and syntax rule fails closed.
	validScope := corpus.Scope{Organization: "org_a", Actions: []string{"plugins:admin"}, Corpora: []string{"*"}}
	baseKeys := map[string]corpus.Scope{funcToken: validScope}
	baseAdminKeys := map[string]corpus.Scope{adminToken: validScope}
	for _, tc := range []struct {
		name  string
		cfg   Config
		code  configCode
		field string
	}{
		{"admin_keys without admin_listen", Config{Listen: "127.0.0.1:8080", ProbeListen: "127.0.0.1:8081", Keys: baseKeys, AdminKeys: baseAdminKeys}, configConflict, "admin_keys"},
		{"admin_listen without admin_keys", Config{Listen: "127.0.0.1:8080", ProbeListen: "127.0.0.1:8081", AdminListen: "127.0.0.1:8082", Keys: baseKeys}, configMissing, "admin_keys"},
		{"invalid admin_listen syntax", Config{Listen: "127.0.0.1:8080", ProbeListen: "127.0.0.1:8081", AdminListen: "not-a-host-port", Keys: baseKeys, AdminKeys: baseAdminKeys}, configInvalid, "admin_listen"},
		{"admin_listen whitespace host", Config{Listen: "127.0.0.1:8080", ProbeListen: "127.0.0.1:8081", AdminListen: "bad host:8082", Keys: baseKeys, AdminKeys: baseAdminKeys}, configInvalid, "admin_listen"},
		{"admin_listen arbitrary hostname", Config{Listen: "127.0.0.1:8080", ProbeListen: "127.0.0.1:8081", AdminListen: "api.internal:8082", Keys: baseKeys, AdminKeys: baseAdminKeys}, configInvalid, "admin_listen"},
		{"admin_listen equals listen", Config{Listen: "127.0.0.1:8080", ProbeListen: "127.0.0.1:8081", AdminListen: "127.0.0.1:8080", Keys: baseKeys, AdminKeys: baseAdminKeys}, configConflict, "admin_listen"},
		{"admin_listen localhost collides ipv4 loopback listen", Config{Listen: "127.0.0.1:8080", ProbeListen: "127.0.0.1:8081", AdminListen: "localhost:8080", Keys: baseKeys, AdminKeys: baseAdminKeys}, configConflict, "admin_listen"},
		{"admin_listen localhost collides ipv6 loopback listen", Config{Listen: "[::1]:8080", ProbeListen: "127.0.0.1:8081", AdminListen: "localhost:8080", Keys: baseKeys, AdminKeys: baseAdminKeys}, configConflict, "admin_listen"},
		{"admin_listen localhost collides ipv4 loopback probe", Config{Listen: "127.0.0.1:8080", ProbeListen: "127.0.0.1:8081", AdminListen: "localhost:8081", Keys: baseKeys, AdminKeys: baseAdminKeys}, configConflict, "admin_listen"},
		{"admin_listen collides wildcard listen", Config{Listen: "0.0.0.0:8080", ProbeListen: "0.0.0.0:8081", AdminListen: "127.0.0.1:8080", Keys: baseKeys, AdminKeys: baseAdminKeys}, configConflict, "admin_listen"},
		{"admin_listen collides mismatched probe host", Config{Listen: "0.0.0.0:8080", ProbeListen: "0.0.0.0:8081", AdminListen: "127.0.0.1:8081", Keys: baseKeys, AdminKeys: baseAdminKeys}, configConflict, "admin_listen"},
		{"short admin key", Config{Listen: "127.0.0.1:8080", ProbeListen: "127.0.0.1:8081", AdminListen: "127.0.0.1:8082", Keys: baseKeys, AdminKeys: map[string]corpus.Scope{"short-key": validScope}}, configInvalid, "admin_keys"},
		{"reused functional key", Config{Listen: "127.0.0.1:8080", ProbeListen: "127.0.0.1:8081", AdminListen: "127.0.0.1:8082", Keys: baseKeys, AdminKeys: map[string]corpus.Scope{funcToken: validScope}}, configConflict, "admin_keys"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := tc.cfg.validateAdminListener()
			var ce *configError
			if !errors.As(err, &ce) || ce.code != tc.code || ce.field != tc.field {
				t.Fatalf("want (%s, %s), got %v", tc.code, tc.field, err)
			}
		})
	}

	// Admin keys must be redacted from logs and never appear in the startup summary.
	var logBuf bytes.Buffer
	logger, err := logging.New(&logBuf, logging.Options{Secrets: dedicated.logSecrets()})
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("startup", dedicated.processSummary(0), "leaked", adminToken)
	output := logBuf.String()
	if strings.Contains(output, adminToken) {
		t.Fatalf("admin key leaked into logs: %s", output)
	}
	if !strings.Contains(output, `"admin_listen":"0.0.0.0:8082"`) || !strings.Contains(output, `"admin_key_count":1`) {
		t.Fatalf("startup summary missing safe admin listener fields: %s", output)
	}
}
