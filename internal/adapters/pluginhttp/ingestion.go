package pluginhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
	"github.com/The-Vibe-Company/quivr-v2/internal/processing"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
)

// Ingestor is the pinned plugin's ingestion Contribution as the engine calls
// it: segment_and_embed for the worker, embed_query for search. Every answer
// is judged with the Contract Runner's checks before anything is stored.
type Ingestor struct {
	Pin *plugins.Pin
}

var (
	_ processing.IngestionPlugin = Ingestor{}
	_ processing.Pinned          = Ingestor{}
	_ retrieval.QueryEncoder     = Ingestor{}
)

func (i Ingestor) contribution() *plugins.Ingestion { return i.Pin.Manifest.Contributions.Ingestion }

// Recipe names the plugin's segmentation by its id and version.
func (i Ingestor) Recipe() string {
	return "plugin:" + i.Pin.Manifest.ID + "@" + i.Pin.Manifest.Version
}

// Producer names the producer of the plugin's Embedding Artifacts.
func (i Ingestor) Producer() string { return i.Recipe() }

// Provenance is recorded with each Segmentation the plugin makes.
func (i Ingestor) Provenance() json.RawMessage {
	b, _ := json.Marshal(map[string]string{"plugin_id": i.Pin.Manifest.ID, "plugin_version": i.Pin.Manifest.Version})
	return b
}

// declared returns the declared space id of a space key.
func (i Ingestor) declared(key string) (string, plugins.VectorSpace, bool) {
	for id, space := range i.contribution().Spaces {
		if plugins.SpaceKey(id, space.Version) == key {
			return id, space, true
		}
	}
	return "", plugins.VectorSpace{}, false
}

// Owns reports whether a space key is one of the plugin's declared spaces.
func (i Ingestor) Owns(key string) bool {
	_, _, ok := i.declared(key)
	return ok
}

// Spaces are the space keys the pin enables, the served one first.
func (i Ingestor) Spaces() []string {
	var keys []string
	for _, s := range i.Pin.EnabledSpaces() {
		keys = append(keys, s.Key)
	}
	return keys
}

// SegmentsOnly reports whether the plugin's plugin_api range admits a
// request with no space (Plugin API 0.7).
func (i Ingestor) SegmentsOnly() bool { return plugins.SegmentsOnly(&i.Pin.Manifest) }

// VectorSpace describes one of the plugin's spaces.
func (i Ingestor) VectorSpace(key string) (content.VectorSpace, bool) {
	id, space, ok := i.declared(key)
	if !ok {
		return content.VectorSpace{}, false
	}
	return content.VectorSpace{ID: key, Manifest: plugins.SpaceManifest(i.Pin.Manifest.ID, id, space), Dimensions: space.Dimensions}, true
}

type ingestionVersion struct {
	CorpusID        string `json:"corpus_id"`
	RecordID        string `json:"record_id"`
	RecordVersionID string `json:"record_version_id"`
}

type segmentAndEmbedRequest struct {
	InvocationID   string                  `json:"invocation_id"`
	IdempotencyKey string                  `json:"idempotency_key"`
	Contribution   string                  `json:"contribution"`
	OrganizationID string                  `json:"organization_id"`
	Configuration  json.RawMessage         `json:"configuration"`
	Version        ingestionVersion        `json:"version"`
	Parts          []plugins.IngestionPart `json:"parts"`
	Spaces         []string                `json:"spaces"`
}

// refused wraps a terminal refusal as content.ErrIngestionRefused.
func refused(format string, args ...any) error {
	return fmt.Errorf("%w: %s", content.ErrIngestionRefused, fmt.Sprintf(format, args...))
}

// SegmentAndEmbed asks the plugin for a Version's segments with a vector in
// each space key; with no key, for the segments alone. A plugin that is unavailable or answers a retryable error
// is retried by the caller; a terminal error or an answer the output checks
// refuse is content.ErrIngestionRefused.
func (i Ingestor) SegmentAndEmbed(ctx context.Context, org, corpusID string, v content.Version, keys []string) ([]processing.PluginSegment, error) {
	ids := make([]string, 0, len(keys))
	byID := map[string]string{}
	for _, key := range keys {
		id, _, ok := i.declared(key)
		if !ok {
			return nil, refused("space %s is not declared by %s@%s", key, i.Pin.Manifest.ID, i.Pin.Manifest.Version)
		}
		ids = append(ids, id)
		byID[id] = key
	}
	sort.Strings(ids)
	parts := []plugins.IngestionPart{}
	for _, p := range v.Manifest.Parts {
		if p.Content.Kind == "text" {
			parts = append(parts, plugins.IngestionPart{Key: p.Key, Role: p.Role, Text: p.Content.Text})
		}
	}
	if len(parts) == 0 || len(parts) > 256 {
		return nil, refused("the Version has %d text Parts; segment_and_embed takes 1 to 256", len(parts))
	}
	identity := append([]string{i.Pin.Generation(), "segment_and_embed", org, v.ID}, ids...)
	request := segmentAndEmbedRequest{InvocationID: invocationID(), IdempotencyKey: content.StableID("ingestion", identity...), Contribution: "ingestion",
		OrganizationID: org, Configuration: i.Pin.Configuration, Version: ingestionVersion{CorpusID: corpusID, RecordID: v.RecordID, RecordVersionID: v.ID}, Parts: parts, Spaces: ids}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	view := plugins.IngestionRequestView{Parts: parts, Spaces: ids}
	m := &i.Pin.Manifest
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(i.contribution().TimeoutMS)*time.Millisecond)
	defer cancel()
	started := time.Now()
	result, err := devhost.InvokeSegmentAndEmbed(callCtx, i.Pin.Endpoint, body, plugins.IngestionMaxResponseBytes(m), func(b []byte) []plugins.Issue {
		return plugins.CheckSegmentAndEmbedOutput(b, view, m)
	})
	observe(i.Pin, org, OpSegmentAndEmbed, started, result, err)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if err := judgeIngestion(result); err != nil {
		return nil, err
	}
	answer, err := plugins.DecodeSegmentAndEmbed(result.Body)
	if err != nil {
		return nil, refused("%v", err)
	}
	out := make([]processing.PluginSegment, len(answer.Segments))
	for n, s := range answer.Segments {
		vectors := map[string][]float32{}
		for id, vector := range s.Vectors {
			vectors[byID[id]] = plugins.Float32s(vector)
		}
		out[n] = processing.PluginSegment{SegmentInput: content.SegmentInput{PartKey: s.PartKey, Start: s.Start, End: s.End, LexicalText: s.LexicalText, Provenance: s.Provenance}, Vectors: vectors}
	}
	return out, nil
}

// Gone decides whether work pinned to the plugin's plan stops after cause:
// only when the plugin was unreachable, or no longer owns the space the work
// needs, and has left the active plan (plugins.Unreachable).
func (i Ingestor) Gone(ctx context.Context, cause error) (*content.Diagnostic, error) {
	if !errors.Is(cause, ErrUnavailable) && !errors.Is(cause, processing.ErrSpaceUnowned) {
		return nil, nil
	}
	return plugins.Unreachable(ctx, i.Pin, "ingestion")
}

// judgeIngestion maps an invocation result: unavailability and retryable errors are
// retried, terminal errors and refused output are content.ErrIngestionRefused.
func judgeIngestion(result *devhost.Result) error {
	switch {
	case result.Error != nil && result.Error.Retryable:
		return &PluginError{Status: result.Status, Code: result.Error.Code, Message: result.Error.Message, Retryable: true}
	case result.Error != nil:
		return refused("%s (HTTP %d): %s", result.Error.Code, result.Status, result.Error.Message)
	case len(result.Issues) > 0 && (result.Status != 200 || result.Issues[0].Code == devhost.CodeInvalidErrorEnvelope):
		return fmt.Errorf("%w: %s", ErrUnavailable, describe(result.Issues))
	case len(result.Issues) > 0:
		return refused("invalid answer: %s", describe(result.Issues))
	}
	return nil
}

// QueryTooLongCode is the error envelope code with which a plugin refuses a
// query over the length its space accepts; its message names the limit.
const QueryTooLongCode = "query_too_long"

// maxRefusalMessage bounds, in code points, a plugin message a search error
// passes on to the API client.
const maxRefusalMessage = 256

// bounded cuts s to at most n code points.
func bounded(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

type embedQueryRequest struct {
	InvocationID   string          `json:"invocation_id"`
	Contribution   string          `json:"contribution"`
	OrganizationID string          `json:"organization_id"`
	Configuration  json.RawMessage `json:"configuration"`
	Space          string          `json:"space"`
	Query          embedQuery      `json:"query"`
}

type embedQuery struct {
	Modality string `json:"modality"`
	Text     string `json:"text"`
}

// EncodeQuery asks the plugin that owns a space for a query's vector. A
// terminal refusal with code query_too_long is retrieval.ErrQueryTooLong
// detailed by the plugin's message, which names its limit; any other terminal
// refusal (the plugin cannot encode this query) is content.ErrInvalid;
// anything else is unavailability.
func (i Ingestor) EncodeQuery(ctx context.Context, org, key, text string) ([]float32, error) {
	id, _, ok := i.declared(key)
	if !ok {
		return nil, fmt.Errorf("%w: space %s is not declared by the pinned ingestion plugin", ErrUnavailable, key)
	}
	body, err := json.Marshal(embedQueryRequest{InvocationID: invocationID(), Contribution: "ingestion", OrganizationID: org, Configuration: i.Pin.Configuration, Space: id, Query: embedQuery{Modality: "text", Text: text}})
	if err != nil {
		return nil, err
	}
	m := &i.Pin.Manifest
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(i.contribution().QueryTimeoutMS)*time.Millisecond)
	defer cancel()
	started := time.Now()
	result, err := devhost.InvokeEmbedQuery(callCtx, i.Pin.Endpoint, body, func(b []byte) []plugins.Issue {
		return plugins.CheckEmbedQueryOutput(b, id, m)
	})
	observe(i.Pin, org, OpEmbedQuery, started, result, err)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if err := judgeIngestion(result); err != nil {
		if errors.Is(err, content.ErrIngestionRefused) && result.Error != nil {
			if result.Error.Code == QueryTooLongCode {
				return nil, publicerr.WithDetail(retrieval.ErrQueryTooLong, "%s", bounded(result.Error.Message, maxRefusalMessage))
			}
			return nil, content.ErrInvalid
		}
		slog.Warn("embed_query failed", "component", "search", "plugin", i.Pin.Manifest.ID, "space", key, "error", err.Error())
		return nil, err
	}
	var answer struct {
		Vector []float64 `json:"vector"`
	}
	if err := json.Unmarshal(result.Body, &answer); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return plugins.Float32s(answer.Vector), nil
}
