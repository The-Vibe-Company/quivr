package quivrplugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"time"
)

// Normalizer turns an input Blob into a source-faithful Manifest.
type Normalizer interface {
	Normalize(context.Context, *NormalizerRequest) (*NormalizerResponse, error)
}

// NormalizerContribution declares routed media types and invocation bounds.
type NormalizerContribution struct {
	MediaTypes []string `json:"media_types"`
	TimeoutMS  int      `json:"timeout_ms"`
	Limits     struct {
		MaxResponseBytes int `json:"max_response_bytes"`
		MaxParts         int `json:"max_parts"`
	} `json:"limits"`
}

// NormalizerRequest carries immutable input metadata and its read reference.
type NormalizerRequest struct {
	InvocationID    string               `json:"invocation_id"`
	IdempotencyKey  string               `json:"idempotency_key"`
	Contribution    string               `json:"contribution"`
	OrganizationID  string               `json:"organization_id"`
	CorpusID        string               `json:"corpus_id"`
	RecordID        string               `json:"record_id"`
	RecordVersionID string               `json:"record_version_id"`
	Source          RelationKey          `json:"source"`
	Input           NormalizerInput      `json:"input"`
	Extensions      map[string]Extension `json:"extensions,omitempty"`
	Provenance      json.RawMessage      `json:"provenance,omitempty"`
	Configuration   json.RawMessage      `json:"configuration"`
	logger          *slog.Logger
}

// NormalizerInput names an input Blob; Reference is a file or signed HTTP URL.
type NormalizerInput struct {
	BlobID    string `json:"blob_id"`
	MediaType string `json:"media_type"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	Reference struct {
		Kind      string `json:"kind"`
		URL       string `json:"url"`
		ExpiresAt string `json:"expires_at,omitempty"`
	} `json:"reference"`
}

func (r *NormalizerRequest) Logger() *slog.Logger { return r.logger }

// ReadInput reads at most the declared size plus one byte, then verifies size
// and SHA-256. HTTP reads honor ctx; input URLs never appear in error messages.
func (r *NormalizerRequest) ReadInput(ctx context.Context) ([]byte, error) {
	var reader io.ReadCloser
	if r.Input.Reference.Kind == "file" {
		u, err := url.Parse(r.Input.Reference.URL)
		if err != nil || (u.Host != "" && u.Host != "localhost") {
			return nil, TerminalError("invalid_input", "invalid local input reference")
		}
		f, err := os.Open(u.Path)
		if err != nil {
			return nil, RetryableError("input_unavailable", "cannot read input")
		}
		reader = f
	} else {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.Input.Reference.URL, nil)
		if err != nil {
			return nil, TerminalError("invalid_input", "invalid input reference")
		}
		InjectTrace(ctx, req.Header)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, RetryableError("input_unavailable", "cannot read input")
		}
		reader = resp.Body
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			reader.Close()
			if resp.StatusCode >= 500 || slices.Contains([]int{401, 403, 408, 425, 429}, resp.StatusCode) {
				return nil, RetryableError("input_unavailable", "input service unavailable")
			}
			return nil, TerminalError("input_unavailable", "input service refused the read")
		}
	}
	defer reader.Close()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.Input.SizeBytes < 0 || r.Input.SizeBytes == math.MaxInt64 {
		return nil, TerminalError("invalid_input", "invalid input size")
	}
	b, err := io.ReadAll(io.LimitReader(reader, r.Input.SizeBytes+1))
	if err != nil {
		return nil, RetryableError("input_unavailable", "cannot read input")
	}
	sum := sha256.Sum256(b)
	if int64(len(b)) != r.Input.SizeBytes || hex.EncodeToString(sum[:]) != r.Input.SHA256 {
		return nil, TerminalError("input_integrity", "input size or SHA-256 differs")
	}
	return b, nil
}

// NormalizedContent supports text and references to existing verified Blobs.
type NormalizedContent struct {
	Kind      string `json:"kind"`
	Text      string `json:"text,omitempty"`
	BlobID    string `json:"blob_id,omitempty"`
	MediaType string `json:"media_type,omitempty"`
}
type NormalizedPart struct {
	Key        string               `json:"key"`
	ParentKey  string               `json:"parent_key,omitempty"`
	Role       string               `json:"role"`
	Content    NormalizedContent    `json:"content"`
	Extensions map[string]Extension `json:"extensions,omitempty"`
}
type NormalizedManifest struct {
	Kind      string           `json:"kind"`
	Parts     []NormalizedPart `json:"parts"`
	Relations []Relation       `json:"relations,omitempty"`
}
type NormalizerWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type NormalizerResponse struct {
	Manifest   NormalizedManifest   `json:"manifest"`
	Extensions map[string]Extension `json:"extensions,omitempty"`
	Language   string               `json:"language,omitempty"`
	Warnings   []NormalizerWarning  `json:"warnings,omitempty"`
}

// ContributionError classifies a normalizer or subscription failure.
// It is shared with ingestion to keep error envelopes uniform.
type ContributionError = IngestError

func TerminalError(code, message string) *ContributionError {
	return TerminalIngestError(code, message)
}
func RetryableError(code, message string) *ContributionError {
	return RetryableIngestError(code, message)
}

func (p *Plugin) Normalizer(impl Normalizer) error {
	if p.m.Normalizer == nil {
		return fmt.Errorf("the manifest declares no normalizer Contribution")
	}
	p.normalizer = impl
	return nil
}

func (p *Plugin) serveNormalize(w http.ResponseWriter, r *http.Request) {
	var req NormalizerRequest
	if !p.readIngestion(w, r, "plugins/v0/normalizer-request.schema.json", &req) {
		return
	}
	if !slices.Contains(p.m.Normalizer.MediaTypes, req.Input.MediaType) {
		refuse(w, 400, "unsupported_media_type", "media type is not declared", Credential{})
		return
	}
	req.logger = p.requestLogger(r.Context(), Credential{}, req.InvocationID).With("idempotency_key", req.IdempotencyKey)
	defer p.ingestPanic(w, req.logger)
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(p.m.Normalizer.TimeoutMS)*time.Millisecond)
	defer cancel()
	out, err := p.normalizer.Normalize(ctx, &req)
	if err != nil {
		p.ingestFail(w, req.logger, err)
		return
	}
	if out != nil && len(out.Manifest.Parts) > p.m.Normalizer.Limits.MaxParts {
		refuse(w, 500, "invalid_response", "parts exceed max_parts", Credential{})
		return
	}
	p.contributionResponse(w, out, "normalizer-response.schema.json", p.m.Normalizer.Limits.MaxResponseBytes)
}

func (p *Plugin) contributionResponse(w http.ResponseWriter, out any, schema string, limit int) {
	b, err := json.Marshal(out)
	if err != nil {
		refuse(w, 500, "invalid_response", "response is not JSON encodable", Credential{})
		return
	}
	if len(b) > limit {
		refuse(w, 500, "response_too_large", "response exceeds max_response_bytes", Credential{})
		return
	}
	if err := validate("plugins/v0/"+schema, b); err != nil {
		refuse(w, 500, "invalid_response", err.Error(), Credential{})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}
