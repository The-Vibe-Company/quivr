package httpapi

import (
	"encoding/json"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

// contentKind reads the union discriminator without assuming which variant it holds.
func contentKind(in transport.IngestCommand_Content) string {
	b, err := json.Marshal(in)
	if err != nil {
		return ""
	}
	var discriminator struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(b, &discriminator) != nil {
		return ""
	}
	return discriminator.Kind
}

func commandFromTransport(in transport.IngestCommand) (content.Command, error) {
	kind := contentKind(in.Content)
	c := content.Command{Key: in.IdempotencyKey, Source: content.Source{CorpusID: in.Source.CorpusId, Namespace: in.Source.Namespace, RecordKey: in.Source.RecordKey}}
	switch kind {
	case "text":
		text, err := in.Content.AsTextContent()
		if err != nil {
			return content.Command{}, err
		}
		c.Content = content.Text{Kind: string(text.Kind), Text: text.Text}
	case "blob":
		blob, err := in.Content.AsBlobContent()
		if err != nil {
			return content.Command{}, err
		}
		c.Content = content.Text{Kind: "blob", BlobID: blob.BlobId, MediaType: blob.MediaType}
	default:
		return content.Command{}, content.ErrUnsupported
	}
	if in.SourceRevision != nil {
		c.Revision = *in.SourceRevision
	}
	if in.SourcePosition != nil {
		c.Position = *in.SourcePosition
	}
	if in.Extensions != nil {
		c.Extensions = map[string]any{}
		for k, v := range *in.Extensions {
			c.Extensions[k] = v
		}
	}
	if in.Provenance != nil {
		c.Provenance = map[string]any{}
		if in.Provenance.Producer != nil {
			c.Provenance["producer"] = *in.Provenance.Producer
		}
		if in.Provenance.ProducerVersion != nil {
			c.Provenance["producer_version"] = *in.Provenance.ProducerVersion
		}
		if in.Provenance.SourceBlobIds != nil {
			ids := make([]any, 0, len(*in.Provenance.SourceBlobIds))
			for _, id := range *in.Provenance.SourceBlobIds {
				ids = append(ids, id)
			}
			c.Provenance["source_blob_ids"] = ids
		}
	}
	return c, nil
}
func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func sourceToTransport(s content.Source) transport.SourceIdentity {
	return transport.SourceIdentity{CorpusId: s.CorpusID, Namespace: s.Namespace, RecordKey: s.RecordKey}
}
func processingToTransport(p content.Processing) transport.ProcessingSummary {
	out := transport.ProcessingSummary{State: transport.ProcessingSummaryState(p.State)}
	if p.Phase != "" {
		phase := transport.ProcessingSummaryPhase(p.Phase)
		out.Phase = &phase
	}
	return out
}
func availabilityToTransport(a content.Availability) transport.Availability {
	return transport.Availability{State: transport.AvailabilityState(a.State), IsCurrent: a.Current, Searchable: a.Searchable}
}
func receiptToTransport(r content.Receipt) transport.Receipt {
	out := transport.Receipt{ReceiptId: r.ID, State: transport.ReceiptState(r.State), RecordId: optionalString(r.RecordID), VersionId: optionalString(r.VersionID), Source: sourceToTransport(r.Source), Processing: processingToTransport(r.Processing), Diagnostics: []transport.Error{}}
	if r.Outcome != "" {
		outcome := transport.ReceiptOutcome(r.Outcome)
		out.Outcome = &outcome
	}
	if r.Availability != nil {
		a := availabilityToTransport(*r.Availability)
		out.Availability = &a
	}
	for _, d := range r.Diagnostics {
		out.Diagnostics = append(out.Diagnostics, transport.Error{Code: d.Code, Message: d.Message, Retryable: d.Retryable})
	}
	return out
}
func recordToTransport(r content.Record) transport.Record {
	return transport.Record{RecordId: r.ID, Source: sourceToTransport(r.Source), Withdrawn: r.Withdrawn, CurrentVersionId: optionalString(r.CurrentVersionID)}
}
func versionToTransport(v content.Version) (transport.Version, error) {
	out := transport.Version{RecordId: v.RecordID, VersionId: v.ID, Availability: availabilityToTransport(v.Availability), Processing: processingToTransport(v.Processing), Relations: []transport.ResolvedRelation{}, Manifest: transport.ManifestContent{Kind: transport.ManifestContentKind(v.Manifest.Kind), Parts: []transport.Part{}}}
	for _, p := range v.Manifest.Parts {
		part := transport.Part{Key: p.Key, Role: p.Role}
		if err := part.Content.FromTextContent(transport.TextContent{Kind: transport.TextContentKind(p.Content.Kind), Text: p.Content.Text}); err != nil {
			return out, err
		}
		out.Manifest.Parts = append(out.Manifest.Parts, part)
	}
	if len(v.Provenance) > 0 {
		p := transport.Provenance{}
		if s, ok := v.Provenance["producer"].(string); ok {
			p.Producer = &s
		}
		if s, ok := v.Provenance["producer_version"].(string); ok {
			p.ProducerVersion = &s
		}
		if ids, ok := v.Provenance["source_blob_ids"].([]any); ok {
			values := []string{}
			for _, id := range ids {
				values = append(values, id.(string))
			}
			p.SourceBlobIds = &values
		}
		out.Provenance = &p
	}
	return out, nil
}
