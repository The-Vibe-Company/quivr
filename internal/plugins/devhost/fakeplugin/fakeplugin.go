// Package fakeplugin is a language-neutral stand-in plugin for tests of the
// plugin tooling. A test binary calls MaybeRun from TestMain; when the
// environment asks for it, the binary serves Plugin Protocol v0 instead of
// running tests, so a manifest's run.command can point at the test binary.
package fakeplugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

// Environment variables understood by the fake plugin.
const (
	EnvEnable = "QUIVR_FAKE_PLUGIN"        // "1" serves instead of testing
	EnvDigest = "QUIVR_FAKE_PLUGIN_DIGEST" // overrides the served manifest digest
	// EnvMode selects the behaviour, unless the first argument does: ok
	// (default), retry, terminal, invalid, large, garbage, exit, unhealthy, and
	// the broken plugins of tests/plugin-contract: malformed-part, bad-checksum,
	// undeclared-namespace, nondeterministic, slow, wrong-error-class,
	// accept-invalid.
	EnvMode   = "QUIVR_FAKE_PLUGIN_MODE"
	EnvMarker = "QUIVR_FAKE_PLUGIN_MARKER" // file appended with "start\n" on every start
)

// Command returns the argv that starts this test binary as the fake plugin.
func Command() []string { return []string{os.Args[0], "-test.run=^$"} }

// MaybeRun serves the fake plugin and exits when EnvEnable is "1".
func MaybeRun() {
	if os.Getenv(EnvEnable) != "1" {
		return
	}
	if err := serve(); err != nil {
		fmt.Fprintln(os.Stderr, "fake plugin:", err)
		os.Exit(3)
	}
	os.Exit(0)
}

func serve() error {
	if marker := os.Getenv(EnvMarker); marker != "" {
		f, err := os.OpenFile(marker, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, _ = f.WriteString("start\n")
		_ = f.Close()
	}
	mode := os.Getenv(EnvMode)
	// A committed manifest selects the mode as the first argument, such as
	// run.command [quivr-fake-plugin, nondeterministic].
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		mode = os.Args[1]
	}
	if mode == "exit" {
		return fmt.Errorf("exiting on purpose")
	}
	raw, err := os.ReadFile(os.Getenv("QUIVR_PLUGIN_MANIFEST"))
	if err != nil {
		return err
	}
	report := plugins.Validate(raw)
	if report.Manifest == nil {
		return fmt.Errorf("invalid manifest: %+v", report.Errors)
	}
	sum := sha256.Sum256(raw)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if d := os.Getenv(EnvDigest); d != "" {
		digest = d
	}
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, status int, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
	mux.HandleFunc("GET /v0/health", func(w http.ResponseWriter, r *http.Request) {
		if mode == "unhealthy" {
			write(w, 503, map[string]any{"code": "warming_up", "message": "not ready", "retryable": true})
			return
		}
		write(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v0/discovery", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]any{
			"plugin_api":      plugins.PluginAPIVersion,
			"plugin":          map[string]string{"id": report.Manifest.ID, "version": report.Manifest.Version},
			"manifest_digest": digest,
			"contributions":   []string{"normalizer"},
		})
	})
	mux.HandleFunc("POST /v0/contributions/normalizer", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			InvocationID string `json:"invocation_id"`
			Input        struct {
				BlobID    string `json:"blob_id"`
				MediaType string `json:"media_type"`
				SHA256    string `json:"sha256"`
				Reference struct {
					URL string `json:"url"`
				} `json:"reference"`
			} `json:"input"`
		}
		body, _ := io.ReadAll(r.Body)
		invalid := json.Unmarshal(body, &request)
		if invalid == nil {
			if issues := plugins.ValidateDocument("normalizer-request.schema.json", body); len(issues) > 0 {
				invalid = fmt.Errorf("%s %s", issues[0].Path, issues[0].Message)
			}
		}
		if invalid != nil {
			if mode == "wrong-error-class" {
				write(w, 503, map[string]any{"code": "invalid_request", "message": invalid.Error(), "retryable": true})
				return
			}
			if mode == "accept-invalid" {
				write(w, 202, map[string]any{"code": "invalid_request", "message": invalid.Error(), "retryable": false})
				return
			}
			write(w, 400, map[string]any{"code": "invalid_request", "message": invalid.Error(), "retryable": false})
			return
		}
		text := "echo " + request.Input.Reference.URL
		ok := map[string]any{"manifest": map[string]any{"kind": "manifest", "parts": []any{
			map[string]any{"key": "body", "role": "section", "content": map[string]any{"kind": "text", "text": text}},
		}}}
		stats := report.Manifest.ID + ".stats"
		if _, declared := report.Manifest.Extensions[stats]; declared {
			// A richer valid answer: the input Blob as a Part and a declared extension.
			ok = map[string]any{
				"manifest": map[string]any{"kind": "manifest", "parts": []any{
					map[string]any{"key": "original", "role": "original", "content": map[string]any{"kind": "blob", "blob_id": request.Input.BlobID, "media_type": request.Input.MediaType}},
					map[string]any{"key": "body", "parent_key": "original", "role": "section", "content": map[string]any{"kind": "text", "text": text}},
				}},
				"extensions": map[string]any{stats: map[string]any{"schema_version": "1", "data": map[string]any{"characters": len(text)}}},
				"language":   "en",
			}
		}
		switch mode {
		case "malformed-part":
			write(w, 200, map[string]any{"manifest": map[string]any{"kind": "manifest", "parts": []any{
				map[string]any{"key": "body", "role": "section", "content": map[string]any{"kind": "text", "text": "broken\x00text"}},
			}}})
		case "bad-checksum":
			// Names a Blob by the checksum of other bytes, not the input Blob.
			other := sha256.Sum256([]byte("not the input"))
			write(w, 200, map[string]any{"manifest": map[string]any{"kind": "manifest", "parts": []any{
				map[string]any{"key": "original", "role": "original", "content": map[string]any{"kind": "blob", "blob_id": "dev-blob-" + hex.EncodeToString(other[:])[:16], "media_type": request.Input.MediaType}},
				map[string]any{"key": "body", "role": "section", "content": map[string]any{"kind": "text", "text": text}},
			}}})
		case "undeclared-namespace":
			write(w, 200, map[string]any{"manifest": ok["manifest"],
				"extensions": map[string]any{"another-plugin.stats": map[string]any{"schema_version": "1", "data": map[string]any{}}}})
		case "nondeterministic":
			write(w, 200, map[string]any{"manifest": map[string]any{"kind": "manifest", "parts": []any{
				map[string]any{"key": "body", "role": "section", "content": map[string]any{"kind": "text", "text": text + " at invocation " + request.InvocationID}},
			}}})
		case "slow":
			select {
			case <-time.After(time.Duration(report.Manifest.Contributions.Normalizer.TimeoutMS)*time.Millisecond + 2*time.Second):
			case <-r.Context().Done():
				return
			}
			write(w, 200, ok)
		case "wrong-error-class":
			write(w, 200, ok)
		case "retry":
			write(w, 503, map[string]any{"code": "backend_busy", "message": "busy", "retryable": true})
		case "terminal":
			write(w, 422, map[string]any{"code": "unreadable", "message": "cannot read", "retryable": false})
		case "garbage":
			w.WriteHeader(502)
			_, _ = w.Write([]byte("bad gateway"))
		case "invalid":
			write(w, 200, map[string]any{"manifest": map[string]any{"kind": "manifest", "parts": []any{
				map[string]any{"key": "a", "role": "section", "content": map[string]any{"kind": "text", "text": "x"}},
				map[string]any{"key": "a", "role": "section", "content": map[string]any{"kind": "text", "text": "y"}},
			}}})
		case "large":
			text := make([]byte, 5<<20)
			for i := range text {
				text[i] = 'x'
			}
			write(w, 200, map[string]any{"manifest": map[string]any{"kind": "manifest", "parts": []any{
				map[string]any{"key": "a", "role": "section", "content": map[string]any{"kind": "text", "text": string(text)}},
			}}})
		default:
			write(w, 200, ok)
		}
	})
	listener, err := net.Listen("tcp", net.JoinHostPort(os.Getenv("QUIVR_PLUGIN_HOST"), os.Getenv("QUIVR_PLUGIN_PORT")))
	if err != nil {
		return err
	}
	server := &http.Server{Handler: mux}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		_ = server.Close()
	}()
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
