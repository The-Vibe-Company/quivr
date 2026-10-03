package pluginhttp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/call"
)

// ConnectorTimeoutCap bounds one connector invocation: the deadline is
// min(declared timeout_ms, ConnectorTimeoutCap). A run fetches at most
// connectors.DefaultMaxPages pages inside its acquisition activity.
const ConnectorTimeoutCap = plugins.ConnectorTimeoutCap

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

// Descriptor exposes installed metadata and the supported behavior ports.
func (c Connector) Descriptor() connectors.Descriptor {
	kind := c.declared()
	schema := kind.CredentialSchema
	if len(schema) == 0 {
		schema = nil
	}
	kind.CredentialSchema = schema
	d := connectors.Descriptor{Kind: c.Name, Description: kind.Description, ConfigSchema: kind.ConfigSchema,
		CredentialSchema: schema, CredentialRequired: kind.NeedsCredential(),
		DefaultInterval: time.Duration(kind.DefaultIntervalSeconds) * time.Second,
		Provider:        "plugin " + c.Pin.Manifest.ID + "@" + c.Pin.Manifest.Version,
		ExtensionOwner:  c.Pin.Manifest.ID, MaxAttachmentBytes: plugins.AttachmentMaxBytes(&c.Pin.Manifest),
		Credentials: c}
	if d.MaxAttachmentBytes > 0 {
		d.Attachments = c
	}
	if plugins.KindPushes(&c.Pin.Manifest, c.Name) {
		d.Receiver = c
	}
	if kind.API != nil {
		d.APIRoutes = kind.API.Routes
	}
	return d
}

func orNull(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return raw
}

func (c Connector) ref(instanceID string, config json.RawMessage) plugins.ConnectorRef {
	return plugins.ConnectorRef{InstanceID: instanceID, Kind: c.Name, Config: config}
}

func (c Connector) configuration() json.RawMessage {
	if len(c.Pin.Configuration) == 0 {
		return json.RawMessage(`{}`)
	}
	return c.Pin.Configuration
}

// Fetch checks discovery against the pinned manifest before asking for each
// page. A plugin that serves
// Plugin API 0.3.1 or later also receives the instance's corpus_id and
// source_namespace. The credential travels only in the request body and is
// looked for in the answer: an answer that echoes it is refused before
// anything from it is used.
func (c Connector) Fetch(ctx context.Context, r connectors.FetchRequest) (connectors.Page, error) {
	started := time.Now()
	checkpoint := orNull(r.Checkpoint)
	result, err := call.Invoke(ctx, c.Pin, call.ConnectorFetch, func(ctx context.Context, served string) ([]byte, error) {
		scoped := c.ref(r.InstanceID, r.Config)
		if plugins.ResolveAPI(served).Speaks(plugins.FeatureInstanceScope) {
			scoped.CorpusID, scoped.SourceNamespace = r.CorpusID, r.Namespace
		}
		if plugins.KindPushes(&c.Pin.Manifest, c.Name) && plugins.ResolveAPI(served).Speaks(plugins.FeaturePush) {
			scoped.WebhookURL = r.WebhookURL
		}
		return plugins.BuildConnectorFetchRequest(plugins.ConnectorFetchRequest{InvocationID: plugins.InvocationID(), Contribution: "connector", OrganizationID: r.Organization, Configuration: c.configuration(), Connector: scoped, Credential: orNull(r.Credential), Checkpoint: checkpoint, Now: r.Now.UTC().Format(time.RFC3339), PageInRun: r.PageInRun, ReadsToday: r.ReadsToday})
	}, func(ctx context.Context, body []byte) []plugins.Issue {
		return plugins.CheckConnectorOutput(ctx, body, checkpoint, &c.Pin.Manifest)
	}, plugins.CredentialSecrets(r.Credential))
	observe(c.Pin, r.Organization, OpConnectorFetch, started, result, err)
	if err != nil {
		return connectors.Page{}, err
	}
	var page plugins.ConnectorPage
	if err := json.Unmarshal(result.Body, &page); err != nil {
		return connectors.Page{}, connectors.SourceError(CodePluginInvalidResponse)
	}
	if page.NotDue {
		return connectors.Page{}, connectors.ErrNotDue
	}
	out := connectors.Page{Checkpoint: page.Checkpoint, More: page.More, Reads: page.Reads, Diagnostics: page.Diagnostics, Notice: page.Notice}
	if p := page.Push; p != nil && plugins.KindPushes(&c.Pin.Manifest, c.Name) {
		out.Push = &connectors.PushStatus{State: p.State, Class: connectors.ErrorClass(p.ErrorClass), Code: p.Code, PollInterval: time.Duration(p.PollIntervalSeconds) * time.Second}
	}
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
	started := time.Now()
	result, err := call.Invoke(ctx, c.Pin, call.CheckCredential, func(context.Context, string) ([]byte, error) {
		return plugins.BuildConnectorCredentialRequest(plugins.ConnectorCredentialRequest{InvocationID: plugins.InvocationID(), Contribution: "connector", OrganizationID: r.Organization, Configuration: c.configuration(), Connector: c.ref(r.InstanceID, r.Config), Credential: orNull(r.Credential), Now: r.Now.UTC().Format(time.RFC3339)})
	}, nil, plugins.CredentialSecrets(r.Credential))
	observe(c.Pin, r.Organization, OpCheckCredential, started, result, err)
	return err
}

func (c Connector) attachmentRequest(r connectors.AttachmentRequest, grant *plugins.ConnectorAttachmentGrant) ([]byte, error) {
	at := r.Attachment
	var extensions json.RawMessage
	if len(r.Extensions) > 0 {
		var err error
		extensions, err = json.Marshal(r.Extensions)
		if err != nil {
			return nil, err
		}
	}
	return plugins.BuildConnectorAttachmentRequest(plugins.ConnectorAttachmentRequest{
		InvocationID: plugins.InvocationID(), OrganizationID: r.Organization, Configuration: c.configuration(), Connector: c.ref(r.InstanceID, r.Config), Credential: r.Credential, Now: r.Now.UTC().Format(time.RFC3339),
		Item:       plugins.ConnectorAttachmentItem{RecordKey: r.RecordKey, Revision: r.Revision, Extensions: extensions},
		Attachment: plugins.ConnectorAttachment{Key: at.Key, ParentKey: at.ParentKey, Role: at.Role, MediaType: at.MediaType, SizeBytes: at.SizeBytes, SHA256: at.SHA256, Extensions: at.Extensions, Ref: at.Ref}, Grant: grant})
}

// DescribeAttachment asks the plugin for the exact size and SHA-256 of one
// attachment, or a skip, judged by plugins.CheckAttachmentAnswer.
func (c Connector) DescribeAttachment(ctx context.Context, r connectors.AttachmentRequest) (connectors.AttachmentDescription, error) {
	request, err := c.attachmentRequest(r, nil)
	if err != nil {
		return connectors.AttachmentDescription{}, connectors.SourceError(CodePluginInvalidResponse)
	}
	started := time.Now()
	result, err := call.Invoke(ctx, c.Pin, call.DescribeAttachment, call.Bytes(request), func(ctx context.Context, body []byte) []plugins.Issue {
		return plugins.CheckAttachmentAnswer(ctx, body, &c.Pin.Manifest)
	}, plugins.CredentialSecrets(r.Credential))
	observe(c.Pin, r.Organization, OpDescribeAttachment, started, result, err)
	if err != nil {
		return connectors.AttachmentDescription{}, err
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
	grant := &plugins.ConnectorAttachmentGrant{URL: g.URL, Method: "PUT", Headers: g.Headers, SizeBytes: g.SizeBytes, SHA256: g.SHA256, MediaType: g.MediaType, ExpiresAt: g.ExpiresAt.UTC().Format(time.RFC3339)}
	if grant.Headers == nil {
		grant.Headers = map[string]string{}
	}
	request, err := c.attachmentRequest(r, grant)
	if err != nil {
		return connectors.SourceError(CodePluginInvalidResponse)
	}
	started := time.Now()
	result, err := call.Invoke(ctx, c.Pin, call.UploadAttachment, call.Bytes(request), nil, grantSecrets(r.Credential, g))
	observe(c.Pin, r.Organization, OpUploadAttachment, started, result, err)
	return err
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
