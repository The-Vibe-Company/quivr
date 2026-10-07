package quivrplugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"
)

// AttachmentSource is implemented by a connector whose items carry
// attachments (Plugin API 0.4). The SDK serves describe_attachment and
// upload_attachment on top of it: it reads the bytes once, bounded by
// attachments.max_bytes, hashes them, keeps them in a private temporary
// file, and uploads that file to the core's grant. The plugin never holds
// storage credentials.
type AttachmentSource interface {
	// OpenAttachment streams the bytes of req.Attachment (find them with
	// its Ref). Return a classified *Error on failure.
	OpenAttachment(ctx context.Context, req *AttachmentRequest) (io.ReadCloser, error)
}

// AttachmentSkipper is optionally implemented beside AttachmentSource. When
// the SDK leaves an attachment out (its bytes exceed attachments.max_bytes,
// or it is empty), it asks for the item's replacement extensions, for
// example to record the skipped attachment. Return nil to keep them.
type AttachmentSkipper interface {
	SkippedAttachment(ctx context.Context, req *AttachmentRequest, reason string) (map[string]Extension, error)
}

// Skip reasons the SDK answers.
const (
	SkipTooLarge = "too_large"
	SkipEmpty    = "empty"
)

// CodeAttachmentChanged is the source error the SDK answers when the bytes
// read for an upload differ from the bytes it described; the core then
// describes the attachment again once.
const CodeAttachmentChanged = "attachment_changed"

// AttachmentItem is the item an attachment belongs to.
type AttachmentItem struct {
	RecordKey  string               `json:"record_key"`
	Revision   string               `json:"revision,omitempty"`
	Extensions map[string]Extension `json:"extensions,omitempty"`
}

// UploadGrant is the core's presigned PUT for one attachment. It never
// prints: its URL and headers are capabilities.
type UploadGrant struct {
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	SizeBytes int64             `json:"size_bytes"`
	SHA256    string            `json:"sha256"`
	MediaType string            `json:"media_type"`
	ExpiresAt time.Time         `json:"expires_at"`
}

func (UploadGrant) String() string             { return Redacted }
func (UploadGrant) GoString() string           { return Redacted }
func (UploadGrant) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(Redacted)) }
func (UploadGrant) LogValue() slog.Value       { return slog.StringValue(Redacted) }

// AttachmentRequest is one describe_attachment or upload_attachment
// invocation. OpenAttachment sees it without the grant.
type AttachmentRequest struct {
	InvocationID   string          `json:"invocation_id"`
	Contribution   string          `json:"contribution"`
	OrganizationID string          `json:"organization_id"`
	Configuration  json.RawMessage `json:"configuration"`
	Connector      Instance        `json:"connector"`
	Credential     Credential      `json:"credential"`
	Now            time.Time       `json:"now"`
	Item           AttachmentItem  `json:"item"`
	Attachment     Attachment      `json:"attachment"`
	Grant          *UploadGrant    `json:"grant,omitempty"`

	logger *slog.Logger
}

// Logger returns a logger that scrubs the credential and the grant.
func (r *AttachmentRequest) Logger() *slog.Logger { return r.logger }

type describeJSON struct {
	SizeBytes      int64                `json:"size_bytes,omitempty"`
	SHA256         string               `json:"sha256,omitempty"`
	Skip           string               `json:"skip,omitempty"`
	ItemExtensions map[string]Extension `json:"item_extensions,omitempty"`
}

type uploadJSON struct {
	Status string `json:"status"`
}

// spoolTTL keeps described bytes as long as a grant lives.
const spoolTTL = 15 * time.Minute

// spool keeps the bytes read by describe_attachment for the following
// upload_attachment, in private temporary files keyed by the instance and
// the attachment ref. Expiry follows the core's clock (the requests' now).
type spool struct {
	mu      sync.Mutex
	dir     string
	entries map[string]spoolEntry
}

type spoolEntry struct {
	path    string
	size    int64
	sha256  string
	expires time.Time
}

func spoolKey(r *AttachmentRequest) string {
	h := sha256.New()
	for _, part := range []string{r.OrganizationID, r.Connector.Kind, r.Connector.ID, r.Attachment.Ref} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *spool) file() (*os.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir == "" {
		dir, err := os.MkdirTemp("", "quivr-plugin-attachments-*")
		if err != nil {
			return nil, err
		}
		s.dir, s.entries = dir, map[string]spoolEntry{}
	}
	return os.CreateTemp(s.dir, "attachment-*")
}

// expire drops the entries older than now.
func (s *spool) expire(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, e := range s.entries {
		if now.After(e.expires) {
			_ = os.Remove(e.path)
			delete(s.entries, key)
		}
	}
}

func (s *spool) put(key string, e spoolEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.entries[key]; ok && old.path != e.path {
		_ = os.Remove(old.path)
	}
	s.entries[key] = e
}

func (s *spool) take(key string) (spoolEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	return e, ok
}

func (s *spool) drop(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok {
		_ = os.Remove(e.path)
		delete(s.entries, key)
	}
}

// read streams the attachment into a spool file, at most max bytes plus one,
// and hashes it. A result with size > max is over the cap; its file is gone.
func (p *Plugin) read(ctx context.Context, impl AttachmentSource, req *AttachmentRequest, max int64) (spoolEntry, error) {
	body, err := impl.OpenAttachment(ctx, req)
	if err != nil {
		return spoolEntry{}, err
	}
	defer body.Close()
	f, err := p.spool.file()
	if err != nil {
		return spoolEntry{}, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(body, max+1))
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil || n > max {
		_ = os.Remove(f.Name())
		if err != nil {
			var classified *Error
			if errors.As(err, &classified) {
				return spoolEntry{}, err
			}
			return spoolEntry{}, TransientError("source_unavailable", "reading the attachment failed")
		}
		return spoolEntry{size: n}, nil
	}
	return spoolEntry{path: f.Name(), size: n, sha256: hex.EncodeToString(h.Sum(nil)), expires: req.Now.Add(spoolTTL)}, nil
}

func (p *Plugin) attachmentHandler(w http.ResponseWriter, r *http.Request, schema string) (*AttachmentRequest, AttachmentSource, Credential, context.Context, context.CancelFunc) {
	var req AttachmentRequest
	k, credential := p.decode(w, r, schema, &req)
	if k == nil {
		return nil, nil, credential, nil, nil
	}
	impl, _ := k.impl.(AttachmentSource)
	redact := credential
	if req.Grant != nil {
		redact = grantRedactor(credential, req.Grant)
	}
	req.logger = p.requestLogger(r.Context(), redact, req.InvocationID)
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(p.m.Connector.Attachments.TimeoutMS)*time.Millisecond)
	p.spool.expire(req.Now)
	return &req, impl, redact, ctx, cancel
}

// grantRedactor scrubs the credential's values, the grant URL and its header
// values.
func grantRedactor(credential Credential, g *UploadGrant) Credential {
	values := []string{g.URL}
	for _, v := range g.Headers {
		values = append(values, v)
	}
	raw, _ := json.Marshal(map[string]any{"credential": json.RawMessage(credentialRaw(credential)), "grant": values})
	return newCredential(raw)
}

func credentialRaw(c Credential) []byte {
	if c.IsNull() {
		return []byte("null")
	}
	return c.raw
}

func (p *Plugin) skip(ctx context.Context, impl AttachmentSource, req *AttachmentRequest, reason string) (describeJSON, error) {
	out := describeJSON{Skip: reason}
	if skipper, ok := impl.(AttachmentSkipper); ok {
		ext, err := skipper.SkippedAttachment(ctx, req, reason)
		if err != nil {
			return out, err
		}
		out.ItemExtensions = ext
	}
	return out, nil
}

func (p *Plugin) serveDescribeAttachment(w http.ResponseWriter, r *http.Request) {
	req, impl, redact, ctx, cancel := p.attachmentHandler(w, r, "plugins/v0/connector-describe-attachment-request.schema.json")
	if req == nil {
		return
	}
	defer cancel()
	defer p.recoverPanic(w, req.logger)
	max := p.m.Connector.Attachments.MaxBytes
	entry, err := p.read(ctx, impl, req, max)
	if err != nil {
		p.fail(w, req.logger, err, redact)
		return
	}
	var out describeJSON
	switch {
	case entry.size > max:
		out, err = p.skip(ctx, impl, req, SkipTooLarge)
	case entry.size == 0:
		out, err = p.skip(ctx, impl, req, SkipEmpty)
	default:
		p.spool.put(spoolKey(req), entry)
		out = describeJSON{SizeBytes: entry.size, SHA256: entry.sha256}
	}
	if err != nil {
		p.fail(w, req.logger, err, redact)
		return
	}
	writeJSON(w, 200, out)
}

// Retain a full 32-way connector burst between pages. The default transport
// keeps only two idle connections per host, reconnecting for most later PUTs.
// This is an idle-pool bound, not a limit on active uploads. Grants never follow
// redirects and never share the engine's authenticated plugin-call transport.
var uploadClient = newAttachmentUploadClient()

func newAttachmentUploadClient() *http.Client {
	transport := http.DefaultTransport
	if defaults, ok := transport.(*http.Transport); ok {
		owned := defaults.Clone()
		owned.MaxIdleConnsPerHost = 32
		owned.MaxIdleConns = 128
		transport = owned
	}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (p *Plugin) serveUploadAttachment(w http.ResponseWriter, r *http.Request) {
	req, impl, redact, ctx, cancel := p.attachmentHandler(w, r, "plugins/v0/connector-upload-attachment-request.schema.json")
	if req == nil {
		return
	}
	defer cancel()
	defer p.recoverPanic(w, req.logger)
	g := req.Grant
	key := spoolKey(req)
	entry, ok := p.spool.take(key)
	if !ok || entry.sha256 != g.SHA256 || entry.size != g.SizeBytes {
		// Not described by this process (a restart), or described bytes that
		// differ: read the source again and compare with the grant.
		grant := req.Grant
		req.Grant = nil
		fresh, err := p.read(ctx, impl, req, g.SizeBytes)
		req.Grant = grant
		if err != nil {
			p.fail(w, req.logger, err, redact)
			return
		}
		if fresh.sha256 != g.SHA256 || fresh.size != g.SizeBytes {
			if fresh.path != "" {
				_ = os.Remove(fresh.path)
			}
			p.fail(w, req.logger, SourceError(CodeAttachmentChanged, "the attachment's bytes changed since they were described"), redact)
			return
		}
		p.spool.put(key, fresh)
		entry = fresh
	}
	if err := put(ctx, g, entry.path); err != nil {
		p.fail(w, req.logger, err, redact)
		return
	}
	p.spool.drop(key)
	writeJSON(w, 200, uploadJSON{Status: "uploaded"})
}

// put sends the file to the grant with exactly its headers.
func put(ctx context.Context, g *UploadGrant, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, g.URL, f)
	if err != nil {
		return SourceError("upload_refused", "the grant URL is not a valid request")
	}
	req.ContentLength = g.SizeBytes
	for name, value := range g.Headers {
		req.Header.Set(name, value)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", g.MediaType)
	}
	resp, err := uploadClient.Do(req)
	if err != nil {
		return TransientError("upload_unavailable", "storage did not answer the upload")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode/100 == 2:
		return nil
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return TransientError("upload_unavailable", fmt.Sprintf("storage answered HTTP %d", resp.StatusCode))
	}
	return SourceError("upload_refused", fmt.Sprintf("storage refused the upload with HTTP %d", resp.StatusCode))
}

func hasAttachments(items []Item) bool {
	for _, item := range items {
		if len(item.Attachments) > 0 {
			return true
		}
	}
	return false
}
