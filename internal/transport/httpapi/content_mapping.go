package httpapi

import (
	"encoding/json"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

// unionKind reads a discriminated union's `kind` without assuming its variant.
func unionKind(in any) string {
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

func extensionsFromTransport(in transport.Extensions) content.Extensions {
	out := make(content.Extensions, len(in))
	for namespace, extension := range in {
		out[namespace] = content.Extension{SchemaVersion: extension.SchemaVersion, Data: extension.Data}
	}
	return out
}

// extensionsToTransport preserves the canonical extension shape byte-for-byte
// through the generated transport representation.
func extensionsToTransport(in content.Extensions) transport.Extensions {
	b, err := json.Marshal(in)
	if err != nil {
		return transport.Extensions{}
	}
	var out transport.Extensions
	if json.Unmarshal(b, &out) != nil {
		return transport.Extensions{}
	}
	return out
}

func relationFromTransport(in transport.RelationInput) content.Relation {
	out := content.Relation{Type: in.Type, Target: content.Source{CorpusID: in.Target.CorpusId, Namespace: in.Target.Namespace, RecordKey: in.Target.RecordKey}}
	if in.SourceTargetRevision != nil {
		out.SourceTargetRevision = *in.SourceTargetRevision
	}
	return out
}

func relationToTransport(in content.Relation) transport.RelationInput {
	out := transport.RelationInput{Type: in.Type, Target: sourceToTransport(in.Target)}
	if in.SourceTargetRevision != "" {
		revision := in.SourceTargetRevision
		out.SourceTargetRevision = &revision
	}
	return out
}

func partFromTransport(in transport.Part) (content.Part, error) {
	out := content.Part{Key: in.Key, Role: in.Role}
	if in.ParentKey != nil {
		out.ParentKey = *in.ParentKey
	}
	switch unionKind(in.Content) {
	case "text":
		text, err := in.Content.AsTextContent()
		if err != nil {
			return out, err
		}
		out.Content = content.Text{Kind: "text", Text: text.Text}
	case "blob":
		blob, err := in.Content.AsBlobContent()
		if err != nil {
			return out, err
		}
		out.Content = content.Text{Kind: "blob", BlobID: blob.BlobId, MediaType: blob.MediaType}
	default:
		return out, content.ErrUnsupported
	}
	if in.Extensions != nil {
		out.Extensions = extensionsFromTransport(*in.Extensions)
	}
	return out, nil
}

func partToTransport(in content.Part) (transport.Part, error) {
	out := transport.Part{Key: in.Key, Role: in.Role}
	if in.ParentKey != "" {
		parent := in.ParentKey
		out.ParentKey = &parent
	}
	switch in.Content.Kind {
	case "text":
		if err := out.Content.FromTextContent(transport.TextContent{Kind: transport.TextContentKind(in.Content.Kind), Text: in.Content.Text}); err != nil {
			return out, err
		}
	case "blob":
		if err := out.Content.FromBlobContent(transport.BlobContent{Kind: transport.BlobContentKind(in.Content.Kind), BlobId: in.Content.BlobID, MediaType: in.Content.MediaType}); err != nil {
			return out, err
		}
	default:
		return out, content.ErrInvalid
	}
	if len(in.Extensions) > 0 {
		extensions := extensionsToTransport(in.Extensions)
		out.Extensions = &extensions
	}
	return out, nil
}

func withdrawalFromTransport(in transport.WithdrawalCommand) content.Withdrawal {
	out := content.Withdrawal{Key: in.IdempotencyKey, Source: content.Source{CorpusID: in.Source.CorpusId, Namespace: in.Source.Namespace, RecordKey: in.Source.RecordKey}}
	if in.Reason != nil {
		out.Reason = *in.Reason
	}
	return out
}

func commandFromTransport(in transport.IngestCommand) (content.Command, error) {
	c := content.Command{Key: in.IdempotencyKey, Source: content.Source{CorpusID: in.Source.CorpusId, Namespace: in.Source.Namespace, RecordKey: in.Source.RecordKey}}
	switch unionKind(in.Content) {
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
	case "manifest":
		manifest, err := in.Content.AsManifestContent()
		if err != nil {
			return content.Command{}, err
		}
		parts := make([]content.Part, 0, len(manifest.Parts))
		for _, p := range manifest.Parts {
			part, err := partFromTransport(p)
			if err != nil {
				return content.Command{}, err
			}
			parts = append(parts, part)
		}
		relations := []content.Relation{}
		if manifest.Relations != nil {
			for _, r := range *manifest.Relations {
				relations = append(relations, relationFromTransport(r))
			}
		}
		c.Content = content.Text{Kind: "manifest"}
		c.Manifest = &content.Manifest{Kind: "manifest", Parts: parts, Relations: relations}
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
		c.Extensions = extensionsFromTransport(*in.Extensions)
	}
	if in.Provenance != nil {
		if in.Provenance.Normalization != nil {
			// Engine-owned: only an external normalization publishes it.
			return content.Command{}, content.ErrInvalid
		}
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
	steps := stepsToTransport(v.Steps)
	out := transport.Version{RecordId: v.RecordID, VersionId: v.ID, AcceptedAt: v.AcceptedAt, Steps: &steps, Availability: availabilityToTransport(v.Availability), Processing: processingToTransport(v.Processing), Relations: []transport.ResolvedRelation{}, Manifest: transport.ManifestContent{Kind: transport.ManifestContentKind(v.Manifest.Kind), Parts: []transport.Part{}}}
	if len(v.Diagnostics) > 0 {
		diagnostics := diagnosticsToTransport(v.Diagnostics)
		out.Diagnostics = &diagnostics
	}
	for _, p := range v.Manifest.Parts {
		part, err := partToTransport(p)
		if err != nil {
			return out, err
		}
		out.Manifest.Parts = append(out.Manifest.Parts, part)
	}
	if len(v.Manifest.Relations) > 0 {
		relations := make([]transport.RelationInput, 0, len(v.Manifest.Relations))
		for _, r := range v.Manifest.Relations {
			relations = append(relations, relationToTransport(r))
		}
		out.Manifest.Relations = &relations
	}
	if len(v.Extensions) > 0 {
		extensions := extensionsToTransport(v.Extensions)
		out.Extensions = &extensions
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
				if value, ok := id.(string); ok {
					values = append(values, value)
				}
			}
			p.SourceBlobIds = &values
		}
		if n, ok := v.Provenance["normalization"].(map[string]any); ok {
			p.Normalization = normalizationToTransport(n)
		}
		out.Provenance = &p
	}
	for _, r := range v.Relations {
		resolved := transport.ResolvedRelation{SourceReference: relationToTransport(r.Source), Status: transport.ResolvedRelationStatus(r.Status)}
		if r.Status == "available" {
			resolved.TargetRecordId = optionalString(r.TargetRecordID)
			resolved.TargetVersionId = optionalString(r.TargetVersionID)
		}
		out.Relations = append(out.Relations, resolved)
	}
	return out, nil
}

func normalizationToTransport(n map[string]any) *transport.NormalizationProvenance {
	text := func(key string) string { value, _ := n[key].(string); return value }
	out := &transport.NormalizationProvenance{PluginId: text("plugin_id"), PluginVersion: text("plugin_version"), PluginApi: text("plugin_api"), Contribution: transport.NormalizationProvenanceContribution(text("contribution")), InvocationId: text("invocation_id"), IdempotencyKey: text("idempotency_key"), InputSha256: text("input_sha256")}
	if f, ok := n["fallback"].(map[string]any); ok {
		code, _ := f["code"].(string)
		message, _ := f["message"].(string)
		out.Fallback = &struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}{Code: code, Message: message}
	}
	return out
}

func diagnosticsToTransport(diagnostics []content.Diagnostic) []transport.Diagnostic {
	out := []transport.Diagnostic{}
	for _, d := range diagnostics {
		out = append(out, transport.Diagnostic{Code: d.Code, Message: d.Message, Retryable: d.Retryable, Plugin: optionalString(d.Plugin), PluginVersion: optionalString(d.PluginVersion), Plan: optionalString(d.Plan), Contribution: optionalString(d.Contribution), InvocationId: optionalString(d.InvocationID)})
	}
	return out
}
