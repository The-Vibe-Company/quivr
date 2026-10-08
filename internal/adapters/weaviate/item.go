package weaviate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

const itemKind = "itemKind"
const passageText = "passageText"
const maxItemPassages = 2400

func itemProperty(f corpus.Field) string {
	b, _ := json.Marshal(f)
	return "k_" + content.Hash(b)[:32]
}

func (s *Store) ensureItemSchema(ctx context.Context, g content.Generation) error {
	if !g.ItemKeywordsProjected {
		return nil
	}
	props := []map[string]any{filterable(itemKind), filterable("recordId"), {"name": passageText, "dataType": []string{"text"}, "indexSearchable": false, "indexFilterable": false}}
	for _, f := range content.ItemFields(g.Fields) {
		props = append(props, searchable(itemProperty(f)))
		if f.Analyzer != "" {
			props = append(props, searchable(itemProperty(f)+"_fr"))
		}
	}
	return s.ensureProperties(ctx, g.Collection, props)
}

// Reconcile additive schemas once per process, including concurrent creation.
func (s *Store) ensureProperties(ctx context.Context, collection string, props []map[string]any) error {
	s.metadataMu.Lock()
	if s.metadataProperties == nil {
		s.metadataProperties = map[string]bool{}
	}
	var missing []map[string]any
	for _, p := range props {
		if !s.metadataProperties[collection+"/"+p["name"].(string)] {
			missing = append(missing, p)
		}
	}
	s.metadataMu.Unlock()
	if len(missing) == 0 {
		return nil
	}
	var schema struct {
		Properties []struct {
			Name string `json:"name"`
		} `json:"properties"`
	}
	if _, err := s.call(ctx, "GET", "/v1/schema/"+collection, nil, &schema); err != nil {
		return err
	}
	present := map[string]bool{}
	for _, p := range schema.Properties {
		present[p.Name] = true
	}
	for _, p := range missing {
		name := p["name"].(string)
		if !present[name] {
			if _, err := s.call(ctx, "POST", "/v1/schema/"+collection+"/properties", p, nil); err != nil {
				schema.Properties = nil
				if _, readErr := s.call(ctx, "GET", "/v1/schema/"+collection, nil, &schema); readErr != nil {
					return readErr
				}
				found := false
				for _, p := range schema.Properties {
					if p.Name == name {
						found = true
					}
				}
				if !found {
					return err
				}
			}
		}
		s.metadataMu.Lock()
		s.metadataProperties[collection+"/"+name] = true
		s.metadataMu.Unlock()
	}
	return nil
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
			if f.Analyzer != "" {
				props[itemProperty(f)+"_fr"] = content.AnalyzeKeywords(text, f.Analyzer)
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
	if err := s.insert(ctx, map[string]any{"class": g.Collection, "id": id, "properties": props}); err != nil {
		return err
	}
	existing, found, err = s.object(ctx, g.Collection, id)
	if err != nil {
		return err
	}
	if !found || !sameProperties(existing.Properties, props) {
		return errors.New("item projection verification mismatch")
	}
	return nil
}

type itemRow struct {
	SegmentID    string `json:"segmentId"`
	GenerationID string `json:"generationId"`
	VersionID    string `json:"versionId"`
	Text         string `json:"passageText"`
	Additional   struct {
		Score    *string  `json:"score"`
		Distance *float64 `json:"distance"`
	} `json:"_additional"`
}

func (r itemRow) candidate(dense bool) content.Candidate {
	score := 0.0
	if dense && r.Additional.Distance != nil {
		score = 1 - *r.Additional.Distance
	} else if r.Additional.Score != nil {
		score, _ = strconv.ParseFloat(*r.Additional.Score, 64)
	}
	return content.Candidate{SegmentID: r.SegmentID, GenerationID: r.GenerationID, VersionID: r.VersionID, Score: score}
}
func (s *Store) itemQuery(ctx context.Context, collection, branch, where string, limit int, text bool) ([]itemRow, error) {
	fields := "segmentId generationId versionId"
	if text {
		fields += " passageText"
	}
	query := fmt.Sprintf("{Get{%s(%swhere:%s,limit:%d){%s _additional{score distance}}}}", collection, branch, where, limit, fields)
	var out struct {
		Data struct {
			Get map[string][]itemRow `json:"Get"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	responseLimit := int64(2 << 20)
	if text {
		// The bounded window can carry more text than a 128-row page.
		responseLimit = 16 << 20
	}
	if _, err := s.callLimit(ctx, "POST", "/v1/graphql", map[string]any{"query": query}, &out, responseLimit); err != nil {
		return nil, err
	}
	if len(out.Errors) > 0 {
		return nil, errors.New("item projection query failed")
	}
	rows, ok := out.Data.Get[collection]
	if !ok {
		return nil, errors.New("item projection response missing")
	}
	return rows, nil
}

func identityConditions(q retrieval.Request) []string {
	var out []string
	for _, field := range []struct {
		name   string
		values []string
	}{{"recordId", q.RecordIDs}, {"versionId", q.VersionIDs}} {
		if len(field.values) > 0 {
			var choices []string
			for _, v := range field.values {
				choices = append(choices, equal(field.name, v))
			}
			out = append(out, or(choices...))
		}
	}
	return out
}

func (s *Store) itemWhere(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request, kind string) (string, error) {
	var clauses []string
	for _, r := range routes {
		if !className.MatchString(r.Generation.Collection) || r.Generation.ID == "" {
			return "", errors.New("invalid projection route")
		}
		typed, missing, err := corpus.ResolveFilters(q.Metadata, r.Generation.Fields)
		if err != nil {
			return "", err
		}
		if len(missing) > 0 {
			continue
		}
		if len(typed) > 0 && !r.Generation.MetadataProjected {
			return "", retrieval.ErrMetadataFilterUnavailable
		}
		if err = s.ensureMetadata(ctx, r.Generation); err != nil {
			return "", err
		}
		values := []string{equal("corpusId", r.CorpusID), equal("generationId", r.Generation.ID)}
		if kind != "item" {
			values = append(values, projectionOwnerFilter(r.Generation, q.EvaluationPlugin))
		}
		for _, f := range typed {
			values = append(values, metadataConditionFor(r.Generation, f))
		}
		clauses = append(clauses, and(values...))
	}
	if len(clauses) == 0 {
		return "", nil
	}
	kinds := equal(itemKind, kind)
	if kind == "vector" {
		// Include earlier enriched objects with the passage kind; nearVector
		// only scores objects carrying this target vector.
		kinds = or(kinds, equal(itemKind, "passage"))
	}
	all := []string{equal("organization", scope.Organization), kinds, or(clauses...)}
	if len(q.SourceNamespaces) > 0 {
		var namespaces []string
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
func termCoverage(text, query string) int {
	terms := strings.FieldsFunc(content.AnalyzeKeywords(query, "french_light"), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	words := map[string]bool{}
	for _, w := range strings.Fields(content.AnalyzeKeywords(text, "french_light")) {
		words[w] = true
	}
	score := 0
	seen := map[string]bool{}
	for _, t := range terms {
		if words[t] && !seen[t] {
			score++
			seen[t] = true
		}
	}
	return score
}

// Fetch one deterministic, bounded passage window. Repeated offset queries
// re-sort the same allow-list and can consume the entire search deadline.
func (s *Store) itemPassages(ctx context.Context, collection, where string) ([]itemRow, error) {
	return s.itemQuery(ctx, collection, `sort:[{path:["segmentId"],order:asc}],`, where, maxItemPassages, true)
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
		routes []retrieval.Route
		fields []corpus.Field
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
			partitions[key] = &partition{fields: fields}
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
			if where == "" {
				continue
			}
			items := map[string]content.Candidate{}
			// An ownerless item may not yet have passages from the selected
			// ingestion owner. Allow one refill at twice the candidate depth;
			// each round has one passage request, without deepen-and-retry.
			for round, depth := 0, limit; round < 2; round, depth = round+1, min(2400, depth*2) {
				more := false
				for _, normalized := range []bool{false, true} {
					var props []string
					for _, f := range p.fields {
						property := itemProperty(f)
						if normalized {
							if f.Analyzer == "" {
								continue
							}
							property += "_fr"
						}
						props = append(props, property+"^"+strconv.Itoa(f.EffectiveBoost()))
					}
					if len(props) == 0 {
						continue
					}
					query := q.Query
					if normalized {
						query = content.AnalyzeKeywords(query, "french_light")
					}
					if strings.TrimSpace(query) == "" {
						continue
					}
					encoded, _ := json.Marshal(props)
					rows, err := s.itemQuery(ctx, collection, fmt.Sprintf("bm25:{query:%s,properties:%s},", quote(query), encoded), where, depth, false)
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
				var ids []string
				for _, c := range ordered(items) {
					ids = append(ids, equal("versionId", c.VersionID))
				}
				rows, err := s.itemPassages(ctx, collection, and(passageWhere, or(ids...)))
				if err != nil {
					return nil, err
				}
				// Passage selection uses lexical term coverage with deterministic ties;
				// every returned ID is subsequently hydrated from canonical storage.
				coverage := map[string]int{}
				for _, r := range rows {
					coverage[r.SegmentID] = termCoverage(r.Text, q.Query)
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
					// Preserve the selected passage on equal original/French scores.
					if prev, ok := sparse[id]; !ok || c.Score > prev.Score {
						sparse[id] = c
					}
				}
				// A full passage window exhausts the scan budget. A broader
				// item allow-list would repeat another truncated passage scan.
				if len(chosen) >= limit || !more || depth >= 2400 || len(rows) == maxItemPassages {
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
		if where != "" {
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
			vector, _ := json.Marshal(q.Vector)
			branch := fmt.Sprintf("nearVector:{vector:%s,targetVectors:[%s]},", vector, quote(target))
			for depth := limit; ; depth = min(2400, depth*2) {
				rows, err := s.itemQuery(ctx, collection, branch, where, depth, false)
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
			pool := map[string]content.Candidate{}
			for _, row := range rows {
				keepBest(pool, row, false)
			}
			rows = ordered(pool)
			for i := range rows {
				rows[i].Score = 1 / float64(60+i+1)
			}
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
