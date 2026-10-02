package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"

	"crypto/hmac"
)

// WithConnectors enables the Connector Instance routes.
func WithConnectors(service connectors.Service) Option {
	return func(a *API) { a.Connectors = service }
}

func connectorFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, corpus.ErrForbidden):
		failure(w, 403, "forbidden")
	case errors.Is(err, corpus.ErrNotFound):
		failure(w, 404, "not_found")
	case errors.Is(err, connectors.ErrConflict), errors.Is(err, connectors.ErrNamespaceInUse), errors.Is(err, connectors.ErrDisabled), errors.Is(err, connectors.ErrTokenInactive):
		failure(w, 409, publicCode(err, "idempotency_conflict"))
	case errors.Is(err, connectors.ErrCredentialsUnavailable):
		// The deployment has no credential_key: retrying cannot succeed until
		// an operator configures one, so the refusal is not retryable.
		e := apiError(503, "credentials_unavailable")
		e.Retryable = false
		send(w, 503, e)
	case errors.Is(err, connectors.ErrUnsupportedKind), errors.Is(err, connectors.ErrInvalidConfig), errors.Is(err, connectors.ErrInvalidCredential), errors.Is(err, connectors.ErrInvalidInterval), errors.Is(err, connectors.ErrInvalid):
		invalid(w, publicCode(err, "invalid_input"), connectors.Field(err))
	default:
		failure(w, 503, "connectors_unavailable")
	}
}

func (a *API) connectorToTransport(in connectors.Instance) transport.Connector {
	out := transport.Connector{ConnectorId: in.ID, CorpusId: in.CorpusID, SourceNamespace: in.Namespace, Kind: transport.ConnectorKind(in.Kind), Enabled: in.Enabled, CreatedAt: in.CreatedAt.UTC(), Config: map[string]any{}}
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
		invalid(w, "invalid_schema", connectors.SchemaPointer(err))
		return false
	}
	if json.Unmarshal(payload, v) != nil {
		failure(w, 422, "invalid_schema")
		return false
	}
	return true
}

// invalid writes a 422 locating the offending request member when known.
func invalid(w http.ResponseWriter, code, field string) {
	e := apiError(422, code)
	if field != "" {
		e.Field = &field
	}
	send(w, 422, e)
}

func rawJSON(v *map[string]any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, _ := json.Marshal(*v)
	return b
}

func (a *API) connectorRoutes(w http.ResponseWriter, r *http.Request, scope corpus.Scope) bool {
	if r.URL.Path == "/v0/connector-kinds" {
		a.connectorKinds(w, r, scope)
		return true
	}
	if r.URL.Path != "/v0/connectors" && !strings.HasPrefix(r.URL.Path, "/v0/connectors/") {
		return false
	}
	if a.Connectors.Store == nil {
		failure(w, 404, "not_found")
		return true
	}
	path := strings.Split(strings.TrimPrefix(r.URL.Path, "/v0/connectors"), "/")
	write := r.Method != "GET"
	action := "connectors:read"
	if write {
		action = "connectors:write"
	}
	switch {
	case len(path) == 1 && r.Method == "POST", len(path) == 1 && r.Method == "GET":
	case len(path) == 2 && path[1] != "" && r.Method == "GET":
	case len(path) == 3 && path[1] != "" && path[2] == "disable" && r.Method == "POST":
	case len(path) == 3 && path[1] != "" && path[2] == "credential" && r.Method == "PUT":
	case len(path) == 3 && path[1] != "" && path[2] == "schedule" && r.Method == "PUT":
	case len(path) == 3 && path[1] != "" && path[2] == "runs" && r.Method == "POST":
	case len(path) <= 3:
		failure(w, 405, "method_not_allowed")
		return true
	default:
		failure(w, 404, "not_found")
		return true
	}
	if !scope.Allows(action) {
		failure(w, 403, "forbidden")
		return true
	}
	ctx := r.Context()
	switch {
	case len(path) == 1 && r.Method == "POST":
		var body transport.ConnectorCreate
		if !decodeInto(w, r, a.connectorSchema, &body) {
			return true
		}
		in := connectors.CreateInput{Key: body.IdempotencyKey, CorpusID: body.CorpusId, Namespace: body.SourceNamespace, Kind: string(body.Kind), Config: rawJSON(&body.Config)}
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
		inst, err := a.Connectors.Create(ctx, scope, in)
		if err != nil {
			connectorFailure(w, err)
			return true
		}
		send(w, 201, a.connectorToTransport(inst))
	case len(path) == 1:
		a.listConnectors(w, r, scope)
	case len(path) == 2:
		inst, err := a.Connectors.Read(ctx, scope, path[1])
		if err != nil {
			connectorFailure(w, err)
			return true
		}
		send(w, 200, a.connectorToTransport(inst))
	case path[2] == "disable":
		var body transport.ActionRequest
		if !decodeInto(w, r, a.actionSchema, &body) {
			return true
		}
		inst, err := a.Connectors.Disable(ctx, scope, path[1])
		if err != nil {
			connectorFailure(w, err)
			return true
		}
		send(w, 200, a.connectorToTransport(inst))
	case path[2] == "runs":
		var body transport.ActionRequest
		if !decodeInto(w, r, a.actionSchema, &body) {
			return true
		}
		req, err := a.Connectors.RequestRun(ctx, scope, path[1], body.IdempotencyKey)
		if err != nil {
			connectorFailure(w, err)
			return true
		}
		send(w, 202, transport.ConnectorRunRequest{ConnectorId: req.ConnectorID, RunAt: req.RunAt.UTC()})
	case path[2] == "schedule":
		var body transport.ScheduleChange
		if !decodeInto(w, r, a.scheduleSchema, &body) {
			return true
		}
		inst, err := a.Connectors.ChangeSchedule(ctx, scope, path[1], body.IntervalSeconds)
		if err != nil {
			connectorFailure(w, err)
			return true
		}
		send(w, 200, a.connectorToTransport(inst))
	default:
		var body transport.CredentialReplace
		if !decodeInto(w, r, a.credentialSchema, &body) {
			return true
		}
		inst, err := a.Connectors.ReplaceCredential(ctx, scope, path[1], connectors.CredentialInput{Key: body.IdempotencyKey, Secret: rawJSON(body.Secret), ExpiresAt: body.ExpiresAt})
		if err != nil {
			connectorFailure(w, err)
			return true
		}
		send(w, 200, a.connectorToTransport(inst))
	}
	return true
}

func (a *API) connectorKinds(w http.ResponseWriter, r *http.Request, s corpus.Scope) {
	if a.Connectors.Store == nil {
		failure(w, 404, "not_found")
		return
	}
	if r.Method != "GET" {
		failure(w, 405, "method_not_allowed")
		return
	}
	if len(r.URL.Query()) > 0 {
		failure(w, 422, "invalid_query")
		return
	}
	catalog, err := a.Connectors.Kinds(s)
	if err != nil {
		connectorFailure(w, err)
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
			failure(w, 503, "connectors_unavailable")
			return
		}
		if k.CredentialSchema != nil {
			var schema map[string]any
			if json.Unmarshal(k.CredentialSchema, &schema) != nil {
				failure(w, 503, "connectors_unavailable")
				return
			}
			d.CredentialSchema = &schema
		}
		out.Items = append(out.Items, d)
	}
	send(w, 200, out)
}

func (a *API) listConnectors(w http.ResponseWriter, r *http.Request, s corpus.Scope) {
	q := r.URL.Query()
	for k, v := range q {
		// An empty limit or page cursor gets its own code below, as on every list.
		if (k != "limit" && k != "page_cursor" && k != "corpus_id") || len(v) != 1 || (k == "corpus_id" && v[0] == "") {
			failure(w, 422, "invalid_query")
			return
		}
	}
	limit, ok := pageLimit(w, q, 100, 100)
	if !ok {
		return
	}
	corpusID := q.Get("corpus_id")
	binding := "connectors|" + scopeDigest(s) + "|" + corpusID
	after := ""
	if q.Has("page_cursor") {
		parts := strings.Split(q.Get("page_cursor"), ".")
		if len(parts) != 2 {
			failure(w, 422, "invalid_cursor")
			return
		}
		b, e1 := base64.RawURLEncoding.DecodeString(parts[0])
		sig, e2 := base64.RawURLEncoding.DecodeString(parts[1])
		var c cursor
		if e1 != nil || e2 != nil || !hmac.Equal(sig, a.signCursor(connectorPageDomain, b)) || json.Unmarshal(b, &c) != nil || c.Scope != binding {
			failure(w, 422, "invalid_cursor")
			return
		}
		after = c.After
	}
	items, err := a.Connectors.List(r.Context(), s, corpusID, after, limit+1)
	if err != nil {
		connectorFailure(w, err)
		return
	}
	page := transport.ConnectorPage{Items: []transport.Connector{}}
	for i, in := range items {
		if i == limit {
			b, _ := json.Marshal(cursor{items[limit-1].ID, binding})
			next := base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(a.signCursor(connectorPageDomain, b))
			page.NextPageCursor = &next
			break
		}
		page.Items = append(page.Items, a.connectorToTransport(in))
	}
	send(w, 200, page)
}
