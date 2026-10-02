package plugins

import "encoding/json"

// ConnectorReceiveRef identifies the source receiving a delivery.
type ConnectorReceiveRef struct {
	InstanceID      string          `json:"instance_id"`
	Kind            string          `json:"kind"`
	CorpusID        string          `json:"corpus_id,omitempty"`
	SourceNamespace string          `json:"source_namespace,omitempty"`
	Config          json.RawMessage `json:"config"`
}

// ConnectorRelayedRequest preserves the caller's request after header filtering.
type ConnectorRelayedRequest struct {
	Path       string              `json:"path,omitempty"`
	Method     string              `json:"method"`
	Query      string              `json:"query"`
	Headers    map[string][]string `json:"headers"`
	BodyBase64 string              `json:"body_base64"`
}

// ConnectorReceiveRequest is the shared receive wire contract used by the
// engine and Contract Runner. Route and Body are present for declared API routes.
type ConnectorReceiveRequest struct {
	InvocationID   string                  `json:"invocation_id"`
	Contribution   string                  `json:"contribution"`
	OrganizationID string                  `json:"organization_id"`
	Configuration  json.RawMessage         `json:"configuration"`
	Connector      ConnectorReceiveRef     `json:"connector"`
	Credential     json.RawMessage         `json:"credential"`
	Checkpoint     json.RawMessage         `json:"checkpoint"`
	Now            string                  `json:"now"`
	ReadsToday     int64                   `json:"reads_today"`
	Request        ConnectorRelayedRequest `json:"request"`
	Route          string                  `json:"route,omitempty"`
	Body           json.RawMessage         `json:"body,omitempty"`
}

// BuildConnectorReceiveRequest applies receive defaults and serializes the
// same request shape for production deliveries and fixture replays.
func BuildConnectorReceiveRequest(r ConnectorReceiveRequest) ([]byte, error) {
	r.Contribution = "connector"
	r.Configuration = defaultConfiguration(r.Configuration)
	if len(r.Credential) == 0 {
		r.Credential = json.RawMessage("null")
	}
	if len(r.Checkpoint) == 0 {
		r.Checkpoint = json.RawMessage("null")
	}
	if r.Request.Headers == nil {
		r.Request.Headers = map[string][]string{}
	}
	if r.Route != "" && len(r.Body) == 0 {
		r.Body = json.RawMessage("null")
	}
	return json.Marshal(r)
}
