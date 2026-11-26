package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

const maxSegments = 256

type pluginConfig struct {
	Backend          string `json:"backend"`
	BaseURL          string `json:"base_url"`
	Auth             string `json:"auth"`
	Model            string `json:"model"`
	RegionMode       string `json:"region_mode"`
	GridRows         int    `json:"grid_rows"`
	GridCols         int    `json:"grid_cols"`
	MaxImageBytes    int64  `json:"max_image_bytes"`
	RequestTimeoutMS int    `json:"request_timeout_ms"`
}

func (c pluginConfig) resolvedModel() string {
	if c.Model != "" {
		return c.Model
	}
	if c.Backend == "http" {
		return httpModelID
	}
	return builtinModelID
}

func decodePluginConfig(raw json.RawMessage) (pluginConfig, error) {
	cfg := pluginConfig{
		Backend:          "builtin",
		Auth:             "none",
		RegionMode:       "full",
		GridRows:         2,
		GridCols:         2,
		MaxImageBytes:    25 << 20,
		RequestTimeoutMS: 3000,
	}
	if len(raw) > 0 && string(raw) != "{}" {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return cfg, quivrplugin.TerminalIngestError("invalid_configuration", err.Error())
		}
	}
	if cfg.Backend != "builtin" && cfg.Backend != "http" {
		return cfg, quivrplugin.TerminalIngestError("invalid_configuration", "backend must be builtin or http")
	}
	if cfg.Backend == "http" && cfg.BaseURL == "" {
		return cfg, quivrplugin.TerminalIngestError("invalid_configuration", "base_url is required when backend is http")
	}
	if cfg.Auth != "none" && cfg.Auth != "bearer" && cfg.Auth != "api-key" {
		return cfg, quivrplugin.TerminalIngestError("invalid_configuration", "auth must be none, bearer, or api-key")
	}
	if cfg.RegionMode != "full" && cfg.RegionMode != "grid" {
		return cfg, quivrplugin.TerminalIngestError("invalid_configuration", "region_mode must be full or grid")
	}
	if cfg.GridRows < 1 || cfg.GridRows > 4 || cfg.GridCols < 1 || cfg.GridCols > 4 {
		return cfg, quivrplugin.TerminalIngestError("invalid_configuration", "grid_rows and grid_cols must be in 1..4")
	}
	if cfg.MaxImageBytes < 1024 || cfg.MaxImageBytes > 25<<20 {
		return cfg, quivrplugin.TerminalIngestError("invalid_configuration", "max_image_bytes out of bounds")
	}
	if cfg.RequestTimeoutMS < 500 || cfg.RequestTimeoutMS > 5000 {
		return cfg, quivrplugin.TerminalIngestError("invalid_configuration", "request_timeout_ms must be in 500..5000")
	}
	return cfg, nil
}

type crossModalPlugin struct{}

// Normalize verifies image integrity, preserves the verified input Blob Part,
// and attaches deterministic image dimension metadata under crossmodal.image.metadata.
func (crossModalPlugin) Normalize(ctx context.Context, req *quivrplugin.NormalizerRequest) (*quivrplugin.NormalizerResponse, error) {
	cfg, err := decodePluginConfig(req.Configuration)
	if err != nil {
		return nil, err
	}
	if req.Input.SizeBytes > cfg.MaxImageBytes {
		return nil, quivrplugin.TerminalError("image_too_large", fmt.Sprintf("image exceeds max_image_bytes (%d)", cfg.MaxImageBytes))
	}
	data, err := req.ReadInput(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, quivrplugin.RetryableError("input_unavailable", "image read cancelled or timed out")
		}
		return nil, err
	}
	decoded, err := decodeImage(data, req.Input.MediaType)
	if err != nil {
		return nil, err
	}
	meta := map[string]any{
		"width":        decoded.width,
		"height":       decoded.height,
		"format":       decoded.format,
		"media_type":   req.Input.MediaType,
		"aspect_ratio": round6(float64(decoded.width) / float64(decoded.height)),
		"sha256":       req.Input.SHA256,
	}
	if len(decoded.textChunks) > 0 {
		meta["text_metadata"] = decoded.textChunks
	}
	return &quivrplugin.NormalizerResponse{
		Manifest: quivrplugin.NormalizedManifest{
			Kind: "manifest",
			Parts: []quivrplugin.NormalizedPart{{
				Key:  "image",
				Role: "image",
				Content: quivrplugin.NormalizedContent{
					Kind:      "blob",
					BlobID:    req.Input.BlobID,
					MediaType: req.Input.MediaType,
				},
			}},
		},
		Extensions: map[string]quivrplugin.Extension{
			"crossmodal.image.metadata": {
				SchemaVersion: "1",
				Data:          meta,
			},
		},
	}, nil
}

// SegmentAndEmbed segments verified image Blob Parts into full-frame or grid
// regions with normalized bounding-box provenance and embeds each region into
// the requested Cross-modal Vector Space.
func (crossModalPlugin) SegmentAndEmbed(ctx context.Context, req *quivrplugin.IngestRequest) ([]quivrplugin.Segment, error) {
	cfg, err := decodePluginConfig(req.Configuration)
	if err != nil {
		return nil, err
	}
	if len(req.BlobParts) == 0 {
		return nil, quivrplugin.TerminalIngestError("no_image_part", "crossmodal.image requires at least one image Blob Part")
	}
	encoders := make(map[string]crossModalEncoder, len(req.Spaces))
	provenanceModel := cfg.resolvedModel()
	for _, space := range req.Spaces {
		enc, modelID, err := newEncoderForSpace(cfg, space)
		if err != nil {
			return nil, err
		}
		encoders[space] = enc
		provenanceModel = modelID
	}

	var segments []quivrplugin.Segment
	for _, bp := range req.BlobParts {
		if !strings.HasPrefix(strings.ToLower(bp.MediaType), "image/") {
			continue
		}
		data, err := bp.ReadBlob(ctx, cfg.MaxImageBytes)
		if err != nil {
			if ctx.Err() != nil {
				return nil, quivrplugin.RetryableIngestError("input_unavailable", "image read cancelled or timed out")
			}
			return nil, err
		}
		decoded, err := decodeImage(data, bp.MediaType)
		if err != nil {
			return nil, err
		}
		regions := planRegions(decoded.width, decoded.height, cfg)
		if len(segments)+len(regions) > maxSegments {
			return nil, quivrplugin.TerminalIngestError("too_many_segments", fmt.Sprintf("image regions exceed max_segments (%d)", maxSegments))
		}
		for _, pr := range regions {
			vectors := make(map[string][]float32, len(req.Spaces))
			for _, space := range req.Spaces {
				vec, err := encoders[space].embedRegion(ctx, decoded, data, pr)
				if err != nil {
					return nil, err
				}
				vectors[space] = vec
			}
			region := pr.region
			segments = append(segments, quivrplugin.Segment{
				PartKey: bp.Key,
				Start:   0,
				End:     0,
				Region:  &region,
				Vectors: vectors,
				Provenance: map[string]any{
					"backend":      cfg.Backend,
					"model":        provenanceModel,
					"region_mode":  cfg.RegionMode,
					"media_type":   bp.MediaType,
					"format":       decoded.format,
					"image_width":  decoded.width,
					"image_height": decoded.height,
					"blob_sha256":  bp.SHA256,
				},
			})
		}
	}
	if len(segments) == 0 {
		return nil, quivrplugin.TerminalIngestError("no_image_part", "no supported image Blob Part was found in the Version")
	}
	return segments, nil
}

// EmbedQuery encodes a natural-language text query into the Cross-modal Vector Space.
func (crossModalPlugin) EmbedQuery(ctx context.Context, req *quivrplugin.QueryRequest) ([]float32, error) {
	cfg, err := decodePluginConfig(req.Configuration)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Query.Text) == "" {
		return nil, quivrplugin.TerminalIngestError("empty_query", "query text must be non-empty")
	}
	enc, _, err := newEncoderForSpace(cfg, req.Space)
	if err != nil {
		return nil, err
	}
	return enc.embedQuery(ctx, req.Query.Text)
}
