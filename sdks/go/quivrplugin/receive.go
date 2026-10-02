package quivrplugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Receiver is implemented by the connector of a kind that declares the push
// mode (Plugin API 0.5). The core owns the public webhook route of each
// Connector Instance and relays every request the source sends to it:
// Receive verifies it with the credential (a signature over the raw body,
// say), answers any challenge, and returns the items it carries. The core
// ingests the items, then answers the source with the Delivery's response.
//
// Map a delivered item exactly as Fetch maps the same source item (same
// Record Key and revision): an item seen by both paths then converges on the
// same Receipt.
type Receiver interface {
	Receive(ctx context.Context, req *ReceiveRequest) (*Delivery, error)
}

// ReceiveRequest is one relayed delivery.
type ReceiveRequest struct {
	InvocationID   string          `json:"invocation_id"`
	Contribution   string          `json:"contribution"`
	OrganizationID string          `json:"organization_id"`
	Configuration  json.RawMessage `json:"configuration"`
	Connector      Instance        `json:"connector"`
	Credential     Credential      `json:"credential"`
	// Checkpoint is the current Acquisition Checkpoint, read-only: a
	// delivery cannot move it, because pull runs own it.
	Checkpoint json.RawMessage `json:"checkpoint"`
	Now        time.Time       `json:"now"`
	ReadsToday int64           `json:"reads_today"`
	Request    RelayedRequest  `json:"request"`
	// Route and Body identify a secure API delivery (since 0.11).
	Route string          `json:"route,omitempty"`
	Body  json.RawMessage `json:"body,omitempty"`

	logger *slog.Logger
}

// DecodeCheckpoint unmarshals the checkpoint into v; it leaves v unchanged
// before the first pull run.
func (r *ReceiveRequest) DecodeCheckpoint(v any) error {
	if len(r.Checkpoint) == 0 || string(r.Checkpoint) == "null" {
		return nil
	}
	return json.Unmarshal(r.Checkpoint, v)
}

// Logger returns a logger that scrubs this request's credential values.
func (r *ReceiveRequest) Logger() *slog.Logger { return r.logger }

// RelayedRequest is the request the source sent, bounded by the core (a body
// of at most 1 MiB, at most 64 header names; no hop-by-hop headers, no Cookie).
type RelayedRequest struct {
	Path   string `json:"path,omitempty"`
	Method string `json:"method"`
	// Query is the raw query string, without the leading ?.
	Query string `json:"query"`
	// Headers are the header values by lowercase name.
	Headers map[string][]string `json:"headers"`
	// BodyBase64 carries the exact body bytes; use Body.
	BodyBase64 string `json:"body_base64"`

	body []byte
}

// Body is the exact body the source sent.
func (r *RelayedRequest) Body() []byte { return r.body }

// Header returns the first value of a header, whatever the case of name.
func (r *RelayedRequest) Header(name string) string {
	if values := r.Headers[strings.ToLower(name)]; len(values) > 0 {
		return values[0]
	}
	return ""
}

// QueryValues parses the query string.
func (r *RelayedRequest) QueryValues() url.Values {
	v, _ := url.ParseQuery(r.Query)
	return v
}

// Verdicts of a delivery.
const (
	VerdictAccepted = "accepted"
	VerdictRefused  = "refused"
)

// Delivery is the plugin's verdict on one relayed request and the answer the
// core returns to the source. Build it with Accept, Respond or Refuse.
type Delivery struct {
	Verdict string
	// Status, ContentType and Body are the answer to the source: 2xx when
	// accepted, 4xx when refused, a text body of at most 64 KiB.
	Status      int
	ContentType string
	Body        string
	// Items are ingested before the source is answered; only when accepted,
	// and without attachments.
	Items []Item
	// Reads counts the source resources the delivery stands for.
	Reads int64
}

// Accept accepts an authentic delivery with its items and answers 200.
func Accept(items ...Item) *Delivery {
	return &Delivery{Verdict: VerdictAccepted, Status: http.StatusOK, Items: items}
}

// Respond accepts an authentic request with no items and answers it with a
// body, for example the answer to a challenge.
func Respond(status int, contentType, body string) *Delivery {
	return &Delivery{Verdict: VerdictAccepted, Status: status, ContentType: contentType, Body: body}
}

// Refuse refuses a request that is not authentic or not for this instance:
// the core answers status (4xx) and changes nothing.
func Refuse(status int, reason string) *Delivery {
	return &Delivery{Verdict: VerdictRefused, Status: status, ContentType: "text/plain", Body: reason}
}

type deliveryJSON struct {
	Verdict  string `json:"verdict"`
	Response struct {
		Status      int    `json:"status"`
		ContentType string `json:"content_type,omitempty"`
		Body        string `json:"body,omitempty"`
	} `json:"response"`
	Items []Item `json:"items,omitempty"`
	Reads int64  `json:"reads,omitempty"`
}

// pushes reports whether a declared kind pushes.
func (p *Plugin) pushes() bool {
	if p.m.Connector == nil {
		return false
	}
	for _, kind := range p.m.Connector.Kinds {
		if kind.Pushes() {
			return true
		}
	}
	return false
}

func (p *Plugin) serveReceive(w http.ResponseWriter, r *http.Request) {
	var req ReceiveRequest
	k, credential := p.decode(w, r, "plugins/v0/connector-receive-request.schema.json", &req)
	if k == nil {
		return
	}
	impl, ok := k.impl.(Receiver)
	if !ok || !p.m.Connector.Kinds[req.Connector.Kind].Pushes() {
		refuse(w, 400, "push_unsupported", fmt.Sprintf("kind %q does not declare the push mode", req.Connector.Kind), credential)
		return
	}
	body, err := base64.StdEncoding.DecodeString(req.Request.BodyBase64)
	if err != nil {
		refuse(w, 400, "invalid_request", "request.body_base64 is not standard base64", credential)
		return
	}
	req.Request.body = body
	req.logger = p.requestLogger(credential, req.InvocationID)
	defer p.recoverPanic(w, req.logger)
	ctx, cancel := context.WithTimeout(r.Context(), p.m.timeoutDur)
	defer cancel()
	delivery, err := impl.Receive(ctx, &req)
	if err != nil {
		p.fail(w, req.logger, err, credential)
		return
	}
	out, problem := p.encodeDelivery(delivery)
	if problem != "" {
		req.logger.Error("the connector returned an invalid delivery", "problem", problem)
		writeJSON(w, 500, envelope{Code: "invalid_response", Message: truncate(credential.Redact(problem)), Retryable: false, Class: ClassSource})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(out)
}

// encodeDelivery encodes a delivery and checks it the way the engine will:
// the verdict against the status and items, no attachments, max_items, the
// response size and schema.
func (p *Plugin) encodeDelivery(d *Delivery) ([]byte, string) {
	if d == nil {
		return nil, "Receive returned no delivery and no error"
	}
	accepted := d.Verdict == VerdictAccepted
	switch {
	case d.Verdict != VerdictAccepted && d.Verdict != VerdictRefused:
		return nil, fmt.Sprintf("verdict %q is neither accepted nor refused", d.Verdict)
	case accepted && (d.Status < 200 || d.Status > 299):
		return nil, fmt.Sprintf("an accepted delivery answers the source with a 2xx status, not %d", d.Status)
	case !accepted && (d.Status < 400 || d.Status > 499):
		return nil, fmt.Sprintf("a refused delivery answers the source with a 4xx status, not %d", d.Status)
	case !accepted && len(d.Items) > 0:
		return nil, "a refused delivery carries no items"
	case len(d.Items) > p.m.maxItems:
		return nil, fmt.Sprintf("%d items exceed max_items %d", len(d.Items), p.m.maxItems)
	case len(d.Body) > 64<<10:
		return nil, fmt.Sprintf("the answer body is %d bytes; the source is answered at most 64 KiB", len(d.Body))
	}
	for _, item := range d.Items {
		if len(item.Attachments) > 0 {
			return nil, fmt.Sprintf("item %q of a delivery carries attachments; a pull run returns them", item.RecordKey)
		}
	}
	out := deliveryJSON{Verdict: d.Verdict, Items: d.Items, Reads: d.Reads}
	out.Response.Status, out.Response.ContentType, out.Response.Body = d.Status, d.ContentType, d.Body
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return nil, "the delivery is not JSON-encodable: " + err.Error()
	}
	if buf.Len() > p.m.maxBytes {
		return nil, fmt.Sprintf("the response is %d bytes; max_response_bytes is %d", buf.Len(), p.m.maxBytes)
	}
	if err := validate("plugins/v0/connector-receive-response.schema.json", buf.Bytes()); err != nil {
		return nil, "the delivery does not match the response schema: " + err.Error()
	}
	return buf.Bytes(), ""
}

// pushProblem checks a page's push status the way the engine will.
func (p *Plugin) pushProblem(s *PushStatus) string {
	switch {
	case s == nil:
		return ""
	case !p.pushes():
		return "a push status goes only in a plugin whose kinds declare the push mode"
	case s.State == PushFailed && (s.ErrorClass == "" || s.Code == ""):
		return "a failed push status carries ErrorClass and Code; use PushHasFailed"
	case s.State != PushFailed && s.ErrorClass != "":
		return "ErrorClass goes only with a failed push status"
	case s.State == PushActive && s.Code != "":
		return "an active push status carries no Code"
	case s.State != PushActive && s.PollIntervalSeconds != 0:
		return "PollIntervalSeconds goes only with an active push status"
	case s.PollIntervalSeconds != 0 && (s.PollIntervalSeconds < 60 || s.PollIntervalSeconds > 86400):
		return fmt.Sprintf("PollIntervalSeconds %d is outside 60–86400", s.PollIntervalSeconds)
	}
	return ""
}
