package main

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/sdks/go/quivrplugin"
)

// OpenAttachment reads one attachment's bytes from Graph: the raw bytes of a
// file or item attachment, or the HTML body of the message. The SDK describes
// and uploads them through the core's grant.
func (m *Mail) OpenAttachment(ctx context.Context, r *quivrplugin.AttachmentRequest) (io.ReadCloser, error) {
	s, err := m.session(r.Configuration, r.Connector.Config, r.Credential)
	if err != nil {
		return nil, err
	}
	var target ref
	switch {
	case strings.HasPrefix(r.Attachment.Ref, attachmentRef) && json.Unmarshal([]byte(strings.TrimPrefix(r.Attachment.Ref, attachmentRef)), &target) == nil && target.Attachment != "":
		resp, err := s.do(ctx, s.messageURL(target.Message)+"/attachments/"+url.PathEscape(target.Attachment)+"/$value")
		if err == errGone {
			return nil, sourceError("attachment_gone")
		}
		if err != nil {
			return nil, err
		}
		return resp.Body, nil
	case strings.HasPrefix(r.Attachment.Ref, bodyRef) && json.Unmarshal([]byte(strings.TrimPrefix(r.Attachment.Ref, bodyRef)), &target) == nil:
		var msg message
		err := s.getJSON(ctx, s.messageURL(target.Message)+"?$select=body", &msg)
		if err == errGone {
			return nil, sourceError("attachment_gone")
		}
		if err != nil {
			return nil, err
		}
		return io.NopCloser(strings.NewReader(msg.Body.Content)), nil
	}
	return nil, sourceError("invalid_attachment_ref")
}

// SkippedAttachment records an attachment left out while its bytes were read
// (larger than announced, or empty) in the mail's attachments_skipped.
func (m *Mail) SkippedAttachment(_ context.Context, r *quivrplugin.AttachmentRequest, reason string) (map[string]quivrplugin.Extension, error) {
	exts := map[string]quivrplugin.Extension{}
	for k, v := range r.Item.Extensions {
		exts[k] = v
	}
	mail, ok := exts[MailExtension]
	if !ok {
		return nil, nil
	}
	data := map[string]any{}
	for k, v := range mail.Data {
		data[k] = v
	}
	name, size := "", int64(0)
	if meta, ok := r.Attachment.Extensions[AttachmentExtension]; ok {
		name, _ = meta.Data["name"].(string)
		if n, ok := meta.Data["size"].(float64); ok {
			size = int64(n)
		}
	}
	list, _ := data["attachments_skipped"].([]any)
	data["attachments_skipped"] = append(append([]any{}, list...), skipped(name, r.Attachment.MediaType, size, reason))
	exts[MailExtension] = quivrplugin.Extension{SchemaVersion: mail.SchemaVersion, Data: data}
	return exts, nil
}
