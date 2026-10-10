package quivrplugin

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

// Connector is one connector kind's implementation. The plugin is stateless
// between invocations: everything it needs to resume is in the checkpoint,
// which the core persists and advances only after the items are accepted.
type Connector interface {
	// Fetch returns one page of items that are new or changed since the
	// request's checkpoint, and the checkpoint that resumes after them.
	// Return a classified *Error on failure, or ErrNotDue when the source
	// asked not to be polled yet.
	Fetch(ctx context.Context, req *FetchRequest) (*Page, error)
	// CheckCredential checks that the source accepts the credential without
	// fetching items. Return an AccessError when it does not.
	CheckCredential(ctx context.Context, req *CredentialRequest) (*CredentialStatus, error)
}

// Instance identifies the Connector Instance the core invokes the plugin for.
type Instance struct {
	ID   string `json:"instance_id"`
	Kind string `json:"kind"`
	// CorpusID and SourceNamespace are the Corpus and Source Namespace the
	// instance writes to (fetch requests, since Plugin API 0.3.1): bind a
	// Relation target to them with the target's Record Key.
	CorpusID        string `json:"corpus_id,omitempty"`
	SourceNamespace string `json:"source_namespace,omitempty"`
	// WebhookURL is the public address where the source delivers to this
	// instance (fetch requests of a push kind, since Plugin API 0.5, when the
	// deployment has a public URL). Register it with the source; the core
	// relays each delivery to Receive.
	WebhookURL string `json:"webhook_url,omitempty"`
	// Config is the instance configuration, already valid against the
	// kind's config_schema.
	Config json.RawMessage `json:"config"`
}

// DecodeConfig unmarshals the instance configuration into v.
func (i Instance) DecodeConfig(v any) error { return json.Unmarshal(i.Config, v) }

// FetchRequest is one fetch invocation.
type FetchRequest struct {
	InvocationID   string `json:"invocation_id"`
	Contribution   string `json:"contribution"`
	OrganizationID string `json:"organization_id"`
	// Configuration is the plugin configuration the installer supplies.
	Configuration json.RawMessage `json:"configuration"`
	Connector     Instance        `json:"connector"`
	Credential    Credential      `json:"credential"`
	// Checkpoint is the opaque checkpoint this plugin returned last; JSON
	// null on the first run. Use DecodeCheckpoint.
	Checkpoint json.RawMessage `json:"checkpoint"`
	// Now is the core's clock: use it instead of time.Now so runs are
	// reproducible.
	Now        time.Time `json:"now"`
	PageInRun  int       `json:"page_in_run"`
	ReadsToday int64     `json:"reads_today"`

	logger *slog.Logger
}

// FirstRun reports whether the core has no checkpoint yet.
func (r *FetchRequest) FirstRun() bool {
	return len(r.Checkpoint) == 0 || string(r.Checkpoint) == "null"
}

// DecodeCheckpoint unmarshals the checkpoint into v; it leaves v unchanged on
// the first run.
func (r *FetchRequest) DecodeCheckpoint(v any) error {
	if r.FirstRun() {
		return nil
	}
	return json.Unmarshal(r.Checkpoint, v)
}

// Logger returns a logger that scrubs this request's credential values from
// messages and attributes.
func (r *FetchRequest) Logger() *slog.Logger { return r.logger }

// CredentialRequest is one check_credential invocation.
type CredentialRequest struct {
	InvocationID   string          `json:"invocation_id"`
	Contribution   string          `json:"contribution"`
	OrganizationID string          `json:"organization_id"`
	Configuration  json.RawMessage `json:"configuration"`
	Connector      Instance        `json:"connector"`
	Credential     Credential      `json:"credential"`
	Now            time.Time       `json:"now"`

	logger *slog.Logger
}

// Logger returns a logger that scrubs this request's credential values.
func (r *CredentialRequest) Logger() *slog.Logger { return r.logger }

// CredentialStatus is a successful credential check.
type CredentialStatus struct {
	// ExpiresAt, when the source says so, drives the credential_expiring
	// health state.
	ExpiresAt *time.Time
}

// Page is one fetched page.
type Page struct {
	// SubmissionConcurrency requests 1..32 parallel submissions (Plugin API 0.15).
	// Zero omits the hint and keeps serial submission.
	SubmissionConcurrency int
	// AllowRepeatedRecordKeys admits repeated revisions of a Record Key in
	// source order (Plugin API 0.19). False omits the optional hint.
	AllowRepeatedRecordKeys bool
	Items                   []Item
	// Checkpoint resumes after this page (any JSON-encodable value, at most
	// limits.max_checkpoint_bytes encoded, 64 KiB by default). Return the request's checkpoint when nothing moved.
	Checkpoint any
	// More asks for another page in the same run; the checkpoint must move.
	More bool
	// Reads counts the source resources this page read.
	Reads int64
	// Diagnostics is shown as Connector Health diagnostics (at most 16 KiB).
	Diagnostics map[string]any
	// Notice reports a condition that ends a run that otherwise completed,
	// such as a spend cap (a code: lowercase, digits, underscores).
	Notice string
	// Push reports a push kind's push channel (Plugin API 0.5); nil reports
	// nothing and the core keeps the previous report.
	Push *PushStatus
}

// Push states.
const (
	PushActive  = "active"
	PushPending = "pending"
	PushFailed  = "failed"
)

// PushStatus is a push kind's view of the push channel it sets up at the
// source (a registered webhook and its subscriptions). Build it with
// PushIsActive, PushIsPending or PushHasFailed.
type PushStatus struct {
	State      string `json:"state"`
	ErrorClass string `json:"error_class,omitempty"`
	Code       string `json:"code,omitempty"`
	// PollIntervalSeconds, only while active, relaxes pull to a safety net:
	// the core runs fetch at most this often.
	PollIntervalSeconds int `json:"poll_interval_seconds,omitempty"`
}

// PushIsActive reports that deliveries are expected; pollEvery (0 for no
// change, else at least a minute) relaxes pull while they arrive.
func PushIsActive(pollEvery time.Duration) *PushStatus {
	return &PushStatus{State: PushActive, PollIntervalSeconds: int(pollEvery / time.Second)}
}

// PushIsPending reports a channel not set up yet; reason is an optional code.
func PushIsPending(reason string) *PushStatus { return &PushStatus{State: PushPending, Code: reason} }

// PushHasFailed reports a channel the source refused or broke. An access
// failure shows as the access_error Connector Health state; polling carries
// the collection meanwhile.
func PushHasFailed(class Class, code string) *PushStatus {
	return &PushStatus{State: PushFailed, ErrorClass: string(class), Code: code}
}

// Item is one source item that is new or changed since the checkpoint.
// Set exactly one of Content and Withdraw.
type Item struct {
	RecordKey      string               `json:"record_key"`
	Revision       string               `json:"revision,omitempty"`
	SourcePosition string               `json:"source_position,omitempty"`
	Content        *Content             `json:"content,omitempty"`
	Extensions     map[string]Extension `json:"extensions,omitempty"`
	// Withdraw asks for a Tombstone, for example a post deleted at the source.
	Withdraw    bool         `json:"withdraw,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Content is text or a Manifest; build it with Text or NewManifest.
type Content struct {
	Kind      string     `json:"kind"`
	Text      string     `json:"text,omitempty"`
	Parts     []Part     `json:"parts,omitzero"`
	Relations []Relation `json:"relations,omitempty"`
}

// Text is plain text content.
func Text(text string) *Content { return &Content{Kind: "text", Text: text} }

// NewManifest describes the text Parts of structured content. On Plugin API
// 0.15, attachments may provide all Parts, so no text Parts are required.
func NewManifest(parts ...Part) *Content { return &Content{Kind: "manifest", Parts: parts} }

// Part is one text Part of a Manifest. Binary Parts are attachments.
type Part struct {
	Key        string               `json:"key"`
	ParentKey  string               `json:"parent_key,omitempty"`
	Role       string               `json:"role"`
	Content    PartText             `json:"content"`
	Extensions map[string]Extension `json:"extensions,omitempty"`
}

// PartText is the text of a Part.
type PartText struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// TextPart builds a text Part.
func TextPart(key, role, text string) Part {
	return Part{Key: key, Role: role, Content: PartText{Kind: "text", Text: text}}
}

// Relation links the item to another Record.
type Relation struct {
	Type                 string      `json:"type"`
	Target               RelationKey `json:"target"`
	SourceTargetRevision string      `json:"source_target_revision,omitempty"`
}

// RelationKey identifies the Record a Relation points at.
type RelationKey struct {
	CorpusID  string `json:"corpus_id"`
	Namespace string `json:"namespace"`
	RecordKey string `json:"record_key"`
}

// Extension is structured metadata in a namespace the plugin declares.
type Extension struct {
	SchemaVersion string         `json:"schema_version"`
	Data          map[string]any `json:"data"`
}

// Attachment is a binary Part whose bytes the core asks for later, only when
// the item is not already accepted (Plugin API 0.4: declare
// contributions.connector.attachments and implement AttachmentSource). Ref
// is an opaque handle the plugin understands. Set SizeBytes and SHA256 when
// the plugin already holds the exact bytes: the core then asks for no
// description. Attachments need Manifest content.
type Attachment struct {
	Key        string               `json:"key"`
	ParentKey  string               `json:"parent_key,omitempty"`
	Role       string               `json:"role"`
	MediaType  string               `json:"media_type"`
	SizeBytes  *int64               `json:"size_bytes,omitempty"`
	SHA256     string               `json:"sha256,omitempty"`
	Extensions map[string]Extension `json:"extensions,omitempty"`
	Ref        string               `json:"ref"`
}

type pageJSON struct {
	SubmissionConcurrency   int             `json:"submission_concurrency,omitempty"`
	AllowRepeatedRecordKeys bool            `json:"allow_repeated_record_keys,omitempty"`
	Items                   []Item          `json:"items"`
	Checkpoint              json.RawMessage `json:"checkpoint"`
	More                    bool            `json:"more"`
	Reads                   int64           `json:"reads,omitempty"`
	Diagnostics             map[string]any  `json:"diagnostics,omitempty"`
	Notice                  string          `json:"notice,omitempty"`
	NotDue                  bool            `json:"not_due,omitempty"`
	Push                    *PushStatus     `json:"push,omitempty"`
}

type credentialJSON struct {
	Status    string     `json:"status"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}
