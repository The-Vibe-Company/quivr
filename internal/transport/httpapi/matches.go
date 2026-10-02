package httpapi

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

// matchPage is the signed payload of a Match history page cursor. It binds
// the Subscription filter and authorization scope in its own signature domain.
type matchPage struct {
	Version      int    `json:"v"`
	Subscription string `json:"sub"`
	Scope        string `json:"s"`
	After        int64  `json:"a"`
}

func (a *API) encodeMatchPage(p matchPage) string {
	b, _ := json.Marshal(p)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(a.signCursor(matchPageDomain, b))
}

func (a *API) decodeMatchPage(token, subscriptionID string, s corpus.Scope) (int64, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return 0, errors.New("invalid_cursor")
	}
	b, e1 := base64.RawURLEncoding.DecodeString(parts[0])
	sig, e2 := base64.RawURLEncoding.DecodeString(parts[1])
	var p matchPage
	if e1 != nil || e2 != nil || !hmac.Equal(sig, a.signCursor(matchPageDomain, b)) || json.Unmarshal(b, &p) != nil || p.Version != 1 || p.After < 0 {
		return 0, errors.New("invalid_cursor")
	}
	if p.Subscription != subscriptionID || p.Scope != scopeDigest(s) {
		return 0, errPageScope
	}
	return p.After, nil
}

// matchRoutes serves /v0/matches, /v0/deliveries/{id} and its attempts.
func (a *API) matchRoutes(w http.ResponseWriter, r *http.Request, scope corpus.Scope) bool {
	path := r.URL.Path
	if path != "/v0/matches" && !strings.HasPrefix(path, "/v0/matches/") && !strings.HasPrefix(path, "/v0/deliveries/") {
		return false
	}
	var resource, id string
	switch {
	case path == "/v0/matches":
		resource = "matches"
	case strings.HasPrefix(path, "/v0/matches/"):
		resource, id = "match", strings.TrimPrefix(path, "/v0/matches/")
	default:
		resource, id = "delivery", strings.TrimPrefix(path, "/v0/deliveries/")
		if d, ok := strings.CutSuffix(id, "/attempts"); ok {
			resource, id = "attempts", d
		}
	}
	if a.Monitoring.MatchStore == nil || (resource != "matches" && (id == "" || strings.Contains(id, "/"))) {
		writeError(w, publicerr.NotFound, nil)
		return true
	}
	if r.Method != "GET" {
		writeError(w, publicerr.MethodNotAllowed, nil)
		return true
	}
	ctx := r.Context()
	switch resource {
	case "match":
		m, err := a.Monitoring.Match(ctx, scope, id)
		respondMonitoring(w, 200, matchToTransport(m), err)
	case "delivery":
		d, err := a.Monitoring.Delivery(ctx, scope, id)
		if err != nil {
			writeError(w, err, publicerr.StorageUnavailable)
			return true
		}
		out, err := deliveryToTransport(d)
		if err != nil {
			writeError(w, publicerr.StorageUnavailable, nil)
			return true
		}
		send(w, 200, out)
	case "attempts":
		a.listAttempts(w, r, scope, id)
	default:
		a.listMatches(w, r, scope)
	}
	return true
}

func (a *API) listMatches(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	var limit int
	var ok bool
	var subscriptionID string
	var after int64
	matches, err := a.Monitoring.Matches(r.Context(), scope, "", 0, 0, func() (string, int64, int, error) {
		q := r.URL.Query()
		for k, v := range q {
			if (k != "subscription_id" && k != "page_cursor" && k != "limit") || len(v) != 1 {
				writeError(w, publicerr.InvalidQuery, nil)
				return "", 0, 0, errResponseWritten
			}
		}
		subscriptionID = q.Get("subscription_id")
		if subscriptionID == "" {
			writeError(w, publicerr.InvalidQuery, nil)
			return "", 0, 0, errResponseWritten
		}
		if q.Has("page_cursor") && q.Get("page_cursor") == "" {
			writeError(w, publicerr.InvalidCursor, nil)
			return "", 0, 0, errResponseWritten
		}
		limit, ok = pageLimit(w, q, 100, 100)
		if !ok {
			return "", 0, 0, errResponseWritten
		}
		if q.Has("page_cursor") {
			var err error
			if after, err = a.decodeMatchPage(q.Get("page_cursor"), subscriptionID, scope); errors.Is(err, errPageScope) {
				writeError(w, publicerr.CursorScopeChanged, nil)
				return "", 0, 0, errResponseWritten
			} else if err != nil {
				writeError(w, publicerr.InvalidCursor, nil)
				return "", 0, 0, errResponseWritten
			}
		}

		return subscriptionID, after, limit + 1, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
		return
	}
	page := transport.MatchPage{Items: make([]transport.Match, 0, min(len(matches), limit))}
	for i, m := range matches {
		if i == limit {
			next := a.encodeMatchPage(matchPage{Version: 1, Subscription: subscriptionID, Scope: scopeDigest(scope), After: matches[limit-1].Position})
			page.NextPageCursor = &next
			break
		}
		page.Items = append(page.Items, matchToTransport(m))
	}
	send(w, 200, page)
}

func matchToTransport(m monitoring.Match) transport.Match {
	out := transport.Match{MatchId: m.ID, SubscriptionId: m.SubscriptionID, SubscriptionVersionId: m.SubscriptionVersionID, SavedQueryId: m.SavedQueryID, SavedQueryVersionId: m.SavedQueryVersionID,
		RecordId: m.RecordID, RecordVersionId: m.RecordVersionID, Evidence: evidenceToTransport(m.Evidence)}
	if m.PreviousMatchID != "" {
		out.PreviousMatchId = &m.PreviousMatchID
	}
	out.Owner = owner(m.Owner)
	return out
}

func evidenceToTransport(e monitoring.MatchEvidence) transport.MatchEvidence {
	out := transport.MatchEvidence{Evaluator: transport.EvaluatorConfig{PluginId: e.Evaluator.PluginID, Version: e.Evaluator.Version, Configuration: e.Evaluator.Configuration}, Explanation: e.Explanation}
	if len(e.PartKeys) > 0 {
		keys := e.PartKeys
		out.PartKeys = &keys
	}
	if e.Details != nil {
		details := e.Details
		out.Details = &details
	}
	return out
}

func deliveryToTransport(d monitoring.Delivery) (transport.Delivery, error) {
	out := transport.Delivery{DeliveryId: d.ID, MatchId: d.MatchID, DestinationId: d.DestinationID, State: transport.DeliveryState(d.State), AttemptCount: d.AttemptCount, Admission: transport.DeliveryAdmission{Allowed: d.Admission.Allowed}}
	if d.Admission.Reason != "" {
		reason := transport.DeliveryAdmissionReason(d.Admission.Reason)
		out.Admission.Reason = &reason
	}
	if d.LastErrorCode != "" {
		// Nothing retries an exhausted Delivery automatically.
		out.LastError = &transport.Error{Code: d.LastErrorCode, Message: d.LastErrorMessage, Retryable: d.LastOutcome != monitoring.AttemptPermanentError && d.State != "exhausted"}
	}
	if d.NextAttemptAt != nil {
		next := d.NextAttemptAt.UTC()
		out.NextAttemptAt = &next
	}
	return out, json.Unmarshal(d.Event, &out.Event)
}

// attemptPage is the signed payload of a Delivery attempt page cursor, bound
// to the Delivery and authorization scope in its own signature domain.
type attemptPage struct {
	Version  int    `json:"v"`
	Delivery string `json:"d"`
	Scope    string `json:"s"`
	After    int    `json:"a"`
}

func (a *API) encodeAttemptPage(p attemptPage) string {
	b, _ := json.Marshal(p)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(a.signCursor(attemptPageDomain, b))
}

func (a *API) decodeAttemptPage(token, deliveryID string, s corpus.Scope) (int, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return 0, errors.New("invalid_cursor")
	}
	b, e1 := base64.RawURLEncoding.DecodeString(parts[0])
	sig, e2 := base64.RawURLEncoding.DecodeString(parts[1])
	var p attemptPage
	if e1 != nil || e2 != nil || !hmac.Equal(sig, a.signCursor(attemptPageDomain, b)) || json.Unmarshal(b, &p) != nil || p.Version != 1 || p.After < 0 {
		return 0, errors.New("invalid_cursor")
	}
	if p.Delivery != deliveryID || p.Scope != scopeDigest(s) {
		return 0, errPageScope
	}
	return p.After, nil
}

// listAttempts pages a Delivery's append-only attempt history. It exposes
// bounded outcome, status and error only: no signature, secret or receiver body.
func (a *API) listAttempts(w http.ResponseWriter, r *http.Request, scope corpus.Scope, deliveryID string) {
	var limit int
	var ok bool
	attempts, err := a.Monitoring.Attempts(r.Context(), scope, "", 0, 0, func() (string, int, int, error) {
		q := r.URL.Query()
		for k, v := range q {
			if (k != "page_cursor" && k != "limit") || len(v) != 1 {
				writeError(w, publicerr.InvalidQuery, nil)
				return "", 0, 0, errResponseWritten
			}
		}
		if q.Has("page_cursor") && q.Get("page_cursor") == "" {
			writeError(w, publicerr.InvalidCursor, nil)
			return "", 0, 0, errResponseWritten
		}
		limit, ok = pageLimit(w, q, 100, 100)
		if !ok {
			return "", 0, 0, errResponseWritten
		}
		after := 0
		if q.Has("page_cursor") {
			var err error
			if after, err = a.decodeAttemptPage(q.Get("page_cursor"), deliveryID, scope); errors.Is(err, errPageScope) {
				writeError(w, publicerr.CursorScopeChanged, nil)
				return "", 0, 0, errResponseWritten
			} else if err != nil {
				writeError(w, publicerr.InvalidCursor, nil)
				return "", 0, 0, errResponseWritten
			}
		}

		return deliveryID, after, limit + 1, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
		return
	}
	page := transport.DeliveryAttemptPage{Items: make([]transport.DeliveryAttempt, 0, min(len(attempts), limit))}
	for i, at := range attempts {
		if i == limit {
			next := a.encodeAttemptPage(attemptPage{Version: 1, Delivery: deliveryID, Scope: scopeDigest(scope), After: attempts[limit-1].Number})
			page.NextPageCursor = &next
			break
		}
		page.Items = append(page.Items, attemptToTransport(at))
	}
	send(w, 200, page)
}

func attemptToTransport(at monitoring.Attempt) transport.DeliveryAttempt {
	out := transport.DeliveryAttempt{AttemptId: at.ID, DeliveryId: at.DeliveryID, Number: at.Number, Outcome: transport.DeliveryAttemptOutcome(at.Outcome)}
	if at.HTTPStatus != 0 {
		status := at.HTTPStatus
		out.HttpStatus = &status
	}
	if at.ErrorCode != "" {
		out.Error = &transport.Error{Code: at.ErrorCode, Message: at.ErrorMessage, Retryable: at.Outcome != monitoring.AttemptPermanentError}
	}
	return out
}
