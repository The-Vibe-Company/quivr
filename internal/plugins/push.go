package plugins

import (
	"context"
	"encoding/json"
	"fmt"
)

// Bounds of a relayed delivery (connector push, Plugin API 0.5). The core
// refuses a larger request before any plugin call, and a plugin answer body
// beyond MaxReceiveAnswerBytes is refused by the response schema.
const (
	MaxRelayBodyBytes     = 1 << 20
	MaxRelayHeaders       = 64
	MaxRelayHeaderValues  = 16
	MaxRelayHeaderBytes   = 16 << 10
	MaxReceiveAnswerBytes = 64 << 10
)

// Push states a plugin reports for its push channel in a fetch answer.
const (
	PushActive  = "active"
	PushPending = "pending"
	PushFailed  = "failed"
)

// Receive verdicts.
const (
	VerdictAccepted = "accepted"
	VerdictRefused  = "refused"
)

// Issue codes for push.
const (
	// CodeInvalidPushStatus: a push status whose fields contradict its state,
	// or one from a plugin whose kinds do not declare push.
	CodeInvalidPushStatus = "invalid_push_status"
	// CodeInvalidVerdict: a receive answer whose status or items contradict
	// its verdict.
	CodeInvalidVerdict = "invalid_verdict"
)

// ConnectorPushStatus is a push kind's report on its push channel.
type ConnectorPushStatus struct {
	State               string `json:"state"`
	ErrorClass          string `json:"error_class,omitempty"`
	Code                string `json:"code,omitempty"`
	PollIntervalSeconds int    `json:"poll_interval_seconds,omitempty"`
}

// ReceiveAnswer is what the core returns to the source.
type ReceiveAnswer struct {
	Status      int    `json:"status"`
	ContentType string `json:"content_type,omitempty"`
	Body        string `json:"body,omitempty"`
}

// ConnectorDelivery is a decoded valid receive answer.
type ConnectorDelivery struct {
	Verdict  string          `json:"verdict"`
	Response ReceiveAnswer   `json:"response"`
	Items    []ConnectorItem `json:"items,omitempty"`
	Reads    int64           `json:"reads,omitempty"`
}

// KindPushes reports whether the manifest's connector kind declares the push
// mode.
func KindPushes(m *Manifest, kind string) bool {
	if m == nil || m.Contributions.Connector == nil {
		return false
	}
	for _, mode := range m.Contributions.Connector.Kinds[kind].Modes {
		if mode == "push" {
			return true
		}
	}
	return false
}

// DeclaresPush reports whether any connector kind of the manifest pushes.
func DeclaresPush(m *Manifest) bool {
	if m == nil || m.Contributions.Connector == nil {
		return false
	}
	for kind := range m.Contributions.Connector.Kinds {
		if KindPushes(m, kind) {
			return true
		}
	}
	return false
}

// pushStatusIssues judges a fetch answer's push status: only a plugin with a
// push kind reports one; failed carries a class and a code, active neither,
// and only active relaxes pull with poll_interval_seconds.
func pushStatusIssues(p *ConnectorPushStatus, m *Manifest) []Issue {
	if p == nil {
		return nil
	}
	issue := func(path, message string) []Issue {
		return []Issue{{Code: CodeInvalidPushStatus, Path: "/push" + path, Message: message}}
	}
	switch {
	case !DeclaresPush(m):
		return issue("", "only a plugin whose connector kinds declare modes [pull, push] reports a push status")
	case p.State == PushFailed && (p.ErrorClass == "" || p.Code == ""):
		return issue("/error_class", "a failed push status carries error_class and code, which Connector Health reports")
	case p.State != PushFailed && p.ErrorClass != "":
		return issue("/error_class", "error_class goes only with state failed")
	case p.State == PushActive && p.Code != "":
		return issue("/code", "an active push status carries no code")
	case p.State != PushActive && p.PollIntervalSeconds != 0:
		return issue("/poll_interval_seconds", "poll_interval_seconds relaxes pull only while push is active")
	}
	return nil
}

// CheckReceiveOutput judges a 200 receive answer as the engine does before it
// ingests anything: the response bound, the schema (unknown fields, such as a
// checkpoint, are rejected), a verdict coherent with the answer status (2xx
// accepted, 4xx refused) and refused carrying no items, then the items like a
// fetch page's, except that a delivery carries no attachments.
func CheckReceiveOutput(ctx context.Context, raw []byte, m *Manifest) []Issue {
	if limit := ConnectorMaxResponseBytes(m); len(raw) > limit {
		return []Issue{{Code: CodeResponseTooLarge, Message: fmt.Sprintf("the response is %d bytes; the limit is %d (declared max_response_bytes, capped by the engine at %d)", len(raw), limit, EngineMaxResponseBytes)}}
	}
	if issues := ValidateDocument("connector-receive-response.schema.json", raw); len(issues) > 0 {
		return issues
	}
	var d ConnectorDelivery
	if err := json.Unmarshal(raw, &d); err != nil {
		return []Issue{{Code: CodeSchema, Message: err.Error()}}
	}
	var issues []Issue
	switch {
	case d.Verdict == VerdictAccepted && (d.Response.Status < 200 || d.Response.Status > 299):
		issues = append(issues, Issue{Code: CodeInvalidVerdict, Path: "/response/status",
			Message: fmt.Sprintf("an accepted delivery is answered with a 2xx status, not %d", d.Response.Status)})
	case d.Verdict == VerdictRefused && d.Response.Status < 400:
		issues = append(issues, Issue{Code: CodeInvalidVerdict, Path: "/response/status",
			Message: fmt.Sprintf("a refused delivery is answered with a 4xx status, not %d", d.Response.Status)})
	}
	if d.Verdict == VerdictRefused && len(d.Items) > 0 {
		issues = append(issues, Issue{Code: CodeInvalidVerdict, Path: "/items",
			Message: "a refused delivery carries no items: the core changes nothing for it"})
	}
	if limit := ConnectorMaxItems(m); len(d.Items) > limit {
		issues = append(issues, Issue{Code: CodeTooManyItems, Path: "/items",
			Message: fmt.Sprintf("%d items exceed the declared max_items %d", len(d.Items), limit)})
	}
	for i, item := range d.Items {
		if len(item.Attachments) > 0 {
			issues = append(issues, Issue{Code: CodeInvalidItem, Path: fmt.Sprintf("/items/%d/attachments", i),
				Message: fmt.Sprintf("item %q of a delivery carries attachments; deliver the item without them and let a pull run return it with its attachments", item.RecordKey)})
			d.Items[i].Attachments = nil
		}
	}
	return append(issues, checkItems(ctx, d.Items, m, false)...)
}
