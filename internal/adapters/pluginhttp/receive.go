package pluginhttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
)

// ReceiveTimeoutCap bounds one receive invocation: the source waits for the
// answer, and sources give up after a few seconds.
const ReceiveTimeoutCap = 8 * time.Second

var _ connectors.Receiver = Connector{}

// Pushes reports whether the kind declares the push mode.
func (c Connector) Pushes() bool { return plugins.KindPushes(&c.Pin.Manifest, c.Name) }

type receiveRequest struct {
	InvocationID   string                 `json:"invocation_id"`
	Contribution   string                 `json:"contribution"`
	OrganizationID string                 `json:"organization_id"`
	Configuration  json.RawMessage        `json:"configuration"`
	Connector      connectorRef           `json:"connector"`
	Credential     json.RawMessage        `json:"credential"`
	Checkpoint     json.RawMessage        `json:"checkpoint"`
	Now            string                 `json:"now"`
	ReadsToday     int64                  `json:"reads_today"`
	Request        devhost.RelayedRequest `json:"request"`
}

// Receive relays one delivery to the plugin's receive route and judges the
// answer with plugins.CheckReceiveOutput, as the Contract Runner does. The
// plugin's discovery is not checked per delivery (a delivery is answered
// within seconds); a plugin that does not match its pin fails its pull runs.
// Every failure is a typed *connectors.Error, and an answer that echoes the
// credential is refused before anything from it is used.
func (c Connector) Receive(ctx context.Context, r connectors.ReceiveRequest) (connectors.Delivery, error) {
	ref := c.ref(r.InstanceID, r.Config)
	ref.CorpusID, ref.SourceNamespace = r.CorpusID, r.Namespace
	headers := r.Request.Headers
	if headers == nil {
		headers = map[string][]string{}
	}
	request, err := json.Marshal(receiveRequest{InvocationID: invocationID(), Contribution: "connector", OrganizationID: r.Organization,
		Configuration: c.configuration(), Connector: ref, Credential: orNull(r.Credential), Checkpoint: orNull(r.Checkpoint),
		Now: r.Now.UTC().Format(time.RFC3339), ReadsToday: r.ReadsToday,
		Request: devhost.RelayedRequest{Method: r.Request.Method, Query: r.Request.Query, Headers: headers, BodyBase64: base64.StdEncoding.EncodeToString(r.Request.Body)}})
	if err != nil {
		return connectors.Delivery{}, connectors.SourceError(CodePluginInvalidResponse)
	}
	timeout := min(c.timeout(), ReceiveTimeoutCap)
	invoke, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	manifest := &c.Pin.Manifest
	started := time.Now()
	result, err := devhost.InvokeConnectorReceive(invoke, c.Pin.Endpoint, request, plugins.ConnectorMaxResponseBytes(manifest), func(body []byte) []plugins.Issue {
		return plugins.CheckReceiveOutput(invoke, body, manifest)
	})
	observe(c.Pin, r.Organization, OpConnectorReceive, started, result, err)
	if failure := judge(result, err, plugins.CredentialSecrets(r.Credential)); failure != nil {
		return connectors.Delivery{}, failure
	}
	var answer plugins.ConnectorDelivery
	if err := json.Unmarshal(result.Body, &answer); err != nil {
		return connectors.Delivery{}, connectors.SourceError(CodePluginInvalidResponse)
	}
	out := connectors.Delivery{Accepted: answer.Verdict == plugins.VerdictAccepted, Status: answer.Response.Status,
		ContentType: answer.Response.ContentType, Body: answer.Response.Body, Reads: answer.Reads}
	for _, item := range answer.Items {
		mapped, err := mapItem(item)
		if err != nil {
			return connectors.Delivery{}, err
		}
		out.Items = append(out.Items, mapped)
	}
	return out, nil
}
