package httpapi

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

// WithConnectors enables the Connector Instance routes.
func WithConnectors(service connectors.Service) Option {
	return func(a *API) { a.Connectors = service }
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

func (a *API) connectorRoutes(w http.ResponseWriter, r *http.Request, scope corpus.Scope) bool {
	if r.URL.Path == "/v0/connector-kinds" {
		a.connectorKinds(w, r, scope)
		return true
	}
	if r.URL.Path != "/v0/connectors" && !strings.HasPrefix(r.URL.Path, "/v0/connectors/") {
		return false
	}
	if a.Connectors.Store == nil {
		writeError(w, publicerr.NotFound, nil)
		return true
	}
	path := strings.Split(strings.TrimPrefix(r.URL.Path, "/v0/connectors"), "/")
	switch {
	case len(path) == 1 && r.Method == "POST", len(path) == 1 && r.Method == "GET":
	case len(path) == 2 && path[1] != "" && r.Method == "GET":
	case len(path) == 3 && path[1] != "" && path[2] == "disable" && r.Method == "POST":
	case len(path) == 3 && path[1] != "" && path[2] == "credential" && r.Method == "PUT":
	case len(path) == 3 && path[1] != "" && path[2] == "schedule" && r.Method == "PUT":
	case len(path) == 3 && path[1] != "" && path[2] == "runs" && r.Method == "POST":
	case len(path) <= 3:
		writeError(w, publicerr.MethodNotAllowed, nil)
		return true
	default:
		writeError(w, publicerr.NotFound, nil)
		return true
	}
	ctx := r.Context()
	switch {
	case len(path) == 1 && r.Method == "POST":
		var body transport.ConnectorCreate
		inst, err := a.Connectors.Create(ctx, scope, connectors.CreateInput{}, func() (connectors.CreateInput, error) {
			if !decodeInto(w, r, a.connectorSchema, &body) {
				return connectors.CreateInput{}, errResponseWritten
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

			return in, nil
		})
		if errors.Is(err, errResponseWritten) {
			return true
		}
		if err != nil {
			writeError(w, err, publicerr.ConnectorsUnavailable)
			return true
		}
		send(w, 201, a.connectorToTransport(inst))
	case len(path) == 1:
		a.listConnectors(w, r, scope)
	case len(path) == 2:
		inst, err := a.Connectors.Read(ctx, scope, path[1])
		if err != nil {
			writeError(w, err, publicerr.ConnectorsUnavailable)
			return true
		}
		send(w, 200, a.connectorToTransport(inst))
	case path[2] == "disable":
		var body transport.ActionRequest
		inst, err := a.Connectors.Disable(ctx, scope, "", func() (string, error) {
			if !decodeInto(w, r, a.actionSchema, &body) {
				return "", errResponseWritten
			}

			return path[1], nil
		})
		if errors.Is(err, errResponseWritten) {
			return true
		}
		if err != nil {
			writeError(w, err, publicerr.ConnectorsUnavailable)
			return true
		}
		send(w, 200, a.connectorToTransport(inst))
	case path[2] == "runs":
		var body transport.ActionRequest
		req, err := a.Connectors.RequestRun(ctx, scope, "", "", func() (string, string, error) {
			if !decodeInto(w, r, a.actionSchema, &body) {
				return "", "", errResponseWritten
			}

			return path[1], body.IdempotencyKey, nil
		})
		if errors.Is(err, errResponseWritten) {
			return true
		}
		if err != nil {
			writeError(w, err, publicerr.ConnectorsUnavailable)
			return true
		}
		send(w, 202, transport.ConnectorRunRequest{ConnectorId: req.ConnectorID, RunAt: req.RunAt.UTC()})
	case path[2] == "schedule":
		var body transport.ScheduleChange
		inst, err := a.Connectors.ChangeSchedule(ctx, scope, "", 0, func() (string, int, error) {
			if !decodeInto(w, r, a.scheduleSchema, &body) {
				return "", 0, errResponseWritten
			}

			return path[1], body.IntervalSeconds, nil
		})
		if errors.Is(err, errResponseWritten) {
			return true
		}
		if err != nil {
			writeError(w, err, publicerr.ConnectorsUnavailable)
			return true
		}
		send(w, 200, a.connectorToTransport(inst))
	default:
		var body transport.CredentialReplace
		inst, err := a.Connectors.ReplaceCredential(ctx, scope, "", connectors.CredentialInput{}, func() (string, connectors.CredentialInput, error) {
			if !decodeInto(w, r, a.credentialSchema, &body) {
				return "", connectors.CredentialInput{}, errResponseWritten
			}

			return path[1], connectors.CredentialInput{Key: body.IdempotencyKey, Secret: rawJSON(body.Secret), ExpiresAt: body.ExpiresAt}, nil
		})
		if errors.Is(err, errResponseWritten) {
			return true
		}
		if err != nil {
			writeError(w, err, publicerr.ConnectorsUnavailable)
			return true
		}
		send(w, 200, a.connectorToTransport(inst))
	}
	return true
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
			parts := strings.Split(q.Get("page_cursor"), ".")
			if len(parts) != 2 {
				writeError(w, publicerr.InvalidCursor, nil)
				return "", "", 0, errResponseWritten
			}
			b, e1 := base64.RawURLEncoding.DecodeString(parts[0])
			sig, e2 := base64.RawURLEncoding.DecodeString(parts[1])
			var c cursor
			if e1 != nil || e2 != nil || !hmac.Equal(sig, a.signCursor(connectorPageDomain, b)) || json.Unmarshal(b, &c) != nil || c.Scope != binding {
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
			b, _ := json.Marshal(cursor{items[limit-1].ID, binding})
			next := base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(a.signCursor(connectorPageDomain, b))
			page.NextPageCursor = &next
			break
		}
		page.Items = append(page.Items, a.connectorToTransport(in))
	}
	send(w, 200, page)
}
