// Package normalization invokes the pinned external normalizer for accepted
// routed Blobs, between acceptance and publication. It validates the output
// with the engine's kind "manifest" rules and stores it durably once per
// Record Version, so re-running the invocation converges and publication,
// rebuilds and retrieval generations read the stored Manifest without ever
// calling the plugin again.
package normalization

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

// Contribution is the only Contribution Plugin API 0.1 invokes.
const Contribution = "normalizer"

// Engine bounds applied on top of the plugin's declared limits.
const (
	// TimeoutCap bounds one invocation: the deadline is min(declared timeout, TimeoutCap).
	TimeoutCap = 2 * time.Minute
	// MaxManifestBytes bounds the stored normalized Manifest, like any canonical
	// object the engine reads back.
	MaxManifestBytes = 2 << 20
	// referenceMargin keeps the signed input reference valid past the deadline.
	referenceMargin = time.Minute
)

// Store keeps the durable normalizer output of each Record Version.
type Store interface {
	content.NormalizationStore
	// SaveNormalized records n for a Version unless one is already recorded,
	// and returns the recorded output.
	SaveNormalized(ctx context.Context, org, versionID string, n content.Normalized) (content.Normalized, error)
}

// Signer issues a short-lived signed GET reference to a stored object.
type Signer interface {
	PresignGet(ctx context.Context, objectKey string, ttl time.Duration) (string, time.Time, error)
}

// Plugin is the protocol client of the pinned plugin.
type Plugin interface {
	CheckDiscovery(ctx context.Context) error
	Normalize(ctx context.Context, request []byte, oc plugins.OutputContext) (pluginhttp.Response, error)
}

// TerminalError is a normalization failure retrying cannot fix. Code is the
// Receipt diagnostic code.
type TerminalError struct {
	Code string
	Err  error
}

func (e *TerminalError) Error() string { return e.Code + ": " + e.Err.Error() }
func (e *TerminalError) Unwrap() error { return e.Err }

// Service runs one normalization per accepted routed Blob Version.
type Service struct {
	Content content.Service
	Store   Store
	Signer  Signer
	Plugin  Plugin
	// Pin is the startup-pinned plugin; nil pins none.
	Pin *plugins.Pin
}

// IdempotencyKey is the stable key of one logical invocation: the Plugin
// Generation placeholder, the Contribution, the Organization, the Record
// Version and the input checksum.
func IdempotencyKey(generation, contribution, org, versionID, inputSHA256 string) string {
	b, _ := json.Marshal([]string{generation, contribution, org, versionID, inputSHA256})
	return "nk_" + content.Hash(b)
}

func invocationID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "inv_" + hex.EncodeToString(b[:])
}

// Normalize invokes the pinned normalizer for the receipt's routed Blob and
// records its validated output. It does nothing for other content, for a
// resolved receipt, or when the Version already has a recorded output.
func (s Service) Normalize(ctx context.Context, org, receiptID string) error {
	work, done, err := s.Content.Repository.Work(ctx, org, receiptID)
	if err != nil || done {
		return err
	}
	c := work.Command
	if c.Content.Kind != "blob" {
		return nil
	}
	if _, found, err := s.Store.Normalized(ctx, org, work.VersionID); err != nil || found {
		return err
	}
	retry := func(code string, err error) error {
		_ = s.Content.Repository.Progress(ctx, org, receiptID, "retrying", code)
		return fmt.Errorf("%s: %w", code, err)
	}
	terminal := func(code string, err error) error {
		_ = s.Content.Repository.Progress(ctx, org, receiptID, "blocked", code)
		return &TerminalError{Code: code, Err: err}
	}
	if !s.Pin.Routed(c.Content.MediaType) {
		return terminal("normalizer_unrouted", fmt.Errorf("no normalizer route for %q", c.Content.MediaType))
	}
	normalizer := s.Pin.Manifest.Contributions.Normalizer
	if err := s.Content.Repository.Progress(ctx, org, receiptID, "running", ""); err != nil {
		return err
	}
	input, err := s.Content.BlobSource.VerifiedBlob(ctx, org, c.Content.BlobID)
	if errors.Is(err, content.ErrUnverifiedBlob) || (err == nil && (input.Blob.SHA256 != c.Content.BlobSHA256 || input.MediaType != c.Content.MediaType)) {
		return terminal("input_unverified", errors.New("the input Blob is no longer the verified accepted input"))
	}
	if err != nil {
		return retry("blob_verification_unavailable", err)
	}
	if err := s.Plugin.CheckDiscovery(ctx); err != nil {
		return retry("plugin_unavailable", err)
	}
	timeout := time.Duration(normalizer.TimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > TimeoutCap {
		timeout = TimeoutCap
	}
	signed, expires, err := s.Signer.PresignGet(ctx, input.Blob.Key, timeout+referenceMargin)
	if err != nil {
		return retry("blob_reference_unavailable", err)
	}
	key := IdempotencyKey(s.Pin.Generation(), Contribution, org, work.VersionID, input.Blob.SHA256)
	provenance := content.Normalization{PluginID: s.Pin.Manifest.ID, PluginVersion: s.Pin.Manifest.Version, PluginAPI: plugins.PluginAPIVersion, Contribution: Contribution, InvocationID: invocationID(), IdempotencyKey: key, InputSHA256: input.Blob.SHA256}
	request, err := buildRequest(work, input, provenance, signed, expires, s.Pin.Configuration)
	if err != nil {
		return terminal("normalizer_request_invalid", err)
	}
	// The plugin output is judged like the Contract Runner does: response
	// bound, schema, kind "manifest" rules, max_parts and input-only Blob Parts.
	// A verification outage is not a verdict on the output: it retries.
	var outage error
	oc := plugins.OutputContext{Manifest: &s.Pin.Manifest, Input: plugins.InputBlob{BlobID: input.ID, MediaType: input.MediaType, SHA256: input.Blob.SHA256},
		VerifyBlob: func(ctx context.Context, id string) (plugins.VerifiedBlob, error) {
			v, err := s.Content.BlobSource.VerifiedBlob(ctx, org, id)
			if err != nil && !errors.Is(err, content.ErrUnverifiedBlob) {
				outage = err
			}
			return plugins.VerifiedBlob{ID: v.ID, MediaType: v.MediaType, SHA256: v.Blob.SHA256}, err
		}}
	invoke, cancel := context.WithTimeout(ctx, timeout)
	response, err := s.Plugin.Normalize(invoke, request, oc)
	cancel()
	if outage != nil {
		return retry("blob_verification_unavailable", outage)
	}
	var declared *pluginhttp.PluginError
	var invalid *pluginhttp.InvalidOutput
	switch {
	case errors.As(err, &declared) && declared.Retryable:
		return retry("normalizer_retryable_error", err)
	case errors.As(err, &declared):
		return terminal("normalizer_failed", err)
	case errors.As(err, &invalid):
		return terminal("normalizer_invalid_output", err)
	case err != nil:
		return retry("plugin_unavailable", err)
	}
	// Extensions, top-level and on Parts, were checked against the namespaces,
	// schema versions and schemas the pinned manifest declares: Part ones stay
	// in the Manifest, top-level ones are recorded beside it and published on
	// the Version.
	manifest := response.Manifest
	for i, p := range manifest.Parts {
		if p.Content.Kind == "blob" {
			// Checked to be the input Blob: record its verified checksum.
			manifest.Parts[i].Content.BlobSHA256 = input.Blob.SHA256
		}
	}
	manifest.Kind = "manifest"
	raw, err := json.Marshal(manifest)
	if err != nil {
		return terminal("normalizer_invalid_output", err)
	}
	if len(raw) > MaxManifestBytes {
		return terminal("normalizer_invalid_output", fmt.Errorf("the normalized Manifest exceeds %d bytes", MaxManifestBytes))
	}
	blob, err := s.Content.Blobs.Put(ctx, org, raw)
	if err != nil {
		return retry("blob_verification_unavailable", err)
	}
	stored, err := s.Store.SaveNormalized(ctx, org, work.VersionID, content.Normalized{Manifest: blob, Provenance: provenance, Extensions: response.Extensions})
	if err != nil {
		return retry("normalization_store_unavailable", err)
	}
	// The same logical invocation must converge: divergent output for the same
	// key is a normalizer_conflict and never overwrites the recorded Manifest,
	// which is the one the Version publishes.
	if stored.Provenance.IdempotencyKey == key && stored.Manifest.SHA256 != blob.SHA256 {
		slog.Warn("normalizer_conflict: divergent output for one idempotency key; the recorded Manifest is kept", "receipt_id", receiptID, "version_id", work.VersionID, "plugin_id", s.Pin.Manifest.ID)
	}
	return nil
}

type source struct {
	CorpusID  string `json:"corpus_id"`
	Namespace string `json:"namespace"`
	RecordKey string `json:"record_key"`
}

type reference struct {
	Kind      string `json:"kind"`
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}

type inputBlob struct {
	BlobID    string    `json:"blob_id"`
	MediaType string    `json:"media_type"`
	SizeBytes int64     `json:"size_bytes"`
	SHA256    string    `json:"sha256"`
	Reference reference `json:"reference"`
}

type request struct {
	InvocationID    string             `json:"invocation_id"`
	IdempotencyKey  string             `json:"idempotency_key"`
	Contribution    string             `json:"contribution"`
	OrganizationID  string             `json:"organization_id"`
	CorpusID        string             `json:"corpus_id"`
	RecordID        string             `json:"record_id"`
	RecordVersionID string             `json:"record_version_id"`
	Source          source             `json:"source"`
	Input           inputBlob          `json:"input"`
	Extensions      content.Extensions `json:"extensions,omitempty"`
	Provenance      map[string]any     `json:"provenance,omitempty"`
	Configuration   json.RawMessage    `json:"configuration"`
}

// buildRequest builds the immutable invocation context and checks it against
// the Plugin Protocol request schema. The body is never inline.
func buildRequest(work content.Work, input content.VerifiedBlob, p content.Normalization, signed string, expires time.Time, configuration json.RawMessage) ([]byte, error) {
	c := work.Command
	submitted := map[string]any{}
	for _, key := range []string{"source_blob_ids", "producer", "producer_version"} {
		if v, ok := c.Provenance[key]; ok {
			submitted[key] = v
		}
	}
	body, err := json.Marshal(request{
		InvocationID: p.InvocationID, IdempotencyKey: p.IdempotencyKey, Contribution: Contribution,
		OrganizationID: work.Organization, CorpusID: c.Source.CorpusID, RecordID: work.RecordID, RecordVersionID: work.VersionID,
		Source: source{CorpusID: c.Source.CorpusID, Namespace: c.Source.Namespace, RecordKey: c.Source.RecordKey},
		Input: inputBlob{BlobID: input.ID, MediaType: input.MediaType, SizeBytes: input.Blob.Size, SHA256: input.Blob.SHA256,
			Reference: reference{Kind: "signed_url", URL: signed, ExpiresAt: expires.UTC().Format(time.RFC3339)}},
		Extensions: c.Extensions, Provenance: submitted, Configuration: configuration,
	})
	if err != nil {
		return nil, err
	}
	if issues := plugins.ValidateDocument("normalizer-request.schema.json", body); len(issues) > 0 {
		return nil, fmt.Errorf("normalizer request violates the protocol: %s %s: %s", issues[0].Code, issues[0].Path, issues[0].Message)
	}
	return body, nil
}
