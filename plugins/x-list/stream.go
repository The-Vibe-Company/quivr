package main

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/sdks/go/quivrplugin"
)

// Webhook mode: posts of the list's members arrive in near real time through
// an X Filtered Stream linked to a webhook at the instance's webhook_url,
// which the core relays to Receive. Pull runs set it up and keep it in sync:
// they page through the list members, turn them into from: rules tagged with
// the instance id, diff those rules with the stream's, and make sure the
// webhook exists, is valid and is linked to the stream. The checkpoint holds
// the setup's progress, so it resumes across pages and runs.
//
// The X API shapes used here (list members, stream rules, webhooks and
// their links) follow X's public API v2 documentation; CI checks them only
// against a fake X.

const (
	defaultResyncEvery  = 15 * time.Minute
	defaultPushPollGap  = 15 * time.Minute
	defaultMaxRules     = 1000
	defaultMaxRuleBytes = 512
	// pushGrace holds a post back from pull while push is active: a post
	// published this recently may still be on its way through the webhook,
	// so a new Record from pull is a post the webhook really missed.
	pushGrace = time.Minute
	// Rule tags name the instance, so instances sharing one X app keep their
	// rules apart and a delivery is kept only by the instance it matched.
	ruleTagPrefix = "quivr:"
)

// webhookConfig is the instance's webhook block.
type webhookConfig struct {
	Enabled       bool  `json:"enabled"`
	ResyncSeconds int64 `json:"resync_interval_seconds"`
	PollSeconds   int64 `json:"poll_interval_seconds"`
	MaxRules      int   `json:"max_rules"`
	MaxRuleLength int   `json:"max_rule_length"`
}

func (w webhookConfig) resyncEvery() time.Duration {
	if w.ResyncSeconds > 0 {
		return time.Duration(w.ResyncSeconds) * time.Second
	}
	return defaultResyncEvery
}

func (w webhookConfig) pollEvery() time.Duration {
	if w.PollSeconds > 0 {
		return time.Duration(w.PollSeconds) * time.Second
	}
	return defaultPushPollGap
}

func (w webhookConfig) maxRules() int {
	if w.MaxRules > 0 {
		return w.MaxRules
	}
	return defaultMaxRules
}

func (w webhookConfig) maxRuleLength() int {
	if w.MaxRuleLength > 0 {
		return w.MaxRuleLength
	}
	return defaultMaxRuleBytes
}

// Setup steps.
const (
	stepMembers = "members"
	stepRules   = "rules"
	stepWebhook = "webhook"
	stepLink    = "link"
)

// pushState is the webhook setup's progress in the checkpoint.
type pushState struct {
	// Step is the setup step in progress; empty between resyncs.
	Step    string   `json:"step,omitempty"`
	Token   string   `json:"token,omitempty"`
	Members []string `json:"members,omitempty"`
	// WebhookID is the X webhook registered for the instance's webhook_url.
	WebhookID string `json:"webhook_id,omitempty"`
	// Rules and MemberCount describe the last applied rule set.
	Rules       int `json:"rules,omitempty"`
	MemberCount int `json:"member_count,omitempty"`
	// SyncedAt is when the last setup ended, successfully or not.
	SyncedAt *time.Time `json:"synced_at,omitempty"`
	// ActiveSince is when push became active; nil while it is not.
	ActiveSince *time.Time   `json:"active_since,omitempty"`
	Failure     *pushFailure `json:"failure,omitempty"`
	// Pending says why push is not set up, when no failure does.
	Pending string `json:"pending,omitempty"`
}

type pushFailure struct {
	Class quivrplugin.Class `json:"class"`
	Code  string            `json:"code"`
}

// active reports whether deliveries are expected.
func (p *pushState) active() bool { return p != nil && p.ActiveSince != nil && p.Failure == nil }

// due reports whether a setup step should run on this page.
func (p *pushState) due(now time.Time, every time.Duration) bool {
	return p.Step != "" || p.SyncedAt == nil || !now.Before(p.SyncedAt.Add(every))
}

// status is the push report of a page.
func (p *pushState) status(cfg webhookConfig) *quivrplugin.PushStatus {
	switch {
	case p.Failure != nil:
		return quivrplugin.PushHasFailed(p.Failure.Class, p.Failure.Code)
	case p.ActiveSince != nil:
		return quivrplugin.PushIsActive(cfg.pollEvery())
	}
	return quivrplugin.PushIsPending(p.Pending)
}

// held reports whether pull holds a post back because the webhook may still
// deliver it.
func (p *pushState) held(created, now time.Time) bool {
	return p.active() && !created.Before(*p.ActiveSince) && created.After(now.Add(-pushGrace))
}

// fail ends the setup with a failure; a transient one is retried at the next
// pull run, others at the next resync.
func (p *pushState) fail(class quivrplugin.Class, code string, now time.Time) {
	p.Step, p.Token, p.Members, p.ActiveSince = "", "", nil, nil
	p.Failure = &pushFailure{Class: class, Code: code}
	if class != quivrplugin.ClassTransient {
		p.SyncedAt = &now
	}
}

// ruleTag names the instance's rules.
func ruleTag(instanceID string) string { return ruleTagPrefix + instanceID }

// packRules turns member ids into from: rules joined by OR, each at most
// maxLength characters, in a stable order.
func packRules(members []string, maxLength int) []string {
	ids := append([]string(nil), members...)
	sort.Slice(ids, func(i, j int) bool { return idAfter(ids[j], ids[i]) })
	var rules []string
	var current strings.Builder
	for i, id := range ids {
		if i > 0 && ids[i-1] == id {
			continue
		}
		term := "from:" + id
		if current.Len() > 0 && current.Len()+len(" OR ")+len(term) > maxLength {
			rules = append(rules, current.String())
			current.Reset()
		}
		if current.Len() > 0 {
			current.WriteString(" OR ")
		}
		current.WriteString(term)
	}
	if current.Len() > 0 {
		rules = append(rules, current.String())
	}
	return rules
}

type streamRule struct {
	ID    string `json:"id,omitempty"`
	Value string `json:"value"`
	Tag   string `json:"tag,omitempty"`
}

type xWebhook struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Valid bool   `json:"valid"`
}

// setupFailure classifies a non-2xx answer of a setup call. X's own error
// codes stay in the plugin log, never in health.
func setupFailure(a *answer, refused string) (quivrplugin.Class, string) {
	switch {
	case a.status == http.StatusUnauthorized, a.status == http.StatusForbidden:
		return quivrplugin.ClassAccess, refused
	case a.status == http.StatusPaymentRequired:
		return quivrplugin.ClassAccess, "credits_depleted"
	case a.status == http.StatusTooManyRequests:
		return quivrplugin.ClassTransient, "rate_limited"
	case a.status >= 500:
		return quivrplugin.ClassTransient, "source_unavailable"
	}
	return quivrplugin.ClassSource, refused
}

// stepSetup runs one setup step. It never fails the fetch: a failure is the
// push status, and pull carries the collection meanwhile.
func (c client) stepSetup(ctx context.Context, p *pushState, cfg webhookConfig, instanceID, webhookURL, listID string, now time.Time) {
	if p.Step == "" {
		p.Step, p.Token, p.Members = stepMembers, "", nil
	}
	call := func(method, path string, q url.Values, body, out any, refused string) bool {
		failed, err := c.send(ctx, method, path, q, body, out)
		if err != nil {
			class, code := quivrplugin.ClassTransient, "source_unavailable"
			if e, ok := err.(*quivrplugin.Error); ok {
				class, code = e.Class, e.Code
			}
			p.fail(class, code, now)
			return false
		}
		if failed != nil {
			class, code := setupFailure(failed, refused)
			p.fail(class, code, now)
			return false
		}
		return true
	}
	switch p.Step {
	case stepMembers:
		q := url.Values{"max_results": {"100"}}
		if p.Token != "" {
			q.Set("pagination_token", p.Token)
		}
		var resp struct {
			Data []User `json:"data"`
			Meta struct {
				NextToken string `json:"next_token"`
			} `json:"meta"`
		}
		if !call(http.MethodGet, "/2/lists/"+url.PathEscape(listID)+"/members", q, nil, &resp, "list_members_forbidden") {
			return
		}
		for _, u := range resp.Data {
			p.Members = append(p.Members, u.ID)
		}
		if p.Token = resp.Meta.NextToken; p.Token == "" {
			p.Step = stepRules
		}
	case stepRules:
		want := packRules(p.Members, cfg.maxRuleLength())
		if len(want) > cfg.maxRules() {
			p.fail(quivrplugin.ClassSource, "rule_limit_exceeded", now)
			return
		}
		var current struct {
			Data []streamRule `json:"data"`
		}
		if !call(http.MethodGet, "/2/tweets/search/stream/rules", nil, nil, &current, "stream_forbidden") {
			return
		}
		tag := ruleTag(instanceID)
		keep := map[string]bool{}
		var drop []string
		for _, r := range current.Data {
			if r.Tag != tag {
				continue
			}
			if slicesContains(want, r.Value) && !keep[r.Value] {
				keep[r.Value] = true
			} else {
				drop = append(drop, r.ID)
			}
		}
		var add []streamRule
		for _, v := range want {
			if !keep[v] {
				add = append(add, streamRule{Value: v, Tag: tag})
			}
		}
		if len(drop) > 0 && !call(http.MethodPost, "/2/tweets/search/stream/rules", nil, map[string]any{"delete": map[string]any{"ids": drop}}, nil, "stream_forbidden") {
			return
		}
		if len(add) > 0 && !call(http.MethodPost, "/2/tweets/search/stream/rules", nil, map[string]any{"add": add}, nil, "stream_forbidden") {
			return
		}
		p.Rules, p.MemberCount, p.Members, p.Step = len(want), len(p.Members), nil, stepWebhook
	case stepWebhook:
		if webhookURL == "" {
			// The deployment has no public_url: push cannot be set up.
			p.Step, p.ActiveSince, p.Failure, p.SyncedAt, p.Pending = "", nil, nil, &now, "webhook_url_missing"
			return
		}
		var list struct {
			Data []xWebhook `json:"data"`
		}
		if !call(http.MethodGet, "/2/webhooks", nil, nil, &list, "webhook_forbidden") {
			return
		}
		var found *xWebhook
		for i := range list.Data {
			if list.Data[i].URL == webhookURL {
				found = &list.Data[i]
			}
		}
		if found == nil {
			// X checks the new webhook with a CRC request, which the core
			// relays to Receive, before it answers.
			var created struct {
				Data xWebhook `json:"data"`
			}
			if !call(http.MethodPost, "/2/webhooks", nil, map[string]any{"url": webhookURL}, &created, "webhook_refused") {
				return
			}
			found = &created.Data
		} else if !found.Valid {
			// X invalidated it (a failed CRC): ask X to check it again.
			var checked struct {
				Data xWebhook `json:"data"`
			}
			if !call(http.MethodPut, "/2/webhooks/"+url.PathEscape(found.ID), nil, nil, &checked, "webhook_invalid") {
				return
			}
			if !checked.Data.Valid {
				p.fail(quivrplugin.ClassAccess, "webhook_invalid", now)
				return
			}
		}
		p.WebhookID, p.Step = found.ID, stepLink
	case stepLink:
		var links struct {
			Data []struct {
				WebhookID string `json:"webhook_id"`
			} `json:"data"`
		}
		if !call(http.MethodGet, "/2/tweets/search/webhooks", nil, nil, &links, "stream_forbidden") {
			return
		}
		linked := false
		for _, l := range links.Data {
			linked = linked || l.WebhookID == p.WebhookID
		}
		if !linked && !call(http.MethodPost, "/2/tweets/search/webhooks/"+url.PathEscape(p.WebhookID), nil, nil, nil, "stream_forbidden") {
			return
		}
		p.Step, p.Failure, p.SyncedAt, p.Pending = "", nil, &now, ""
		if p.ActiveSince == nil {
			p.ActiveSince = &now
		}
	}
}

func slicesContains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// pushDiagnostics adds the webhook setup to the health diagnostics.
func pushDiagnostics(d map[string]any, p *pushState) {
	if p == nil {
		return
	}
	d["push_rules"] = p.Rules
	d["push_members"] = p.MemberCount
	if p.SyncedAt != nil {
		d["push_synced_at"] = p.SyncedAt.UTC().Format(time.RFC3339)
	}
	if p.Step != "" {
		d["push_setup_step"] = p.Step
	}
}
