package online_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/online"
)

// The CLI owns declaration validation before contacting a deployment. The
// acceptance owner exercises reconciliation on a real engine instead of a fake.
func TestApplyRejectsInvalidDeclarationsBeforeNetwork(t *testing.T) {
	for _, body := range []string{
		`{"version":1,"corpora":[{"idempotency_key":"articles","name":"Articles","quota":3}]}`,
		`{"version":1,"corpora":[{"idempotency_key":"articles","name":"Articles"}],"connectors":[{"idempotency_key":"feed","corpus":"articles","source_namespace":"feed","kind":"rss","config":{},"credential":{"secret":{"token":"private-test-value"}}}]}`,
		`{"version":1,"corpora":[{"idempotency_key":"articles","name":"Articles"},{"idempotency_key":"articles","name":"Other"}]}`,
		`{"version":1,"corpora":[{"idempotency_key":"articles","name":"Articles"}],"connectors":[{"idempotency_key":"feed","corpus":"missing","source_namespace":"feed","kind":"rss","config":{}}]}`,
		`{"version":1,"corpora":[{"idempotency_key":"articles","name":"Articles"}]} {}`,
	} {
		file := filepath.Join(t.TempDir(), "sources.json")
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		r := run(t, map[string]string{online.EnvAPIURL: "http://127.0.0.1:1"}, "apply", "-f", file)
		if r.code != online.ExitUsage || !strings.Contains(r.stderr, "invalid sources declaration") || strings.Contains(r.stderr, "private-test-value") {
			t.Fatalf("invalid declaration: exit %d, output %q %q", r.code, r.stdout, r.stderr)
		}
	}
}
