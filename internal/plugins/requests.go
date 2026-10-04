package plugins

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

// NormalizerSource identifies the source Record passed to a normalizer.
//
// Source is kept as a wire type here so the engine and the Contract Runner use
// exactly the same JSON shape when they invoke a normalizer.
type NormalizerSource struct {
	CorpusID  string `json:"corpus_id"`
	Namespace string `json:"namespace"`
	RecordKey string `json:"record_key"`
}

// NormalizerReference identifies where a normalizer can read its input Blob.
// Kind is either file (for local development) or signed_url (for an engine
// invocation). ExpiresAt is required by the signed_url schema and omitted for
// file references.
type NormalizerReference struct {
	Kind      string `json:"kind"`
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// NormalizerInput is the immutable Blob metadata and reference in a
// NormalizerRequest.
type NormalizerInput struct {
	BlobID    string              `json:"blob_id"`
	MediaType string              `json:"media_type"`
	SizeBytes int64               `json:"size_bytes"`
	SHA256    string              `json:"sha256"`
	Reference NormalizerReference `json:"reference"`
}

// NormalizerRequest is the normalizer Contribution wire request.
type NormalizerRequest struct {
	InvocationID    string           `json:"invocation_id"`
	IdempotencyKey  string           `json:"idempotency_key"`
	Contribution    string           `json:"contribution"`
	OrganizationID  string           `json:"organization_id"`
	CorpusID        string           `json:"corpus_id"`
	RecordID        string           `json:"record_id"`
	RecordVersionID string           `json:"record_version_id"`
	Source          NormalizerSource `json:"source"`
	Input           NormalizerInput  `json:"input"`
	Extensions      json.RawMessage  `json:"extensions,omitempty"`
	Provenance      json.RawMessage  `json:"provenance,omitempty"`
	Configuration   json.RawMessage  `json:"configuration"`
}

// BuildNormalizerRequest applies the normalizer Contribution and the shared
// empty-configuration default, then serializes the wire request.
func BuildNormalizerRequest(r NormalizerRequest) ([]byte, error) {
	r.Contribution = "normalizer"
	r.Configuration = defaultConfiguration(r.Configuration)
	return json.Marshal(r)
}

// IngestionVersion identifies the Record Version sent to segment_and_embed.
type IngestionVersion struct {
	CorpusID        string `json:"corpus_id"`
	RecordID        string `json:"record_id"`
	RecordVersionID string `json:"record_version_id"`
}

// SegmentAndEmbedRequest is the ingestion segment_and_embed wire request.
type SegmentAndEmbedRequest struct {
	InvocationID   string           `json:"invocation_id"`
	IdempotencyKey string           `json:"idempotency_key"`
	Contribution   string           `json:"contribution"`
	OrganizationID string           `json:"organization_id"`
	Configuration  json.RawMessage  `json:"configuration"`
	Version        IngestionVersion `json:"version"`
	Language       string           `json:"language,omitempty"`
	Parts          []IngestionPart  `json:"parts"`
	Spaces         []string         `json:"spaces"`
}

// BuildSegmentAndEmbedRequest applies the ingestion Contribution and shared
// empty-configuration default. Parts, Spaces and Language are deliberately
// left untouched so nil and explicit empty arrays retain their wire meaning.
func BuildSegmentAndEmbedRequest(r SegmentAndEmbedRequest) ([]byte, error) {
	r.Contribution = "ingestion"
	r.Configuration = defaultConfiguration(r.Configuration)
	return json.Marshal(r)
}

// EmbedQuery is the query member of an EmbedQueryRequest.
type EmbedQuery struct {
	Modality string `json:"modality"`
	Text     string `json:"text"`
}

// EmbedQueryRequest is the ingestion embed_query wire request.
type EmbedQueryRequest struct {
	InvocationID   string          `json:"invocation_id"`
	Contribution   string          `json:"contribution"`
	OrganizationID string          `json:"organization_id"`
	Configuration  json.RawMessage `json:"configuration"`
	Space          string          `json:"space"`
	Query          EmbedQuery      `json:"query"`
}

// BuildEmbedQueryRequest applies the ingestion Contribution and shared empty-
// configuration default.
func BuildEmbedQueryRequest(r EmbedQueryRequest) ([]byte, error) {
	r.Contribution = "ingestion"
	r.Configuration = defaultConfiguration(r.Configuration)
	return json.Marshal(r)
}

// SubscriptionPart is one text Part in a SubscriptionRequest. Vectors are
// raw JSON because both the engine and fixture runner already own their
// numeric vector representation; the builder only transports it.
type SubscriptionPart struct {
	Vectors json.RawMessage `json:"vectors,omitempty"`
	Key     string          `json:"key"`
	Role    string          `json:"role"`
	Text    string          `json:"text"`
}

// SubscriptionRef names the Subscription Version represented by an
// evaluation.
type SubscriptionRef struct {
	SubscriptionID        string `json:"subscription_id"`
	SubscriptionVersionID string `json:"subscription_version_id"`
	SavedQueryID          string `json:"saved_query_id"`
	SavedQueryVersionID   string `json:"saved_query_version_id"`
	Owner                 string `json:"owner,omitempty"`
}

// SubscriptionEvaluation is one distinct evaluation in a subscription
// request. Expression, Configuration and QueryVector are raw JSON so a
// caller can preserve the validated wire value without a second model.
type SubscriptionEvaluation struct {
	QueryVector   json.RawMessage   `json:"query_vector,omitempty"`
	ID            string            `json:"id"`
	Expression    json.RawMessage   `json:"expression"`
	Configuration json.RawMessage   `json:"configuration"`
	Subscriptions []SubscriptionRef `json:"subscriptions"`
}

// SubscriptionRecord is the evaluated Record Version in a subscription
// request.
type SubscriptionRecord struct {
	VectorSpaceID   string             `json:"vector_space_id,omitempty"`
	VectorsReady    *bool              `json:"vectors_ready,omitempty"`
	CorpusID        string             `json:"corpus_id"`
	RecordID        string             `json:"record_id"`
	RecordVersionID string             `json:"record_version_id"`
	Enriched        bool               `json:"enriched"`
	Parts           []SubscriptionPart `json:"parts"`
	Source          json.RawMessage    `json:"source"`
	AcceptedAt      string             `json:"accepted_at"`
	Provenance      json.RawMessage    `json:"provenance"`
	Extensions      json.RawMessage    `json:"extensions,omitempty"`
}

// SubscriptionRequest is the subscription Contribution wire request.
type SubscriptionRequest struct {
	InvocationID   string                   `json:"invocation_id"`
	IdempotencyKey string                   `json:"idempotency_key"`
	Contribution   string                   `json:"contribution"`
	OrganizationID string                   `json:"organization_id"`
	Record         SubscriptionRecord       `json:"record"`
	Evaluations    []SubscriptionEvaluation `json:"evaluations"`
	Configuration  json.RawMessage          `json:"configuration"`
}

// BuildSubscriptionRequest applies the subscription Contribution and shared
// empty-configuration default.
func BuildSubscriptionRequest(r SubscriptionRequest) ([]byte, error) {
	r.Contribution = "subscription"
	r.Configuration = defaultConfiguration(r.Configuration)
	return json.Marshal(r)
}

// SubscriptionKeyInput names the fields that participate in a subscription
// idempotency key. IncludeVectors is true exactly when the evaluator's
// contribution opts into vector fields; the vector fields then participate in
// the key even when their values are nil.
type SubscriptionKeyInput struct {
	Generation     string
	OrganizationID string
	VersionID      string
	Enriched       bool
	Evaluations    []SubscriptionEvaluation
	IncludeVectors bool
	VectorSpaceID  string
	VectorsReady   *bool
}

// SubscriptionKey returns the stable subscription key used by the engine and
// Contract Runner. Its identity layout matches the existing evaluator:
// generation, contribution, organization, version, enrichment, each
// evaluation's id/expression/configuration, and (when opted in) vector state.
func SubscriptionKey(in SubscriptionKeyInput) string {
	identity := []any{in.Generation, "subscription", in.OrganizationID, in.VersionID, in.Enriched}
	for _, evaluation := range in.Evaluations {
		identity = append(identity, evaluation.ID, rawJSONValue(evaluation.Expression), rawJSONValue(evaluation.Configuration))
	}
	if in.IncludeVectors {
		identity = append(identity, in.VectorSpaceID, in.VectorsReady, in.Evaluations)
	}
	key, _ := json.Marshal(identity)
	return "sk_" + content.Hash(key)
}

// ConnectorRef identifies the Connector Instance sent to fetch, credential
// and attachment routes. Scope and WebhookURL remain optional so callers can
// gate them on the Plugin API served by discovery.
type ConnectorRef struct {
	InstanceID      string          `json:"instance_id"`
	Kind            string          `json:"kind"`
	CorpusID        string          `json:"corpus_id,omitempty"`
	SourceNamespace string          `json:"source_namespace,omitempty"`
	WebhookURL      string          `json:"webhook_url,omitempty"`
	Config          json.RawMessage `json:"config"`
}

// ConnectorFetchRequest is the connector fetch wire request.
type ConnectorFetchRequest struct {
	InvocationID   string          `json:"invocation_id"`
	Contribution   string          `json:"contribution"`
	OrganizationID string          `json:"organization_id"`
	Configuration  json.RawMessage `json:"configuration"`
	Connector      ConnectorRef    `json:"connector"`
	Credential     json.RawMessage `json:"credential"`
	Checkpoint     json.RawMessage `json:"checkpoint"`
	Now            string          `json:"now"`
	PageInRun      int             `json:"page_in_run"`
	ReadsToday     int64           `json:"reads_today"`
}

// BuildConnectorFetchRequest applies connector defaults. A missing credential
// or checkpoint is encoded as JSON null, as required by the protocol.
func BuildConnectorFetchRequest(r ConnectorFetchRequest) ([]byte, error) {
	r.Contribution = "connector"
	r.Configuration = defaultConfiguration(r.Configuration)
	r.Credential = nullJSON(r.Credential)
	r.Checkpoint = nullJSON(r.Checkpoint)
	return json.Marshal(r)
}

// ConnectorCredentialRequest is the connector check_credential wire request.
type ConnectorCredentialRequest struct {
	InvocationID   string          `json:"invocation_id"`
	Contribution   string          `json:"contribution"`
	OrganizationID string          `json:"organization_id"`
	Configuration  json.RawMessage `json:"configuration"`
	Connector      ConnectorRef    `json:"connector"`
	Credential     json.RawMessage `json:"credential"`
	Now            string          `json:"now"`
}

// BuildConnectorCredentialRequest applies connector defaults and serializes a
// check_credential request.
func BuildConnectorCredentialRequest(r ConnectorCredentialRequest) ([]byte, error) {
	r.Contribution = "connector"
	r.Configuration = defaultConfiguration(r.Configuration)
	r.Credential = nullJSON(r.Credential)
	return json.Marshal(r)
}

// ConnectorAttachmentItem identifies the source item that owns an attachment.
type ConnectorAttachmentItem struct {
	RecordKey  string          `json:"record_key"`
	Revision   string          `json:"revision,omitempty"`
	Extensions json.RawMessage `json:"extensions,omitempty"`
}

// ConnectorAttachmentGrant is the presigned object-storage grant used by an
// upload_attachment request.
type ConnectorAttachmentGrant struct {
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	SizeBytes int64             `json:"size_bytes"`
	SHA256    string            `json:"sha256"`
	MediaType string            `json:"media_type"`
	ExpiresAt string            `json:"expires_at"`
}

// ConnectorAttachmentRequest is shared by describe_attachment and
// upload_attachment. Grant is omitted for describe_attachment.
type ConnectorAttachmentRequest struct {
	InvocationID   string                    `json:"invocation_id"`
	Contribution   string                    `json:"contribution"`
	OrganizationID string                    `json:"organization_id"`
	Configuration  json.RawMessage           `json:"configuration"`
	Connector      ConnectorRef              `json:"connector"`
	Credential     json.RawMessage           `json:"credential"`
	Now            string                    `json:"now"`
	Item           ConnectorAttachmentItem   `json:"item"`
	Attachment     ConnectorAttachment       `json:"attachment"`
	Grant          *ConnectorAttachmentGrant `json:"grant,omitempty"`
}

// BuildConnectorAttachmentRequest applies connector defaults and serializes a
// describe_attachment or upload_attachment request.
func BuildConnectorAttachmentRequest(r ConnectorAttachmentRequest) ([]byte, error) {
	r.Contribution = "connector"
	r.Configuration = defaultConfiguration(r.Configuration)
	r.Credential = nullJSON(r.Credential)
	if r.Grant != nil && r.Grant.Headers == nil {
		grant := *r.Grant
		grant.Headers = map[string]string{}
		r.Grant = &grant
	}
	return json.Marshal(r)
}

// NormalizerKey returns the normalizer idempotency key used by the engine.
func NormalizerKey(generation, contribution, organizationID, versionID, inputSHA256 string) string {
	b, _ := json.Marshal([]string{generation, contribution, organizationID, versionID, inputSHA256})
	return "nk_" + content.Hash(b)
}

// IngestionKey returns the ingestion idempotency key. Space ids are sorted in
// a copy so callers retain their request order while retries share identity.
func IngestionKey(generation, organizationID, versionID string, spaces []string) string {
	ids := append([]string(nil), spaces...)
	sort.Strings(ids)
	values := append([]string{generation, "segment_and_embed", organizationID, versionID}, ids...)
	return content.StableID("ingestion", values...)
}

func defaultConfiguration(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func nullJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return raw
}

func rawJSONValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return string(raw)
	}
	// json.Unmarshal rejects multiple values. Keep that behavior while using a
	// decoder so integers remain json.Number rather than float64.
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return string(raw)
	}
	return value
}
