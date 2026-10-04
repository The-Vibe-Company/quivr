package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

// Kind is the connector kind.
const Kind = "m365_mail"

// Public cloud endpoints; the installer's pin configuration may override them
// (national clouds, local fakes). They are deployment configuration, never
// instance configuration, so an API caller cannot send a deposited secret
// elsewhere.
const (
	DefaultLoginEndpoint = "https://login.microsoftonline.com"
	DefaultGraphEndpoint = "https://graph.microsoft.com/v1.0"
)

// MaxBackfill bounds how far back an instance may start collecting.
const MaxBackfill = 7 * 24 * time.Hour

// MaxAttachmentBytes is the engine's attachment cap; larger attachments are
// listed in attachments_skipped instead.
const MaxAttachmentBytes int64 = 25 << 20

// pageSize bounds messages per delta page (and memory per page).
const pageSize = 10

// Mail implements the m365_mail kind. It keeps access tokens in process
// memory only; everything else comes with each request.
type Mail struct {
	client *http.Client
	tokens *tokenCache
	// Sleep waits between in-run retries; tests replace it.
	Sleep func(context.Context, time.Duration) error
}

// New returns the connector (a default HTTP client when nil).
func New(client *http.Client) *Mail {
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	return &Mail{client: client, tokens: newTokenCache(), Sleep: sleep}
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

// endpoints is the plugin configuration the installer pins.
type endpoints struct {
	Login string `json:"login_endpoint"`
	Graph string `json:"graph_endpoint"`
}

func parseEndpoints(raw json.RawMessage) endpoints {
	var e endpoints
	_ = json.Unmarshal(raw, &e)
	if e.Login == "" {
		e.Login = DefaultLoginEndpoint
	}
	if e.Graph == "" {
		e.Graph = DefaultGraphEndpoint
	}
	e.Login, e.Graph = strings.TrimRight(e.Login, "/"), strings.TrimRight(e.Graph, "/")
	return e
}

type config struct {
	TenantID      string     `json:"tenant_id"`
	Mailbox       string     `json:"mailbox"`
	Folder        string     `json:"folder"`
	BackfillSince *time.Time `json:"backfill_since"`
}

func parseConfig(raw json.RawMessage) (config, bool) {
	var c config
	if err := json.Unmarshal(raw, &c); err != nil || c.TenantID == "" || c.Mailbox == "" {
		return c, false
	}
	if c.Folder == "" {
		c.Folder = "inbox"
	}
	return c, true
}

// backfillGrace lets an instance whose first poll comes shortly after its
// creation keep a backfill_since that was just within 7 days.
const backfillGrace = time.Hour

// checkpoint is the Acquisition Checkpoint: the Graph nextLink/deltaLink to
// resume from, and the start of the collection window.
type checkpoint struct {
	Link  string    `json:"link,omitempty"`
	Since time.Time `json:"since"`
}

// session is one invocation's view of an instance.
type session struct {
	m    *Mail
	end  endpoints
	cfg  config
	cred credential
}

func (m *Mail) session(configuration, instance json.RawMessage, cred quivrplugin.Credential) (session, error) {
	cfg, ok := parseConfig(instance)
	if !ok {
		return session{}, sourceError("invalid_config")
	}
	c, err := parseCredential(cred)
	if err != nil {
		return session{}, accessError("invalid_client_credential")
	}
	return session{m: m, end: parseEndpoints(configuration), cfg: cfg, cred: c}, nil
}

// CheckCredential asks the identity platform for an access token.
func (m *Mail) CheckCredential(ctx context.Context, r *quivrplugin.CredentialRequest) (*quivrplugin.CredentialStatus, error) {
	s, err := m.session(r.Configuration, r.Connector.Config, r.Credential)
	if err != nil {
		return nil, err
	}
	if _, err := s.token(ctx); err != nil {
		return nil, err
	}
	return &quivrplugin.CredentialStatus{}, nil
}

// Fetch reads one delta page. An expired or reset delta state restarts the
// round over the collection window, bounded to the last 7 days; items are
// deduplicated downstream by their revision-bearing idempotency keys.
func (m *Mail) Fetch(ctx context.Context, r *quivrplugin.FetchRequest) (*quivrplugin.Page, error) {
	s, err := m.session(r.Configuration, r.Connector.Config, r.Credential)
	if err != nil {
		return nil, err
	}
	now := r.Now
	if now.IsZero() {
		now = time.Now()
	}
	var cp checkpoint
	if r.DecodeCheckpoint(&cp) != nil {
		cp = checkpoint{}
	}
	if r.FirstRun() && s.cfg.BackfillSince != nil &&
		(s.cfg.BackfillSince.Before(now.Add(-MaxBackfill-backfillGrace)) || s.cfg.BackfillSince.After(now.Add(5*time.Minute))) {
		// No checkpoint yet: the window is checked when collection starts.
		return nil, sourceError("invalid_config")
	}
	if cp.Since.IsZero() {
		cp.Since = now.UTC().Truncate(time.Second)
		if s.cfg.BackfillSince != nil {
			cp.Since = s.cfg.BackfillSince.UTC()
		}
	}
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
	if err == errResync {
		err = s.getJSON(ctx, restart(), &page)
	}
	if err == errGone {
		return nil, accessError("folder_not_found")
	}
	if err != nil {
		return nil, err
	}
	if page.Next == "" && page.Delta == "" {
		return nil, sourceError("invalid_delta_response")
	}
	out := &quivrplugin.Page{More: page.Next != ""}
	for _, raw := range page.Value {
		item, ok, err := s.item(ctx, raw)
		if err != nil {
			return nil, err
		}
		if ok {
			out.Items = append(out.Items, item)
		}
	}
	cp.Link = page.Delta
	if page.Next != "" {
		cp.Link = page.Next
	}
	out.Checkpoint = cp
	return out, nil
}

type deltaPage struct {
	Value []json.RawMessage `json:"value"`
	Next  string            `json:"@odata.nextLink"`
	Delta string            `json:"@odata.deltaLink"`
}

func (s session) mailboxURL() string { return s.end.Graph + "/users/" + url.PathEscape(s.cfg.Mailbox) }

// owned reports whether a stored link targets the configured Graph endpoint;
// anything else is ignored rather than followed with a bearer token.
func (s session) owned(link string) bool {
	return link != "" && strings.HasPrefix(link, s.end.Graph+"/")
}

const messageFields = "id,internetMessageId,subject,body,from,sender,toRecipients,ccRecipients,sentDateTime,receivedDateTime,conversationId,hasAttachments"

func (s session) initialDelta(since time.Time) string {
	return s.mailboxURL() + "/mailFolders/" + url.PathEscape(s.cfg.Folder) + "/messages/delta?$select=" + messageFields +
		"&$filter=" + escapeQuery("receivedDateTime ge "+since.UTC().Format(time.RFC3339))
}

func escapeQuery(v string) string { return strings.ReplaceAll(url.QueryEscape(v), "+", "%20") }

func accessError(code string) *quivrplugin.Error {
	return quivrplugin.AccessError(code, "Microsoft 365 refused access: "+code)
}

func transientError(code string) *quivrplugin.Error {
	return quivrplugin.TransientError(code, "Microsoft 365 is unavailable: "+code)
}

func sourceError(code string) *quivrplugin.Error {
	return quivrplugin.SourceError(code, "Microsoft 365 answered unusable data: "+code)
}
