package pluginhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/call"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
	"github.com/The-Vibe-Company/quivr-v2/internal/processing"
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

	request := plugins.SegmentAndEmbedRequest{InvocationID: plugins.InvocationID(), IdempotencyKey: plugins.IngestionKey(i.Pin.Generation(), org, v.ID, ids), Contribution: "ingestion",
		OrganizationID: org, Configuration: i.Pin.Configuration, Version: plugins.IngestionVersion{CorpusID: corpusID, RecordID: v.RecordID, RecordVersionID: v.ID}, Parts: parts, Spaces: ids}
	body, err := plugins.BuildSegmentAndEmbedRequest(request)
	if err != nil {
		return nil, err
	}
	view := plugins.IngestionRequestView{Parts: parts, Spaces: ids}
	m := &i.Pin.Manifest
	started := time.Now()
	result, err := call.Invoke(ctx, i.Pin, call.SegmentAndEmbed, call.Bytes(body), func(ctx context.Context, b []byte) []plugins.Issue {
		return plugins.CheckSegmentAndEmbedOutput(b, view, m)
	}, nil)
	observe(i.Pin, org, OpSegmentAndEmbed, started, result, err)
	if err != nil {
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

// QueryTooLongCode is the error envelope code with which a plugin refuses a
// query over the length its space accepts; its message names the limit.
const QueryTooLongCode = "query_too_long"

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
	result, err := i.embedQuery(ctx, plugins.InvocationID(), org, id, text)
	observe(i.Pin, org, OpEmbedQuery, started, result, err)
	if err != nil {
		return nil, err
	}
	var answer struct {
		Vector []float64 `json:"vector"`
	}
	if err := json.Unmarshal(result.Body, &answer); err != nil {
		return nil, err
	}
	return plugins.Float32s(answer.Vector), nil
}

// embedQuery posts one embed_query within the contribution's query_timeout_ms.
func (i Ingestor) embedQuery(ctx context.Context, invocation, org, space, text string) (*devhost.Result, error) {
	body, err := plugins.BuildEmbedQueryRequest(plugins.EmbedQueryRequest{InvocationID: invocation, Contribution: "ingestion", OrganizationID: org, Configuration: i.Pin.Configuration, Space: space, Query: plugins.EmbedQuery{Modality: "text", Text: text}})
	if err != nil {
		return nil, err
	}
	m := &i.Pin.Manifest
	return call.Invoke(ctx, i.Pin, call.EmbedQuery, call.Bytes(body), func(ctx context.Context, b []byte) []plugins.Issue { return plugins.CheckEmbedQueryOutput(b, space, m) }, nil)
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
		if result, err := i.embedQuery(ctx, "warm-up-"+plugins.InvocationID(), WarmUpOrganization, space.ID, "warm-up"); err != nil && result == nil {
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
	}
	return nil
}
