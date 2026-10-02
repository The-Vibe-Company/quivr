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

// Bind records the selected owner even when stored artifacts avoid a call.
func (i Ingestor) Bind(ctx context.Context) error { return plugins.BindIngestion(ctx, i.Pin) }

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
// request with no space.
func (i Ingestor) SegmentsOnly() bool { return i.Pin.Speaks(plugins.FeatureSegmentsOnly) }

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

// refused is content.ErrIngestionRefused for the reason message states, which
// a Version quarantined for it shows with the plugin's name and version.
func (i Ingestor) refused(format string, args ...any) error {
	reason := content.Diagnostic{Code: content.ErrIngestionRefused.Error(), Message: fmt.Sprintf(format, args...),
		Plugin: i.Pin.Manifest.ID, PluginVersion: i.Pin.Manifest.Version, Contribution: "ingestion"}
	return &content.Refusal{Reason: reason}
}

// SegmentAndEmbed asks the plugin for a Version's segments with a vector in
// each space key; with no key, for the segments alone. A plugin that is unavailable or answers a retryable error
// is retried by the caller; a terminal error or an answer the output checks
// refuse is content.ErrIngestionRefused.
func (i Ingestor) SegmentAndEmbed(ctx context.Context, org, corpusID string, v content.Version, keys []string) ([]processing.PluginSegment, error) {
	if err := plugins.BindIngestion(ctx, i.Pin); err != nil {
		return nil, err
	}
	if err := halted(ctx, i.Pin); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(keys))
	byID := map[string]string{}
	for _, key := range keys {
		id, _, ok := i.declared(key)
		if !ok {
			return nil, i.refused("space %s is not declared by %s@%s", key, i.Pin.Manifest.ID, i.Pin.Manifest.Version)
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
	switch {
	case len(parts) == 0:
		return nil, i.refused("the Version has no text Part to index")
	case len(parts) > 256:
		return nil, i.refused("the Version has %d text Parts; segment_and_embed takes at most 256", len(parts))
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
	callCtx, cancel := context.WithTimeout(ctx, min(time.Duration(i.contribution().TimeoutMS)*time.Millisecond, processing.SegmentAndEmbedTimeoutCap))
	defer cancel()
	started := time.Now()
	result, err := devhost.InvokeSegmentAndEmbed(callCtx, i.Pin.Endpoint, body, plugins.IngestionMaxResponseBytes(m), func(b []byte) []plugins.Issue {
		return plugins.CheckSegmentAndEmbedOutput(b, view, m)
	})
	observe(i.Pin, org, OpSegmentAndEmbed, started, result, err)
	if err != nil && ctx.Err() == nil && errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("%w: %w: %v", ErrUnavailable, processing.ErrPluginDeadline, err)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if err := i.judge(result); err != nil {
		return nil, err
	}
	answer, err := plugins.DecodeSegmentAndEmbed(result.Body)
	if err != nil {
		return nil, i.refused("%s@%s answered segments the engine cannot read: %v", m.ID, m.Version, err)
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
	if err := plugins.BindIngestion(ctx, i.Pin); err != nil {
		return nil, err
	}
	return plugins.Unreachable(ctx, i.Pin, "ingestion")
}

// judge maps an invocation result: unavailability and retryable errors are
// retried, terminal errors and refused output are content.ErrIngestionRefused,
// whose reason names the plugin's code and message, each cut to
// maxRefusalMessage code points.
func (i Ingestor) judge(result *devhost.Result) error {
	m := &i.Pin.Manifest
	switch {
	case result.Error != nil && result.Error.Retryable:
		return &PluginError{Status: result.Status, Code: result.Error.Code, Message: result.Error.Message, Retryable: true}
	case result.Error != nil:
		return i.refused("%s@%s refused it (%s): %s", m.ID, m.Version, bounded(result.Error.Code, maxRefusalMessage), bounded(result.Error.Message, maxRefusalMessage))
	case len(result.Issues) > 0 && (result.Status != 200 || result.Issues[0].Code == devhost.CodeInvalidErrorEnvelope):
		return fmt.Errorf("%w: %s", ErrUnavailable, describe(result.Issues))
	case len(result.Issues) > 0:
		return i.refused("%s@%s gave an answer the engine refuses: %s", m.ID, m.Version, bounded(describe(result.Issues), maxRefusalMessage))
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
	started := time.Now()
	result, err := i.embedQuery(ctx, invocationID(), org, id, text)
	observe(i.Pin, org, OpEmbedQuery, started, result, err)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if err := i.judge(result); err != nil {
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

// embedQuery posts one embed_query within the contribution's query_timeout_ms.
func (i Ingestor) embedQuery(ctx context.Context, invocation, org, space, text string) (*devhost.Result, error) {
	body, err := json.Marshal(embedQueryRequest{InvocationID: invocation, Contribution: "ingestion", OrganizationID: org, Configuration: i.Pin.Configuration, Space: space, Query: embedQuery{Modality: "text", Text: text}})
	if err != nil {
		return nil, err
	}
	m := &i.Pin.Manifest
	ctx, cancel := context.WithTimeout(ctx, time.Duration(i.contribution().QueryTimeoutMS)*time.Millisecond)
	defer cancel()
	return devhost.InvokeEmbedQuery(ctx, i.Pin.Endpoint, body, func(b []byte) []plugins.Issue {
		return plugins.CheckEmbedQueryOutput(b, space, m)
	})
}

// WarmUpOrganization is the organization_id of a warm-up embed_query: it
// belongs to no Organization.
const WarmUpOrganization = "engine.warm-up"

// Warm sends one embed_query to each space the pin enables, served first, so
// that the plugin loads what it loads on first use (core.ingest starts its
// tokenizer process) before a search waits for it, not within that search's
// latency budget. Any answer, even an error envelope such as an unavailable
// embedding service, means the plugin did that loading; only a plugin that
// does not answer is an error (ErrUnavailable). The calls count for no
// Organization, so they are not observed.
func (i Ingestor) Warm(ctx context.Context) error {
	for _, space := range i.Pin.EnabledSpaces() {
		if _, err := i.embedQuery(ctx, "warm-up-"+invocationID(), WarmUpOrganization, space.ID, "warm-up"); err != nil {
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
	}
	return nil
}
