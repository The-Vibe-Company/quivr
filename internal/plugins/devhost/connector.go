package devhost

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

// Connector Contribution routes (Plugin API 0.3).
const (
	ConnectorFetchRoute           = "/v0/contributions/connector/fetch"
	ConnectorCheckCredentialRoute = "/v0/contributions/connector/check_credential"
	// ConnectorReceiveRoute relays a push delivery (Plugin API 0.5).
	ConnectorReceiveRoute = "/v0/contributions/connector/receive"
)

// Connector fixture defaults (contracts/plugins/v0/connector-fixture.schema.json).
const (
	DevConnectorNow          = "2026-01-01T00:00:00Z"
	DefaultConnectorMaxPages = 10
)

// IsConnectorFixture reports whether a fixture file is a connector fixture:
// it has a top-level connector property.
func IsConnectorFixture(raw []byte) bool {
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) != nil {
		return false
	}
	_, ok := probe["connector"]
	return ok
}

// ConnectorExpectedPage is one page a connector fixture expects.
type ConnectorExpectedPage struct {
	RecordKeys []string `json:"record_keys"`
	More       *bool    `json:"more,omitempty"`
}

// ConnectorExpectedError is an error class (and optionally code) a connector
// fixture expects.
type ConnectorExpectedError struct {
	Class string `json:"error_class"`
	Code  string `json:"code,omitempty"`
}

// ConnectorExpectedCredential is the expected check_credential answer: status
// ok, or an error class.
type ConnectorExpectedCredential struct {
	Status string `json:"status,omitempty"`
	Class  string `json:"error_class,omitempty"`
	Code   string `json:"code,omitempty"`
}

// ConnectorRun is a development connector run built from a connector fixture.
type ConnectorRun struct {
	Kind       string
	Config     json.RawMessage
	Credential json.RawMessage
	Checkpoint json.RawMessage
	Now        string
	MaxPages   int
	// PluginAPI is the Plugin API version the plugin's discovery serves; it
	// decides whether fetch requests carry the instance scope.
	PluginAPI string
	Expect    struct {
		Pages           []ConnectorExpectedPage      `json:"pages,omitempty"`
		Error           *ConnectorExpectedError      `json:"error,omitempty"`
		CheckCredential *ConnectorExpectedCredential `json:"check_credential,omitempty"`
	}
	// Receives are the fixture's push deliveries (Plugin API 0.5).
	Receives []ConnectorReceiveCase

	configuration json.RawMessage
	short         string
	pushes        bool
}

// ConnectorReceiveCase is one delivery a connector fixture relays to receive.
type ConnectorReceiveCase struct {
	Description string `json:"description,omitempty"`
	Route       string `json:"route,omitempty"`
	Request     struct {
		Path       string            `json:"path,omitempty"`
		Method     string            `json:"method"`
		Query      string            `json:"query,omitempty"`
		Headers    map[string]string `json:"headers,omitempty"`
		Body       *string           `json:"body,omitempty"`
		BodyBase64 *string           `json:"body_base64,omitempty"`
	} `json:"request"`
	Expect *ConnectorExpectedDelivery `json:"expect,omitempty"`
}

// ConnectorExpectedDelivery is what a receive case expects.
type ConnectorExpectedDelivery struct {
	Verdict      string                  `json:"verdict,omitempty"`
	Status       int                     `json:"status,omitempty"`
	RecordKeys   []string                `json:"record_keys,omitempty"`
	BodyContains string                  `json:"body_contains,omitempty"`
	Error        *ConnectorExpectedError `json:"error,omitempty"`
}

// RelayedRequest is the request a receive invocation relays: lowercase header
// names, the exact body in base64.
type RelayedRequest = plugins.ConnectorRelayedRequest

type connectorFixture struct {
	Connector struct {
		Kind   string          `json:"kind"`
		Config json.RawMessage `json:"config"`
	} `json:"connector"`
	Credential    json.RawMessage        `json:"credential,omitempty"`
	Configuration json.RawMessage        `json:"configuration,omitempty"`
	Checkpoint    json.RawMessage        `json:"checkpoint,omitempty"`
	Now           string                 `json:"now,omitempty"`
	MaxPages      int                    `json:"max_pages,omitempty"`
	Expect        json.RawMessage        `json:"expect,omitempty"`
	Receive       []ConnectorReceiveCase `json:"receive,omitempty"`
}

type connectorRef = plugins.ConnectorRef
type connectorFetchRequest = plugins.ConnectorFetchRequest
type connectorCredentialRequest = plugins.ConnectorCredentialRequest

// BuildConnectorRun reads a connector fixture and validates it: the fixture
// schema, the plugin configuration, and the Connector Instance configuration
// and credential against the kind's declared schemas. The Connector Instance
// id is dev-connector-<digest> and invocation ids dev-invocation-<digest>-<suffix>,
// from the first 16 hex digits of the SHA-256 of the fixture bytes; the
// Organization is dev-organization. Issues report an invalid fixture; err
// reports I/O failures.
func BuildConnectorRun(path string, m *plugins.Manifest) (*ConnectorRun, []plugins.Issue, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	if issues := plugins.ValidateDocument("connector-fixture.schema.json", raw); len(issues) > 0 {
		return nil, issues, nil
	}
	var f connectorFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(raw)
	run := &ConnectorRun{Kind: f.Connector.Kind, Config: f.Connector.Config, Credential: f.Credential, Checkpoint: f.Checkpoint,
		Now: f.Now, MaxPages: f.MaxPages, configuration: f.Configuration, short: hex.EncodeToString(sum[:])[:16]}
	if len(run.Credential) == 0 {
		run.Credential = json.RawMessage("null")
	}
	if len(run.Checkpoint) == 0 {
		run.Checkpoint = json.RawMessage("null")
	}
	if run.Now == "" {
		run.Now = DevConnectorNow
	}
	if run.MaxPages == 0 {
		run.MaxPages = DefaultConnectorMaxPages
	}
	if len(run.configuration) == 0 {
		run.configuration = json.RawMessage(`{}`)
	}
	if len(f.Expect) > 0 {
		if err := json.Unmarshal(f.Expect, &run.Expect); err != nil {
			return nil, nil, err
		}
	}
	run.Receives, run.pushes = f.Receive, plugins.KindPushes(m, run.Kind)
	issues := plugins.ValidateConfiguration(m, run.configuration)
	if len(run.Receives) > 0 && !run.pushes {
		issues = append(issues, plugins.Issue{Code: plugins.CodeInvalidConfig, Path: "/receive",
			Message: fmt.Sprintf("kind %q does not declare the push mode; only a push kind's fixture has receive cases", run.Kind)})
	}
	issues = append(issues, plugins.ValidateConnectorInstance(m, run.Kind, run.Config, run.Credential)...)
	for i, c := range run.Receives {
		if c.Route == "" {
			continue
		}
		var api *plugins.ConnectorAPI
		if m.Contributions.Connector != nil {
			api = m.Contributions.Connector.Kinds[run.Kind].API
		}
		valid := false
		var requestSchema json.RawMessage
		if api != nil {
			for _, route := range api.Routes {
				if route.Name == c.Route && route.Method == c.Request.Method && fixturePathMatches(route.Path, c.Request.Path) {
					valid = true
					requestSchema = route.RequestSchema
				}
			}
		}
		if !valid {
			issues = append(issues, plugins.Issue{Code: plugins.CodeInvalidConfig, Path: fmt.Sprintf("/receive/%d/route", i), Message: "route, method and path must match a declared API route"})
		}
		raw, err := base64.StdEncoding.DecodeString(c.Relayed().BodyBase64)
		if err != nil || !(json.Valid(raw) || (c.Request.Method == "GET" && len(raw) == 0)) {
			issues = append(issues, plugins.Issue{Code: plugins.CodeInvalidConfig, Path: fmt.Sprintf("/receive/%d/request/body", i), Message: "API body must be JSON (an empty GET body becomes null)"})
			continue
		}
		if c.Request.Method == "GET" && len(raw) == 0 {
			raw = []byte("null")
		}
		if valid {
			for _, issue := range plugins.ValidateConnectorAPIBody(requestSchema, raw) {
				issue.Path = fmt.Sprintf("/receive/%d/request%s", i, issue.Path)
				issues = append(issues, issue)
			}
		}
	}
	if len(issues) > 0 {
		return nil, issues, nil
	}
	return run, nil, nil
}

func (r *ConnectorRun) ref(kind string) connectorRef {
	return connectorRef{InstanceID: "dev-connector-" + r.short, Kind: kind, Config: r.Config}
}

// Dev scope of a replayed fixture's fetch requests, sent when the run's
// PluginAPI (the version discovery serves) is 0.3.1 or later.
const (
	DevCorpusID        = "dev-corpus"
	DevSourceNamespace = "dev-namespace"
)

// DevWebhookBase prefixes the webhook_url of a replayed fixture's fetch
// requests: a push kind's plugin sees an address it cannot reach, as a
// fixture never involves the real source.
const DevWebhookBase = "https://quivr.invalid/v0/connector-webhooks/"

func (r *ConnectorRun) scopedRef(kind string) connectorRef {
	ref := r.ref(kind)
	if plugins.ResolveAPI(r.PluginAPI).Speaks(plugins.FeatureInstanceScope) {
		ref.CorpusID, ref.SourceNamespace = DevCorpusID, DevSourceNamespace
	}
	if r.pushes && plugins.ResolveAPI(r.PluginAPI).Speaks(plugins.FeaturePush) {
		ref.WebhookURL = DevWebhookBase + ref.InstanceID
	}
	return ref
}

// FetchRequest builds the fetch request of one page of the run. suffix makes
// the invocation id unique per attempt.
func (r *ConnectorRun) FetchRequest(checkpoint json.RawMessage, page int, readsToday int64, suffix string) []byte {
	body, _ := plugins.BuildConnectorFetchRequest(connectorFetchRequest{
		InvocationID: fmt.Sprintf("dev-invocation-%s-%s", r.short, suffix), Contribution: "connector",
		OrganizationID: "dev-organization", Configuration: r.configuration, Connector: r.scopedRef(r.Kind),
		Credential: r.Credential, Checkpoint: checkpoint, Now: r.Now, PageInRun: page, ReadsToday: readsToday,
	})
	return body
}

// CheckCredentialRequest builds the check_credential request of the run.
func (r *ConnectorRun) CheckCredentialRequest(suffix string) []byte {
	body, _ := plugins.BuildConnectorCredentialRequest(connectorCredentialRequest{
		InvocationID: fmt.Sprintf("dev-invocation-%s-%s", r.short, suffix), Contribution: "connector",
		OrganizationID: "dev-organization", Configuration: r.configuration, Connector: r.ref(r.Kind),
		Credential: r.Credential, Now: r.Now,
	})
	return body
}

// Relayed turns a fixture receive case into the relayed request: header names
// lowercased, a text body encoded as base64.
func (c ConnectorReceiveCase) Relayed() RelayedRequest {
	out := RelayedRequest{Path: c.Request.Path, Method: c.Request.Method, Query: c.Request.Query, Headers: map[string][]string{}}
	names := make([]string, 0, len(c.Request.Headers))
	for name := range c.Request.Headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		lower := strings.ToLower(name)
		out.Headers[lower] = append(out.Headers[lower], c.Request.Headers[name])
	}
	switch {
	case c.Request.BodyBase64 != nil:
		out.BodyBase64 = *c.Request.BodyBase64
	case c.Request.Body != nil:
		out.BodyBase64 = base64.StdEncoding.EncodeToString([]byte(*c.Request.Body))
	}
	return out
}

// ReceiveRequest builds the receive request of one relayed delivery of the
// run, with the dev scope and the fixture's checkpoint.
func (r *ConnectorRun) ReceiveRequest(relayed RelayedRequest, suffix string) []byte {
	body, _ := plugins.BuildConnectorReceiveRequest(plugins.ConnectorReceiveRequest{
		InvocationID:   fmt.Sprintf("dev-invocation-%s-%s", r.short, suffix),
		OrganizationID: "dev-organization", Configuration: r.configuration,
		Connector: plugins.ConnectorReceiveRef{InstanceID: "dev-connector-" + r.short, Kind: r.Kind,
			Config: r.Config, CorpusID: DevCorpusID, SourceNamespace: DevSourceNamespace},
		Credential: r.Credential, Now: r.Now, Checkpoint: r.Checkpoint, Request: relayed})
	return body
}

// InvokeConnectorReceive posts a receive request and applies check to a 200
// body, such as a closure over plugins.CheckReceiveOutput.
func InvokeConnectorReceive(ctx context.Context, baseURL string, request []byte, maxResponseBytes int, check func(body []byte) []plugins.Issue) (*Result, error) {
	return invoke(ctx, baseURL, ConnectorReceiveRoute, request, maxResponseBytes, check)
}

// InvokeConnectorFetch posts a fetch request and applies check to a 200 body,
// such as a closure over plugins.CheckConnectorOutput.
func InvokeConnectorFetch(ctx context.Context, baseURL string, request []byte, maxResponseBytes int, check func(body []byte) []plugins.Issue) (*Result, error) {
	return invoke(ctx, baseURL, ConnectorFetchRoute, request, maxResponseBytes, check)
}

// InvokeCheckCredential posts a check_credential request and validates a 200
// body with plugins.CheckCredentialOutput.
func InvokeCheckCredential(ctx context.Context, baseURL string, request []byte) (*Result, error) {
	return invoke(ctx, baseURL, ConnectorCheckCredentialRoute, request, 64<<10, plugins.CheckCredentialOutput)
}

// AttachmentItem is the item an attachment request names.
type AttachmentItem = plugins.ConnectorAttachmentItem

// AttachmentGrant is the presigned PUT of an upload_attachment request.
type AttachmentGrant = plugins.ConnectorAttachmentGrant
type attachmentRequest = plugins.ConnectorAttachmentRequest

// AttachmentRequest builds a describe_attachment request (grant nil) or an
// upload_attachment request of the run.
func (r *ConnectorRun) AttachmentRequest(item AttachmentItem, at plugins.ConnectorAttachment, grant *AttachmentGrant, suffix string) []byte {
	body, _ := plugins.BuildConnectorAttachmentRequest(attachmentRequest{
		InvocationID: fmt.Sprintf("dev-invocation-%s-%s", r.short, suffix), Contribution: "connector",
		OrganizationID: "dev-organization", Configuration: r.configuration, Connector: r.ref(r.Kind),
		Credential: r.Credential, Now: r.Now, Item: item, Attachment: at, Grant: grant})
	return body
}

// Attachment routes (Plugin API 0.4).
const (
	ConnectorDescribeAttachmentRoute = "/v0/contributions/connector/describe_attachment"
	ConnectorUploadAttachmentRoute   = "/v0/contributions/connector/upload_attachment"
)

// attachmentAnswerBytes bounds a describe_attachment or upload_attachment answer.
const attachmentAnswerBytes = 1 << 20

// InvokeDescribeAttachment posts a describe_attachment request and applies
// check to a 200 body, such as a closure over plugins.CheckAttachmentAnswer.
func InvokeDescribeAttachment(ctx context.Context, baseURL string, request []byte, check func(body []byte) []plugins.Issue) (*Result, error) {
	return invoke(ctx, baseURL, ConnectorDescribeAttachmentRoute, request, attachmentAnswerBytes, check)
}

// InvokeUploadAttachment posts an upload_attachment request and validates a
// 200 body with plugins.CheckUploadAnswer.
func InvokeUploadAttachment(ctx context.Context, baseURL string, request []byte) (*Result, error) {
	return invoke(ctx, baseURL, ConnectorUploadAttachmentRoute, request, attachmentAnswerBytes, plugins.CheckUploadAnswer)
}

// ReceiveCaseRequest adds declared-route metadata to the shared receive wire
// builder; legacy cases retain their original unnamed webhook shape.
func (r *ConnectorRun) ReceiveCaseRequest(c ConnectorReceiveCase, suffix string) []byte {
	body := r.ReceiveRequest(c.Relayed(), suffix)
	if c.Route == "" {
		return body
	}
	var request plugins.ConnectorReceiveRequest
	_ = json.Unmarshal(body, &request)
	request.Route = c.Route
	request.Body, _ = base64.StdEncoding.DecodeString(request.Request.BodyBase64)
	body, _ = plugins.BuildConnectorReceiveRequest(request)
	return body
}

func fixturePathMatches(pattern, path string) bool {
	a, b := strings.Split(pattern, "/"), strings.Split(path, "/")
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if b[i] == "" || b[i] == "." || b[i] == ".." || (!strings.HasPrefix(a[i], "{") && a[i] != b[i]) {
			return false
		}
	}
	return true
}
