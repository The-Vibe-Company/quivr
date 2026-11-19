package weaviate

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	wv "github.com/weaviate/weaviate-go-client/v6"
	"github.com/weaviate/weaviate-go-client/v6/collections"
	"github.com/weaviate/weaviate-go-client/v6/query"
	"github.com/weaviate/weaviate-go-client/v6/query/filter"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/keywords"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

const itemKind = "itemKind"
const passageText = "passageText"
const maxItemPassages = 2400

func itemProperty(f corpus.Field) string {
	b, _ := json.Marshal(f)
	return "k_" + content.Hash(b)[:32]
}

// analyzedProperty stores the field's keyword copy written by its analyzer.
func analyzedProperty(f corpus.Field, a keywords.Analyzer) string {
	return itemProperty(f) + "_" + a.Property
}

// fieldAnalyzers lists the distinct analyzers fields name, in field order.
func fieldAnalyzers(fields []corpus.Field) []keywords.Analyzer {
	var out []keywords.Analyzer
	seen := map[string]bool{}
	for _, f := range fields {
		if a, ok := keywords.Lookup(f.Analyzer); ok && !seen[a.Name] {
			seen[a.Name] = true
			out = append(out, a)
		}
	}
	return out
}

func (s *Store) ensureItemSchema(ctx context.Context, g content.Generation) error {
	if !g.ItemKeywordsProjected {
		return nil
	}
	props := []collections.Property{filterable(itemKind), filterable("recordId"), {Name: passageText, DataType: collections.DataTypeText, IndexSearchable: flag(false), IndexFilterable: flag(false)}}
	for _, f := range content.ItemFields(g.Fields) {
		props = append(props, searchable(itemProperty(f)))
		if a, ok := keywords.Lookup(f.Analyzer); ok {
			props = append(props, searchable(analyzedProperty(f, a)))
		}
	}
	return s.ensureProperties(ctx, g.Collection, props)
}

func (s *Store) publishItem(ctx context.Context, g content.Generation, org, corpusID, namespace string, v content.Version, metadata map[string]any) error {
	props := map[string]any{"organization": org, "corpusId": corpusID, "generationId": g.ID, "versionId": v.ID, "recordId": v.RecordID, sourceNamespaceProperty: namespace, itemKind: "item"}
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
	existing, found, err := s.object(ctx, g.Collection, id)
	if err != nil {
		return err
	}
	if found {
		if sameProperties(existing.Properties, props) {
			return nil
		}
		return errors.New("immutable item projection mismatch")
	}
	if err := s.insert(ctx, g.Collection, projected{id: id, properties: props}); err != nil {
		return err
	}
	existing, found, err = s.written(ctx, g.Collection, id)
	if err != nil {
		return err
	}
	if !found || !sameProperties(existing.Properties, props) {
		return errors.New("item projection verification mismatch")
	}
	return nil
}

type itemRow struct {
	SegmentID    string
	GenerationID string
	VersionID    string
	Text         string
	score        *float64
	distance     *float64
}

func (r itemRow) candidate(dense bool) content.Candidate {
	score := 0.0
	if dense && r.distance != nil {
		score = 1 - *r.distance
	} else if r.score != nil {
		score = *r.score
	}
	return content.Candidate{SegmentID: r.SegmentID, GenerationID: r.GenerationID, VersionID: r.VersionID, Score: score}
}

func itemProperties(text bool) []string {
	fields := []string{"segmentId", "generationId", "versionId"}
	if text {
		fields = append(fields, passageText)
	}
	return fields
}

func itemRowOf(o storedObject) itemRow {
	text := func(key string) string { v, _ := o.Properties[key].(string); return v }
	return itemRow{SegmentID: text("segmentId"), GenerationID: text("generationId"), VersionID: text("versionId"), Text: text(passageText)}
}

// itemQuery runs one scored item query; search.properties is set here.
func (s *Store) itemQuery(ctx context.Context, collection string, search ranked) ([]itemRow, error) {
	search.properties = itemProperties(false)
	objects, err := s.rank(ctx, collection, search)
	if err != nil {
		if errors.Is(err, errResponseLimit) {
			return nil, err
		}
		return nil, queryFailed(err, "item projection query failed")
	}
	rows := make([]itemRow, len(objects))
	for i, o := range objects {
		rows[i] = itemRowOf(o.storedObject)
		rows[i].score, rows[i].distance = o.score, o.distance
	}
	return rows, nil
}

func identityConditions(q retrieval.Request) []filter.Expr {
	var out []filter.Expr
	for _, field := range []struct {
		name   string
		values []string
	}{{"recordId", q.RecordIDs}, {"versionId", q.VersionIDs}} {
		if len(field.values) > 0 {
			var choices []filter.Expr
			for _, v := range field.values {
				choices = append(choices, equal(field.name, v))
			}
			out = append(out, or(choices...))
		}
	}
	return out
}

func (s *Store) itemWhere(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request, kind string) (filter.Expr, error) {
	var clauses []filter.Expr
	for _, r := range routes {
		if !className.MatchString(r.Generation.Collection) || r.Generation.ID == "" {
			return nil, errors.New("invalid projection route")
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
		if err = s.ensureMetadata(ctx, r.Generation); err != nil {
			return nil, err
		}
		values := []filter.Expr{equal("corpusId", r.CorpusID), equal("generationId", r.Generation.ID)}
		if kind != "item" {
			values = append(values, projectionOwnerFilter(r.Generation, q.EvaluationPlugin))
		}
		for _, f := range typed {
			values = append(values, metadataConditionFor(r.Generation, f))
		}
		clauses = append(clauses, and(values...))
	}
	if len(clauses) == 0 {
		return nil, nil
	}
	kinds := equal(itemKind, kind)
	if kind == "vector" {
		// Include earlier enriched objects with the passage kind; nearVector
		// only scores objects carrying this target vector.
		kinds = or(kinds, equal(itemKind, "passage"))
	}
	all := []filter.Expr{equal("organization", scope.Organization), kinds, or(clauses...)}
	if len(q.SourceNamespaces) > 0 {
		var namespaces []filter.Expr
		for _, v := range q.SourceNamespaces {
			namespaces = append(namespaces, equal(sourceNamespaceProperty, v))
		}
		all = append(all, or(namespaces...))
	}
	all = append(all, identityConditions(q)...)
	return and(all...), nil
}

func candidateKey(c content.Candidate, grouped bool) string {
	if grouped {
		return c.GenerationID + "/" + c.VersionID
	}
	return c.GenerationID + "/" + c.SegmentID
}
func keepBest(pool map[string]content.Candidate, c content.Candidate, grouped bool) {
	key := candidateKey(c, grouped)
	prev, ok := pool[key]
	if !ok || c.Score > prev.Score || (c.Score == prev.Score && c.SegmentID < prev.SegmentID) {
		pool[key] = c
	}
}
func ordered(pool map[string]content.Candidate) []content.Candidate {
	out := make([]content.Candidate, 0, len(pool))
	for _, c := range pool {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].SegmentID != out[j].SegmentID {
			return out[i].SegmentID < out[j].SegmentID
		}
		return candidateKey(out[i], true) < candidateKey(out[j], true)
	})
	return out
}

// termCoverage counts the distinct query terms text contains, under whichever
// of the fields' analyzers matches the most.
func termCoverage(text, query string, analyzers []keywords.Analyzer) int {
	best := 0
	for _, a := range analyzers {
		words := map[string]bool{}
		for _, w := range strings.Fields(a.Analyze(text)) {
			words[w] = true
		}
		score := 0
		seen := map[string]bool{}
		for _, t := range strings.Fields(a.Analyze(query)) {
			if words[t] && !seen[t] {
				score++
				seen[t] = true
			}
		}
		best = max(best, score)
	}
	return best
}

// Fetch one deterministic, bounded passage window. Repeated offset queries
// re-sort the same allow-list and can consume the entire search deadline.
func (s *Store) itemPassages(ctx context.Context, collection string, where filter.Expr) ([]itemRow, bool, error) {
	window := func(limit int) ([]itemRow, error) {
		var result *query.Result
		err := s.call(ctx, func(ctx context.Context, c *wv.Client) (err error) {
			result, err = c.Collections.Use(collection).Query.OverAll(ctx, query.OverAll{Filter: where, Limit: limit, Sort: []query.SortExpr{{Properties: []string{"segmentId"}}}, ReturnProperties: itemProperties(true)})
			return err
		})
		if tooLarge(err) {
			return nil, errResponseLimit
		}
		if err != nil {
			return nil, queryFailed(err, "item projection query failed")
		}
		rows := make([]itemRow, len(result.Objects))
		for i, o := range result.Objects {
			rows[i] = itemRowOf(stored(o))
		}
		return rows, nil
	}
	rows, err := window(maxItemPassages)
	if errors.Is(err, errResponseLimit) {
		// One canonical passage is bounded by its 2 MiB source blob, well under
		// the index's message limit. Recover once rather than retrying smaller pages.
		rows, err = window(1)
		return rows, true, err
	}
	return rows, len(rows) == maxItemPassages, err
}

// itemSearch consolidates sparse copies and max passage scores BEFORE fusion.
// Alpha is applied once to one sparse/dense score per item, irrespective of
// the number of passages, analyzers, or Corpus configuration partitions.
func (s *Store) itemSearch(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
	collection := routes[0].Generation.Collection
	grouped := q.GroupBy == "record"
	limit := q.K
	if limit == 0 {
		limit = retrieval.CandidateLimit
	}
	sparse, dense := map[string]content.Candidate{}, map[string]content.Candidate{}
	type partition struct {
		routes    []retrieval.Route
		fields    []corpus.Field
		analyzers []keywords.Analyzer
	}
	partitions := map[string]*partition{}
	var partitionKeys []string
	for _, r := range routes {
		if r.Generation.Collection != collection {
			return nil, errors.New("invalid projection route")
		}
		if err := s.ensureItemSchema(ctx, r.Generation); err != nil {
			return nil, err
		}
		fields := content.ItemFields(r.Generation.Fields)
		b, _ := json.Marshal(fields)
		key := string(b)
		if partitions[key] == nil {
			partitions[key] = &partition{fields: fields, analyzers: fieldAnalyzers(fields)}
			partitionKeys = append(partitionKeys, key)
		}
		partitions[key].routes = append(partitions[key].routes, r)
	}
	sort.Strings(partitionKeys)
	if q.Mode != "semantic" {
		for _, key := range partitionKeys {
			p := partitions[key]
			where, err := s.itemWhere(ctx, p.routes, scope, q, "item")
			if err != nil {
				return nil, err
			}
			if where == nil {
				continue
			}
			// The unnamed request scores the fields' own text; each analyzer then
			// scores its copies with the query analyzed the same way.
			requests := append([]keywords.Analyzer{{}}, p.analyzers...)
			passageAnalyzers := p.analyzers
			if len(passageAnalyzers) == 0 {
				passageAnalyzers = []keywords.Analyzer{keywords.Folded}
			}
			items := map[string]content.Candidate{}
			// An ownerless item may not yet have passages from the selected
			// ingestion owner. Allow one refill at twice the candidate depth;
			// passage retrieval has one window and bounded byte-limit recovery.
			for round, depth := 0, limit; round < 2; round, depth = round+1, min(2400, depth*2) {
				more := false
				for _, analyzer := range requests {
					var props []string
					for _, f := range p.fields {
						property := itemProperty(f)
						if analyzer.Name != "" {
							if f.Analyzer != analyzer.Name {
								continue
							}
							property = analyzedProperty(f, analyzer)
						}
						props = append(props, property+"^"+strconv.Itoa(f.EffectiveBoost()))
					}
					terms := q.Query
					if analyzer.Name != "" {
						terms = analyzer.Analyze(terms)
					}
					if strings.TrimSpace(terms) == "" {
						continue
					}
					rows, err := s.itemQuery(ctx, collection, ranked{keywords: terms, keywordProperties: props, filter: where, limit: depth})
					if err != nil {
						return nil, err
					}
					more = more || len(rows) == depth
					for _, r := range rows {
						c := r.candidate(false)
						keepBest(items, c, true)
					}
				}
				if len(items) == 0 {
					break
				}
				passageWhere, err := s.itemWhere(ctx, p.routes, scope, q, "passage")
				if err != nil {
					return nil, err
				}
				var ids []filter.Expr
				for _, c := range ordered(items) {
					ids = append(ids, equal("versionId", c.VersionID))
				}
				rows, exhausted, err := s.itemPassages(ctx, collection, and(passageWhere, or(ids...)))
				if err != nil {
					return nil, err
				}
				// Passage selection uses lexical term coverage with deterministic ties;
				// every returned ID is subsequently hydrated from canonical storage.
				coverage := map[string]int{}
				for _, r := range rows {
					coverage[r.SegmentID] = termCoverage(r.Text, q.Query, passageAnalyzers)
				}
				sort.Slice(rows, func(i, j int) bool {
					a, b := coverage[rows[i].SegmentID], coverage[rows[j].SegmentID]
					if a != b {
						return a > b
					}
					return rows[i].SegmentID < rows[j].SegmentID
				})
				chosen := map[string]bool{}
				for _, r := range rows {
					item, ok := items[r.GenerationID+"/"+r.VersionID]
					if !ok || r.SegmentID == "" {
						continue
					}
					c := r.candidate(false)
					c.Score = item.Score
					id := candidateKey(c, grouped)
					if grouped && chosen[id] {
						continue
					}
					chosen[id] = true
					// Preserve the selected passage on equal original/analyzed scores.
					if prev, ok := sparse[id]; !ok || c.Score > prev.Score {
						sparse[id] = c
					}
				}
				// A full window or byte limit exhausts the scan budget. A broader
				// item allow-list would repeat another truncated passage scan.
				if len(chosen) >= limit || !more || depth >= 2400 || exhausted {
					break
				}
			}
		}
	}
	if q.Mode != "lexical" {
		where, err := s.itemWhere(ctx, routes, scope, q, "vector")
		if err != nil {
			return nil, err
		}
		if where != nil {
			target := ""
			for _, r := range routes {
				space := q.Space
				if space == "" {
					space = r.Generation.SpaceID
				}
				name := s.VectorName(r.Generation, space)
				if target != "" && target != name {
					return nil, errors.New("routes store different vectors")
				}
				target = name
				if err := s.ensureVectors(ctx, r.Generation); err != nil {
					return nil, err
				}
			}
			for depth := limit; ; depth = min(2400, depth*2) {
				rows, err := s.itemQuery(ctx, collection, ranked{vector: q.Vector, target: target, filter: where, limit: depth})
				if err != nil {
					return nil, err
				}
				for _, r := range rows {
					if r.SegmentID != "" {
						keepBest(dense, r.candidate(true), grouped)
					}
				}
				if !grouped || len(dense) >= limit || len(rows) < depth || depth >= 2400 {
					break
				}
			}
		}
	}
	if q.Mode == "lexical" {
		return ordered(sparse), nil
	}
	if q.Mode == "semantic" {
		return ordered(dense), nil
	}
	alpha, fusion := 0.5, retrieval.FusionRelativeScore
	if q.Hybrid != nil {
		alpha, fusion = q.Hybrid.Alpha, q.Hybrid.Fusion
	}
	return fuseItems(sparse, dense, alpha, fusion, grouped), nil
}

func fuseItems(sparse, dense map[string]content.Candidate, alpha float64, fusion string, grouped bool) []content.Candidate {
	out := map[string]content.Candidate{}
	for _, leg := range []struct {
		pool   map[string]content.Candidate
		weight float64
		dense  bool
	}{{sparse, 1 - alpha, false}, {dense, alpha, true}} {
		rows := ordered(leg.pool)
		if len(rows) == 0 || leg.weight == 0 {
			continue
		}
		lo, hi := rows[len(rows)-1].Score, rows[0].Score
		for i, c := range rows {
			score := 1.0
			if fusion == retrieval.FusionRanked {
				score = 1 / float64(60+i+1)
			} else if hi != lo {
				score = (c.Score - lo) / (hi - lo)
			}
			key := candidateKey(c, grouped)
			prev, ok := out[key]
			if ok {
				if leg.dense {
					c.Score = prev.Score
				} else {
					c = prev
				}
			} else {
				c.Score = 0
			}
			c.Score += leg.weight * score
			out[key] = c
		}
	}
	return ordered(out)
}

// Search queries routes in partitions that one index request can rank
// together: by recipe (item keywords or passages) and, for a vector search,
// by the named vector that stores the space, which differs between
// generations built with different index settings.
func (s *Store) Search(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
	if len(routes) == 0 {
		return nil, errors.New("projection route missing")
	}
	type partition struct {
		item   bool
		routes []retrieval.Route
	}
	partitions := map[string]*partition{}
	var keys []string
	for _, r := range routes {
		// Partitions are merged, so every route must share one collection.
		if r.Generation.Collection != routes[0].Generation.Collection {
			return nil, errors.New("invalid projection route")
		}
		item := r.Generation.ItemKeywordsProjected && q.Field != retrieval.FieldLexical
		key := strconv.FormatBool(item)
		if q.Mode != "lexical" {
			space := q.Space
			if space == "" {
				space = r.Generation.SpaceID
			}
			key += "/" + s.VectorName(r.Generation, space)
		}
		if partitions[key] == nil {
			partitions[key] = &partition{item: item}
			keys = append(keys, key)
		}
		partitions[key].routes = append(partitions[key].routes, r)
	}
	sort.Strings(keys)
	var out []content.Candidate
	// Each partition's BM25 and hybrid scores are normalized over its own
	// candidates (item and passage BM25 also score different populations).
	// Reciprocal ranks put mixed partitions on a common scale without
	// reapplying hybrid alpha. Semantic scores are 1 minus the distance in
	// one space, comparable as they are.
	mixedRanks := len(keys) > 1 && q.Mode != "semantic"
	for _, key := range keys {
		p := partitions[key]
		var rows []content.Candidate
		var err error
		if p.item {
			rows, err = s.itemSearch(ctx, p.routes, scope, q)
		} else {
			if len(q.RecordIDs) > 0 {
				for _, r := range p.routes {
					if !r.Generation.ItemKeywordsProjected {
						return nil, retrieval.ErrMetadataFilterUnavailable
					}
				}
			}
			rows, err = s.searchLegacy(ctx, p.routes, scope, q)
		}
		if err != nil {
			return nil, err
		}
		if mixedRanks {
			rows = retrieval.NormalizeCandidateRanks(rows)
		}
		out = append(out, rows...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].SegmentID < out[j].SegmentID
	})
	if q.K > 0 && len(out) > q.K {
		out = out[:q.K]
	}
	return out, nil
}
