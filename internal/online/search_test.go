package online_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/online"
	"github.com/The-Vibe-Company/quivr-v2/internal/testutil/apicontract"
)

const hitJSON = `{"items":[{"record_id":"rec_1","version_id":"ver_1","part_key":"body","segment_id":"seg_1","segmentation_id":"sgm_1","projection_generation_id":"gen_1","rank":1,"excerpt":{"text":"Eclipse over\nthe city","start":4,"end":24,"coordinate_system":"unicode_codepoint"},"availability":{"state":"retrieval_ready","is_current":true,"searchable":true}}],"retrieval_profile":{"name":"default","version":"v1"}}`

type result struct {
	code           int
	stdout, stderr string
}

func run(t *testing.T, env map[string]string, args ...string) result {
	t.Helper()
	var out, errw bytes.Buffer
	code := online.RunContext(context.Background(), args[0], args[1:], online.Env{
		Getenv: func(k string) string { return env[k] },
		Stdout: &out, Stderr: &errw,
	})
	return result{code, out.String(), errw.String()}
}

// server answers /v0/search with status and body and records the last request.
func server(t *testing.T, status int, body string) (*httptest.Server, *http.Request, *map[string]any) {
	t.Helper()
	var last http.Request
	var payload map[string]any
	s := httptest.NewServer(apicontract.Handler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last = *r
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &payload)
		if r.URL.Path != "/v0/search" || r.Method != http.MethodPost {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})))
	t.Cleanup(s.Close)
	return s, &last, &payload
}

func TestSearchPrintsRankedHitsWithProvenance(t *testing.T) {
	s, last, payload := server(t, 200, hitJSON)
	r := run(t, map[string]string{online.EnvAPIURL: s.URL + "/", online.EnvAPIKey: "env-key"},
		"search", "eclipse", "--corpus", "c1,c2", "--corpus", "c3", "--mode", "lexical", "--limit", "5", "--source", "feed-a,feed-b", "city")
	if r.code != online.ExitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	for _, want := range []string{"1 hit (profile default, version v1)", "1. record rec_1  version ver_1  part body  [4,24)", "   Eclipse over\n   the city\n"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, r.stdout)
		}
	}
	if got := last.Header.Get("Authorization"); got != "Bearer env-key" {
		t.Errorf("Authorization = %q", got)
	}
	want := map[string]any{"query": "eclipse city", "corpus_ids": []any{"c1", "c2", "c3"}, "mode": "lexical", "limit": float64(5), "filter": map[string]any{"source_namespaces": []any{"feed-a", "feed-b"}}}
	if b1, b2 := mustJSON(*payload), mustJSON(want); b1 != b2 {
		t.Errorf("request body = %s, want %s", b1, b2)
	}
}

func TestSearchOmitsUnsetOptionsAndFlagsOverrideEnv(t *testing.T) {
	s, last, payload := server(t, 200, hitJSON)
	r := run(t, map[string]string{online.EnvAPIURL: "http://127.0.0.1:1", online.EnvAPIKey: "env-key"},
		"search", "--api-url", s.URL, "--api-key", "flag-key", "--corpus", "c1", "q")
	if r.code != online.ExitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if got := last.Header.Get("Authorization"); got != "Bearer flag-key" {
		t.Errorf("Authorization = %q", got)
	}
	if b := mustJSON(*payload); b != `{"corpus_ids":["c1"],"query":"q"}` {
		t.Errorf("request body = %s", b)
	}
}

func TestSearchWithoutKeySendsNoAuthorization(t *testing.T) {
	s, last, _ := server(t, 200, hitJSON)
	if r := run(t, map[string]string{online.EnvAPIURL: s.URL}, "search", "--corpus", "c1", "q"); r.code != online.ExitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if got := last.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestSearchJSONPrintsTheResponseUnchanged(t *testing.T) {
	s, _, _ := server(t, 200, hitJSON)
	r := run(t, map[string]string{online.EnvAPIURL: s.URL}, "search", "--json", "--corpus", "c1", "q")
	if r.code != online.ExitOK || r.stdout != hitJSON+"\n" {
		t.Fatalf("exit %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
	}
}

func TestSearchExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		exit   int
		stderr []string
	}{
		{"bad key", 401, `{"code":"invalid_api_key","message":"unknown key","retryable":false}`, online.ExitDenied, []string{"quivr: invalid_api_key: unknown key", online.EnvAPIKey}},
		{"scope", 403, `{"code":"forbidden","message":"not allowed","retryable":false}`, online.ExitDenied, []string{"quivr: forbidden: not allowed", "Corpus scope"}},
		{"malformed", 400, `{"code":"malformed_json","message":"bad","retryable":false}`, online.ExitInvalid, []string{"quivr: malformed_json: bad"}},
		{"invalid", 422, `{"code":"invalid_schema","message":"limit too small","retryable":false,"field":"/limit"}`, online.ExitInvalid, []string{"quivr: invalid_schema: limit too small (/limit)"}},
		{"unavailable", 503, `{"code":"search_unavailable","message":"down","retryable":true}`, online.ExitUnavailable, []string{"search_unavailable", "retry later"}},
		{"proxy error", 502, `<html>bad gateway</html>`, online.ExitUnavailable, []string{"quivr: HTTP 502"}},
		{"unexpected", 302, ``, online.ExitFailed, []string{"quivr: HTTP 302"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s *httptest.Server
			if tc.name == "proxy error" || tc.name == "unexpected" {
				// These deliberately malformed intermediary responses exercise the
				// CLI's transport fallback, not a successful Quivr API fake.
				s = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
				}))
				t.Cleanup(s.Close)
			} else {
				s, _, _ = server(t, tc.status, tc.body)
			}
			r := run(t, map[string]string{online.EnvAPIURL: s.URL, online.EnvAPIKey: "k"}, "search", "--corpus", "c1", "q")
			if r.code != tc.exit || r.stdout != "" {
				t.Fatalf("exit %d (want %d) stdout %q stderr %q", r.code, tc.exit, r.stdout, r.stderr)
			}
			for _, want := range tc.stderr {
				if !strings.Contains(r.stderr, want) {
					t.Errorf("stderr missing %q: %s", want, r.stderr)
				}
			}
		})
	}
}

func TestSearchUnreachableServer(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	r := run(t, map[string]string{online.EnvAPIURL: "http://" + addr}, "search", "--corpus", "c1", "q")
	if r.code != online.ExitUnavailable || !strings.Contains(r.stderr, "quivr: unreachable:") || !strings.Contains(r.stderr, online.EnvAPIURL) {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
}

func TestSearchNeverPrintsURLCredentials(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	r := run(t, map[string]string{online.EnvAPIURL: "http://user:s3cret@" + addr}, "search", "--corpus", "c1", "q")
	if r.code != online.ExitUnavailable || strings.Contains(r.stderr, "s3cret") {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
}

func TestSearchInterruptedIsNotAnOutage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		cancel()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(s.Close)
	var out, errw bytes.Buffer
	code := online.RunContext(ctx, "search", []string{"--corpus", "c1", "q"}, online.Env{
		Getenv: func(k string) string { return map[string]string{online.EnvAPIURL: s.URL}[k] },
		Stdout: &out, Stderr: &errw,
	})
	if code != online.ExitInterrupted || !strings.Contains(errw.String(), "interrupted") {
		t.Fatalf("exit %d stderr %q", code, errw.String())
	}
}

func TestSearchUsageErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		env  map[string]string
		args []string
		want string
	}{
		"no query":     {map[string]string{online.EnvAPIURL: "http://x"}, []string{"--corpus", "c1"}, "needs a query"},
		"no corpus":    {map[string]string{online.EnvAPIURL: "http://x"}, []string{"q"}, "at least one --corpus"},
		"no url":       {nil, []string{"--corpus", "c1", "q"}, "set " + online.EnvAPIURL},
		"bad url":      {map[string]string{online.EnvAPIURL: "localhost:8080"}, []string{"--corpus", "c1", "q"}, "invalid API URL"},
		"bad flag":     {map[string]string{online.EnvAPIURL: "http://x"}, []string{"--nope", "--corpus", "c1", "q"}, "-nope"},
		"bad limit":    {map[string]string{online.EnvAPIURL: "http://x"}, []string{"--limit", "ten", "--corpus", "c1", "q"}, "-limit"},
		"after dashes": {map[string]string{online.EnvAPIURL: "http://x"}, []string{"--", "--corpus", "c1"}, "at least one --corpus"},
	} {
		t.Run(name, func(t *testing.T) {
			r := run(t, tc.env, append([]string{"search"}, tc.args...)...)
			if r.code != online.ExitUsage || !strings.Contains(r.stderr, tc.want) {
				t.Fatalf("exit %d stderr %q", r.code, r.stderr)
			}
		})
	}
}

func TestSearchHelpNamesTheServerAndEnvironment(t *testing.T) {
	r := run(t, nil, "search", "--help")
	if r.code != online.ExitOK {
		t.Fatalf("exit %d", r.code)
	}
	for _, want := range []string{"needs a running Quivr server", online.EnvAPIURL, online.EnvAPIKey, "--corpus", "--json", "Exit codes"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("help missing %q:\n%s", want, r.stdout)
		}
	}
}

// Online commands reach Quivr only through the public API: they must never
// link the storage, workflow or search adapters the engine processes use.
func TestOnlineCommandsLinkNoInfrastructure(t *testing.T) {
	gobin := filepath.Join(runtime.GOROOT(), "bin", "go")
	out, err := exec.Command(gobin, "list", "-deps", "github.com/The-Vibe-Company/quivr-v2/internal/online").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, dep := range strings.Fields(string(out)) {
		for _, banned := range []string{"github.com/jackc/pgx", "go.temporal.io/", "github.com/aws/", "/internal/adapters", "/internal/app", "/internal/orchestration"} {
			if strings.Contains(dep, banned) {
				t.Errorf("internal/online depends on %s", dep)
			}
		}
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
