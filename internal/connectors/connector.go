// Package connectors owns Connector Instances: pull acquisition endpoints bound
// to one Corpus and one Source Namespace, their Deposited Credentials, their
// Acquisition Checkpoints and their Connector Health. Built-in connectors fetch
// from a source; every item enters the engine through the same ingestion
// command path as the public API.
package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"sort"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ErrorClass separates a source refusing access from a transient outage and
// from a source returning unusable data.
type ErrorClass string

const (
	ClassAccess    ErrorClass = "access"
	ClassTransient ErrorClass = "transient"
	ClassSource    ErrorClass = "source"
)

// Error is a typed acquisition failure. Code is a stable public diagnostic
// (never a message containing source data or secrets). RetryAfter, when set,
// defers the next run until the source accepts requests again (e.g. a rate
// limit reset); it never shortens the configured interval.
type Error struct {
	Class      ErrorClass
	Code       string
	RetryAfter time.Duration
}

func (e *Error) Error() string { return string(e.Class) + ": " + e.Code }

// ErrNotDue is returned by Fetch when the source asked not to be polled yet
// (for example an RSS ttl). The run finishes as skipped: no poll happened, so
// neither last_success_at nor last_error changes.
var ErrNotDue = errors.New("not_due")

// AccessError, TransientError and SourceError build typed failures.
func AccessError(code string) error    { return &Error{Class: ClassAccess, Code: code} }
func TransientError(code string) error { return &Error{Class: ClassTransient, Code: code} }
func SourceError(code string) error    { return &Error{Class: ClassSource, Code: code} }

// Item is one source item that is new or changed since the checkpoint.
type Item struct {
	// RecordKey is the source-provided stable identity within the Source Namespace.
	RecordKey string
	// Revision identifies the item content; empty derives it from the content.
	Revision string
	// Position is an optional monotonic Source Position.
	Position   string
	Content    content.Text
	Manifest   *content.Manifest
	Extensions content.Extensions
	// Withdraw asks for a Tombstone instead of a new version (source terms, e.g. deleted posts).
	Withdraw bool
	// Attachments are binary Parts whose bytes the source uploads later,
	// through the connector's AttachmentExchanger, only when the item is not
	// already accepted; the Acquirer appends each verified Blob to the
	// Manifest as a Blob Part. A connector returning attachments should set
	// Revision so an unchanged item is recognised before anything is read.
	Attachments []Attachment
}

// Attachment is one binary Part of an item, described by the source.
type Attachment struct {
	Key        string
	ParentKey  string
	Role       string
	MediaType  string
	Extensions content.Extensions
	// Ref is the opaque handle the connector gives back to read the bytes.
	Ref string
	// SizeBytes and SHA256, when both set, are the exact bytes: the Acquirer
	// then asks for no description before it issues the upload grant.
	SizeBytes *int64
	SHA256    string
}

// AttachmentRequest names one attachment of one item for the connector.
type AttachmentRequest struct {
	Organization string
	InstanceID   string
	Config       json.RawMessage
	Credential   json.RawMessage
	Now          time.Time
	RecordKey    string
	Revision     string
	// Extensions are the item's current extensions.
	Extensions content.Extensions
	Attachment Attachment
}

// AttachmentDescription is the exact size and SHA-256 of an attachment's
// bytes, or Skip: why it is left out, with the item's replacement
// extensions when ItemExtensions is set.
type AttachmentDescription struct {
	SizeBytes      int64
	SHA256         string
	Skip           string
	ItemExtensions content.Extensions
}

// UploadGrant is one presigned PUT, pinned to the exact length, checksum and
// media type, that the core issues for one attachment.
type UploadGrant struct {
	URL       string
	Headers   map[string]string
	SizeBytes int64
	SHA256    string
	MediaType string
	ExpiresAt time.Time
}

// CodeAttachmentChanged is the source error an AttachmentExchanger returns
// when the bytes changed since they were described; the Acquirer describes
// the attachment again once.
const CodeAttachmentChanged = "attachment_changed"

// AttachmentExchanger is implemented by a Connector whose items carry
// attachments (plugin kinds, Plugin API 0.4). The source never writes to
// storage on its own: it describes the bytes, the core issues a grant, the
// source uploads against it and the core verifies what was stored.
type AttachmentExchanger interface {
	DescribeAttachment(context.Context, AttachmentRequest) (AttachmentDescription, error)
	UploadAttachment(context.Context, AttachmentRequest, UploadGrant) error
	// MaxAttachmentBytes is the connector's own cap; the engine's
	// MaxAttachmentBytes still applies.
	MaxAttachmentBytes() int64
}

// FetchRequest is one page request. Credential is the decrypted secret JSON,
// held only in memory for the duration of the fetch.
type FetchRequest struct {
	// Organization and InstanceID identify the Connector Instance, for
	// connectors that run outside the engine (plugin kinds).
	Organization string
	InstanceID   string
	// CorpusID and Namespace are the Corpus and Source Namespace the
	// instance writes to, for connectors that bind Relation targets
	// themselves (plugin kinds).
	CorpusID   string
	Namespace  string
	Config     json.RawMessage
	Credential json.RawMessage
	Checkpoint json.RawMessage
	Now        time.Time
	// PageInRun is 0 for the first page of a run, then counts up.
	PageInRun int
	// ReadsToday is the number of source resources read during the current
	// UTC day, including earlier pages of this run.
	ReadsToday int64
}

// Page is a fetched page and the checkpoint that resumes after it. More asks
// for another page in the same run.
type Page struct {
	Items      []Item
	Checkpoint json.RawMessage
	More       bool
	// Reads counts the source resources this page read (for sources that bill
	// or rate-limit per resource). It feeds the per-UTC-day usage counters.
	Reads int64
	// Diagnostics is an optional kind-defined JSON object exposed as
	// health.diagnostics; the latest page's value replaces the previous one.
	Diagnostics json.RawMessage
	// Notice is a diagnostic code for a run that completed normally but must
	// report a condition (e.g. a spend cap); it ends the run.
	Notice string
}

// Connector is the internal contract of one connector kind. It is shaped so a
// future connector Plugin Contribution can implement it remotely: pure
// config/credential/checkpoint in, items/checkpoint/typed errors out.
type Connector interface {
	Kind() string
	// ConfigSchema and CredentialSchema are JSON Schemas; a nil
	// CredentialSchema means the kind takes no credential.
	ConfigSchema() []byte
	CredentialSchema() []byte
	DefaultInterval() time.Duration
	Fetch(context.Context, FetchRequest) (Page, error)
}

// CredentialRequest asks whether the source accepts a Deposited Credential.
type CredentialRequest struct {
	Organization string
	InstanceID   string
	Config       json.RawMessage
	Credential   json.RawMessage
	Now          time.Time
}

// CredentialChecker is optionally implemented by a Connector that can check
// a credential without fetching (plugin kinds, through check_credential). The
// Acquirer calls it at the start of a run whose credential was deposited
// after the last successful poll; a typed *Error ends the run like a fetch.
type CredentialChecker interface {
	CheckCredential(context.Context, CredentialRequest) error
}

// ExtensionOwner is optionally implemented by a Connector that runs as a
// pinned plugin: its items may write the extension namespaces that plugin
// owns, because its output was validated against the plugin's manifest.
type ExtensionOwner interface {
	ExtensionOwner() string
}

// Provider is optionally implemented by a Connector to name who provides its
// kind in startup errors; built-in kinds are provided by "the engine".
type Provider interface {
	Provider() string
}

func providerOf(c Connector) string {
	if p, ok := c.(Provider); ok {
		return p.Provider()
	}
	return "the engine"
}

// ConfigChecker is optionally implemented by a Connector whose configuration
// has rules JSON Schema cannot express (e.g. a bounded backfill window).
type ConfigChecker interface {
	CheckConfig(config json.RawMessage, now time.Time) error
}

type registered struct {
	connector  Connector
	config     *jsonschema.Schema
	credential *jsonschema.Schema
}

// Registry resolves the connector kinds enabled in this deployment.
type Registry struct{ kinds map[string]registered }

// NewRegistry compiles each connector's schemas. Each kind resolves to
// exactly one provider: a kind listed twice (for example a built-in kind and
// a pinned plugin's) refuses startup.
func NewRegistry(list ...Connector) (*Registry, error) {
	r := &Registry{kinds: map[string]registered{}}
	for _, c := range list {
		if other, exists := r.kinds[c.Kind()]; exists {
			return nil, fmt.Errorf("connector kind %q is provided by %s and by %s; each kind resolves to one provider", c.Kind(), providerOf(other.connector), providerOf(c))
		}
		entry := registered{connector: c}
		var err error
		if entry.config, err = compile(c.Kind()+"/config", c.ConfigSchema()); err != nil {
			return nil, err
		}
		if c.CredentialSchema() != nil {
			if entry.credential, err = compile(c.Kind()+"/credential", c.CredentialSchema()); err != nil {
				return nil, err
			}
		}
		r.kinds[c.Kind()] = entry
	}
	return r, nil
}

func compile(name string, schema []byte) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytesReader(schema))
	if err != nil {
		return nil, fmt.Errorf("%s schema: %w", name, err)
	}
	compiler := jsonschema.NewCompiler()
	url := "https://quivr.invalid/connectors/" + name
	if err = compiler.AddResource(url, doc); err != nil {
		return nil, err
	}
	return compiler.Compile(url)
}

// Enabled lists enabled kinds.
func (r *Registry) Enabled() []string {
	kinds := make([]string, 0, len(r.kinds))
	for k := range r.kinds {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}

// Lookup returns the connector of an enabled kind.
func (r *Registry) Lookup(kind string) (Connector, bool) {
	if r == nil {
		return nil, false
	}
	e, ok := r.kinds[kind]
	return e.connector, ok
}

// validate checks config and secret against the kind's schemas. Failures
// carry the JSON Pointer of the offending member: config under /config, the
// secret under secretAt (its location in the calling request).
func (r *Registry) validate(kind string, config, secret json.RawMessage, secretAt string) error {
	e, ok := r.kinds[kind]
	if !ok {
		return ErrUnsupportedKind
	}
	if err := validateJSON(e.config, config); err != nil {
		return WithField(ErrInvalidConfig, "/config"+location(err))
	}
	if secret != nil {
		if e.credential == nil {
			return WithField(ErrInvalidCredential, secretAt)
		}
		if err := validateJSON(e.credential, secret); err != nil {
			return WithField(ErrInvalidCredential, secretAt+location(err))
		}
	}
	return nil
}

func validateJSON(schema *jsonschema.Schema, raw json.RawMessage) error {
	if raw == nil {
		return errors.New("missing document")
	}
	doc, err := jsonschema.UnmarshalJSON(bytesReader(raw))
	if err != nil {
		return err
	}
	return schema.Validate(doc)
}
