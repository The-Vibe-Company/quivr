package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

// WebhookPath prefixes the public webhook route of a Connector Instance whose
// kind declares the push mode: <public URL>/v0/connector-webhooks/<id>.
const WebhookPath = "/v0/connector-webhooks/"

// WebhookURL is the public webhook address of an instance, or "" when the
// deployment has no public URL.
func WebhookURL(publicURL, id string) string {
	if publicURL == "" {
		return ""
	}
	return strings.TrimRight(publicURL, "/") + WebhookPath + url.PathEscape(id)
}

// Push states a kind reports for its push channel, and the derived public
// state (degraded) of Connector Health.
const (
	PushActive   = "active"
	PushPending  = "pending"
	PushFailed   = "failed"
	PushDegraded = "degraded"
)

// CodeMissedDeliveries is the delivery error of a pull run that, with push
// active since before it started, still created new Record Versions: posts
// the source did not deliver.
const CodeMissedDeliveries = "missed_deliveries"

// PushStatus is a push kind's report on the push channel it sets up at the
// source, returned with a fetched page. The core keeps the latest report.
type PushStatus struct {
	State string
	// Class and Code say why a failed channel failed (Code alone may say
	// why a pending one is pending).
	Class ErrorClass
	Code  string
	// PollInterval, only while active, relaxes pull to a safety net.
	PollInterval time.Duration
}

// PushHealth is the push part of Connector Health: the kind's latest report
// on its channel, the core's latest delivery failure and the last accepted
// delivery.
type PushHealth struct {
	// Setup is the reported state (active, pending or failed).
	Setup        string
	SetupError   *RunError
	PollInterval time.Duration
	// DeliveryError is set by a delivery the core could not complete, or by
	// missed deliveries, and cleared by the next accepted delivery (one that
	// carries items, for missed deliveries).
	DeliveryError  *RunError
	LastDeliveryAt *time.Time
}

// State is the public push state: degraded on a failed setup or a delivery
// error, otherwise the reported setup state.
func (p PushHealth) State() string {
	if p.Setup == PushFailed || p.DeliveryError != nil {
		return PushDegraded
	}
	return p.Setup
}

// Error is the public push error: an access refusal first (it is what
// Connector Health reports), then the delivery error, then a failed setup's.
func (p PushHealth) Error() *RunError {
	setup := p.SetupError
	if p.Setup != PushFailed {
		setup = nil
	}
	switch {
	case setup != nil && setup.Class == ClassAccess:
		return setup
	case p.DeliveryError != nil:
		return p.DeliveryError
	}
	return setup
}

// Healthy reports whether pull may relax to the reported poll interval.
func (p PushHealth) Healthy() bool { return p.Setup == PushActive && p.DeliveryError == nil }

// AccessRefused reports whether push is refused access, by the setup or a
// delivery, which Connector Health reports as access_error while pull
// carries the collection.
func (p PushHealth) AccessRefused() bool {
	setup := p.Setup == PushFailed && p.SetupError != nil && p.SetupError.Class == ClassAccess
	return setup || p.DeliveryError != nil && p.DeliveryError.Class == ClassAccess
}

// Relayed is a request a source sent to an instance's public webhook route,
// bounded by the transport: lowercase header names, the exact body.
type Relayed struct {
	Method  string
	Path    string
	Query   string
	Headers map[string][]string
	Body    []byte
}

// ReceiveRequest relays one delivery to a push kind.
type ReceiveRequest struct {
	Organization string
	InstanceID   string
	CorpusID     string
	Namespace    string
	Config       json.RawMessage
	Credential   json.RawMessage
	// Checkpoint is read-only: a delivery never moves it.
	Checkpoint json.RawMessage
	Now        time.Time
	ReadsToday int64
	Request    Relayed
	// Route and Body are present only for declared API routes.
	Route string
	Body  json.RawMessage
}

// Delivery is a push kind's verdict and the answer the core returns to the
// source.
type Delivery struct {
	Accepted    bool
	Status      int
	ContentType string
	Body        string
	Items       []Item
	Reads       int64
}

// Receiver is implemented by a Connector that can receive relayed deliveries
// (plugin kinds). Pushes reports whether this kind declares the push mode.
type Receiver interface {
	Pushes() bool
	Receive(context.Context, ReceiveRequest) (Delivery, error)
}

// DeliveryOutcome is what one relayed delivery changed, recorded on the
// instance with its push health.
type DeliveryOutcome struct {
	// Accepted: the kind accepted the delivery and every item was handled.
	Accepted bool
	// Carried: the delivery carried items; Fresh: one reserved a new Version.
	Carried bool
	Fresh   bool
	Reads   int64
	// Failure is the delivery error to record, or nil.
	Failure *RunError
}

// PushStore persists what relayed deliveries need and change.
type PushStore interface {
	// LoadDelivery reads an instance by id alone (the public route carries
	// no Organization), with its checkpoint and current sealed credential.
	LoadDelivery(ctx context.Context, id string) (Target, error)
	// RecordDelivery commits a delivery's outcome: the last delivery, the
	// delivery error, usage and re-evaluated Connector Health.
	RecordDelivery(ctx context.Context, org, id string, outcome DeliveryOutcome) error
}

// ErrNoWebhook answers a request to a webhook route no enabled push instance
// owns; the transport answers 404 before any plugin is called.
var ErrNoWebhook = errors.New("no webhook")

// RelayAnswer is what the core returns to the source.
type RelayAnswer struct {
	Status      int
	ContentType string
	Body        string
	// ErrorCode identifies an engine-generated failure, never a plugin reply.
	ErrorCode string
	// RetryAfter asks the source to retry later (a 503).
	RetryAfter time.Duration
	Receipts   []content.Receipt
	Allow      string
}

// RetryDelivery is the delay a source is asked to wait before retrying a
// delivery the core could not complete.
const RetryDelivery = 30 * time.Second

// Relay relays deliveries from the public webhook route to the push kind of
// the instance, ingests the items it returns through the same submission as
// a pull run (same idempotency keys, provenance and Relation binding, so an
// item seen by both paths converges on the same Receipt), and answers the
// source according to the kind's verdict.
type Relay struct {
	Store    PushStore
	Registry *Registry
	Sealer   Sealer
	Ingest   Ingestor
}

func unavailable(code string) RelayAnswer {
	return RelayAnswer{Status: 503, ContentType: "text/plain", Body: "temporarily unavailable; retry later", ErrorCode: code, RetryAfter: RetryDelivery}
}

// Deliver relays one request to instance id. It returns ErrNoWebhook for an
// unknown or disabled instance, or one whose kind does not push. A refused
// delivery changes nothing; a failure is recorded as push health and answered
// with a retryable status (503) or a terminal one (500).
func (r Relay) Deliver(ctx context.Context, id string, req Relayed) (RelayAnswer, error) {
	target, err := r.Store.LoadDelivery(ctx, id)
	if errors.Is(err, corpus.ErrNotFound) {
		return RelayAnswer{}, ErrNoWebhook
	}
	if err != nil {
		slog.Warn("connector delivery failed", "connector_id", id, "code", "storage_unavailable")
		return unavailable("storage_unavailable"), nil
	}
	connector, ok := r.Registry.Lookup(target.Kind)
	receiver, pushes := connector.(Receiver)
	if !target.Enabled || !ok || !pushes || !receiver.Pushes() {
		return RelayAnswer{}, ErrNoWebhook
	}
	return r.deliver(ctx, target, connector, receiver, req, "", nil)
}

func (r Relay) deliver(ctx context.Context, target Target, connector Connector, receiver Receiver, req Relayed, route string, body json.RawMessage) (RelayAnswer, error) {
	id := target.ID
	org := target.Organization
	fail := func(class ErrorClass, code string) (RelayAnswer, error) {
		slog.Warn("connector delivery failed", "connector_id", id, "class", string(class), "code", code)
		if err := r.Store.RecordDelivery(ctx, org, id, DeliveryOutcome{Failure: &RunError{Class: class, Code: code, At: time.Now()}}); err != nil {
			slog.Warn("connector delivery outcome not recorded", "connector_id", id)
		}
		if class == ClassTransient {
			return unavailable(code), nil
		}
		return RelayAnswer{Status: 500, ContentType: "text/plain", Body: "the delivery cannot be processed", ErrorCode: code}, nil
	}
	var err error
	var credential json.RawMessage
	if target.Sealed != nil {
		if target.Sealed.ExpiresAt != nil && !target.Sealed.ExpiresAt.After(time.Now()) {
			return fail(ClassAccess, "credential_expired")
		}
		if credential, err = r.Sealer.Open(org, id, *target.Sealed); err != nil {
			return fail(ClassAccess, "credential_unreadable")
		}
	}
	delivery, err := receiver.Receive(ctx, ReceiveRequest{Organization: org, InstanceID: id, CorpusID: target.CorpusID, Namespace: target.Namespace,
		Config: target.Config, Credential: credential, Checkpoint: target.Checkpoint, Now: time.Now(), ReadsToday: target.ReadsToday, Request: req, Route: route, Body: body})
	if err != nil {
		var typed *Error
		if errors.As(err, &typed) {
			return fail(typed.Class, typed.Code)
		}
		return fail(ClassTransient, "source_unavailable")
	}
	answer := RelayAnswer{Status: delivery.Status, ContentType: delivery.ContentType, Body: delivery.Body}
	if !delivery.Accepted {
		// Not authentic, or not for this instance: no side effects.
		return answer, nil
	}
	if route != "" && req.Method == "GET" {
		if delivery.Status == 204 && delivery.Body != "" {
			return fail(ClassSource, "plugin_invalid_response")
		}
		if len(delivery.Items) > 0 {
			return fail(ClassSource, "challenge_has_items")
		}
		if err := r.Store.RecordDelivery(ctx, org, id, DeliveryOutcome{Accepted: true, Reads: delivery.Reads}); err != nil {
			return unavailable("storage_unavailable"), nil
		}
		return answer, nil
	}
	if route != "" {
		answer.Receipts = []content.Receipt{}
	}
	if owner, ok := connector.(ExtensionOwner); ok {
		ctx = content.WithExtensionWriter(ctx, owner.ExtensionOwner())
	}
	scope := corpus.Scope{Organization: org, Actions: []string{"content:write", "content:read"}, Corpora: []string{target.CorpusID}}
	submitter := Acquirer{Ingest: r.Ingest}
	rc := runContext{connector: connector, target: target, credential: credential}
	outcome := DeliveryOutcome{Accepted: true, Carried: len(delivery.Items) > 0, Reads: delivery.Reads}
	for _, item := range delivery.Items {
		_, receipt, err := submitter.submit(ctx, scope, rc, item)
		switch {
		case err == nil:
			outcome.Fresh = outcome.Fresh || receipt.NewRevision
			if route != "" {
				answer.Receipts = append(answer.Receipts, receipt)
			}
		case errors.Is(err, corpus.ErrNotFound), errors.Is(err, corpus.ErrForbidden):
			return fail(ClassAccess, "corpus_unavailable")
		case errors.Is(err, content.ErrConflict), errors.Is(err, content.ErrInvalid), errors.Is(err, content.ErrUnsupported), errors.Is(err, content.ErrUnverifiedBlob):
			// One bad item never blocks the source; the source is still answered.
			outcome.Failure = &RunError{Class: ClassSource, Code: "item_rejected", At: time.Now()}
		default:
			// The source retries; the items already accepted replay their Receipts.
			return fail(ClassTransient, "ingestion_unavailable")
		}
	}
	if err := r.Store.RecordDelivery(ctx, org, id, outcome); err != nil {
		// The items are accepted; a retried delivery replays them.
		return unavailable("storage_unavailable"), nil
	}
	if route != "" {
		if outcome.Failure != nil {
			return RelayAnswer{}, ErrPushItemRejected
		}
		answer.Status = 202
		answer.ContentType, answer.Body = "", ""
	}
	return answer, nil
}
