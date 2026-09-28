// Package m365mail is the built-in m365_mail connector: it collects one
// Microsoft 365 mailbox folder through Microsoft Graph with an app-only
// (client credentials) registration. Each page of the folder's message delta
// becomes Items; the delta link is the Acquisition Checkpoint, committed by
// the Acquirer only after the page's items were durably accepted.
//
// It deliberately uses net/http and JSON rather than the Graph SDK: four
// endpoints (token, delta, attachment list, raw attachment bytes) are needed.
package m365mail

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
)

// Kind is the public connector kind.
const Kind = "m365_mail"

// Public cloud endpoints; deployments may override them (national clouds,
// local fakes). They are deployment configuration, never instance
// configuration, so an API caller cannot send a deposited secret elsewhere.
const (
	DefaultLoginEndpoint = "https://login.microsoftonline.com"
	DefaultGraphEndpoint = "https://graph.microsoft.com/v1.0"
)

// MaxBackfill bounds how far back an instance may start collecting.
const MaxBackfill = 7 * 24 * time.Hour

// pageSize bounds messages per delta page (and memory per page).
const pageSize = 10

// Connector implements connectors.Connector for m365_mail.
type Connector struct {
	login, graph string
	client       *http.Client
	tokens       *tokenCache
	// Sleep waits between in-run retries; tests replace it.
	Sleep func(context.Context, time.Duration) error
}

// New returns a connector using the given endpoints (defaults when empty).
func New(loginEndpoint, graphEndpoint string, client *http.Client) *Connector {
	if loginEndpoint == "" {
		loginEndpoint = DefaultLoginEndpoint
	}
	if graphEndpoint == "" {
		graphEndpoint = DefaultGraphEndpoint
	}
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	return &Connector{login: strings.TrimRight(loginEndpoint, "/"), graph: strings.TrimRight(graphEndpoint, "/"), client: client, tokens: newTokenCache(), Sleep: sleep}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (*Connector) Kind() string                   { return Kind }
func (*Connector) DefaultInterval() time.Duration { return time.Minute }

func (*Connector) ConfigSchema() []byte {
	return []byte(`{"title":"Microsoft 365 mailbox","description":"Collects the messages and attachments of a mailbox folder through Microsoft Graph.","type":"object","additionalProperties":false,"required":["tenant_id","mailbox"],"properties":{
"tenant_id":{"type":"string","pattern":"^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$","title":"Tenant ID","description":"Directory (tenant) ID or verified domain.","examples":["example.onmicrosoft.com"]},
"mailbox":{"type":"string","minLength":1,"maxLength":320,"pattern":"^[^/\\\\?#\\s]+$","title":"Mailbox","description":"User principal name or ID of the mailbox.","examples":["shared-inbox@example.org"]},
"folder":{"type":"string","minLength":1,"maxLength":512,"pattern":"^[^/\\\\?#\\s]+$","title":"Folder","description":"Well-known folder name or folder ID. Defaults to the inbox.","examples":["inbox"]},
"backfill_since":{"type":"string","minLength":20,"maxLength":40,"title":"Backfill since","description":"RFC 3339 timestamp of the oldest message to collect.","examples":["2026-01-01T00:00:00Z"]}}}`)
}

func (*Connector) CredentialSchema() []byte {
	return []byte(`{"title":"Application credential","description":"App registration with Mail.Read application permission: a client secret, or a certificate and its private key.","type":"object","additionalProperties":false,"required":["client_id"],"properties":{
"client_id":{"type":"string","minLength":1,"maxLength":128,"title":"Client ID"},
"client_secret":{"type":"string","minLength":1,"maxLength":1024,"title":"Client secret","writeOnly":true},
"certificate_pem":{"type":"string","minLength":1,"maxLength":16384,"title":"Certificate (PEM)"},
"private_key_pem":{"type":"string","minLength":1,"maxLength":16384,"title":"Private key (PEM)","writeOnly":true}},
"oneOf":[
 {"title":"Client secret","required":["client_secret"],"not":{"anyOf":[{"required":["certificate_pem"]},{"required":["private_key_pem"]}]}},
 {"title":"Certificate","required":["certificate_pem","private_key_pem"],"not":{"required":["client_secret"]}}]}`)
}

// CredentialRequired: Graph refuses unauthenticated mailbox reads.
func (*Connector) CredentialRequired() bool { return true }

type config struct {
	TenantID      string     `json:"tenant_id"`
	Mailbox       string     `json:"mailbox"`
	Folder        string     `json:"folder"`
	BackfillSince *time.Time `json:"backfill_since"`
}

func parseConfig(raw json.RawMessage) (config, error) {
	var c config
	if err := json.Unmarshal(raw, &c); err != nil || c.TenantID == "" || c.Mailbox == "" {
		return c, errors.New("invalid m365_mail config")
	}
	if c.Folder == "" {
		c.Folder = "inbox"
	}
	return c, nil
}

// backfillGrace lets a creation retried shortly after it succeeded replay
// instead of being refused because its backfill_since aged past 7 days.
const backfillGrace = time.Hour

// CheckConfig bounds backfill_since to the last 7 days (not in the future).
func (*Connector) CheckConfig(raw json.RawMessage, now time.Time) error {
	c, err := parseConfig(raw)
	if err != nil {
		return err
	}
	if c.BackfillSince != nil && (c.BackfillSince.Before(now.Add(-MaxBackfill-backfillGrace)) || c.BackfillSince.After(now.Add(5*time.Minute))) {
		return errors.New("backfill_since must be within the last 7 days")
	}
	return nil
}

// checkpoint is the Acquisition Checkpoint: the Graph nextLink/deltaLink to
// resume from, and the start of the collection window.
type checkpoint struct {
	Link  string    `json:"link,omitempty"`
	Since time.Time `json:"since"`
}

// Fetch reads one delta page. An expired or reset delta state restarts the
// round over the collection window, bounded to the last 7 days; items are
// deduplicated downstream by their revision-bearing idempotency keys.
func (c *Connector) Fetch(ctx context.Context, r connectors.FetchRequest) (connectors.Page, error) {
	cfg, err := parseConfig(r.Config)
	if err != nil {
		return connectors.Page{}, connectors.SourceError("invalid_config")
	}
	cred, err := parseCredential(r.Credential)
	if err != nil {
		return connectors.Page{}, connectors.AccessError("invalid_client_credential")
	}
	var cp checkpoint
	if len(r.Checkpoint) > 0 && json.Unmarshal(r.Checkpoint, &cp) != nil {
		cp = checkpoint{}
	}
	now := r.Now
	if now.IsZero() {
		now = time.Now()
	}
	if cp.Since.IsZero() {
		cp.Since = now.UTC().Truncate(time.Second)
		if cfg.BackfillSince != nil {
			cp.Since = cfg.BackfillSince.UTC()
		}
	}
	s := session{c: c, cfg: cfg, cred: cred}
	// A restarted round (expired delta state, or a stored link that no longer
	// targets the configured endpoint) re-reads at most the last 7 days.
	restart := func() string {
		if floor := now.Add(-MaxBackfill).UTC(); cp.Since.Before(floor) {
			cp.Since = floor
		}
		return s.initialDelta(cp.Since)
	}
	link := cp.Link
	if !s.owned(link) {
		link = restart()
	}
	var page deltaPage
	err = s.getJSON(ctx, link, &page)
	if errors.Is(err, errResync) {
		err = s.getJSON(ctx, restart(), &page)
	}
	if errors.Is(err, errGone) {
		return connectors.Page{}, connectors.AccessError("folder_not_found")
	}
	if err != nil {
		return connectors.Page{}, err
	}
	if page.Next == "" && page.Delta == "" {
		return connectors.Page{}, connectors.SourceError("invalid_delta_response")
	}
	out := connectors.Page{More: page.Next != ""}
	for _, raw := range page.Value {
		item, ok, err := s.item(ctx, raw)
		if err != nil {
			return connectors.Page{}, err
		}
		if ok {
			out.Items = append(out.Items, item)
		}
	}
	cp.Link = page.Delta
	if page.Next != "" {
		cp.Link = page.Next
	}
	out.Checkpoint, _ = json.Marshal(cp)
	return out, nil
}

type deltaPage struct {
	Value []json.RawMessage `json:"value"`
	Next  string            `json:"@odata.nextLink"`
	Delta string            `json:"@odata.deltaLink"`
}

// session is one Fetch's view of an instance.
type session struct {
	c    *Connector
	cfg  config
	cred credential
}

func (s session) mailboxURL() string { return s.c.graph + "/users/" + url.PathEscape(s.cfg.Mailbox) }

// owned reports whether a stored link targets the configured Graph endpoint;
// anything else is ignored rather than followed with a bearer token.
func (s session) owned(link string) bool {
	return link != "" && strings.HasPrefix(link, s.c.graph+"/")
}

const messageFields = "id,internetMessageId,subject,body,from,sender,toRecipients,ccRecipients,sentDateTime,receivedDateTime,conversationId,hasAttachments"

func (s session) initialDelta(since time.Time) string {
	return s.mailboxURL() + "/mailFolders/" + url.PathEscape(s.cfg.Folder) + "/messages/delta?$select=" + messageFields +
		"&$filter=" + escapeQuery("receivedDateTime ge "+since.UTC().Format(time.RFC3339))
}

func escapeQuery(v string) string { return strings.ReplaceAll(url.QueryEscape(v), "+", "%20") }
