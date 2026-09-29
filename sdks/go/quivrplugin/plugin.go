// Package quivrplugin implements the Quivr Plugin Protocol v0 connector
// Contribution (Plugin API 0.3) so that a source collector is one Go type.
// See the contract in contracts/plugins/v0/README.md and the guide in
// sdks/go/README.md.
package quivrplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Environment of the local run convention (quivr plugin dev and test).
const (
	EnvHost     = "QUIVR_PLUGIN_HOST"
	EnvPort     = "QUIVR_PLUGIN_PORT"
	EnvManifest = "QUIVR_PLUGIN_MANIFEST"
)

// maxRequestBytes bounds a request body the SDK reads.
const maxRequestBytes = 16 << 20

// Plugin serves one quivr-plugin.yaml.
type Plugin struct {
	m         *loadedManifest
	kinds     map[string]*kind
	logger    *slog.Logger
	configSch *jsonschema.Schema
	spool     spool
}

type kind struct {
	impl       Connector
	config     *jsonschema.Schema
	credential *jsonschema.Schema
	// optional: the manifest sets credential_required: false.
	optional bool
}

// Option configures a Plugin.
type Option func(*Plugin)

// WithLogger sets the base logger (default: text to stderr). Request loggers
// wrap it with credential redaction.
func WithLogger(l *slog.Logger) Option { return func(p *Plugin) { p.logger = l } }

// New loads the manifest at path. An empty path uses QUIVR_PLUGIN_MANIFEST,
// then quivr-plugin.yaml in the working directory.
func New(path string, opts ...Option) (*Plugin, error) {
	if path == "" {
		path = os.Getenv(EnvManifest)
	}
	if path == "" {
		path = "quivr-plugin.yaml"
	}
	m, err := loadManifest(path)
	if err != nil {
		return nil, err
	}
	p := &Plugin{m: m, kinds: map[string]*kind{}, logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	for _, opt := range opts {
		opt(p)
	}
	if m.Configuration != nil && len(m.Configuration.Schema) > 0 {
		if p.configSch, err = compileDeclared(m.Configuration.Schema); err != nil {
			return nil, fmt.Errorf("configuration.schema: %w", err)
		}
	}
	for name, declared := range m.Connector.Kinds {
		k := &kind{optional: declared.CredentialRequired != nil && !*declared.CredentialRequired}
		if k.config, err = compileDeclared(declared.ConfigSchema); err != nil {
			return nil, fmt.Errorf("kind %s config_schema: %w", name, err)
		}
		if len(declared.CredentialSchema) > 0 {
			if k.credential, err = compileDeclared(declared.CredentialSchema); err != nil {
				return nil, fmt.Errorf("kind %s credential_schema: %w", name, err)
			}
		}
		p.kinds[name] = k
	}
	return p, nil
}

// Manifest returns the loaded manifest.
func (p *Plugin) Manifest() Manifest { return p.m.Manifest }

// Connector registers the implementation of a declared kind.
func (p *Plugin) Connector(kindName string, impl Connector) error {
	k, ok := p.kinds[kindName]
	if !ok {
		return fmt.Errorf("kind %q is not declared under contributions.connector.kinds in the manifest", kindName)
	}
	k.impl = impl
	return nil
}

// MustConnector is Connector that panics on an undeclared kind.
func (p *Plugin) MustConnector(kindName string, impl Connector) *Plugin {
	if err := p.Connector(kindName, impl); err != nil {
		panic(err)
	}
	return p
}

func (p *Plugin) checkRegistered() error {
	var missing []string
	for name, k := range p.kinds {
		if k.impl == nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("no implementation registered for declared kinds %v", missing)
	}
	for name, k := range p.kinds {
		if _, ok := k.impl.(Receiver); p.m.Connector.Kinds[name].Pushes() && !ok {
			return fmt.Errorf("kind %q declares the push mode, so its implementation must implement Receiver", name)
		}
	}
	if p.m.Connector.Attachments != nil {
		for name, k := range p.kinds {
			if _, ok := k.impl.(AttachmentSource); !ok {
				return fmt.Errorf("the manifest declares attachments, so kind %q must implement AttachmentSource", name)
			}
		}
	}
	return nil
}

// Handler serves the Plugin Protocol routes. It fails when a declared kind
// has no implementation.
func (p *Plugin) Handler() (http.Handler, error) {
	if err := p.checkRegistered(); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v0/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v0/discovery", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{
			"plugin_api":      p.m.pluginAPI,
			"plugin":          map[string]string{"id": p.m.ID, "version": p.m.Version},
			"manifest_digest": p.m.digest,
			"contributions":   []string{"connector"},
		})
	})
	mux.HandleFunc("POST /v0/contributions/connector/fetch", p.serveFetch)
	mux.HandleFunc("POST /v0/contributions/connector/check_credential", p.serveCheckCredential)
	if p.pushes() {
		mux.HandleFunc("POST /v0/contributions/connector/receive", p.serveReceive)
	}
	if p.m.Connector.Attachments != nil {
		mux.HandleFunc("POST /v0/contributions/connector/describe_attachment", p.serveDescribeAttachment)
		mux.HandleFunc("POST /v0/contributions/connector/upload_attachment", p.serveUploadAttachment)
	}
	return mux, nil
}

// Serve listens on QUIVR_PLUGIN_HOST:QUIVR_PLUGIN_PORT (default
// 127.0.0.1:8080) until SIGINT or SIGTERM.
func (p *Plugin) Serve() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	host, port := os.Getenv(EnvHost), os.Getenv(EnvPort)
	if host == "" {
		host = "127.0.0.1"
	}
	if port == "" {
		port = "8080"
	}
	return p.ServeAddr(ctx, net.JoinHostPort(host, port))
}

// ServeAddr listens on addr until ctx ends.
func (p *Plugin) ServeAddr(ctx context.Context, addr string) error {
	handler, err := p.Handler()
	if err != nil {
		return err
	}
	server := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	errs := make(chan error, 1)
	go func() { errs <- server.ListenAndServe() }()
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// refuse answers a terminal protocol refusal, scrubbing credential values.
func refuse(w http.ResponseWriter, status int, code, message string, credential Credential) {
	writeJSON(w, status, envelope{Code: code, Message: truncate(credential.Redact(message)), Retryable: false})
}

func truncate(s string) string {
	if len(s) > 1000 {
		return s[:1000] + "…"
	}
	if s == "" {
		return "error"
	}
	return s
}

// common is the part of both connector requests the SDK checks.
type common struct {
	Configuration json.RawMessage `json:"configuration"`
	Connector     Instance        `json:"connector"`
	Credential    json.RawMessage `json:"credential"`
}

// decode reads, schema-checks and semantically checks a request. It answers
// the refusal itself and returns nil when the request is invalid.
func (p *Plugin) decode(w http.ResponseWriter, r *http.Request, schema string, into any) (*kind, Credential) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	if err != nil || len(body) > maxRequestBytes {
		refuse(w, 400, "invalid_request", "the request body is unreadable or larger than 16 MiB", Credential{})
		return nil, Credential{}
	}
	var c common
	_ = json.Unmarshal(body, &c)
	credential := newCredential(c.Credential)
	if err := validate(schema, body); err != nil {
		refuse(w, 400, "invalid_request", err.Error(), credential)
		return nil, credential
	}
	k, ok := p.kinds[c.Connector.Kind]
	if !ok {
		refuse(w, 400, "unknown_kind", fmt.Sprintf("kind %q is not declared by %s", c.Connector.Kind, p.m.ID), credential)
		return nil, credential
	}
	if p.configSch != nil {
		if err := validateWith(p.configSch, c.Configuration); err != nil {
			refuse(w, 400, "invalid_configuration", "configuration "+err.Error(), credential)
			return nil, credential
		}
	}
	if err := validateWith(k.config, c.Connector.Config); err != nil {
		refuse(w, 400, "invalid_config", "connector config "+err.Error(), credential)
		return nil, credential
	}
	switch {
	case k.credential == nil && !credential.IsNull():
		refuse(w, 400, "invalid_credential", fmt.Sprintf("kind %q takes no credential", c.Connector.Kind), credential)
		return nil, credential
	case k.credential != nil && credential.IsNull() && !k.optional:
		refuse(w, 400, "invalid_credential", fmt.Sprintf("kind %q needs a credential", c.Connector.Kind), credential)
		return nil, credential
	case k.credential != nil && !credential.IsNull():
		// Only the location: a schema message may quote the value.
		if err := validateWith(k.credential, c.Credential); err != nil {
			refuse(w, 400, "invalid_credential", "the credential does not match the kind's credential_schema", credential)
			return nil, credential
		}
	}
	if err := json.Unmarshal(body, into); err != nil {
		refuse(w, 400, "invalid_request", err.Error(), credential)
		return nil, credential
	}
	return k, credential
}

// fail answers an error returned (or a panic raised) by an implementation.
func (p *Plugin) fail(w http.ResponseWriter, log *slog.Logger, err error, credential Credential) {
	var classified *Error
	if errors.As(err, &classified) {
		status, env := classified.envelope()
		env.Message = truncate(credential.Redact(env.Message))
		writeJSON(w, status, env)
		return
	}
	// An unclassified error is most often an I/O failure (a network error):
	// retry it. The message stays generic; the log keeps the detail.
	log.Error("connector failed with an unclassified error", "error", err.Error())
	writeJSON(w, 503, envelope{Code: "unexpected_error", Message: "the plugin failed with an unclassified error; see the plugin log", Retryable: true, Class: ClassTransient})
}

func (p *Plugin) recoverPanic(w http.ResponseWriter, log *slog.Logger) {
	if v := recover(); v != nil {
		log.Error("connector panicked", "panic", fmt.Sprint(v))
		writeJSON(w, 500, envelope{Code: "internal_error", Message: "the plugin failed unexpectedly; see the plugin log", Retryable: false, Class: ClassSource})
	}
}

func (p *Plugin) requestLogger(credential Credential, invocation string) *slog.Logger {
	return slog.New(redactingHandler{next: p.logger.Handler(), credential: credential}).With("invocation_id", invocation)
}

func (p *Plugin) serveFetch(w http.ResponseWriter, r *http.Request) {
	var req FetchRequest
	k, credential := p.decode(w, r, "plugins/v0/connector-fetch-request.schema.json", &req)
	if k == nil {
		return
	}
	req.logger = p.requestLogger(credential, req.InvocationID)
	defer p.recoverPanic(w, req.logger)
	ctx, cancel := context.WithTimeout(r.Context(), p.m.timeoutDur)
	defer cancel()
	page, err := k.impl.Fetch(ctx, &req)
	if errors.Is(err, ErrNotDue) {
		writeJSON(w, 200, pageJSON{Items: []Item{}, Checkpoint: req.Checkpoint, NotDue: true})
		return
	}
	if err != nil {
		p.fail(w, req.logger, err, credential)
		return
	}
	body, problem := p.encodePage(page)
	if problem != "" {
		req.logger.Error("the connector returned an invalid page", "problem", problem)
		writeJSON(w, 500, envelope{Code: "invalid_response", Message: truncate(credential.Redact(problem)), Retryable: false, Class: ClassSource})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(body)
}

// encodePage encodes a page and checks it the way the engine will: the
// response schema, max_items, the response size and the checkpoint and
// diagnostics bounds. It returns a problem description when the page is
// invalid.
func (p *Plugin) encodePage(page *Page) ([]byte, string) {
	if page == nil {
		return nil, "Fetch returned no page and no error"
	}
	checkpoint, err := json.Marshal(page.Checkpoint)
	if err != nil {
		return nil, "the checkpoint is not JSON-encodable: " + err.Error()
	}
	out := pageJSON{Items: page.Items, Checkpoint: checkpoint, More: page.More, Reads: page.Reads, Diagnostics: page.Diagnostics, Notice: page.Notice, Push: page.Push}
	if out.Items == nil {
		out.Items = []Item{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return nil, "the page is not JSON-encodable: " + err.Error()
	}
	body := buf.Bytes()
	diagnostics, _ := json.Marshal(page.Diagnostics)
	switch {
	case p.m.Connector.Attachments == nil && hasAttachments(page.Items):
		return nil, "items carry attachments; declare contributions.connector.attachments in the manifest (Plugin API 0.4) and implement AttachmentSource"
	case len(page.Items) > p.m.maxItems:
		return nil, fmt.Sprintf("%d items exceed max_items %d; answer More and return the rest on the next page", len(page.Items), p.m.maxItems)
	case len(body) > p.m.maxBytes:
		return nil, fmt.Sprintf("the response is %d bytes; max_response_bytes is %d", len(body), p.m.maxBytes)
	case compactLen(checkpoint) > p.m.maxCheckpt:
		return nil, fmt.Sprintf("the checkpoint encodes to %d bytes; max_checkpoint_bytes is %d", compactLen(checkpoint), p.m.maxCheckpt)
	case p.pushProblem(page.Push) != "":
		return nil, p.pushProblem(page.Push)
	case page.Diagnostics != nil && len(diagnostics) > MaxDiagnosticsBytes:
		return nil, fmt.Sprintf("diagnostics encode to %d bytes; the core stores at most %d", len(diagnostics), MaxDiagnosticsBytes)
	}
	if err := validate("plugins/v0/connector-fetch-response.schema.json", body); err != nil {
		return nil, "the page does not match the response schema: " + err.Error()
	}
	return body, ""
}

func (p *Plugin) serveCheckCredential(w http.ResponseWriter, r *http.Request) {
	var req CredentialRequest
	k, credential := p.decode(w, r, "plugins/v0/connector-check-credential-request.schema.json", &req)
	if k == nil {
		return
	}
	req.logger = p.requestLogger(credential, req.InvocationID)
	defer p.recoverPanic(w, req.logger)
	ctx, cancel := context.WithTimeout(r.Context(), p.m.timeoutDur)
	defer cancel()
	status, err := k.impl.CheckCredential(ctx, &req)
	if err != nil {
		p.fail(w, req.logger, err, credential)
		return
	}
	out := credentialJSON{Status: "ok"}
	if status != nil && status.ExpiresAt != nil {
		t := status.ExpiresAt.UTC()
		out.ExpiresAt = &t
	}
	writeJSON(w, 200, out)
}
