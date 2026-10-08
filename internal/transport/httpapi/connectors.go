package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
)

// WithConnectors enables the Connector Instance routes.
func WithConnectors(service connectors.Service) Option {
	return func(a *API) { a.Connectors = service }
}

func (a *API) connectorToTransport(in connectors.Instance) transport.Connector {
	out := transport.Connector{ConnectorId: in.ID, CorpusId: in.CorpusID, SourceNamespace: in.Namespace, Kind: transport.ConnectorKind(in.Kind), Enabled: in.Enabled, CreatedAt: in.CreatedAt.UTC(), Config: map[string]any{}}
	if in.WorkQueue != "" {
		q := transport.ConnectorWorkQueue(in.WorkQueue)
		out.WorkQueue = &q
	}
	_ = json.Unmarshal(in.Config, &out.Config)
	if in.PushPolicy != nil {
		out.PushPolicy = &transport.ConnectorPushPolicy{}
		if in.PushPolicy.RatePerSecond != 0 {
			v := in.PushPolicy.RatePerSecond
			out.PushPolicy.RatePerSecond = &v
		}
		if in.PushPolicy.Burst != 0 {
			v := in.PushPolicy.Burst
			out.PushPolicy.Burst = &v
		}
		if in.PushPolicy.AllowedCIDRs != nil {
			v := in.PushPolicy.AllowedCIDRs
			out.PushPolicy.AllowedCidrs = &v
		}
	}
	out.Schedule.IntervalSeconds = int(in.Interval.Seconds())
	out.HealthPolicy.SilentAfterSeconds = int(in.SilentAfter.Seconds())
	out.HealthPolicy.CredentialWarningSeconds = int(in.CredentialWarning.Seconds())
	if in.PausedAt != nil {
		v := in.PausedAt.UTC()
		out.PausedAt = &v
	}
	if in.DisabledAt != nil {
		v := in.DisabledAt.UTC()
		out.DisabledAt = &v
	}
	if c := in.Credential; c != nil {
		out.Credential = &transport.CredentialMetadata{Version: c.Version, DepositedAt: c.DepositedAt.UTC()}
		if c.ExpiresAt != nil {
			v := c.ExpiresAt.UTC()
			out.Credential.ExpiresAt = &v
		}
	}
	h := in.Health
	out.Health = transport.ConnectorHealth{State: transport.ConnectorHealthState(h.State), EvaluatedAt: h.EvaluatedAt.UTC()}
	if h.LastSuccessAt != nil {
		v := h.LastSuccessAt.UTC()
		out.Health.LastSuccessAt = &v
	}
	if h.LastItemAt != nil {
		v := h.LastItemAt.UTC()
		out.Health.LastItemAt = &v
	}
	if h.LastError != nil {
		out.Health.LastError = &transport.ConnectorError{Code: h.LastError.Code, At: h.LastError.At.UTC()}
	}
	if u := h.Usage; u != nil {
		out.Health.Usage = &transport.ConnectorUsage{Day: u.Day.Format(time.DateOnly), ItemsRead: int(u.ItemsRead), PreviousDayItemsRead: int(u.PreviousDayItemsRead)}
	}
	if len(h.Diagnostics) > 0 {
		var d map[string]any
		if json.Unmarshal(h.Diagnostics, &d) == nil && d != nil {
			out.Health.Diagnostics = &d
		}
	}
	if p := h.Push; p != nil {
		push := &transport.ConnectorPush{State: transport.ConnectorPushState(p.State())}
		if e := p.Error(); e != nil {
			push.Error = &transport.ConnectorPushError{Class: transport.ConnectorPushErrorClass(e.Class), Code: e.Code, At: e.At.UTC()}
		}
		if p.LastDeliveryAt != nil {
			v := p.LastDeliveryAt.UTC()
			push.LastDeliveryAt = &v
		}
		if p.Healthy() && p.PollInterval > 0 {
			v := int(p.PollInterval / time.Second)
			push.PollIntervalSeconds = &v
		}
		out.Health.Push = push
	}
	if u := a.Connectors.WebhookURL(in); u != "" {
		out.WebhookUrl = &u
	}
	return out
}

// decodeInto validates raw JSON against a contract schema and decodes it.
func decodeInto(w http.ResponseWriter, r *http.Request, schema interface{ Validate(any) error }, v any) bool {
	return decodeIntoAtMost(w, r, maxRequestBytes, schema, v)
}

// decodeIntoAtMost is decodeInto for a body of at most limit bytes.
func decodeIntoAtMost(w http.ResponseWriter, r *http.Request, limit int64, schema interface{ Validate(any) error }, v any) bool {
	raw, payload, ok := readJSON(w, r, limit)
	if !ok {
		return false
	}
	if err := schema.Validate(raw); err != nil {
		writeError(w, publicerr.WithField(publicerr.InvalidSchema, connectors.SchemaPointer(err)), nil)
		return false
	}
	if json.Unmarshal(payload, v) != nil {
		writeError(w, publicerr.InvalidSchema, nil)
		return false
	}
	return true
}

func rawJSON(v *map[string]any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, _ := json.Marshal(*v)
	return b
}

func (a *API) connectorsAvailable(w http.ResponseWriter) bool {
	if a.Connectors.Store == nil {
		writeError(w, publicerr.NotFound, nil)
		return false
	}
	return true
}

// connectorIDRequired preserves the old family router's 405 for a matched
// connector route whose identifier segment is empty.
func connectorIDRequired(w http.ResponseWriter, connectorID string) bool {
	if connectorID == "" {
		writeError(w, publicerr.MethodNotAllowed, nil)
		return false
	}
	return true
}

func (a *API) handleCreateConnector(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	if !a.connectorsAvailable(w) {
		return
	}
	var body transport.ConnectorCreate
	inst, err := a.Connectors.Create(r.Context(), scope, connectors.CreateInput{}, func() (connectors.CreateInput, error) {
		if !decodeInto(w, r, a.schemas["ConnectorCreate"], &body) {
			return connectors.CreateInput{}, errResponseWritten
		}
		in := connectors.CreateInput{Key: body.IdempotencyKey, CorpusID: body.CorpusId, Namespace: body.SourceNamespace, Kind: string(body.Kind), Config: rawJSON(&body.Config)}
		if body.WorkQueue != nil {
			in.WorkQueue = string(*body.WorkQueue)
		}
		if body.PushPolicy != nil {
			in.PushPolicy = &connectors.PushPolicy{}
			if body.PushPolicy.RatePerSecond != nil {
				in.PushPolicy.RatePerSecond = *body.PushPolicy.RatePerSecond
			}
			if body.PushPolicy.Burst != nil {
				in.PushPolicy.Burst = *body.PushPolicy.Burst
			}
			if body.PushPolicy.AllowedCidrs != nil {
				in.PushPolicy.AllowedCIDRs = *body.PushPolicy.AllowedCidrs
			}
		}
		if body.Schedule != nil {
			in.IntervalSeconds = body.Schedule.IntervalSeconds
		}
		if body.HealthPolicy != nil {
			in.SilentAfterSeconds, in.CredentialWarningSeconds = body.HealthPolicy.SilentAfterSeconds, body.HealthPolicy.CredentialWarningSeconds
		}
		if body.Credential != nil {
			in.Secret, in.ExpiresAt = rawJSON(body.Credential.Secret), body.Credential.ExpiresAt
		}

		return in, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.ConnectorsUnavailable)
		return
	}
	send(w, 201, a.connectorToTransport(inst))
}

func (a *API) handleGetConnector(w http.ResponseWriter, r *http.Request, scope corpus.Scope, connectorID string) {
	if !a.connectorsAvailable(w) {
		return
	}
	if !connectorIDRequired(w, connectorID) {
		return
	}
	inst, err := a.Connectors.Read(r.Context(), scope, connectorID)
	if err != nil {
		writeError(w, err, publicerr.ConnectorsUnavailable)
		return
	}
	send(w, 200, a.connectorToTransport(inst))
}

func (a *API) handleDisableConnector(w http.ResponseWriter, r *http.Request, scope corpus.Scope, connectorID string) {
	if !a.connectorsAvailable(w) {
		return
	}
	if !connectorIDRequired(w, connectorID) {
		return
	}
	var body transport.ActionRequest
	inst, err := a.Connectors.Disable(r.Context(), scope, "", func() (string, error) {
		if !decodeInto(w, r, a.schemas["ActionRequest"], &body) {
			return "", errResponseWritten
		}
		return connectorID, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.ConnectorsUnavailable)
		return
	}
	send(w, 200, a.connectorToTransport(inst))
}

func (a *API) handleConnectorPause(w http.ResponseWriter, r *http.Request, scope corpus.Scope, connectorID string, paused bool) {
	if !a.connectorsAvailable(w) || !connectorIDRequired(w, connectorID) {
		return
	}
	var body transport.ActionRequest
	command := a.Connectors.Resume
	if paused {
		command = a.Connectors.Pause
	}
	inst, err := command(r.Context(), scope, "", func() (string, error) {
		if !decodeInto(w, r, a.schemas["ActionRequest"], &body) {
			return "", errResponseWritten
		}
		return connectorID, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.ConnectorsUnavailable)
		return
	}
	send(w, 200, a.connectorToTransport(inst))
}

func (a *API) handleRequestConnectorRun(w http.ResponseWriter, r *http.Request, scope corpus.Scope, connectorID string) {
	if !a.connectorsAvailable(w) {
		return
	}
	if !connectorIDRequired(w, connectorID) {
		return
	}
	var body transport.ActionRequest
	req, err := a.Connectors.RequestRun(r.Context(), scope, "", "", func() (string, string, error) {
		if !decodeInto(w, r, a.schemas["ActionRequest"], &body) {
			return "", "", errResponseWritten
		}
		return connectorID, body.IdempotencyKey, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.ConnectorsUnavailable)
		return
	}
	send(w, 202, transport.ConnectorRunRequest{ConnectorId: req.ConnectorID, RunAt: req.RunAt.UTC()})
}

func (a *API) handleChangeConnectorSchedule(w http.ResponseWriter, r *http.Request, scope corpus.Scope, connectorID string) {
	if !a.connectorsAvailable(w) {
		return
	}
	if !connectorIDRequired(w, connectorID) {
		return
	}
	var body transport.ScheduleChange
	inst, err := a.Connectors.ChangeSchedule(r.Context(), scope, "", 0, func() (string, int, error) {
		if !decodeInto(w, r, a.schemas["ScheduleChange"], &body) {
			return "", 0, errResponseWritten
		}
		return connectorID, body.IntervalSeconds, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.ConnectorsUnavailable)
		return
	}
	send(w, 200, a.connectorToTransport(inst))
}

func (a *API) handleReplaceConnectorCredential(w http.ResponseWriter, r *http.Request, scope corpus.Scope, connectorID string) {
	if !a.connectorsAvailable(w) {
		return
	}
	if !connectorIDRequired(w, connectorID) {
		return
	}
	var body transport.CredentialReplace
	inst, err := a.Connectors.ReplaceCredential(r.Context(), scope, "", connectors.CredentialInput{}, func() (string, connectors.CredentialInput, error) {
		if !decodeInto(w, r, a.schemas["CredentialReplace"], &body) {
			return "", connectors.CredentialInput{}, errResponseWritten
		}
		return connectorID, connectors.CredentialInput{Key: body.IdempotencyKey, Secret: rawJSON(body.Secret), ExpiresAt: body.ExpiresAt}, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.ConnectorsUnavailable)
		return
	}
	send(w, 200, a.connectorToTransport(inst))
}

func (a *API) connectorKinds(w http.ResponseWriter, r *http.Request, s corpus.Scope) {
	if a.Connectors.Store == nil {
		writeError(w, publicerr.NotFound, nil)
		return
	}
	if r.Method != "GET" {
		writeError(w, publicerr.MethodNotAllowed, nil)
		return
	}
	if len(r.URL.Query()) > 0 {
		writeError(w, publicerr.InvalidQuery, nil)
		return
	}
	catalog, err := a.Connectors.Kinds(s)
	if err != nil {
		writeError(w, err, publicerr.ConnectorsUnavailable)
		return
	}
	out := transport.ConnectorKindCatalog{CredentialDeposits: "unavailable", MinIntervalSeconds: int(catalog.MinInterval / time.Second), Items: []transport.ConnectorKindDescription{}}
	if catalog.CredentialDeposits {
		out.CredentialDeposits = "available"
	}
	for _, k := range catalog.Kinds {
		d := transport.ConnectorKindDescription{Kind: k.Kind, Title: k.Title, Credential: transport.ConnectorKindDescriptionCredential(k.Credential), DefaultIntervalSeconds: int(k.DefaultInterval / time.Second)}
		if k.Description != "" {
			d.Description = &k.Description
		}
		if json.Unmarshal(k.ConfigSchema, &d.ConfigSchema) != nil {
			writeError(w, publicerr.ConnectorsUnavailable, nil)
			return
		}
		if k.CredentialSchema != nil {
			var schema map[string]any
			if json.Unmarshal(k.CredentialSchema, &schema) != nil {
				writeError(w, publicerr.ConnectorsUnavailable, nil)
				return
			}
			d.CredentialSchema = &schema
		}
		out.Items = append(out.Items, d)
	}
	send(w, 200, out)
}

func (a *API) listConnectors(w http.ResponseWriter, r *http.Request, s corpus.Scope) {
	if !a.connectorsAvailable(w) {
		return
	}
	var limit int
	var ok bool
	var binding string
	items, err := a.Connectors.List(r.Context(), s, "", "", 0, func() (string, string, int, error) {
		q := r.URL.Query()
		for k, v := range q {
			// An empty limit or page cursor gets its own code below, as on every list.
			if (k != "limit" && k != "page_cursor" && k != "corpus_id") || len(v) != 1 || (k == "corpus_id" && v[0] == "") {
				writeError(w, publicerr.InvalidQuery, nil)
				return "", "", 0, errResponseWritten
			}
		}
		limit, ok = pageLimit(w, q, 100, 100)
		if !ok {
			return "", "", 0, errResponseWritten
		}
		corpusID := q.Get("corpus_id")
		binding = "connectors|" + scopeDigest(s) + "|" + corpusID
		after := ""
		if q.Has("page_cursor") {
			var c cursor
			if a.decodePage(connectorPageDomain, q.Get("page_cursor"), &c) != nil || c.Scope != binding {
				writeError(w, publicerr.InvalidCursor, nil)
				return "", "", 0, errResponseWritten
			}
			after = c.After
		}

		return corpusID, after, limit + 1, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.ConnectorsUnavailable)
		return
	}
	page := transport.ConnectorPage{Items: []transport.Connector{}}
	for i, in := range items {
		if i == limit {
			next := a.encodePage(connectorPageDomain, cursor{items[limit-1].ID, binding})
			page.NextPageCursor = &next
			break
		}
		page.Items = append(page.Items, a.connectorToTransport(in))
	}
	send(w, 200, page)
}
