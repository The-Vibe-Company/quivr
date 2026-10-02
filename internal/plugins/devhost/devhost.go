// Package devhost runs a plugin locally and talks Plugin Protocol v0 to it:
// launch the manifest's run command on an assigned port, wait for health,
// check that discovery matches the manifest, build a request from an
// invocation fixture and invoke the normalizer with the engine's validation.
// `quivr plugin dev` uses it, and the Contract Runner can reuse it. It needs
// no Quivr stack.
package devhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

// Issue codes added by the local host, beside the codes of package plugins.
const (
	CodeDiscoveryMismatch    = "discovery_mismatch"
	CodeResponseTooLarge     = plugins.CodeResponseTooLarge
	CodeInvalidErrorEnvelope = "invalid_error_envelope"
	// CodeUnexpectedDecision is a subscription decision that differs from the
	// decision a fixture expects.
	CodeUnexpectedDecision = "unexpected_decision"
)

// Environment variables a plugin process receives (the local run convention).
const (
	EnvHost     = "QUIVR_PLUGIN_HOST"
	EnvPort     = "QUIVR_PLUGIN_PORT"
	EnvManifest = "QUIVR_PLUGIN_MANIFEST"
)

// Options describe how to launch a plugin process.
type Options struct {
	Dir      string    // working directory, normally the plugin directory
	Command  []string  // argv, normally the manifest's run.command
	Manifest string    // manifest path passed as QUIVR_PLUGIN_MANIFEST
	Host     string    // defaults to 127.0.0.1
	Port     int       // 0 picks a free port
	Env      []string  // extra environment entries
	Output   io.Writer // receives the plugin's stdout and stderr; nil discards
}

// Process is a running plugin.
type Process struct {
	BaseURL string
	Port    int

	cmd  *exec.Cmd
	done chan struct{}
	err  error
	once sync.Once
}

// FreePort returns a TCP port that was free on host a moment ago.
func FreePort(host string) (int, error) {
	l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// Start launches the plugin command with the local run convention
// environment: QUIVR_PLUGIN_HOST, QUIVR_PLUGIN_PORT and QUIVR_PLUGIN_MANIFEST.
func Start(opts Options) (*Process, error) {
	if len(opts.Command) == 0 {
		return nil, errors.New("the manifest declares no run.command")
	}
	host := opts.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := opts.Port
	if port == 0 {
		var err error
		if port, err = FreePort(host); err != nil {
			return nil, fmt.Errorf("assign a port: %w", err)
		}
	}
	manifest := opts.Manifest
	if manifest != "" {
		if abs, err := filepath.Abs(manifest); err == nil {
			manifest = abs
		}
	}
	cmd := exec.Command(opts.Command[0], opts.Command[1:]...)
	cmd.Dir = opts.Dir
	cmd.Env = append(os.Environ(), opts.Env...)
	cmd.Env = append(cmd.Env, EnvHost+"="+host, EnvPort+"="+strconv.Itoa(port), EnvManifest+"="+manifest)
	out := opts.Output
	if out == nil {
		out = io.Discard
	}
	cmd.Stdout, cmd.Stderr = out, out
	// A descendant that escapes the process group while holding the output
	// pipe must not block Wait forever.
	cmd.WaitDelay = 2 * time.Second
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", strings.Join(opts.Command, " "), err)
	}
	p := &Process{BaseURL: "http://" + net.JoinHostPort(host, strconv.Itoa(port)), Port: port, cmd: cmd, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// Done is closed when the process has exited.
func (p *Process) Done() <-chan struct{} { return p.done }

// ExitError describes how the process ended; valid after Done is closed.
func (p *Process) ExitError() error {
	if p.err == nil {
		return errors.New("exit status 0")
	}
	return p.err
}

// Stop asks the plugin (and its process group) to terminate, then kills it
// after grace.
func (p *Process) Stop(grace time.Duration) error {
	p.once.Do(func() {
		select {
		case <-p.done:
		default:
			_ = terminate(p.cmd.Process)
			select {
			case <-p.done:
			case <-time.After(grace):
				_ = kill(p.cmd.Process)
				<-p.done
			}
		}
		_ = kill(p.cmd.Process) // children left behind in the process group
	})
	return nil
}

// client never follows a redirect: the Plugin Protocol has none, and a 307
// or 308 would resend a request body (a connector's credential) to another
// address. A 3xx answer carries no error envelope, so it counts as plugin
// unavailability, for the engine and the Contract Runner alike.
var client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func get(ctx context.Context, url string, timeout time.Duration) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, err
}

// WaitHealthy polls GET /v0/health until it returns 200, the process exits or
// ctx ends.
func (p *Process) WaitHealthy(ctx context.Context) error {
	return waitHealthy(ctx, p.BaseURL, p.done, p.ExitError)
}

// WaitHealthyAt polls GET /v0/health on an already running plugin until it
// returns 200 or ctx ends.
func WaitHealthyAt(ctx context.Context, baseURL string) error {
	return waitHealthy(ctx, baseURL, nil, nil)
}

func waitHealthy(ctx context.Context, baseURL string, done <-chan struct{}, exitErr func() error) error {
	last := "no answer yet"
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, body, err := get(ctx, baseURL+"/v0/health", 2*time.Second)
		switch {
		case err != nil && ctx.Err() != nil:
			// keep the last meaningful answer
		case err != nil:
			last = err.Error()
		case status == http.StatusOK:
			if issues := plugins.ValidateDocument("health.schema.json", body); len(issues) > 0 {
				return fmt.Errorf("GET /v0/health returned an invalid body: %s", issues[0].Message)
			}
			return nil
		default:
			last = fmt.Sprintf("GET /v0/health returned %d %s", status, describeEnvelope(body))
		}
		select {
		case <-done:
			return fmt.Errorf("plugin exited (%v) before becoming healthy", exitErr())
		case <-ctx.Done():
			return fmt.Errorf("plugin not healthy at %s: %s", baseURL, last)
		case <-ticker.C:
		}
	}
}

func describeEnvelope(body []byte) string {
	var e Envelope
	if json.Unmarshal(body, &e) == nil && e.Code != "" {
		return e.Code + ": " + e.Message
	}
	return strings.TrimSpace(string(body))
}

// CheckDiscovery fetches GET /v0/discovery and compares it with the inspected
// manifest: schema, manifest digest, plugin id and version, Plugin API version
// within the declared range, and Contributions.
func CheckDiscovery(ctx context.Context, baseURL string, report plugins.Report) ([]plugins.Issue, error) {
	_, issues, err := Discover(ctx, baseURL, report)
	return issues, err
}

// Discover is CheckDiscovery that also returns the Plugin API version the
// discovery document serves (empty when the document is unusable).
func Discover(ctx context.Context, baseURL string, report plugins.Report) (string, []plugins.Issue, error) {
	status, body, err := get(ctx, baseURL+"/v0/discovery", 5*time.Second)
	if err != nil {
		return "", nil, fmt.Errorf("GET /v0/discovery: %w", err)
	}
	if status != http.StatusOK {
		return "", []plugins.Issue{{Code: CodeDiscoveryMismatch, Message: fmt.Sprintf("GET /v0/discovery returned %d %s", status, describeEnvelope(body))}}, nil
	}
	if issues := plugins.ValidateDocument("discovery.schema.json", body); len(issues) > 0 {
		return "", issues, nil
	}
	var doc struct {
		PluginAPI string `json:"plugin_api"`
		Plugin    struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		} `json:"plugin"`
		ManifestDigest string   `json:"manifest_digest"`
		Contributions  []string `json:"contributions"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", nil, err
	}
	var issues []plugins.Issue
	mismatch := func(path, format string, args ...any) {
		issues = append(issues, plugins.Issue{Code: CodeDiscoveryMismatch, Path: path, Message: fmt.Sprintf(format, args...)})
	}
	if doc.ManifestDigest != report.ManifestDigest {
		mismatch("/manifest_digest", "discovery serves manifest digest %s but %s has %s; the running plugin was built from a different quivr-plugin.yaml",
			doc.ManifestDigest, report.Path, report.ManifestDigest)
	}
	m := report.Manifest
	if m == nil {
		return doc.PluginAPI, issues, nil
	}
	if doc.Plugin.ID != m.ID {
		mismatch("/plugin/id", "discovery serves plugin id %q; the manifest declares %q", doc.Plugin.ID, m.ID)
	}
	if doc.Plugin.Version != m.Version {
		mismatch("/plugin/version", "discovery serves plugin version %q; the manifest declares %q", doc.Plugin.Version, m.Version)
	}
	if r, err := plugins.ParseRange(m.Compatibility.PluginAPI); err == nil {
		if v, err := plugins.ParseVersion(doc.PluginAPI); err != nil || !r.Contains(v) {
			mismatch("/plugin_api", "discovery implements Plugin API %s, outside the declared range %q", doc.PluginAPI, m.Compatibility.PluginAPI)
		} else if !slices.Contains(plugins.SupportedPluginAPIVersions, doc.PluginAPI) {
			mismatch("/plugin_api", "discovery implements Plugin API %s; this engine serves %v", doc.PluginAPI, plugins.SupportedPluginAPIVersions)
		} else {
			if connector := m.Contributions.Connector; connector != nil {
				api := plugins.ResolveAPI(doc.PluginAPI)
				for _, kind := range connector.Kinds {
					if kind.API != nil && !api.Speaks(plugins.FeatureConnectorAPI) {
						mismatch("/plugin_api", "discovery implements Plugin API %s, but declared connector API routes need %s or later", doc.PluginAPI, plugins.FeatureSince(plugins.FeatureConnectorAPI))
						break
					}
				}
			}
			for _, name := range m.Contributions.Names() {
				if feature, known := plugins.ContributionFeature(name); known && !plugins.ResolveAPI(doc.PluginAPI).Speaks(feature) {
					mismatch("/plugin_api", "discovery implements Plugin API %s, but the declared %s Contribution needs Plugin API %s or later", doc.PluginAPI, name, plugins.FeatureSince(feature))
				}
			}
		}
	}
	// Contributions are a set: discovery may list them in any order.
	declared := m.Contributions.Names()
	served := slices.Clone(doc.Contributions)
	slices.Sort(served)
	if !slices.Equal(served, slices.Sorted(slices.Values(declared))) {
		mismatch("/contributions", "discovery lists Contributions %v; the manifest declares %v", doc.Contributions, declared)
	}
	return doc.PluginAPI, issues, nil
}

// fixture mirrors contracts/plugins/v0/plugin-fixture.schema.json.
type fixture struct {
	Input struct {
		Path      string `json:"path"`
		MediaType string `json:"media_type"`
	} `json:"input"`
	Configuration json.RawMessage `json:"configuration,omitempty"`
	Source        json.RawMessage `json:"source,omitempty"`
	Extensions    json.RawMessage `json:"extensions,omitempty"`
	Provenance    json.RawMessage `json:"provenance,omitempty"`
}

type sourceIdentity struct {
	CorpusID  string `json:"corpus_id"`
	Namespace string `json:"namespace"`
	RecordKey string `json:"record_key"`
}

type fileReference struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
}

type inputBlob struct {
	BlobID    string        `json:"blob_id"`
	MediaType string        `json:"media_type"`
	SizeBytes int           `json:"size_bytes"`
	SHA256    string        `json:"sha256"`
	Reference fileReference `json:"reference"`
}

type normalizerRequest struct {
	InvocationID    string          `json:"invocation_id"`
	IdempotencyKey  string          `json:"idempotency_key"`
	Contribution    string          `json:"contribution"`
	OrganizationID  string          `json:"organization_id"`
	CorpusID        string          `json:"corpus_id"`
	RecordID        string          `json:"record_id"`
	RecordVersionID string          `json:"record_version_id"`
	Source          json.RawMessage `json:"source"`
	Input           inputBlob       `json:"input"`
	Extensions      json.RawMessage `json:"extensions,omitempty"`
	Provenance      json.RawMessage `json:"provenance,omitempty"`
	Configuration   json.RawMessage `json:"configuration"`
}

// FileURL returns the absolute file:// URL of path.
func FileURL(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	slashed := filepath.ToSlash(abs)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed // Windows drive letters
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String(), nil
}

// BuildFixtureRequest turns an invocation fixture into the development
// normalizer request: the input file (relative to the fixture) is referenced
// through an absolute file:// URL with its computed size and SHA-256, and the
// identity fields get deterministic development values. Issues report an
// invalid fixture or a configuration the manifest schema rejects; err reports
// I/O failures.
func BuildFixtureRequest(path string, m *plugins.Manifest) ([]byte, []plugins.Issue, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	if issues := plugins.ValidateDocument("plugin-fixture.schema.json", raw); len(issues) > 0 {
		return nil, issues, nil
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, nil, err
	}
	inputPath := filepath.FromSlash(f.Input.Path)
	if !filepath.IsAbs(inputPath) {
		inputPath = filepath.Join(filepath.Dir(path), inputPath)
	}
	// Resolve symlinks like the SDK helpers so both build the same file:// URL.
	if resolved, err := filepath.EvalSymlinks(inputPath); err == nil {
		inputPath = resolved
	}
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return nil, nil, fmt.Errorf("fixture input: %w", err)
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	short := digest[:16]
	config := f.Configuration
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	if issues := plugins.ValidateConfiguration(m, config); len(issues) > 0 {
		return nil, issues, nil
	}
	source := f.Source
	corpusID := "dev-corpus"
	if len(source) == 0 {
		source, _ = json.Marshal(sourceIdentity{CorpusID: corpusID, Namespace: "dev", RecordKey: f.Input.Path})
	} else {
		var s sourceIdentity
		_ = json.Unmarshal(source, &s)
		corpusID = s.CorpusID
	}
	fileURL, err := FileURL(inputPath)
	if err != nil {
		return nil, nil, err
	}
	request := normalizerRequest{
		InvocationID:    "dev-invocation-" + short,
		IdempotencyKey:  "dev:" + digest,
		Contribution:    "normalizer",
		OrganizationID:  "dev-organization",
		CorpusID:        corpusID,
		RecordID:        "dev-record-" + short,
		RecordVersionID: "dev-version-" + short,
		Source:          source,
		Input: inputBlob{
			BlobID: "dev-blob-" + short, MediaType: f.Input.MediaType, SizeBytes: len(data), SHA256: digest,
			Reference: fileReference{Kind: "file", URL: fileURL},
		},
		Extensions:    f.Extensions,
		Provenance:    f.Provenance,
		Configuration: config,
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, nil, err
	}
	if issues := plugins.ValidateDocument("normalizer-request.schema.json", body); len(issues) > 0 {
		return nil, issues, nil
	}
	return body, nil, nil
}

// Envelope is the Plugin Protocol error envelope.
type Envelope struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	// Class and RetryAfterSeconds are carried by connector errors (Plugin API 0.3).
	Class             string `json:"error_class,omitempty"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
}

// Result is one normalizer invocation as the engine would judge it.
type Result struct {
	Status int
	Body   []byte
	// Error is the plugin's error envelope for a non-2xx answer.
	Error *Envelope
	// Issues are contract violations: an invalid response (schema or the
	// engine's Manifest rules), a response over the size limit, or a non-2xx
	// answer without a valid envelope (plugin unavailability).
	Issues []plugins.Issue
}

// InvokeNormalizer posts request to POST /v0/contributions/normalizer and
// validates the answer: at most maxResponseBytes are read, a 200 body goes
// through plugins.ValidateNormalizerResponse, and other statuses must carry
// the error envelope. ctx bounds the invocation (use the manifest timeout).
func InvokeNormalizer(ctx context.Context, baseURL string, request []byte, maxResponseBytes int) (*Result, error) {
	return InvokeNormalizerWith(ctx, baseURL, request, maxResponseBytes, plugins.ValidateNormalizerResponse)
}

// InvokeNormalizerWith is InvokeNormalizer with the check applied to a 200
// body, such as a closure over plugins.CheckNormalizerOutput with the
// invocation context.
func InvokeNormalizerWith(ctx context.Context, baseURL string, request []byte, maxResponseBytes int, check func(body []byte) []plugins.Issue) (*Result, error) {
	return invoke(ctx, baseURL, "/v0/contributions/normalizer", request, maxResponseBytes, check)
}

func invoke(ctx context.Context, baseURL, route string, request []byte, maxResponseBytes int, check func(body []byte) []plugins.Issue) (*Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+route, bytes.NewReader(request))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", route, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxResponseBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("read %s response: %w", route, err)
	}
	result := &Result{Status: resp.StatusCode, Body: body}
	if len(body) > maxResponseBytes {
		result.Body = nil
		result.Issues = []plugins.Issue{{Code: CodeResponseTooLarge,
			Message: fmt.Sprintf("the response exceeds the declared max_response_bytes (%d)", maxResponseBytes)}}
		return result, nil
	}
	if resp.StatusCode == http.StatusOK {
		result.Issues = check(body)
		return result, nil
	}
	if issues := plugins.ValidateDocument("error.schema.json", body); len(issues) > 0 {
		result.Issues = []plugins.Issue{{Code: CodeInvalidErrorEnvelope,
			Message: fmt.Sprintf("HTTP %d without a valid error envelope counts as plugin unavailability: %s", resp.StatusCode, describeEnvelope(body))}}
		return result, nil
	}
	var envelope Envelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	result.Error = &envelope
	return result, nil
}

// InvokeSearch posts one retrieval search round; check is normally a closure
// over a plugins.RetrievalSession.
func InvokeSearch(ctx context.Context, baseURL string, request []byte, maxResponseBytes int, check func(body []byte) []plugins.Issue) (*Result, error) {
	return invoke(ctx, baseURL, plugins.SearchRoute, request, maxResponseBytes, check)
}
