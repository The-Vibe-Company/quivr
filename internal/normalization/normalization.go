// Package normalization invokes the pinned external normalizer for accepted
// routed Blobs, between acceptance and publication. It validates the output
// with the engine's kind "manifest" rules and records one durable outcome per
// Record Version, so re-running the invocation converges and publication,
// rebuilds and retrieval generations read the stored outcome without ever
// calling the plugin again.
//
// Failures are classified once (see Normalize): plugin unavailability retries
// without limit and never quarantines; plugin-declared retryable errors and
// invocation timeouts retry up to the declared retry budget, capped by the
// engine; terminal errors, invalid output and an exhausted budget record a
// failed outcome, which publication turns into a quarantined Version, or,
// on an optional route, a fallback to the built-in text path.
package normalization

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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
	// MaxAttemptsCap bounds the declared retry.max_attempts: the number of
	// invocations of one Version that may end in a retryable error or a
	// timeout before it is quarantined.
	MaxAttemptsCap = 5
	// MaxManifestBytes bounds the stored normalized Manifest, like any canonical
	// object the engine reads back.
	MaxManifestBytes = 2 << 20
	// referenceMargin keeps the signed input reference valid past the deadline.
	referenceMargin = time.Minute
)

// Public diagnostic codes of a normalization that could not publish the
// plugin's output. They are documented in contracts/http/v0/openapi.yaml.
const (
	CodeFailed           = "normalizer_failed"
	CodeInvalidOutput    = "normalizer_invalid_output"
	CodeTimeout          = "normalizer_timeout"
	CodeRetriesExhausted = "normalizer_retries_exhausted"
	CodeInputUnverified  = "input_unverified"
	CodeRequestInvalid   = "normalizer_request_invalid"
)

// Receipt progress codes while normalization retries.
const (
	codeUnavailable = "plugin_unavailable"
	codeRetrying    = "normalizer_retrying"
)

// Store keeps the durable normalization outcome of each Record Version.
type Store interface {
	content.NormalizationStore
	// SaveNormalized records n for a Version unless an outcome is already
	// recorded, and returns the recorded outcome.
	SaveNormalized(ctx context.Context, org, versionID string, n content.Normalized) (content.Normalized, error)
	// RecordConflict records, once, a divergent output for the recorded
	// idempotency key. It never changes the recorded outcome.
	RecordConflict(ctx context.Context, org, versionID string, c content.NormalizationConflict) error
	// CountAttempt durably counts one invocation that ended in a retryable
	// error or a timeout and returns the Version's count.
	CountAttempt(ctx context.Context, org, versionID, code, invocationID string) (int, error)
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

// Service runs one normalization per accepted routed Blob Version.
type Service struct {
	Content content.Service
	Store   Store
	Signer  Signer
	// Plugin, when set, is the protocol client for every resolved pin
	// (tests); nil talks to each resolved pin with pluginhttp.
	Plugin Plugin
	// Pin routes a media type to its normalizer in the plan the work is
	// pinned to (plugins.Live); nil routes none.
	Pin Router
}

// Router resolves the pinned normalizer of an accepted Blob media type, in
// the Pipeline Plan of the work ctx carries (plugins.Live).
type Router interface {
	Normalizer(ctx context.Context, mediaType string) (*plugins.Pin, plugins.RouteConfig, bool)
}

func (s Service) client(pin *plugins.Pin) Plugin {
	if s.Plugin != nil {
		return s.Plugin
	}
	return pluginhttp.Client{Pin: pin}
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

// MaxAttempts is the retry budget of a normalizer: its declared
// retry.max_attempts, capped by MaxAttemptsCap.
func MaxAttempts(n *plugins.Normalizer) int {
	attempts := plugins.DefaultMaxAttempts
	if n != nil && n.Retry.MaxAttempts > 0 {
		attempts = n.Retry.MaxAttempts
	}
	return min(attempts, MaxAttemptsCap)
}

// invocation is one normalization attempt of a Version.
type invocation struct {
	org, receiptID string
	work           content.Work
	optional       bool
	budget         int
	provenance     content.Normalization
	// plan names the plan of work stopped because the normalizer left it.
	plan string
}

// Normalize invokes the pinned normalizer for the receipt's routed Blob and
// records its outcome. It does nothing for other content, for a resolved
// receipt, for a Version that already has an outcome, and it never calls the
// plugin for a withdrawn Record, a superseded Version or a media type that is
// no longer routed: publication decides those. A nil error means an outcome is
// recorded or none is needed; an error means the activity must retry.
func (s Service) Normalize(ctx context.Context, org, receiptID string) error {
	return s.normalize(ctx, org, receiptID, false)
}

// Renormalize is Normalize for a Version already published quarantined, whose
// failed outcome a quarantine reprocess moved aside: it records a new outcome
// for it, with the normalizer the plan of ctx routes its media type to.
func (s Service) Renormalize(ctx context.Context, org, receiptID string) error {
	return s.normalize(ctx, org, receiptID, true)
}

func (s Service) normalize(ctx context.Context, org, receiptID string, published bool) error {
	work, done, err := s.Content.Repository.Work(ctx, org, receiptID)
	if err != nil || (done && !published) {
		return err
	}
	c := work.Command
	if c.Content.Kind != "blob" {
		return nil
	}
	if _, found, err := s.Store.Normalized(ctx, org, work.VersionID); err != nil || found {
		return err
	}
	if s.Content.Supersession != nil {
		withdrawn, superseded, err := s.Content.Supersession.Superseded(ctx, org, work.RecordID, work.VersionID)
		if err != nil {
			return s.retry(ctx, org, receiptID, "normalization_store_unavailable", err)
		}
		if withdrawn || superseded {
			return nil
		}
	}
	if s.Pin == nil {
		return nil
	}
	pin, route, routed := s.Pin.Normalizer(ctx, c.Content.MediaType)
	if !routed {
		return nil
	}
	plugin := s.client(pin)
	normalizer := pin.Manifest.Contributions.Normalizer
	inv := invocation{org: org, receiptID: receiptID, work: work, optional: route.Mode == plugins.RouteOptional, budget: MaxAttempts(normalizer),
		provenance: content.Normalization{PluginID: pin.Manifest.ID, PluginVersion: pin.Manifest.Version, PluginAPI: pin.PluginAPI(), Contribution: Contribution, InvocationID: invocationID(),
			IdempotencyKey: IdempotencyKey(pin.Generation(), Contribution, org, work.VersionID, c.Content.BlobSHA256), InputSHA256: c.Content.BlobSHA256}}
	input, err := s.Content.BlobSource.VerifiedBlob(ctx, org, c.Content.BlobID)
	if errors.Is(err, content.ErrUnverifiedBlob) || (err == nil && (input.Blob.SHA256 != c.Content.BlobSHA256 || input.MediaType != c.Content.MediaType)) {
		return s.fail(ctx, inv, CodeInputUnverified, "The input Blob is no longer the verified accepted input.", false)
	}
	if err != nil {
		return s.retry(ctx, org, receiptID, "blob_verification_unavailable", err)
	}
	if err := plugin.CheckDiscovery(ctx); err != nil {
		return s.unavailable(ctx, inv, pin, err)
	}
	// Running only once the plugin answers: an outage keeps the receipt's
	// retrying plugin_unavailable diagnostic between attempts.
	if err := s.Content.Repository.Progress(ctx, org, receiptID, "running", ""); err != nil {
		return err
	}
	timeout := time.Duration(normalizer.TimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > TimeoutCap {
		timeout = TimeoutCap
	}
	signed, expires, err := s.Signer.PresignGet(ctx, input.Blob.Key, timeout+referenceMargin)
	if err != nil {
		return s.retry(ctx, org, receiptID, "blob_reference_unavailable", err)
	}
	request, err := buildRequest(work, input, inv.provenance, signed, expires, pin.Configuration)
	if err != nil {
		return s.fail(ctx, inv, CodeRequestInvalid, err.Error(), false)
	}
	// The plugin output is judged like the Contract Runner does: response
	// bound, schema, kind "manifest" rules, max_parts and input-only Blob Parts.
	// A verification outage is not a verdict on the output: it retries.
	var outage error
	oc := plugins.OutputContext{Manifest: &pin.Manifest, Input: plugins.InputBlob{BlobID: input.ID, MediaType: input.MediaType, SHA256: input.Blob.SHA256},
		VerifyBlob: func(ctx context.Context, id string) (plugins.VerifiedBlob, error) {
			v, err := s.Content.BlobSource.VerifiedBlob(ctx, org, id)
			if err != nil && !errors.Is(err, content.ErrUnverifiedBlob) {
				outage = err
			}
			return plugins.VerifiedBlob{ID: v.ID, MediaType: v.MediaType, SHA256: v.Blob.SHA256}, err
		}}
	invoke, cancel := context.WithTimeout(ctx, timeout)
	response, err := plugin.Normalize(invoke, request, oc)
	timedOut := errors.Is(invoke.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	cancel()
	if outage != nil {
		return s.retry(ctx, org, receiptID, "blob_verification_unavailable", outage)
	}
	var declared *pluginhttp.PluginError
	var invalid *pluginhttp.InvalidOutput
	switch {
	case errors.As(err, &declared) && declared.Retryable:
		return s.counted(ctx, inv, CodeRetriesExhausted, fmt.Sprintf("The normalizer kept answering the retryable error %s: %s", declared.Code, declared.Message))
	case errors.As(err, &declared):
		return s.fail(ctx, inv, CodeFailed, fmt.Sprintf("The normalizer refused the input with %s: %s", declared.Code, declared.Message), false)
	case errors.As(err, &invalid):
		return s.fail(ctx, inv, CodeInvalidOutput, invalid.Error(), false)
	case err != nil && timedOut:
		return s.counted(ctx, inv, CodeTimeout, fmt.Sprintf("The normalizer did not answer within %s.", timeout))
	case err != nil:
		return s.unavailable(ctx, inv, pin, err)
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
		return s.fail(ctx, inv, CodeInvalidOutput, err.Error(), false)
	}
	if len(raw) > MaxManifestBytes {
		return s.fail(ctx, inv, CodeInvalidOutput, fmt.Sprintf("the normalized Manifest exceeds %d bytes", MaxManifestBytes), false)
	}
	blob, err := s.Content.Blobs.Put(ctx, org, raw)
	if err != nil {
		return s.retry(ctx, org, receiptID, "blob_verification_unavailable", err)
	}
	stored, err := s.Store.SaveNormalized(ctx, org, work.VersionID, content.Normalized{Outcome: content.OutcomeNormalized, Manifest: blob, Provenance: inv.provenance, InputBlobID: input.ID, Extensions: response.Extensions})
	if err != nil {
		return s.retry(ctx, org, receiptID, "normalization_store_unavailable", err)
	}
	// The same logical invocation must converge: divergent output for the same
	// key is a normalizer_conflict. It never overwrites the recorded outcome,
	// which is the one the Version publishes, and it is recorded so that the
	// Version read shows it.
	if stored.Provenance.IdempotencyKey == inv.provenance.IdempotencyKey && stored.Outcome == content.OutcomeNormalized && (stored.Manifest.SHA256 != blob.SHA256 || !sameExtensions(stored.Extensions, response.Extensions)) {
		slog.Warn("normalizer_conflict: divergent output for one idempotency key; the recorded Manifest is kept", "receipt_id", receiptID, "version_id", work.VersionID, "plugin_id", inv.provenance.PluginID, "invocation_id", inv.provenance.InvocationID)
		if err := s.Store.RecordConflict(ctx, org, work.VersionID, content.NormalizationConflict{InvocationID: inv.provenance.InvocationID, ManifestSHA256: blob.SHA256}); err != nil {
			return s.retry(ctx, org, receiptID, "normalization_store_unavailable", err)
		}
	}
	return nil
}

// sameExtensions compares two outputs' top-level extensions by their
// canonical JSON (map keys are sorted), empty and absent being equal.
func sameExtensions(a, b content.Extensions) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(x) == string(y)
}

// maxMessageRunes bounds a stored and published failure message.
const maxMessageRunes = 1000

// boundedMessage makes a failure message, which may quote the plugin's own
// error text, storable and publishable: valid UTF-8 without NUL, at most
// maxMessageRunes characters, never empty.
func boundedMessage(message string) string {
	message = strings.ReplaceAll(strings.ToValidUTF8(message, "�"), "\x00", "")
	if runes := []rune(message); len(runes) > maxMessageRunes {
		message = string(runes[:maxMessageRunes])
	}
	if strings.TrimSpace(message) == "" {
		message = "The normalizer failed."
	}
	return message
}

// unavailable retries an unreachable normalizer without limit, unless the
// work is pinned to a plan the normalizer has left: past the work's budget,
// the failure is recorded with the plan, and publication quarantines the
// Version rather than moving it to another normalizer (plugins.Unreachable).
func (s Service) unavailable(ctx context.Context, inv invocation, pin *plugins.Pin, err error) error {
	reason, countErr := plugins.Unreachable(ctx, pin, Contribution)
	switch {
	case countErr != nil:
		return s.retry(ctx, inv.org, inv.receiptID, "normalization_store_unavailable", countErr)
	case reason != nil:
		inv.plan = reason.Plan
		return s.fail(ctx, inv, reason.Code, reason.Message, true)
	}
	return s.retry(ctx, inv.org, inv.receiptID, codeUnavailable, err)
}

// retry reports a failure that retrying can fix; it never records an outcome.
func (s Service) retry(ctx context.Context, org, receiptID, code string, err error) error {
	_ = s.Content.Repository.Progress(ctx, org, receiptID, "retrying", code)
	return fmt.Errorf("%s: %w", code, err)
}

// counted counts a retryable error or a timeout against the Version's retry
// budget: the activity retries until the budget is spent, then the failure is
// recorded with code (retryable, since the same input may succeed later).
func (s Service) counted(ctx context.Context, inv invocation, code, message string) error {
	attempts, err := s.Store.CountAttempt(ctx, inv.org, inv.work.VersionID, code, inv.provenance.InvocationID)
	if err != nil {
		return s.retry(ctx, inv.org, inv.receiptID, "normalization_store_unavailable", err)
	}
	if attempts < inv.budget {
		return s.retry(ctx, inv.org, inv.receiptID, codeRetrying, fmt.Errorf("attempt %d of %d: %s", attempts, inv.budget, message))
	}
	return s.fail(ctx, inv, code, fmt.Sprintf("%s (%d attempts)", message, attempts), true)
}

// fail records the outcome of a normalization that cannot publish the
// plugin's output. An optional route falls back to the built-in text path;
// otherwise, or when the built-in path refuses the bytes too, the failure is
// recorded and publication quarantines the Version. Nothing from the plugin
// is stored.
func (s Service) fail(ctx context.Context, inv invocation, code, message string, retryable bool) error {
	message = boundedMessage(message)
	slog.Warn("normalization failed", "code", code, "optional", inv.optional, "receipt_id", inv.receiptID, "version_id", inv.work.VersionID, "plugin_id", inv.provenance.PluginID, "invocation_id", inv.provenance.InvocationID)
	failure := &content.NormalizationFailure{Code: code, Message: message, Retryable: retryable, Plan: inv.plan}
	outcome := content.Normalized{Outcome: content.OutcomeFailed, Provenance: inv.provenance, InputBlobID: inv.work.Command.Content.BlobID, Failure: failure}
	if inv.optional {
		m, err := s.Content.BuiltinManifest(ctx, inv.org, inv.work.Command.Content)
		switch {
		case err == nil:
			raw, err := json.Marshal(m)
			if err != nil {
				return err
			}
			blob, err := s.Content.Blobs.Put(ctx, inv.org, raw)
			if err != nil {
				return s.retry(ctx, inv.org, inv.receiptID, "blob_verification_unavailable", err)
			}
			outcome.Outcome = content.OutcomeFallback
			outcome.Manifest = blob
			outcome.Provenance.Fallback = &content.NormalizationFallback{Code: code, Message: message}
		case !content.BuiltinRefusal(err):
			return s.retry(ctx, inv.org, inv.receiptID, "blob_verification_unavailable", err)
		}
	}
	if _, err := s.Store.SaveNormalized(ctx, inv.org, inv.work.VersionID, outcome); err != nil {
		return s.retry(ctx, inv.org, inv.receiptID, "normalization_store_unavailable", err)
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
