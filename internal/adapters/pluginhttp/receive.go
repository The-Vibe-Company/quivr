package pluginhttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/call"
)

// ReceiveTimeoutCap bounds one receive invocation: the source waits for the
// answer, and sources give up after a few seconds.
const ReceiveTimeoutCap = plugins.ReceiveTimeoutCap

var _ connectors.Receiver = Connector{}

// Receive relays one delivery to the plugin's receive route and judges the
// answer with plugins.CheckReceiveOutput, as the Contract Runner does. The
// plugin's discovery is checked within the delivery deadline.
// Every failure is a typed *connectors.Error, and an answer that echoes the
// credential is refused before anything from it is used.
func (c Connector) Receive(ctx context.Context, r connectors.ReceiveRequest) (connectors.Delivery, error) {
	request, err := plugins.BuildConnectorReceiveRequest(plugins.ConnectorReceiveRequest{
		InvocationID: plugins.InvocationID(), OrganizationID: r.Organization,
		Configuration: c.configuration(), Connector: plugins.ConnectorReceiveRef{
			InstanceID: r.InstanceID, Kind: c.Name, Config: r.Config, CorpusID: r.CorpusID, SourceNamespace: r.Namespace},
		Credential: r.Credential, Checkpoint: r.Checkpoint,
		Now: r.Now.UTC().Format(time.RFC3339), ReadsToday: r.ReadsToday, Route: r.Route, Body: r.Body,
		Request: plugins.ConnectorRelayedRequest{Path: r.Request.Path, Method: r.Request.Method, Query: r.Request.Query,
			Headers: r.Request.Headers, BodyBase64: base64.StdEncoding.EncodeToString(r.Request.Body)}})
	if err != nil {
		return connectors.Delivery{}, connectors.SourceError(CodePluginInvalidResponse)
	}
	started := time.Now()
	result, err := call.Invoke(ctx, c.Pin, call.ConnectorReceive, call.Bytes(request), func(ctx context.Context, body []byte) []plugins.Issue {
		return plugins.CheckReceiveOutput(ctx, body, &c.Pin.Manifest)
	}, plugins.CredentialSecrets(r.Credential))
	observe(c.Pin, r.Organization, OpConnectorReceive, started, result, err)
	if err != nil {
		return connectors.Delivery{}, err
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
