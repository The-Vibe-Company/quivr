// Package weaviate adapts the pinned local engine; its physical schema stays private.
package weaviate

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/keywords"
	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
	"github.com/The-Vibe-Company/quivr/internal/outbound"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/The-Vibe-Company/quivr/internal/visibility"
)

var className = regexp.MustCompile(`^[A-Z][A-Za-z0-9_]*$`)

type Store struct {
	grpc       pb.WeaviateClient
	Endpoint   string
	Client     *http.Client
	apiKey     string
	Visibility visibility.Resolver
	// PurgeTimeout bounds each purge selection/delete request independently of search.
	PurgeTimeout time.Duration
	purgeBatch   atomic.Int64
	purgeLimit   atomic.Int64
	// LegacySpace is the built-in space, whose named vector keeps the name it
	// had before named spaces (legacyVector), so generations built before and
	// after them stay searchable in one query.
	LegacySpace string
	// DefaultIndex configures the legacy named vector created and reconciled
	// during Bootstrap; nil preserves content.DefaultVectorIndex (rq-8, rescoreLimit 0).
	DefaultIndex *content.VectorIndex
	// vectors remembers the named vectors known to exist, by collection.
	vectors            *sync.Map
	vectorSchema       *semaphore.Weighted
	metadataMu         sync.Mutex
	metadataProperties map[string]bool
	schemaSync         map[string]*semaphore.Weighted
	metrics            atomic.Pointer[telemetry.DependencyMetrics]
}

// legacyVector is the named vector of the built-in space and of every
// generation built before named spaces.
const legacyVector = "semantic_text_v1"

// lexicalProperty holds an ingestion plugin's keyword-search text.
const lexicalProperty = "lexicalText"

// VectorName is the named vector a generation stores a space's vectors
// under: legacyVector for a generation built before named spaces; for a
// space the generation records an index setting for, a name derived from
// the space key and that setting, so a changed setting builds a new index;
// otherwise legacyVector for the built-in space and a name derived from the
// space key alone.
func (s *Store) VectorName(g content.Generation, space string) string {
	if !g.SpacesProjected {
		return legacyVector
	}
	if index := spaceIndex(g, space); index != nil {
		return "s_" + content.Hash([]byte("quivr/named-vector/v2\x00" + space + "\x00" + index.Quantization + "\x00" + strconv.Itoa(index.RescoreLimit)))[:24]
	}
	if space == s.LegacySpace {
		return legacyVector
	}
	return "s_" + content.Hash([]byte("quivr/named-vector/v1\x00" + space))[:24]
}

// spaceIndex is the index setting g records for space, nil when none.
func spaceIndex(g content.Generation, space string) *content.VectorIndex {
	for _, sp := range g.Spaces {
		if sp.ID == space {
			return sp.Index
		}
	}
	return nil
}

// distance maps a registry metric to the Weaviate distance.
func distance(metric string) string {
	switch metric {
	case "dot":
		return "dot"
	case "l2":
		return "l2-squared"
	}
	return "cosine"
}

// vectorConfig declares an HNSW named vector stored as index says; a nil
// index (a space recorded before index settings) gets the default. "none"
// also opts out of a quantization the database applies by default.
func vectorConfig(metric string, index *content.VectorIndex) map[string]any {
	if index == nil {
		index = &content.DefaultVectorIndex
	}
	config := map[string]any{"distance": distance(metric)}
	if index.EF != 0 {
		config["ef"] = index.EF
	}
	switch index.Quantization {
	case content.QuantizationRQ8, content.QuantizationRQ1:
		rq := map[string]any{"enabled": true, "bits": 8}
		if index.Quantization == content.QuantizationRQ1 {
			rq["bits"] = 1
		}
		if index.Quantization == content.QuantizationRQ8 || index.RescoreLimit > 0 {
			rq["rescoreLimit"] = index.RescoreLimit
		}
		config["rq"] = rq
	case content.QuantizationNone:
		config["skipDefaultQuantization"] = true
	}
	return map[string]any{"vectorizer": map[string]any{"none": nil}, "vectorIndexType": "hnsw", "vectorIndexConfig": config}
}

// rq8Setting identifies a mutable RQ-8 limit using the generation's recorded
// setting. Other compression settings and unrelated vector options stay intact.
func rq8Setting(raw any, index *content.VectorIndex) (map[string]any, int, bool) {
	if index != nil && index.Quantization != content.QuantizationRQ8 {
		return nil, 0, false
	}
	vector, _ := raw.(map[string]any)
	config, _ := vector["vectorIndexConfig"].(map[string]any)
	rq, _ := config["rq"].(map[string]any)
	if vector["vectorIndexType"] != "hnsw" || rq["enabled"] != true || rq["bits"] != float64(8) {
		return nil, 0, false
	}
	limit := 0
	if index != nil {
		limit = index.RescoreLimit
	}
	return rq, limit, true
}

// hnswEFSetting identifies a mutable HNSW query-time ef setting. When index
// requests ef=0 (index default), it matches only if an explicit non-default
// ef override is currently present so reconciliation removes it cleanly.
func hnswEFSetting(raw any, index *content.VectorIndex) (map[string]any, int, bool) {
	if index == nil {
		return nil, 0, false
	}
	vector, _ := raw.(map[string]any)
	config, _ := vector["vectorIndexConfig"].(map[string]any)
	if vector["vectorIndexType"] != "hnsw" || config == nil {
		return nil, 0, false
	}
	if index.EF == 0 {
		if val, ok := config["ef"]; !ok || val == nil || val == float64(-1) {
			return nil, 0, false
		}
		return config, 0, true
	}
	return config, index.EF, true
}

func hnswEFMatches(config map[string]any, want int) bool {
	if want == 0 {
		val, ok := config["ef"]
		return !ok || val == nil || val == float64(-1)
	}
	return config["ef"] == float64(want)
}

func applyHNSWEF(config map[string]any, want int) {
	if want == 0 {
		delete(config, "ef")
		return
	}
	config["ef"] = want
}

// ensureVectors adds missing spaces and reconciles mutable RQ-8 limits before
// caching a generation's named vectors. Vector names and graph contents stay
// unchanged, including for generations recorded before index settings.
func (s *Store) ensureVectors(ctx context.Context, g content.Generation) error {
	if !g.SpacesProjected {
		return nil
	}
	known := s.vectors
	if known == nil {
		known = &sync.Map{}
	}
	cached := func() bool {
		for _, sp := range g.Spaces {
			if _, ok := known.Load(g.Collection + "/" + s.VectorName(g, sp.ID)); !ok {
				return false
			}
		}
		return true
	}
	if cached() {
		return nil
	}
	// Parallel tenant queries share named-vector definitions. Serialize
	// cache misses so that one class PUT includes previous additions.
	if s.vectorSchema != nil {
		if err := s.vectorSchema.Acquire(ctx, 1); err != nil {
			return err
		}
		defer s.vectorSchema.Release(1)
		if cached() {
			return nil
		}
	}
	var class map[string]any
	if _, err := s.call(ctx, "GET", "/v1/schema/"+g.Collection, nil, &class); err != nil {
		return err
	}
	configs, _ := class["vectorConfig"].(map[string]any)
	if configs == nil {
		configs = map[string]any{}
	}
	changed := false
	limits := map[string]int{}
	efs := map[string]int{}
	for _, sp := range g.Spaces {
		name := s.VectorName(g, sp.ID)
		if _, ok := configs[name]; !ok {
			configs[name] = vectorConfig(sp.Metric, sp.Index)
			changed = true
			if sp.Index == nil || sp.Index.Quantization == content.QuantizationRQ8 {
				limit := 0
				if sp.Index != nil {
					limit = sp.Index.RescoreLimit
				}
				limits[name] = limit
			}
			if sp.Index != nil && sp.Index.EF != 0 {
				efs[name] = sp.Index.EF
			}
		} else {
			if rq, limit, ok := rq8Setting(configs[name], sp.Index); ok {
				limits[name] = limit
				if rq["rescoreLimit"] != float64(limit) {
					rq["rescoreLimit"] = limit
					changed = true
				}
			}
			if cfg, ef, ok := hnswEFSetting(configs[name], sp.Index); ok {
				efs[name] = ef
				if !hnswEFMatches(cfg, ef) {
					applyHNSWEF(cfg, ef)
					changed = true
				}
			}
		}
	}
	if changed {
		class["vectorConfig"] = configs
		if _, err := s.call(ctx, "PUT", "/v1/schema/"+g.Collection, class, nil); err != nil {
			return err
		}
		class = nil
		if _, err := s.call(ctx, "GET", "/v1/schema/"+g.Collection, nil, &class); err != nil {
			return err
		}
		configs, _ = class["vectorConfig"].(map[string]any)
	}
	for _, sp := range g.Spaces {
		name := s.VectorName(g, sp.ID)
		if _, ok := configs[name]; !ok {
			return errors.New("projection named vector missing")
		}
		if limit, check := limits[name]; check {
			rq, _, ok := rq8Setting(configs[name], sp.Index)
			if !ok || rq["rescoreLimit"] != float64(limit) {
				return errors.New("projection named vector rescore setting differs")
			}
		}
		if ef, check := efs[name]; check {
			vec, _ := configs[name].(map[string]any)
			cfg, _ := vec["vectorIndexConfig"].(map[string]any)
			if cfg == nil || !hnswEFMatches(cfg, ef) {
				return errors.New("projection named vector ef setting differs")
			}
		}
	}
	for _, sp := range g.Spaces {
		name := s.VectorName(g, sp.ID)
		known.Store(g.Collection+"/"+name, true)
	}
	return nil
}

func New(endpoint string, apiKey ...string) *Store {
	return newStore(endpoint, outbound.DefaultTransport(), apiKey)
}

func newStore(endpoint string, transport http.RoundTripper, apiKey []string) *Store {
	store := &Store{Endpoint: strings.TrimRight(endpoint, "/"), vectors: &sync.Map{}, vectorSchema: semaphore.NewWeighted(1)}
	store.Client = &http.Client{Timeout: 4 * time.Second, Transport: telemetry.Transport(store.countRequests(store.authenticate(transport, apiKey)), "weaviate.request")}
	if store.apiKey != "" {
		store.Client.CheckRedirect = outbound.CheckRedirect
	}
	return store
}
func (s *Store) call(ctx context.Context, method, path string, in, out any) (int, error) {
	return s.callLimit(ctx, method, path, in, out, 2<<20)
}

var errResponseLimit = errors.New("projection response exceeds byte limit")

const maxPooledResponseBuf = 512 << 10

var responseBufPool = sync.Pool{
	New: func() any {
		return bytes.NewBuffer(make([]byte, 0, 4<<10))
	},
}

func (s *Store) callLimit(ctx context.Context, method, path string, in, out any, responseLimit int64) (int, error) {
	body, err := requestBody(in)
	if err != nil {
		return 0, err
	}
	return s.send(ctx, s.Client, method, path, body, out, responseLimit)
}

// writeCall sends a write whose deadline grows with its encoded size.
func (s *Store) writeCall(ctx context.Context, method, path string, in, out any) (int, error) {
	body, err := requestBody(in)
	if err != nil {
		return 0, err
	}
	client := s.Client
	if timeout := s.writeTimeout(len(body)); timeout != client.Timeout {
		sized := *client
		sized.Timeout = timeout
		client = &sized
	}
	return s.send(ctx, client, method, path, body, out, 2<<20)
}

func requestBody(in any) ([]byte, error) {
	if in == nil {
		return nil, nil
	}
	return json.Marshal(in)
}

// writeThroughputFloor is the slowest sustained upload, in bytes per second,
// a write deadline allows on top of the request timeout.
const writeThroughputFloor = 1 << 20

// maxWriteTimeout caps a size-scaled write deadline; a request timeout
// configured above it is kept as is.
const maxWriteTimeout = 60 * time.Second

// writeTimeout extends the request timeout by the time size bytes take at
// writeThroughputFloor. A zero request timeout stays unbounded.
func (s *Store) writeTimeout(size int) time.Duration {
	base := s.Client.Timeout
	if base <= 0 || size <= 0 {
		return base
	}
	transfer := time.Duration(size/writeThroughputFloor)*time.Second +
		time.Duration(size%writeThroughputFloor)*time.Second/writeThroughputFloor
	return min(base+transfer, max(base, maxWriteTimeout))
}

func (s *Store) send(ctx context.Context, client *http.Client, method, path string, body []byte, out any, responseLimit int64) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, s.Endpoint+path, bytes.NewReader(body))
	if err != nil {
		return 0, s.safeError(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return 0, errors.New("projection connection unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return res.StatusCode, errors.New("projection request failed")
	}
	if out != nil {
		buf := responseBufPool.Get().(*bytes.Buffer)
		buf.Reset()
		limited := &io.LimitedReader{R: res.Body, N: responseLimit}
		_, readErr := buf.ReadFrom(limited)
		if readErr != nil {
			err = readErr
		} else {
			err = json.Unmarshal(buf.Bytes(), out)
		}
		if err != nil && limited.N == 0 {
			err = errResponseLimit
		}
		if buf.Cap() <= maxPooledResponseBuf {
			buf.Reset()
			responseBufPool.Put(buf)
		}
	}
	return res.StatusCode, s.safeError(err)
}
func (s *Store) Ready(ctx context.Context) error {
	_, err := s.call(ctx, "GET", "/v1/.well-known/ready", nil, nil)
	return err
}
func (s *Store) Bootstrap(ctx context.Context, collection string) error {
	if !className.MatchString(collection) {
		return errors.New("invalid projection route")
	}
	var existing struct {
		VectorConfig map[string]any `json:"vectorConfig"`
		MultiTenancy struct {
			Enabled              bool `json:"enabled"`
			AutoTenantCreation   bool `json:"autoTenantCreation"`
			AutoTenantActivation bool `json:"autoTenantActivation"`
		} `json:"multiTenancyConfig"`
		Properties []struct {
			Name string `json:"name"`
		} `json:"properties"`
	}
	status, err := s.call(ctx, "GET", "/v1/schema/"+collection, nil, &existing)
	if err == nil {
		if collection == InitialCollection && (!existing.MultiTenancy.Enabled || existing.MultiTenancy.AutoTenantCreation || !existing.MultiTenancy.AutoTenantActivation) {
			return errors.New("projection tenant configuration differs")
		}
		present := map[string]bool{}
		for _, p := range existing.Properties {
			present[p.Name] = true
		}
		// A collection created before source routing, named spaces, or lexical
		// plugin metadata gains the missing properties. Older objects lack a
		// value, so their immutable anchors keep their original bytes.
		properties := []struct {
			name   string
			config map[string]any
		}{
			{projectionPluginProperty, filterable(projectionPluginProperty)},
			{sourceMediaTypeProperty, filterable(sourceMediaTypeProperty)},
			{sourceNamespaceProperty, filterable(sourceNamespaceProperty)},
			{lexicalProperty, searchable(lexicalProperty)},
		}
		for _, property := range properties {
			if present[property.name] {
				continue
			}
			if _, err = s.call(ctx, "POST", "/v1/schema/"+collection+"/properties", property.config, nil); err != nil {
				return err
			}
		}
		// Bootstrap owns only the legacy default. Other named vectors require
		// their generation's recorded settings, reconciled by ensureVectors.
		rq, wantRescore, checkRescore := rq8Setting(existing.VectorConfig[legacyVector], s.DefaultIndex)
		needRescore := checkRescore && rq["rescoreLimit"] != float64(wantRescore)
		cfg, wantEF, checkEF := hnswEFSetting(existing.VectorConfig[legacyVector], s.DefaultIndex)
		needEF := checkEF && !hnswEFMatches(cfg, wantEF)
		if needRescore || needEF {
			var class map[string]any
			if _, err = s.call(ctx, "GET", "/v1/schema/"+collection, nil, &class); err != nil {
				return err
			}
			configs, _ := class["vectorConfig"].(map[string]any)
			if needRescore {
				rq, _, ok := rq8Setting(configs[legacyVector], s.DefaultIndex)
				if !ok {
					return errors.New("projection legacy vector setting changed")
				}
				rq["rescoreLimit"] = wantRescore
			}
			if needEF {
				cfg, _, ok := hnswEFSetting(configs[legacyVector], s.DefaultIndex)
				if !ok {
					return errors.New("projection legacy vector setting changed")
				}
				applyHNSWEF(cfg, wantEF)
			}
			if _, err = s.call(ctx, "PUT", "/v1/schema/"+collection, class, nil); err != nil {
				return err
			}
			if _, err = s.call(ctx, "GET", "/v1/schema/"+collection, nil, &existing); err != nil {
				return err
			}
			if needRescore {
				rq, _, ok := rq8Setting(existing.VectorConfig[legacyVector], s.DefaultIndex)
				if !ok || rq["rescoreLimit"] != float64(wantRescore) {
					return errors.New("projection legacy vector rescore setting differs")
				}
			}
			if needEF {
				vec, _ := existing.VectorConfig[legacyVector].(map[string]any)
				cfg, _ := vec["vectorIndexConfig"].(map[string]any)
				if cfg == nil || !hnswEFMatches(cfg, wantEF) {
					return errors.New("projection legacy vector ef setting differs")
				}
			}
		}
		return nil
	}
	if status != 404 {
		return err
	}
	properties := []any{}
	for _, name := range []string{"organization", "corpusId", "generationId", "segmentId", "versionId", "segmentationId", projectionPluginProperty, sourceMediaTypeProperty, sourceNamespaceProperty} {
		property := filterable(name)
		if collection == InitialCollection && (name == "organization" || name == "corpusId" || name == "generationId") {
			// Retain these values for canonical hydration and replay checks.
			property["indexFilterable"] = false
		}
		properties = append(properties, property)
	}
	for _, name := range []string{"title", "body", lexicalProperty} {
		properties = append(properties, searchable(name))
	}
	schema := map[string]any{"class": collection, "properties": properties, "vectorConfig": map[string]any{legacyVector: vectorConfig("cosine", s.DefaultIndex)}, "invertedIndexConfig": map[string]any{"stopwords": map[string]any{"preset": "none"}}, "replicationConfig": map[string]any{"factor": 1}}
	if collection == InitialCollection {
		// A read already admitted before archive may still reach its shard.
		// Canonical routing rejects new reads while the Corpus is archived.
		schema["multiTenancyConfig"] = map[string]bool{"enabled": true, "autoTenantCreation": false, "autoTenantActivation": true}
	}
	_, err = s.call(ctx, "POST", "/v1/schema", schema, nil)
	return err
}

// sourceNamespaceProperty holds the Record's Source Namespace for filtering.
const sourceNamespaceProperty = "sourceNamespace"

// projectionPluginProperty identifies the ingestion plugin that made a
// plugin segmentation. Empty for legacy anchors projected before ownership
// was recorded.
const projectionPluginProperty = "projectionPlugin"

// sourceMediaTypeProperty records the source Blob media type used to route a
// plugin segmentation. Plugin recipes with no recorded type use text/plain.
const sourceMediaTypeProperty = "sourceMediaType"

// searchable declares a text property keyword search scores (BM25).
func searchable(name string) map[string]any {
	return map[string]any{"name": name, "dataType": []string{"text"}, "tokenization": "word", "indexSearchable": true}
}

// filterable declares an exact-match text property that search never scores.
func filterable(name string) map[string]any {
	return map[string]any{"name": name, "dataType": []string{"text"}, "tokenization": "field", "indexFilterable": true, "indexSearchable": false}
}

func uuidOf(stable string) string {
	h := content.Hash([]byte(stable))
	return h[:8] + "-" + h[8:12] + "-5" + h[13:16] + "-a" + h[17:20] + "-" + h[20:32]
}

// objectID names the lexical object promotion publishes for a segment.
func objectID(org, generation, segment string) string {
	return uuidOf(content.StableID("projection", org, generation, segment))
}

// enrichedID names the object carrying one embedding payload for a segment. It
// is scoped like objectID: segments belong to one Version, and neither another
// generation nor another Version can share the identity.
func enrichedID(org, generation, segment, payloadSHA string) string {
	return uuidOf(content.StableID("projection-embedding", org, generation, segment, payloadSHA))
}

var lexicalProperties = []string{"organization", "corpusId", "generationId", "versionId", "segmentationId", "segmentId", projectionPluginProperty, sourceMediaTypeProperty, sourceNamespaceProperty, recordACLProperty, recordACLRestrictedProperty, "body", "title", lexicalProperty, "recordId", itemKind, passageText}

type storedObject struct {
	Properties map[string]any       `json:"properties"`
	Vectors    map[string][]float32 `json:"vectors"`
}

// object reads one object by identity; found is false only on a definite 404.
func (s *Store) object(ctx context.Context, collection, id string) (storedObject, bool, error) {
	var stored storedObject
	status, err := s.call(ctx, "GET", tenantPath(ctx, "/v1/objects/"+collection+"/"+id+"?include=vector"), nil, &stored)
	if status == http.StatusNotFound {
		return stored, false, nil
	}
	return stored, err == nil, err
}

// anchorProperties reads one anchor object's properties without fetching vector payloads.
func (s *Store) anchorProperties(ctx context.Context, collection, id string) (storedObject, bool, error) {
	var stored storedObject
	status, err := s.call(ctx, "GET", tenantPath(ctx, "/v1/objects/"+collection+"/"+id), nil, &stored)
	if status == http.StatusNotFound {
		return stored, false, nil
	}
	return stored, err == nil, err
}

// anchorFetchConcurrency bounds parallel REST fallback reads when gRPC is unavailable.
const anchorFetchConcurrency = 16

// fetchAnchors deduplicates anchor IDs and retrieves their properties in one
// gRPC batch query when available, falling back to bounded parallel REST GETs
// for any IDs not returned or when operating over HTTP only.
func (s *Store) fetchAnchors(ctx context.Context, collection string, ids []string) (map[string]storedObject, error) {
	unique := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	anchors := make(map[string]storedObject, len(unique))
	if len(unique) == 0 {
		return anchors, nil
	}
	missing := unique
	if s.grpc != nil {
		propsByID, err := s.grpcAnchorProperties(ctx, collection, unique)
		if err != nil && !errors.Is(err, errResponseLimit) {
			return nil, err
		}
		if err == nil {
			missing = missing[:0]
			for _, id := range unique {
				if props, ok := propsByID[id]; ok {
					anchors[id] = storedObject{Properties: props}
				} else {
					missing = append(missing, id)
				}
			}
		}
	}
	if len(missing) == 0 {
		return anchors, nil
	}
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(anchorFetchConcurrency)
	for _, id := range missing {
		goRecover(gctx, g, "fetch_anchors", func() error {
			anchor, projected, err := s.anchorProperties(gctx, collection, id)
			if err != nil {
				return err
			}
			if !projected {
				return retrieval.ErrProjectionMissing
			}
			mu.Lock()
			anchors[id] = anchor
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return anchors, nil
}

func sameProperties(stored, want map[string]any) bool {
	for key, value := range want {
		if !sameMetadata(stored[key], value) {
			if strings.HasPrefix(key, "m_") {
				a, aok := stored[key].(string)
				b, bok := value.(string)
				if aok && bok {
					na, aok := corpus.FilterDate(a)
					nb, bok := corpus.FilterDate(b)
					if aok && bok && na == nb {
						continue
					}
				}
			}
			return false
		}
	}
	return true
}

// projectionBatch bounds write requests and identity lookups.
const projectionBatch = 100

type projectionObject struct {
	Class      string               `json:"class"`
	Tenant     string               `json:"tenant,omitempty"`
	ID         string               `json:"id"`
	Properties map[string]any       `json:"properties"`
	Vectors    map[string][]float32 `json:"vectors,omitempty"`
	// encoded holds the validated little-endian payload of each named vector,
	// so the gRPC write and verification reuse it instead of encoding again.
	encoded map[string][]byte
}

const embeddingRevision = "embeddingRevision"

func embeddingRevisionProperty() map[string]any {
	return map[string]any{"name": embeddingRevision, "dataType": []string{"int"}, "indexFilterable": false, "indexSearchable": false}
}

type projectionRow struct {
	SegmentID   string `json:"segmentId"`
	Kind        string `json:"itemKind"`
	Revision    int64  `json:"embeddingRevision"`
	LexicalText string `json:"lexicalText"`
	Additional  struct {
		ID      string `json:"id"`
		Created int64  `json:"creationTimeUnix,string"`
	} `json:"_additional"`
}

// projectionRows lists identities and only the requested properties without
// fetching vectors.
func (s *Store) projectionRows(ctx context.Context, collection string, where filter, limit int, order searchOrder, properties []string, created bool) ([]projectionRow, error) {
	return s.projectionRowsLimit(ctx, collection, where, limit, order, properties, created, 2<<20)
}

func (s *Store) projectionRowsLimit(ctx context.Context, collection string, where filter, limit int, order searchOrder, properties []string, created bool, responseLimit int) ([]projectionRow, error) {
	if s.grpc != nil {
		return s.grpcProjectionRows(ctx, collection, where, limit, order, properties, created, responseLimit)
	}
	qb := make([]byte, 0, len("{Get{(")+len(collection)+where.encodedLen()+order.encodedLen()+len(properties)*16+96)
	qb = append(qb, "{Get{"...)
	qb = append(qb, collection...)
	qb = append(qb, '(')
	if tenantArg := tenantArgument(ctx); tenantArg != "" {
		qb = append(qb, tenantArg...)
	}
	if where.operator != 0 {
		qb = append(qb, "where:"...)
		qb = where.appendGraphQL(qb)
		qb = append(qb, ',')
	}
	qb = append(qb, " limit:"...)
	qb = strconv.AppendInt(qb, int64(limit), 10)
	if order.field != "" {
		qb = append(qb, ',')
		qb = order.appendGraphQL(qb)
		qb = qb[:len(qb)-1]
	}
	qb = append(qb, "){"...)
	for _, prop := range properties {
		qb = append(qb, prop...)
		qb = append(qb, ' ')
	}
	if created {
		qb = append(qb, "_additional{id creationTimeUnix}"...)
	} else {
		qb = append(qb, "_additional{id}"...)
	}
	qb = append(qb, "}}}"...)
	query := ownedBytesToString(qb)
	var response struct {
		Data struct {
			Get map[string][]projectionRow `json:"Get"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	if _, err := s.callLimit(ctx, "POST", "/v1/graphql", map[string]any{"query": query}, &response, int64(responseLimit)); err != nil {
		return nil, err
	}
	rows := response.Data.Get[collection]
	if len(response.Errors) > 0 || rows == nil {
		return nil, errors.New("projection query failed")
	}
	for _, row := range rows {
		if row.Additional.ID == "" {
			return nil, errors.New("projection identity missing")
		}
	}
	return rows, nil
}

func (s *Store) verifyObject(ctx context.Context, want projectionObject) error {
	stored, found, err := s.object(ctx, want.Class, want.ID)
	if err != nil {
		return err
	}
	if !found || !sameProperties(stored.Properties, want.Properties) {
		return errors.New("projection verification mismatch")
	}
	if !sameVectors(stored.Vectors, want) {
		return errors.New("embedding projection verification failed")
	}
	return nil
}

// sameVectors reports whether every stored named vector has exactly the bits
// of the wanted one, comparing against the payload encoded at validation.
func sameVectors(stored map[string][]float32, want projectionObject) bool {
	for name, vector := range want.Vectors {
		expected, ok := want.encoded[name]
		if !ok {
			var err error
			if expected, err = content.VectorBytes(vector); err != nil {
				return false
			}
		}
		if !vectorBits(stored[name], expected) {
			return false
		}
	}
	return true
}

// vectorBits compares a decoded vector with a valid little-endian payload.
// The payload holds only finite values, so equal bits also reject NaN and
// infinities, as encoding the stored vector would.
func vectorBits(stored []float32, expected []byte) bool {
	if len(stored) == 0 || len(expected) != 4*len(stored) {
		return false
	}
	for i, x := range stored {
		if math.Float32bits(x) != binary.LittleEndian.Uint32(expected[4*i:]) {
			return false
		}
	}
	return true
}

// verifyConcurrency bounds the parallel identity reads that reconcile a
// batch whose acknowledgement was lost.
const verifyConcurrency = 8

func (s *Store) verifyBatch(ctx context.Context, objects []projectionObject) error {
	if len(objects) == 1 {
		return s.verifyObject(ctx, objects[0])
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(verifyConcurrency)
	for _, object := range objects {
		goRecover(gctx, g, "verify_batch", func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			return s.verifyObject(gctx, object)
		})
	}
	return g.Wait()
}

// insertBatch trusts per-object success and reconciles lost responses by
// deterministic identity. Explicit failures and malformed responses are errors.
func (s *Store) insertBatch(ctx context.Context, objects []projectionObject) error {
	if len(objects) == 0 {
		return nil
	}
	for i := range objects {
		objects[i].Tenant = tenantFrom(ctx)
	}
	if s.grpc != nil {
		return s.grpcInsertBatch(ctx, objects)
	}
	var result []struct {
		ID     string `json:"id"`
		Result struct {
			Status string `json:"status"`
			Errors any    `json:"errors"`
		} `json:"result"`
	}
	// Request only identities; returning properties/vectors can exceed the
	// bounded response decoder even for a modest number of large objects.
	status, err := s.writeCall(ctx, "POST", "/v1/batch/objects", map[string]any{"objects": objects, "fields": []string{"id"}}, &result)
	if err != nil {
		if status == 0 {
			return s.verifyBatch(ctx, objects)
		}
		if status >= 400 {
			return fmt.Errorf("projection batch request failed (HTTP %d): %w", status, err)
		}
		return errors.New("projection publication response invalid")
	}
	if len(result) != len(objects) {
		return errors.New("projection publication failed")
	}
	for i, r := range result {
		if r.ID != objects[i].ID || r.Result.Status != "SUCCESS" || r.Result.Errors != nil {
			return errors.New("projection publication failed")
		}
	}
	return nil
}

// publishAnchors leaves matching anchors untouched, including anchors written
// before routing properties were introduced. Batch upserts can change the
// document ID of an existing anchor, so on replay inspect only IDs that exist.
func (s *Store) publishAnchors(ctx context.Context, g content.Generation, org string, objects []projectionObject) error {
	if len(objects) == 0 {
		return nil
	}
	ids := make([]string, len(objects))
	for i, object := range objects {
		ids[i] = object.ID
	}
	rows, err := s.projectionRows(ctx, g.Collection, containsAny("id", ids), len(ids), searchOrder{}, nil, false)
	if err != nil {
		return err
	}
	exists := make(map[string]bool, len(rows))
	for _, row := range rows {
		exists[row.Additional.ID] = true
	}
	// A unified snapshot replaces the seed after enrichment. Find it by
	// segment when the seed ID is absent, so Publish replay does not restore it.
	var segments []string
	for _, object := range objects {
		if !exists[object.ID] && object.Properties[itemKind] == segmentKind {
			segments = append(segments, object.Properties["segmentId"].(string))
		}
	}
	snapshots := map[string]projectionRow{}
	if len(segments) > 0 {
		scope := and(generationFilter(ctx, org, g.ID), equal(itemKind, segmentKind))
		rows, err := s.projectionRows(ctx, g.Collection, and(scope, containsAny("segmentId", segments)),
			embeddingInventoryLimit, searchOrder{}, []string{"segmentId", embeddingRevision}, true)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if best, ok := snapshots[row.SegmentID]; !ok || compareEmbeddings(row, best) > 0 {
				snapshots[row.SegmentID] = row
			}
		}
		if len(rows) == embeddingInventoryLimit {
			for _, segment := range segments {
				if _, ok := snapshots[segment]; ok {
					continue
				}
				matched, err := s.projectionRows(ctx, g.Collection, and(scope, equal("segmentId", segment)),
					1, searchOrder{field: embeddingRevision, desc: true}, []string{"segmentId"}, false)
				if err != nil {
					return err
				}
				if len(matched) > 0 {
					snapshots[segment] = matched[0]
				}
			}
		}
	}
	pending := objects[:0]
	for _, object := range objects {
		id := object.ID
		if object.Properties[itemKind] == segmentKind {
			if row, ok := snapshots[object.Properties["segmentId"].(string)]; ok {
				id = row.Additional.ID
			}
		}
		if exists[object.ID] || id != object.ID {
			stored, found, err := s.object(ctx, g.Collection, id)
			if err != nil {
				return err
			}
			if found {
				item := object.Properties[itemKind] == "item"
				if !item {
					for _, key := range []string{sourceNamespaceProperty, projectionPluginProperty, sourceMediaTypeProperty} {
						if stored.Properties[key] == nil {
							delete(object.Properties, key)
						}
					}
				}
				// A replay can encounter a legacy passage at this segment ID.
				// Its separate vectors still reference the legacy layout.
				if object.Properties[itemKind] == segmentKind && stored.Properties[itemKind] == "passage" {
					object.Properties[itemKind] = "passage"
				}
				if sameProperties(stored.Properties, object.Properties) {
					continue
				}
				if item {
					return errors.New("immutable item projection mismatch")
				}
				if object.Properties[itemKind] == segmentKind {
					return errors.New("unified segment projection mismatch")
				}
			} else if object.Properties[itemKind] == segmentKind {
				// Embedding publication may have retired the selected
				// snapshot. Reconcile its successor on the next attempt.
				return retrieval.ErrProjectionMissing
			}
		}
		pending = append(pending, object)
	}
	return s.insertBatch(ctx, pending)
}

// Publish seeds segments and, for item-keyword generations, writes a separate
// immutable BM25 anchor for the Version. Older generations serve BM25 from
// immutable passage anchors, so their vectors remain in separate objects.
func (s *Store) Publish(ctx context.Context, g content.Generation, org, corpusID, namespace string, v content.Version, seg content.Segmentation) error {
	ctx, err := tenantContext(ctx, g.Collection, org, corpusID, g.ID)
	if err != nil {
		return err
	}
	if !className.MatchString(g.Collection) {
		return errors.New("invalid projection route")
	}
	if len(v.RecordACL) > 0 {
		if err := s.ensurePropertiesLazy(ctx, g.Collection, visibilityPropertyNames, visibilityProperties); err != nil {
			return err
		}
	}
	if err := s.ensureItemSchema(ctx, g); err != nil {
		return err
	}
	if err := s.ensureMetadata(ctx, g); err != nil {
		return err
	}
	metadata := map[string]any{}
	if g.MetadataProjected {
		values := content.ProjectionMetadata(v, g.Fields)
		for _, f := range corpus.FilterFields(g.Fields) {
			if value, ok := values[f.Name]; ok {
				metadata[metadataPropertyFor(g, f)] = metadataValue(value, f.Type)
			}
		}
	}
	texts, _ := content.ProjectionText(v, seg, g.Fields)
	objects := make([]projectionObject, 0, projectionBatch)
	flush := func() error {
		err := s.publishAnchors(ctx, g, org, objects)
		objects = objects[:0]
		return err
	}

	for i, p := range seg.Segments {
		id := objectID(org, g.ID, p.ID)
		properties := map[string]any{"organization": org, "corpusId": corpusID, "generationId": g.ID, "versionId": v.ID, "segmentationId": seg.ID, "segmentId": p.ID, sourceNamespaceProperty: namespace, "body": texts[i].Body, "title": texts[i].Title}
		addRecordACL(properties, v.RecordACL)
		if g.ItemKeywordsProjected {
			delete(properties, "body")
			delete(properties, "title")
			properties[itemKind] = segmentKind
			properties[passageText] = p.Text
			properties["recordId"] = v.RecordID
		}
		for key, value := range metadata {
			properties[key] = value
		}
		if pluginID := content.PluginOfRecipe(seg.Recipe); pluginID != "" {
			mediaType := v.SourceMediaType
			if mediaType == "" {
				mediaType = "text/plain"
			}
			properties[projectionPluginProperty] = pluginID
			properties[sourceMediaTypeProperty] = mediaType
		}
		if g.SpacesProjected && p.Derivation.LexicalText != "" {
			properties[lexicalProperty] = p.Derivation.LexicalText
		}
		lastPassageWithItem := g.ItemKeywordsProjected && i == len(seg.Segments)-1
		if len(objects) == projectionBatch || (lastPassageWithItem && len(objects) == projectionBatch-1) {
			if err := flush(); err != nil {
				return err
			}
		}
		objects = append(objects, projectionObject{Class: g.Collection, ID: id, Properties: properties})
	}
	if g.ItemKeywordsProjected {
		objects = append(objects, itemAnchor(g, org, corpusID, namespace, v, metadata))
	}
	return flush()
}

func itemAnchor(g content.Generation, org, corpusID, namespace string, v content.Version, metadata map[string]any) projectionObject {
	props := map[string]any{"organization": org, "corpusId": corpusID, "generationId": g.ID, "versionId": v.ID, "recordId": v.RecordID, sourceNamespaceProperty: namespace, itemKind: "item"}
	addRecordACL(props, v.RecordACL)
	for k, value := range metadata {
		props[k] = value
	}
	values := content.ItemText(v, g.Fields)
	for _, f := range content.ItemFields(g.Fields) {
		if text := values[f.Name]; text != "" {
			props[itemProperty(f)] = text
			if a, ok := keywords.Lookup(f.Analyzer); ok {
				props[analyzedProperty(f, a)] = a.Analyze(text)
			}
		}
	}
	id := uuidOf(content.StableID("item-keywords", org, g.ID, v.ID))
	return projectionObject{Class: g.Collection, ID: id, Properties: props}
}

// projectionOwnerFilter keeps normal search on the served projection(s) of a
// generation. Legacy anchors without ownership remain searchable. An
// evaluation query selects one exact owner and intentionally excludes those
// legacy anchors.
func projectionOwnerFilter(g content.Generation, evaluationPlugin string) filter {
	if evaluationPlugin != "" {
		return equal(projectionPluginProperty, evaluationPlugin)
	}
	// NotEqual uses the inverse equality bitmap in the pinned engine, so it
	// includes absent properties without requiring a collection null-state
	// index. Existing collections cannot add that index during bootstrap.
	known := map[string]bool{}
	for _, sp := range g.Spaces {
		if sp.OwnerPluginID != "" {
			known[sp.OwnerPluginID] = true
		}
	}
	if r := g.IngestionRouting; r != nil {
		if r.Default != "" {
			known[r.Default] = true
		}
		for _, owner := range r.Routes {
			known[owner] = true
		}
	}
	ids := make([]string, 0, len(known))
	for owner := range known {
		ids = append(ids, owner)
	}
	sort.Strings(ids)
	var legacy []filter
	if g.Collection != InitialCollection {
		legacy = append(legacy, equal("generationId", g.ID))
	} else if len(ids) == 0 {
		return filter{}
	}
	for _, owner := range ids {
		legacy = append(legacy, notEqual(projectionPluginProperty, owner))
	}
	operands := []filter{and(legacy...)}
	if routing := g.IngestionRouting; routing != nil && routing.Default != "" {
		mediaTypes := make([]string, 0, len(routing.Routes))
		for mediaType := range routing.Routes {
			mediaTypes = append(mediaTypes, mediaType)
		}
		sort.Strings(mediaTypes)
		for _, mediaType := range mediaTypes {
			operands = append(operands, and(equal(projectionPluginProperty, routing.Routes[mediaType]), equal(sourceMediaTypeProperty, mediaType)))
		}
		defaultOperands := []filter{equal(projectionPluginProperty, routing.Default)}
		for _, mediaType := range mediaTypes {
			defaultOperands = append(defaultOperands, notEqual(sourceMediaTypeProperty, mediaType))
		}
		operands = append(operands, and(defaultOperands...))
	} else {
		owners := map[string]bool{}
		for _, space := range g.Spaces {
			if space.Role == content.SpaceServed && space.OwnerPluginID != "" {
				owners[space.OwnerPluginID] = true
			}
		}
		ownerIDs := make([]string, 0, len(owners))
		for owner := range owners {
			ownerIDs = append(ownerIDs, owner)
		}
		sort.Strings(ownerIDs)
		for _, owner := range ownerIDs {
			operands = append(operands, equal(projectionPluginProperty, owner))
		}
	}
	if len(operands) == 1 {
		return operands[0]
	}
	return or(operands...)
}

// Search queries every routed (Corpus, generation) pair in one request so hybrid
// fusion sees a single candidate set. Routes must share one physical collection.
func (s *Store) searchLegacy(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
	if len(routes) == 0 {
		return nil, errors.New("projection route missing")
	}
	collection := routes[0].Generation.Collection
	filters := []filter{}
	for _, r := range routes {
		if r.Generation.Collection != collection || !className.MatchString(collection) || r.Generation.ID == "" {
			return nil, errors.New("invalid projection route")
		}
		routeFilters := []filter{projectionOwnerFilter(r.Generation, q.EvaluationPlugin), routeVisibility(ctx, r.CorpusID)}
		if tenantFrom(ctx) == "" {
			routeFilters = append([]filter{equal("corpusId", r.CorpusID), equal("generationId", r.Generation.ID)}, routeFilters...)
		}
		typed, missing, err := corpus.ResolveFilters(q.Metadata, r.Generation.Fields)
		if err != nil {
			return nil, err
		}
		if len(missing) > 0 {
			continue
		}
		if len(typed) > 0 && !r.Generation.MetadataProjected {
			return nil, retrieval.ErrMetadataFilterUnavailable
		}
		// An empty generation may be routed before its first publication.
		// Reconcile declared fields before querying their schema properties.
		if len(typed) > 0 {
			if err := s.ensureMetadata(ctx, r.Generation); err != nil {
				return nil, err
			}
		}
		for _, f := range typed {
			routeFilters = append(routeFilters, metadataConditionFor(r.Generation, f))
		}
		filters = append(filters, and(routeFilters...))
	}
	if len(filters) == 0 {
		return []content.Candidate{}, nil
	}
	operands := []filter{or(filters...)}
	if tenantFrom(ctx) == "" {
		operands = append([]filter{equal("organization", scope.Organization)}, operands...)
	}
	// The source filter is part of the candidate query, so ranking and the
	// candidate limit apply within it in every mode.
	if len(q.SourceNamespaces) > 0 {
		operands = append(operands, containsAny(sourceNamespaceProperty, q.SourceNamespaces))
	}
	operands = append(operands, identityConditions(q)...)
	where := and(operands...)
	// For a vector search, every route names the space's vectors the same
	// way, or the query cannot rank them together; a keyword search reads
	// no vector.
	target := ""
	for _, r := range routes {
		if q.Mode == "lexical" {
			break
		}
		space := q.Space
		if space == "" {
			space = r.Generation.SpaceID
		}
		name := s.VectorName(r.Generation, space)
		if target != "" && name != target {
			return nil, errors.New("routes store the space under different vectors")
		}
		target = name
		// A generation's named vectors exist from its first vector write; a
		// semantic or hybrid search before any (a new install, a Corpus whose
		// Versions still wait for their vectors) adds them, and finds nothing.
		if err := s.ensureVectors(ctx, r.Generation); err != nil {
			return nil, err
		}
	}
	// The source field ranks the title and body as written; the lexical field
	// ranks the lexical text an ingestion plugin produced.
	properties := []string{"title^2", "body"}
	if q.Field == retrieval.FieldLexical {
		properties = []string{lexicalProperty}
	}
	branch := searchBranch{mode: q.Mode, query: q.Query, properties: properties, vector: q.Vector, target: target, alpha: 0.5}
	if q.Hybrid != nil {
		branch.alpha = q.Hybrid.Alpha
		branch.ranked = q.Hybrid.Fusion == retrieval.FusionRanked
	}
	if branch.mode != "semantic" && branch.mode != "hybrid" {
		branch.mode = "lexical"
	}
	limit := retrieval.CandidateLimit
	if q.K > 0 {
		limit = q.K
	}
	rows, err := s.searchRows(ctx, collection, branch, where, searchOrder{}, limit, false, false)
	if err != nil {
		return nil, err
	}
	result := make([]content.Candidate, 0, len(rows))
	for _, r := range rows {
		if r.SegmentID == "" || r.GenerationID == "" {
			return nil, errors.New("projection candidate invalid")
		}
		candidate := r.candidate(q.Mode == "semantic")
		candidate.VersionID = ""
		result = append(result, candidate)
	}
	return result, nil
}

// PublishEmbeddings attaches named vectors by publishing a replacement
// snapshot before retiring the unified segment's seed or an older snapshot.
// Legacy passage and item keyword anchors remain immutable.
func (s *Store) PublishEmbeddings(ctx context.Context, g content.Generation, org string, data []content.EmbeddingData) error {
	if !className.MatchString(g.Collection) {
		return errors.New("invalid embedding projection")
	}
	if err := s.ensureItemSchema(ctx, g); err != nil {
		return err
	}
	if err := s.ensureVectors(ctx, g); err != nil {
		return err
	}
	// One enriched object per segment, in the order segments first appear.
	var order []string
	bySegment := map[string][]encodedEmbedding{}
	for _, p := range data {
		e := p.Artifact
		if e.Organization != org || !g.Carries(e.SpaceID) {
			return errors.New("incompatible embedding projection")
		}
		raw, err := content.VectorBytes(p.Vector)
		if err != nil || content.Hash(raw) != e.Payload.SHA256 {
			return errors.New("embedding payload mismatch")
		}
		if _, ok := bySegment[e.SegmentID]; !ok {
			order = append(order, e.SegmentID)
		}
		bySegment[e.SegmentID] = append(bySegment[e.SegmentID], encodedEmbedding{EmbeddingData: p, raw: raw})
	}

	if len(order) == 0 {
		return nil
	}
	if g.Collection == InitialCollection {
		corpusID := data[0].Artifact.CorpusID
		for _, d := range data {
			if d.Artifact.CorpusID != corpusID {
				return errors.New("embedding projection spans tenants")
			}
		}
		var err error
		ctx, err = tenantContext(ctx, g.Collection, org, corpusID, g.ID)
		if err != nil {
			return err
		}
	}
	if !g.ItemKeywordsProjected {
		if err := s.ensureProperties(ctx, g.Collection, []map[string]any{embeddingRevisionProperty()}); err != nil {
			return err
		}
	}
	for segments := range slices.Chunk(order, projectionBatch) {
		if err := s.publishEmbeddingBatch(ctx, g, org, segments, bySegment); err != nil {
			return err
		}
	}
	return nil
}

// encodedEmbedding pairs an embedding with the payload validated against its
// checksum, which is exactly the payload the gRPC write sends.
type encodedEmbedding struct {
	content.EmbeddingData
	raw []byte
}

func (s *Store) embeddingObject(g content.Generation, org, segment string, vectors []encodedEmbedding) (projectionObject, error) {
	keys := make([]string, 0, len(vectors))
	named := make(map[string][]float32, len(vectors))
	encoded := make(map[string][]byte, len(vectors))
	checksums := make(map[string]string, len(vectors))
	for _, p := range vectors {
		name := s.VectorName(g, p.Artifact.SpaceID)
		checksum := p.Artifact.Payload.SHA256
		if old, ok := checksums[name]; ok && old != checksum {
			return projectionObject{}, errors.New("conflicting embedding payloads for named vector")
		}
		checksums[name] = checksum
		named[name] = p.Vector
		encoded[name] = p.raw
		keys = append(keys, p.Artifact.SpaceID+"="+checksum)
	}
	payload := vectors[0].Artifact.Payload.SHA256
	if len(keys) > 1 {
		// Several spaces: the identity covers every payload, by space.
		sort.Strings(keys)
		payload = content.Hash([]byte(strings.Join(keys, "\n")))
	}
	return projectionObject{
		Class: g.Collection, ID: enrichedID(org, g.ID, segment, payload),
		Properties: map[string]any{"segmentId": segment}, Vectors: named, encoded: encoded,
	}, nil
}

// A legacy segment has an anchor and an enriched object. Allow room for
// partial writes and in-flight replacements in the bounded inventory.
const embeddingInventoryLimit = 3 * projectionBatch

func (s *Store) embeddingRows(ctx context.Context, g content.Generation, where filter, order searchOrder) ([]projectionRow, error) {
	properties := []string{"segmentId", embeddingRevision}
	if g.ItemKeywordsProjected {
		properties = append(properties, itemKind)
	}
	rows, err := s.projectionRows(ctx, g.Collection, where, embeddingInventoryLimit, order, properties, true)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.SegmentID == "" || row.Revision < 0 {
			return nil, errors.New("invalid embedding inventory")
		}
	}
	return rows, nil
}

func (s *Store) publishEmbeddingBatch(ctx context.Context, g content.Generation, org string, segments []string, bySegment map[string][]encodedEmbedding) error {
	scope := generationFilter(ctx, org, g.ID)
	inventory := func(segments []string) ([]projectionRow, error) {
		return s.embeddingRows(ctx, g, and(scope, containsAny("segmentId", segments)), searchOrder{})
	}
	rows, err := inventory(segments)
	if err != nil {
		return err
	}
	full := len(rows) >= embeddingInventoryLimit
	rowsBySegment := make(map[string][]projectionRow, len(segments))
	for _, row := range rows {
		rowsBySegment[row.SegmentID] = append(rowsBySegment[row.SegmentID], row)
	}

	type pendingSpec struct {
		object    projectionObject
		anchorID  string
		replacing bool
		revision  int64
	}
	var replacements []string
	specs := make([]pendingSpec, 0, len(segments))
	anchorIDs := make([]string, 0, len(segments))
	var existing []projectionObject
	for _, segment := range segments {
		object, err := s.embeddingObject(g, org, segment, bySegment[segment])
		if err != nil {
			return err
		}
		anchorID := objectID(org, g.ID, segment)
		rows := rowsBySegment[segment]
		if full {
			// Read the highest revisions for each segment independently when
			// the bounded inventory could have omitted them.
			rows, err = s.embeddingRows(ctx, g, and(scope, equal("segmentId", segment)), searchOrder{field: embeddingRevision, desc: true})
			if err != nil {
				return err
			}
		}
		var revision int64
		var found, replacing, hasSeed bool
		var snapshot projectionRow
		for _, row := range rows {
			switch row.Additional.ID {
			case anchorID:
				hasSeed = row.Kind == segmentKind
			case object.ID:
				found = true
			default:
				replacing = true
				revision = max(revision, row.Revision)
			}
			if row.Additional.ID != anchorID && row.Kind == segmentKind &&
				(snapshot.Additional.ID == "" || compareEmbeddings(row, snapshot) > 0) {
				snapshot = row
			}
		}
		if replacing || hasSeed {
			replacements = append(replacements, segment)
		}
		if len(rows) >= embeddingInventoryLimit && !found {
			// An older object with the requested ID may be beyond the page.
			if _, found, err = s.object(ctx, g.Collection, object.ID); err != nil {
				return err
			}
		}
		if found {
			existing = append(existing, object)
			continue
		}
		if snapshot.Additional.ID != "" {
			anchorID = snapshot.Additional.ID
		}
		if replacing && revision >= math.MaxInt32 {
			return errors.New("embedding revision exhausted")
		}
		specs = append(specs, pendingSpec{object: object, anchorID: anchorID, replacing: replacing, revision: revision})
		anchorIDs = append(anchorIDs, anchorID)
	}
	anchors, err := s.fetchAnchors(ctx, g.Collection, anchorIDs)
	if err != nil {
		return err
	}
	pending := make([]projectionObject, 0, len(specs))
	for _, spec := range specs {
		anchor, projected := anchors[spec.anchorID]
		if !projected {
			return retrieval.ErrProjectionMissing
		}
		object := spec.object
		for _, key := range lexicalProperties {
			// A legacy anchor may have no value to copy.
			if value, ok := anchor.Properties[key]; ok {
				object.Properties[key] = value
			}
		}
		if g.ItemKeywordsProjected && anchor.Properties[itemKind] != segmentKind {
			object.Properties[itemKind] = "vector"
		}
		for key, value := range anchor.Properties {
			if strings.HasPrefix(key, "m_") {
				object.Properties[key] = value
			}
		}
		if spec.replacing {
			object.Properties[embeddingRevision] = spec.revision + 1
		}
		pending = append(pending, object)
	}
	if err := s.insertBatch(ctx, pending); err != nil {
		return err
	}
	// Verify replayed identities before destructive cleanup so a corrupt
	// winner cannot trigger deletion of older objects.
	if err := s.verifyBatch(ctx, existing); err != nil {
		return err
	}
	if len(replacements) > 0 {
		// Read the replacements together after the write. Concurrent publishers
		// can see different sets, so each may only delete objects that lose to a
		// higher-ranked object. This prevents writers from deleting both replacements.
		rows, err = inventory(replacements)
		if err != nil {
			return err
		}
		if len(rows) >= embeddingInventoryLimit {
			for _, segment := range replacements {
				if err := s.removeStaleEnrichedOverflow(ctx, g, org, scope, segment); err != nil {
					return err
				}
			}
		} else if err := s.removeStaleEnriched(ctx, g, org, rows); err != nil {
			return err
		}
		// Re-verify after cleanup so a superseded replayed identity that was
		// just retired cannot return nil and commit deleted vectors.
		if err := s.verifyBatch(ctx, existing); err != nil {
			return err
		}
	}
	return nil
}

// Embedding revisions order successive replacements. Creation time orders
// older objects without a revision and IDs break ties between concurrent writes.
func compareEmbeddings(a, b projectionRow) int {
	if n := cmp.Compare(a.Revision, b.Revision); n != 0 {
		return n
	}
	if a.Revision == 0 {
		if n := cmp.Compare(a.Additional.Created, b.Additional.Created); n != 0 {
			return n
		}
	}
	return cmp.Compare(a.Additional.ID, b.Additional.ID)
}

func (s *Store) removeStaleEnriched(ctx context.Context, g content.Generation, org string, rows []projectionRow) error {
	winners := make(map[string]projectionRow)
	for _, row := range rows {
		if row.Additional.ID == objectID(org, g.ID, row.SegmentID) {
			continue
		}
		if winner, ok := winners[row.SegmentID]; !ok || compareEmbeddings(row, winner) > 0 {
			winners[row.SegmentID] = row
		}
	}
	var seeds []string
	for _, row := range rows {
		id := row.Additional.ID
		winner, ok := winners[row.SegmentID]
		if !ok || id == winner.Additional.ID {
			continue
		}
		if id == objectID(org, g.ID, row.SegmentID) {
			if row.Kind == segmentKind && winner.Kind == segmentKind {
				seeds = append(seeds, id)
			}
			continue
		}
		// Delete observed identities. A filter excluding the winner's ID could
		// match a concurrent insert before Weaviate indexes its ID.
		if status, err := s.call(ctx, "DELETE", tenantPath(ctx, "/v1/objects/"+g.Collection+"/"+id), nil, nil); err != nil && status != http.StatusNotFound {
			return err
		}
	}
	return s.removeSegmentSeeds(ctx, g.Collection, seeds)
}

// Retire only the seed identities observed alongside a confirmed snapshot.
func (s *Store) removeSegmentSeeds(ctx context.Context, collection string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	operands := make([]any, 0, len(ids))
	for _, id := range ids {
		operands = append(operands, equalText("id", id))
	}
	where := map[string]any{"operator": "Or", "operands": operands}
	result, err := s.deleteWhere(ctx, s.Client, collection, where)
	if err != nil || result.Complete {
		return err
	}
	// The deployment may cap a batch below our write size. Delete the
	// observed identities directly so truncation never leaves seeds behind.
	for _, id := range ids {
		if status, err := s.call(ctx, "DELETE", tenantPath(ctx, "/v1/objects/"+collection+"/"+id), nil, nil); err != nil && status != http.StatusNotFound {
			return err
		}
	}
	return nil
}

// An interrupted series of replacements can exceed the shared inventory.
func (s *Store) removeStaleEnrichedOverflow(ctx context.Context, g content.Generation, org string, scope filter, segment string) error {
	where := and(scope, equal("segmentId", segment))
	for {
		rows, err := s.embeddingRows(ctx, g, where, searchOrder{})
		if err != nil {
			return err
		}
		if err := s.removeStaleEnriched(ctx, g, org, rows); err != nil {
			return err
		}
		if len(rows) < embeddingInventoryLimit {
			return nil
		}
	}
}

// deleteWhere runs one batch delete by filter. Weaviate deletes at most its
// configured maximum matches per call; the result is complete only when fewer
// objects matched than that limit and none failed.
func (s *Store) deleteWhere(ctx context.Context, client *http.Client, collection string, where map[string]any) (retrieval.PurgeResult, error) {
	if !className.MatchString(collection) {
		return retrieval.PurgeResult{}, errors.New("invalid projection collection")
	}
	var response purgeResponse

	scoped := Store{Endpoint: s.Endpoint, Client: client, apiKey: s.apiKey}
	if err := scoped.purgeCall(ctx, "DELETE", tenantPath(ctx, "/v1/batch/objects"), map[string]any{"match": map[string]any{"class": collection, "where": where}, "output": "verbose"}, &response); err != nil {
		return retrieval.PurgeResult{}, err
	}
	r := response.Results
	if r == nil || r.Matches == nil || r.Successful == nil || r.Failed == nil || *r.Matches < 0 || *r.Successful < 0 || *r.Successful > *r.Matches || *r.Failed < 0 {
		return retrieval.PurgeResult{}, errors.New("projection purge result missing or invalid")
	}
	result := retrieval.PurgeResult{Deleted: *r.Successful}
	// Failed slots may already have committed tombstones. Only matches beyond
	// the server's admitted delete limit are known to be outside this attempt.
	if r.Limit > 0 {
		result.RemainingAtLeast = max(0, *r.Matches-r.Limit)
	}
	if *r.Failed > 0 {
		reason := "provider reported failed objects"
		for _, object := range r.Objects {
			if object.Errors != nil && len(object.Errors.Error) > 0 {
				reason = object.Errors.Error[0].Message
				break
			}
		}
		return result, fmt.Errorf("projection delete: %d failed, %d successful: %.1024s", *r.Failed, *r.Successful, s.redact(reason))
	}
	// Missing limits or incomplete success counts never prove completion.
	result.Complete = *r.Successful == *r.Matches && (*r.Matches == 0 || (r.Limit > 0 && *r.Matches < r.Limit))
	return result, nil
}

func (s *Store) purgeClient() *http.Client {
	c := *s.Client
	c.Timeout = s.purgeRequestTimeout()
	return &c
}

func equalText(path, value string) map[string]any {
	return map[string]any{"path": []string{path}, "operator": "Equal", "valueText": value}
}

// PurgeGeneration deletes a native tenant or drains the matching objects of
// a legacy (Organization, Corpus, generation) route.
func (s *Store) PurgeGeneration(ctx context.Context, collection, org, corpusID, generationID string) (retrieval.PurgeResult, error) {
	if org == "" || corpusID == "" || generationID == "" {
		return retrieval.PurgeResult{}, errors.New("incomplete generation purge filter")
	}
	if collection == InitialCollection {
		client := Store{Endpoint: s.Endpoint, Client: s.purgeClient(), apiKey: s.apiKey}
		status, err := client.call(ctx, "DELETE", tenantsPath(collection), []string{tenantName(org, corpusID, generationID)}, nil)
		if status == http.StatusNotFound {
			return retrieval.PurgeResult{Complete: true}, nil
		}
		return retrieval.PurgeResult{Complete: err == nil}, err
	}
	return s.purgeGenerationWindow(ctx, collection, org, corpusID, generationID)
}

// PurgeVersion deletes every object of one Record Version, lexical and
// enriched, in every generation of the collection, by filter.
func (s *Store) PurgeVersion(ctx context.Context, collection, org, versionID string) (retrieval.PurgeResult, error) {
	if collection == InitialCollection || org == "" || versionID == "" {
		return retrieval.PurgeResult{}, errors.New("incomplete version purge filter")
	}
	return s.deleteWhere(ctx, s.purgeClient(), collection, map[string]any{"operator": "And", "operands": []any{
		equalText("organization", org), equalText("versionId", versionID)}})
}

// PurgeVersionInGeneration deletes one dead Version within a live tenant.
// Generation retirement uses a separate whole-tenant operation.
func (s *Store) PurgeVersionInGeneration(ctx context.Context, collection, org, corpusID, generationID, versionID string, archived bool) (retrieval.PurgeResult, error) {
	if collection != InitialCollection || versionID == "" {
		return retrieval.PurgeResult{}, errors.New("invalid tenant version purge")
	}
	ctx, err := tenantContext(ctx, collection, org, corpusID, generationID)
	if err != nil {
		return retrieval.PurgeResult{}, err
	}
	client := s.purgeClient()
	result, err := s.deleteWhere(ctx, client, collection, equalText("versionId", versionID))
	if archived {
		// Batch delete can auto-activate a tenant even with no matches.
		cleanup, cancel := lifecycle.CleanupContext(ctx, 5*time.Second)
		err = errors.Join(err, s.SetGenerationActive(cleanup, collection, org, corpusID, generationID, false))
		cancel()
	}
	if err != nil {
		// A rebuild may have retired this route after the claim. Missing
		// tenants already satisfy the deletion.
		reader := &Store{Endpoint: s.Endpoint, Client: client, apiKey: s.apiKey}
		if exists, check := reader.tenantExists(ctx, collection); check == nil && !exists {
			result.Complete = true
			return result, nil
		}
	}
	return result, err
}

var _ retrieval.PurgeProjection = (*Store)(nil)
