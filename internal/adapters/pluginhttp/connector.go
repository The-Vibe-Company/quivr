package pluginhttp

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
)

// ConnectorTimeoutCap bounds one connector invocation: the deadline is
// min(declared timeout_ms, ConnectorTimeoutCap). A run fetches at most
// connectors.DefaultMaxPages pages inside its acquisition activity.
const ConnectorTimeoutCap = 30 * time.Second

// Connector Health codes the engine reports for a plugin kind, beside the
// codes the plugin declares in its error envelopes.
const (
	// CodePluginUnavailable: no answer, a timeout, a redirect, a non-2xx
	// answer without a valid envelope, or a discovery document that does not
	// match the pinned manifest.
	CodePluginUnavailable = "plugin_unavailable"
	// CodePluginInvalidError: an envelope without a connector error class, or
	// with one that contradicts its retryable flag.
	CodePluginInvalidError = "plugin_invalid_error"
	// CodePluginInvalidResponse: a 200 answer plugins.CheckConnectorOutput refuses.
	CodePluginInvalidResponse = "plugin_invalid_response"
	// CodeCredentialLeak: an answer that contains a credential value.
	CodeCredentialLeak = "credential_leak"
	// CodeAttachmentsUnsupported: items with attachments from a plugin whose
	// manifest declares no attachments (Plugin API 0.4).
	CodeAttachmentsUnsupported = "attachments_unsupported"
)

// Connector is one connector kind of a pinned plugin, installed in the
// connector Registry beside the built-in kinds. It implements
// connectors.Connector over Plugin Protocol 0.3 (0.4 for attachments) and
// judges every answer with the checks the Contract Runner uses
// (plugins.CheckConnectorOutput, plugins.CheckAttachmentAnswer). Every failure is
// a typed *connectors.Error, so the Acquirer maps it to Connector Health like
// a built-in kind's and never advances the checkpoint past it.
type Connector struct {
	Pin  *plugins.Pin
	Name string
}

var (
	_ connectors.Connector           = Connector{}
	_ connectors.CredentialChecker   = Connector{}
	_ connectors.CredentialRequirer  = Connector{}
	_ connectors.ExtensionOwner      = Connector{}
	_ connectors.Provider            = Connector{}
	_ connectors.AttachmentExchanger = Connector{}
)

// Connectors installs every connector kind of the pinned plugins.
func Connectors(pins *plugins.PinSet) []connectors.Connector {
	var out []connectors.Connector
	for _, k := range pins.Connectors() {
		out = append(out, Connector{Pin: k.Pin, Name: k.Kind})
	}
	return out
}

func (c Connector) declared() plugins.ConnectorKind {
	return c.Pin.Manifest.Contributions.Connector.Kinds[c.Name]
}

func (c Connector) Kind() string         { return c.Name }
func (c Connector) ConfigSchema() []byte { return c.declared().ConfigSchema }

// Description is the kind's manifest description.
func (c Connector) Description() string { return c.declared().Description }

// CredentialSchema is nil for a kind that declares no credential.
func (c Connector) CredentialSchema() []byte {
	if schema := c.declared().CredentialSchema; len(schema) > 0 {
		return schema
	}
	return nil
}

// CredentialRequired: a kind that declares a credential schema needs one,
// unless its manifest sets credential_required: false.
func (c Connector) CredentialRequired() bool {
	d := c.declared()
	d.CredentialSchema = c.CredentialSchema()
	return d.NeedsCredential()
}

func (c Connector) DefaultInterval() time.Duration {
	return time.Duration(c.declared().DefaultIntervalSeconds) * time.Second
}

// ExtensionOwner is the plugin id: its items may write the namespaces it owns.
func (c Connector) ExtensionOwner() string { return c.Pin.Manifest.ID }

func (c Connector) Provider() string {
	return "plugin " + c.Pin.Manifest.ID + "@" + c.Pin.Manifest.Version
}

type connectorRef struct {
	InstanceID      string          `json:"instance_id"`
	Kind            string          `json:"kind"`
	CorpusID        string          `json:"corpus_id,omitempty"`
	SourceNamespace string          `json:"source_namespace,omitempty"`
	Config          json.RawMessage `json:"config"`
}

type fetchRequest struct {
	InvocationID   string          `json:"invocation_id"`
	Contribution   string          `json:"contribution"`
	OrganizationID string          `json:"organization_id"`
	Configuration  json.RawMessage `json:"configuration"`
	Connector      connectorRef    `json:"connector"`
	Credential     json.RawMessage `json:"credential"`
	Checkpoint     json.RawMessage `json:"checkpoint"`
	Now            string          `json:"now"`
	PageInRun      int             `json:"page_in_run"`
	ReadsToday     int64           `json:"reads_today"`
}

type credentialRequest struct {
	InvocationID   string          `json:"invocation_id"`
	Contribution   string          `json:"contribution"`
	OrganizationID string          `json:"organization_id"`
	Configuration  json.RawMessage `json:"configuration"`
	Connector      connectorRef    `json:"connector"`
	Credential     json.RawMessage `json:"credential"`
	Now            string          `json:"now"`
}

func orNull(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return raw
}

func (c Connector) ref(instanceID string, config json.RawMessage) connectorRef {
	return connectorRef{InstanceID: instanceID, Kind: c.Name, Config: config}
}

func (c Connector) configuration() json.RawMessage {
	if len(c.Pin.Configuration) == 0 {
		return json.RawMessage(`{}`)
	}
	return c.Pin.Configuration
}

func (c Connector) timeout() time.Duration {
	d := time.Duration(c.Pin.Manifest.Contributions.Connector.TimeoutMS) * time.Millisecond
	if d <= 0 || d > ConnectorTimeoutCap {
		return ConnectorTimeoutCap
	}
	return d
}

// servedAPI remembers, per plugin endpoint and manifest digest, the Plugin API
// version its discovery served at the first page of the latest run.
var servedAPI sync.Map

func (c Connector) servedKey() string { return c.Pin.Endpoint + " " + c.Pin.ManifestDigest }

// served checks discovery at the first page of a run (or when no version is
// known yet) and returns the Plugin API version the plugin serves.
func (c Connector) served(ctx context.Context, pageInRun int) (string, error) {
	if pageInRun > 0 {
		if v, ok := servedAPI.Load(c.servedKey()); ok {
			return v.(string), nil
		}
	}
	v, err := (Client{Pin: c.Pin}).Discover(ctx)
	if err != nil {
		return "", err
	}
	servedAPI.Store(c.servedKey(), v)
	return v, nil
}

// Fetch asks the plugin for one page. The first page of a run first checks
// the discovery document against the pinned manifest. A plugin that serves
// Plugin API 0.3.1 or later also receives the instance's corpus_id and
// source_namespace. The credential travels only in the request body and is
// looked for in the answer: an answer that echoes it is refused before
// anything from it is used.
func (c Connector) Fetch(ctx context.Context, r connectors.FetchRequest) (connectors.Page, error) {
	served, err := c.served(ctx, r.PageInRun)
	if err != nil {
		return connectors.Page{}, connectors.TransientError(CodePluginUnavailable)
	}
	checkpoint := orNull(r.Checkpoint)
	scoped := c.ref(r.InstanceID, r.Config)
	if plugins.SendsInstanceScope(served) {
		scoped.CorpusID, scoped.SourceNamespace = r.CorpusID, r.Namespace
	}
	request, err := json.Marshal(fetchRequest{InvocationID: invocationID(), Contribution: "connector", OrganizationID: r.Organization,
		Configuration: c.configuration(), Connector: scoped, Credential: orNull(r.Credential), Checkpoint: checkpoint,
		Now: r.Now.UTC().Format(time.RFC3339), PageInRun: r.PageInRun, ReadsToday: r.ReadsToday})
	if err != nil {
		return connectors.Page{}, connectors.SourceError(CodePluginInvalidResponse)
	}
	invoke, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	manifest := &c.Pin.Manifest
	check := func(body []byte) []plugins.Issue {
		return plugins.CheckConnectorOutput(invoke, body, checkpoint, manifest)
	}
	result, err := devhost.InvokeConnectorFetch(invoke, c.Pin.Endpoint, request, plugins.ConnectorMaxResponseBytes(manifest), check)
	if failure := judge(result, err, plugins.CredentialSecrets(r.Credential)); failure != nil {
		return connectors.Page{}, failure
	}
	var page plugins.ConnectorPage
	if err := json.Unmarshal(result.Body, &page); err != nil {
		return connectors.Page{}, connectors.SourceError(CodePluginInvalidResponse)
	}
	if page.NotDue {
		return connectors.Page{}, connectors.ErrNotDue
	}
	out := connectors.Page{Checkpoint: page.Checkpoint, More: page.More, Reads: page.Reads, Diagnostics: page.Diagnostics, Notice: page.Notice}
	for _, item := range page.Items {
		mapped, err := mapItem(item)
		if err != nil {
			return connectors.Page{}, err
		}
		out.Items = append(out.Items, mapped)
	}
	return out, nil
}

// CheckCredential asks the plugin whether the source accepts a credential. A
// reported expires_at is not used yet.
func (c Connector) CheckCredential(ctx context.Context, r connectors.CredentialRequest) error {
	if err := (Client{Pin: c.Pin}).CheckDiscovery(ctx); err != nil {
		return connectors.TransientError(CodePluginUnavailable)
	}
	request, err := json.Marshal(credentialRequest{InvocationID: invocationID(), Contribution: "connector", OrganizationID: r.Organization,
		Configuration: c.configuration(), Connector: c.ref(r.InstanceID, r.Config), Credential: orNull(r.Credential), Now: r.Now.UTC().Format(time.RFC3339)})
	if err != nil {
		return connectors.SourceError(CodePluginInvalidResponse)
	}
	invoke, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	result, err := devhost.InvokeCheckCredential(invoke, c.Pin.Endpoint, request)
	return judge(result, err, plugins.CredentialSecrets(r.Credential))
}

// MaxAttachmentBytes is the plugin's effective attachments.max_bytes; 0 when
// its manifest declares no attachments.
func (c Connector) MaxAttachmentBytes() int64 { return plugins.AttachmentMaxBytes(&c.Pin.Manifest) }

func (c Connector) attachmentTimeout() time.Duration {
	return time.Duration(plugins.AttachmentTimeoutMS(&c.Pin.Manifest)) * time.Millisecond
}

type attachmentItem struct {
	RecordKey  string             `json:"record_key"`
	Revision   string             `json:"revision,omitempty"`
	Extensions content.Extensions `json:"extensions,omitempty"`
}

type attachmentGrant struct {
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	SizeBytes int64             `json:"size_bytes"`
	SHA256    string            `json:"sha256"`
	MediaType string            `json:"media_type"`
	ExpiresAt string            `json:"expires_at"`
}

type attachmentRequest struct {
	credentialRequest
	Item       attachmentItem              `json:"item"`
	Attachment plugins.ConnectorAttachment `json:"attachment"`
	Grant      *attachmentGrant            `json:"grant,omitempty"`
}

func (c Connector) attachmentRequest(r connectors.AttachmentRequest, grant *attachmentGrant) ([]byte, error) {
	at := r.Attachment
	return json.Marshal(attachmentRequest{
		credentialRequest: credentialRequest{InvocationID: invocationID(), Contribution: "connector", OrganizationID: r.Organization,
			Configuration: c.configuration(), Connector: c.ref(r.InstanceID, r.Config), Credential: orNull(r.Credential), Now: r.Now.UTC().Format(time.RFC3339)},
		Item: attachmentItem{RecordKey: r.RecordKey, Revision: r.Revision, Extensions: r.Extensions},
		Attachment: plugins.ConnectorAttachment{Key: at.Key, ParentKey: at.ParentKey, Role: at.Role, MediaType: at.MediaType, SizeBytes: at.SizeBytes,
			SHA256: at.SHA256, Extensions: at.Extensions, Ref: at.Ref},
		Grant: grant})
}

// DescribeAttachment asks the plugin for the exact size and SHA-256 of one
// attachment, or a skip, judged by plugins.CheckAttachmentAnswer.
func (c Connector) DescribeAttachment(ctx context.Context, r connectors.AttachmentRequest) (connectors.AttachmentDescription, error) {
	request, err := c.attachmentRequest(r, nil)
	if err != nil {
		return connectors.AttachmentDescription{}, connectors.SourceError(CodePluginInvalidResponse)
	}
	invoke, cancel := context.WithTimeout(ctx, c.attachmentTimeout())
	defer cancel()
	manifest := &c.Pin.Manifest
	result, err := devhost.InvokeDescribeAttachment(invoke, c.Pin.Endpoint, request, func(body []byte) []plugins.Issue {
		return plugins.CheckAttachmentAnswer(invoke, body, manifest)
	})
	if failure := judge(result, err, plugins.CredentialSecrets(r.Credential)); failure != nil {
		return connectors.AttachmentDescription{}, failure
	}
	var answer plugins.AttachmentAnswer
	if err := json.Unmarshal(result.Body, &answer); err != nil {
		return connectors.AttachmentDescription{}, connectors.SourceError(CodePluginInvalidResponse)
	}
	return connectors.AttachmentDescription{SizeBytes: answer.SizeBytes, SHA256: answer.SHA256, Skip: answer.Skip, ItemExtensions: answer.ItemExtensions}, nil
}

// UploadAttachment has the plugin upload one attachment to a grant. The
// grant URL and headers are secrets like the credential: an answer that
// echoes them is refused.
func (c Connector) UploadAttachment(ctx context.Context, r connectors.AttachmentRequest, g connectors.UploadGrant) error {
	grant := &attachmentGrant{URL: g.URL, Method: "PUT", Headers: g.Headers, SizeBytes: g.SizeBytes, SHA256: g.SHA256, MediaType: g.MediaType, ExpiresAt: g.ExpiresAt.UTC().Format(time.RFC3339)}
	if grant.Headers == nil {
		grant.Headers = map[string]string{}
	}
	request, err := c.attachmentRequest(r, grant)
	if err != nil {
		return connectors.SourceError(CodePluginInvalidResponse)
	}
	invoke, cancel := context.WithTimeout(ctx, c.attachmentTimeout())
	defer cancel()
	result, err := devhost.InvokeUploadAttachment(invoke, c.Pin.Endpoint, request)
	return judge(result, err, grantSecrets(r.Credential, g))
}

// grantSecrets are the strings an upload_attachment answer must not echo:
// the credential's values, the grant URL and its header values.
func grantSecrets(credential json.RawMessage, g connectors.UploadGrant) []string {
	secrets := append(plugins.CredentialSecrets(credential), g.URL)
	for _, v := range g.Headers {
		if len(v) >= plugins.MinSecretLength {
			secrets = append(secrets, v)
		}
	}
	return secrets
}

// judge maps one invocation to nil (a valid 200 answer) or a typed failure.
// Nothing the plugin wrote (a message, an invalid output) is logged or kept:
// only its declared class and code reach Connector Health. An answer that
// contains one of the secrets (credential values, a grant) is refused.
func judge(result *devhost.Result, err error, secrets []string) error {
	if err != nil || result == nil {
		return connectors.TransientError(CodePluginUnavailable)
	}
	if plugins.ContainsSecret(result.Body, secrets) {
		return connectors.SourceError(CodeCredentialLeak)
	}
	if e := result.Error; e != nil {
		if len(plugins.ConnectorErrorIssues(e.Class, e.Retryable)) > 0 {
			return connectors.SourceError(CodePluginInvalidError)
		}
		return &connectors.Error{Class: connectors.ErrorClass(e.Class), Code: e.Code, RetryAfter: time.Duration(e.RetryAfterSeconds) * time.Second}
	}
	if len(result.Issues) > 0 {
		if result.Status != 200 || result.Issues[0].Code == devhost.CodeInvalidErrorEnvelope {
			return connectors.TransientError(CodePluginUnavailable)
		}
		if result.Issues[0].Code == plugins.CodeAttachmentsUnsupported {
			return connectors.SourceError(CodeAttachmentsUnsupported)
		}
		return connectors.SourceError(CodePluginInvalidResponse)
	}
	return nil
}

// mapItem turns a checked item into the Acquirer's item.
func mapItem(item plugins.ConnectorItem) (connectors.Item, error) {
	out := connectors.Item{RecordKey: item.RecordKey, Revision: item.Revision, Position: item.SourcePosition, Extensions: item.Extensions, Withdraw: item.Withdraw}
	for _, at := range item.Attachments {
		out.Attachments = append(out.Attachments, connectors.Attachment{Key: at.Key, ParentKey: at.ParentKey, Role: at.Role, MediaType: at.MediaType,
			Extensions: at.Extensions, Ref: at.Ref, SizeBytes: at.SizeBytes, SHA256: at.SHA256})
	}
	if item.Withdraw {
		return out, nil
	}
	var text content.Text
	if err := json.Unmarshal(item.Content, &text); err != nil {
		return connectors.Item{}, connectors.SourceError(CodePluginInvalidResponse)
	}
	if text.Kind == "text" {
		out.Content = text
		return out, nil
	}
	var manifest content.Manifest
	if err := json.Unmarshal(item.Content, &manifest); err != nil {
		return connectors.Item{}, connectors.SourceError(CodePluginInvalidResponse)
	}
	out.Manifest = &manifest
	return out, nil
}
