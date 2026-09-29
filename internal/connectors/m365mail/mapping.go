package m365mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"sort"
	"strings"
	"unicode"

	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"golang.org/x/net/html"
)

// Extension namespaces declared in content.BuiltinExtensions.
const (
	MailExtension       = "connector.m365_mail"
	AttachmentExtension = "connector.m365_mail.attachment"
)

type address struct {
	EmailAddress struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	} `json:"emailAddress"`
}

type message struct {
	ID                string          `json:"id"`
	Removed           json.RawMessage `json:"@removed"`
	InternetMessageID string          `json:"internetMessageId"`
	Subject           string          `json:"subject"`
	Body              struct {
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	} `json:"body"`
	From             *address  `json:"from"`
	Sender           *address  `json:"sender"`
	To               []address `json:"toRecipients"`
	Cc               []address `json:"ccRecipients"`
	SentDateTime     string    `json:"sentDateTime"`
	ReceivedDateTime string    `json:"receivedDateTime"`
	ConversationID   string    `json:"conversationId"`
	HasAttachments   bool      `json:"hasAttachments"`
}

type attachment struct {
	Type         string `json:"@odata.type"`
	ID           string `json:"id"`
	Name         string `json:"name"`
	ContentType  string `json:"contentType"`
	Size         int64  `json:"size"`
	IsInline     bool   `json:"isInline"`
	LastModified string `json:"lastModifiedDateTime"`
}

// item turns one delta entry into an Item. Removed entries (deleted or moved
// out) are ignored: a source disappearance never withdraws a Record. An entry
// carrying only changed properties is re-read in full.
func (s session) item(ctx context.Context, raw json.RawMessage) (connectors.Item, bool, error) {
	var msg message
	if json.Unmarshal(raw, &msg) != nil || msg.ID == "" {
		return connectors.Item{}, false, connectors.SourceError("invalid_delta_response")
	}
	if len(msg.Removed) > 0 {
		return connectors.Item{}, false, nil
	}
	if msg.ReceivedDateTime == "" {
		err := s.getJSON(ctx, s.messageURL(msg.ID)+"?$select="+messageFields, &msg)
		if errors.Is(err, errGone) {
			return connectors.Item{}, false, nil
		}
		if err != nil {
			return connectors.Item{}, false, err
		}
	}
	var list []attachment
	if msg.HasAttachments {
		var err error
		if list, err = s.attachments(ctx, msg.ID); errors.Is(err, errGone) {
			return connectors.Item{}, false, nil
		} else if err != nil {
			return connectors.Item{}, false, err
		}
	}
	return s.mapMessage(msg, list), true, nil
}

func (s session) messageURL(id string) string {
	return s.mailboxURL() + "/messages/" + url.PathEscape(id)
}

func (s session) attachments(ctx context.Context, id string) ([]attachment, error) {
	var all []attachment
	link := s.messageURL(id) + "/attachments?$select=id,name,contentType,size,isInline,lastModifiedDateTime"
	for pages := 0; link != "" && pages < 20; pages++ {
		var page struct {
			Value []attachment `json:"value"`
			Next  string       `json:"@odata.nextLink"`
		}
		if err := s.getJSON(ctx, link, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Value...)
		link = ""
		if s.owned(page.Next) {
			link = page.Next
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	return all, nil
}

func recordKey(m message) string {
	if k := strings.TrimSpace(m.InternetMessageID); k != "" && content.ValidText(k) {
		return k
	}
	return "graph:" + m.ID
}

// revision identifies the mail content and its attachment set; read state
// and change keys are excluded so reading a mail creates no new Version.
func revision(m message, list []attachment) string {
	b, _ := json.Marshal(struct {
		Key, Subject, BodyType, Body, Sent, Received, Conversation string
		From, Sender                                               *address
		To, Cc                                                     []address
		Attachments                                                []attachment
	}{recordKey(m), m.Subject, m.Body.ContentType, m.Body.Content, m.SentDateTime, m.ReceivedDateTime, m.ConversationID, m.From, m.Sender, m.To, m.Cc, list})
	return "sha256:" + content.Hash(b)
}

func (s session) mapMessage(m message, list []attachment) connectors.Item {
	item := connectors.Item{RecordKey: recordKey(m), Revision: revision(m, list)}
	parts := []content.Part{}
	if subject := clean(strings.TrimSpace(m.Subject)); subject != "" {
		parts = append(parts, content.Part{Key: "title", Role: "title", Content: content.Text{Kind: "text", Text: subject}})
	}
	body := m.Body.Content
	if strings.EqualFold(m.Body.ContentType, "html") {
		body = htmlToText(m.Body.Content)
		if original := m.Body.Content; strings.TrimSpace(original) != "" && content.ValidText(original) {
			item.Attachments = append(item.Attachments, connectors.Attachment{Key: "original_body", Role: "original_body", MediaType: "text/html",
				Open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(original)), nil }})
		}
	}
	if body = clean(strings.TrimSpace(body)); body != "" {
		parts = append(parts, content.Part{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: body}})
	}
	if len(parts) == 0 {
		// A Manifest needs a text Part; describe the mail from its headers.
		summary := "Mail received " + m.ReceivedDateTime
		if m.From != nil && m.From.EmailAddress.Address != "" {
			summary += " from " + m.From.EmailAddress.Address
		}
		parts = append(parts, content.Part{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: clean(summary)}})
	}
	item.Manifest = &content.Manifest{Kind: "manifest", Parts: parts}
	// Source strings are cleaned (valid UTF-8, no NUL) like the text Parts, and
	// recipient lists are bounded, so one unusual mail cannot stall the source.
	data := map[string]any{"graph_id": m.ID, "internet_message_id": clean(m.InternetMessageID), "conversation_id": clean(m.ConversationID), "subject": clean(m.Subject),
		"sent_at": clean(m.SentDateTime), "received_at": clean(m.ReceivedDateTime), "folder": s.cfg.Folder,
		"to": addresses(m.To), "cc": addresses(m.Cc), "to_count": len(m.To), "cc_count": len(m.Cc), "attachments_skipped": []any{}}
	if m.From != nil {
		data["from"] = addresses([]address{*m.From})[0]
	}
	if m.Sender != nil {
		data["sender"] = addresses([]address{*m.Sender})[0]
	}
	skip := func(name, mediaType string, size int64, reason string) {
		data["attachments_skipped"] = append(data["attachments_skipped"].([]any), map[string]any{"name": clean(name), "media_type": mediaType, "size": size, "reason": reason})
	}
	for i, a := range list {
		kind := strings.TrimPrefix(a.Type, "#microsoft.graph.")
		reason := ""
		switch {
		case kind != "fileAttachment" && kind != "itemAttachment":
			reason = "reference_attachment"
		case a.Size > connectors.MaxAttachmentBytes:
			reason = "too_large"
		case a.Size <= 0:
			reason = "empty"
		}
		mediaType := mediaTypeOf(a.ContentType)
		if kind == "itemAttachment" {
			mediaType = "message/rfc822"
		}
		if reason != "" {
			skip(a.Name, mediaType, a.Size, reason)
			continue
		}
		link := s.messageURL(m.ID) + "/attachments/" + url.PathEscape(a.ID) + "/$value"
		attachmentType := strings.TrimSuffix(kind, "Attachment")
		item.Attachments = append(item.Attachments, connectors.Attachment{
			Key: "attachment-" + itoa(i+1), Role: "attachment", MediaType: mediaType,
			Extensions: content.Extensions{AttachmentExtension: {SchemaVersion: "1", Data: map[string]any{"name": clean(a.Name), "size": a.Size, "is_inline": a.IsInline, "attachment_type": attachmentType}}},
			Skip:       func(reason string) { skip(a.Name, mediaType, a.Size, reason) },
			Open: func(ctx context.Context) (io.ReadCloser, error) {
				resp, err := s.do(ctx, link)
				if errors.Is(err, errGone) {
					return nil, connectors.SourceError("attachment_gone")
				}
				if err != nil {
					return nil, err
				}
				return resp.Body, nil
			}})
	}
	item.Extensions = content.Extensions{MailExtension: {SchemaVersion: "1", Data: data}}
	return item
}

// maxRecipients bounds each recipient list kept in the header extension; the
// full count is kept alongside.
const maxRecipients = 100

func addresses(list []address) []any {
	if len(list) > maxRecipients {
		list = list[:maxRecipients]
	}
	out := make([]any, 0, len(list))
	for _, a := range list {
		out = append(out, map[string]any{"name": clean(a.EmailAddress.Name), "address": clean(a.EmailAddress.Address)})
	}
	return out
}

func mediaTypeOf(v string) string {
	t, _, err := mime.ParseMediaType(v)
	if err != nil || t == "" || !strings.Contains(t, "/") {
		return "application/octet-stream"
	}
	return t
}

func itoa(n int) string { return fmt.Sprintf("%02d", n) }

// clean keeps valid UTF-8 text without NUL bytes.
func clean(s string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(s, "�"), "\x00", "")
}

// htmlToText renders an HTML mail body as plain text: scripts, styles and
// head are dropped, block elements and <br> break lines, whitespace inside a
// line collapses and blank lines are removed.
func htmlToText(src string) string {
	z := html.NewTokenizer(strings.NewReader(src))
	var b strings.Builder
	skip := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			return normalizeLines(b.String())
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "script", "style", "head", "title", "noscript":
				if tt := z.Token().Type; tt != html.SelfClosingTagToken {
					skip++
				}
			case "br", "p", "div", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6", "table", "ul", "ol", "blockquote", "hr":
				b.WriteByte('\n')
			case "td", "th":
				b.WriteByte(' ')
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "script", "style", "head", "title", "noscript":
				if skip > 0 {
					skip--
				}
			case "p", "div", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6", "table", "ul", "ol", "blockquote":
				b.WriteByte('\n')
			}
		case html.TextToken:
			if skip == 0 {
				b.Write(z.Text())
			}
		}
	}
}

func normalizeLines(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.Join(strings.FieldsFunc(line, func(r rune) bool { return unicode.IsSpace(r) }), " ")
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
