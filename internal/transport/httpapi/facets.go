package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
)

const maxConcurrentFacets = 8

// Exact aggregation has more work than one bounded catalog page. Keep response
// headroom within the server's 30-second write deadline.
const facetTimeout = 25 * time.Second

func (a *API) CountFacets(ctx context.Context, in transport.CountFacetsRequestObject) (transport.CountFacetsResponseObject, error) {
	return transport.CountFacetsResponseFunc(func(w http.ResponseWriter) { a.countFacets(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) countFacets(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	raw, ok := decodeRequest(w, r, a.schemas["FacetRequest"])
	if !ok {
		return
	}
	encoded, _ := json.Marshal(raw)
	var wire struct {
		CorpusIDs []string             `json:"corpus_ids"`
		Fields    []content.FacetField `json:"fields"`
		Filter    *struct {
			Metadata []corpus.MetadataFilter `json:"metadata"`
			Sources  []string                `json:"source_namespaces"`
		} `json:"filter"`
		After    *string `json:"accepted_after"`
		Before   *string `json:"accepted_before"`
		Accuracy string  `json:"accuracy"`
	}
	if json.Unmarshal(encoded, &wire) != nil {
		writeError(w, publicerr.InvalidSchema, nil)
		return
	}
	q := content.FacetQuery{Records: content.RecordQuery{CorpusIDs: wire.CorpusIDs}, Fields: wire.Fields, Fast: wire.Accuracy == "fast"}
	if wire.Filter != nil {
		q.Records.Metadata = wire.Filter.Metadata
		q.SourceNamespaces = wire.Filter.Sources
	}
	values := url.Values{}
	if wire.After != nil {
		values.Set("accepted_after", *wire.After)
	}
	if wire.Before != nil {
		values.Set("accepted_before", *wire.Before)
	}
	if q.Records.AcceptedAfter, ok = recordTime(w, values, "accepted_after"); !ok {
		return
	}
	if q.Records.AcceptedBefore, ok = recordTime(w, values, "accepted_before"); !ok {
		return
	}
	if q.Records.AcceptedAfter != nil && q.Records.AcceptedBefore != nil && q.Records.AcceptedAfter.After(*q.Records.AcceptedBefore) {
		writeError(w, publicerr.InvalidQuery, nil)
		return
	}
	seen := map[string]bool{}
	for i, f := range q.Fields {
		if seen[f.Field] {
			writeError(w, publicerr.InvalidQuery, nil)
			return
		}
		seen[f.Field] = true
		if f.Limit == 0 {
			q.Fields[i].Limit = 20
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), facetTimeout)
	defer cancel()
	select {
	case a.facets <- struct{}{}:
		defer func() { <-a.facets }()
	default:
		w.Header().Set("Retry-After", "1")
		writeError(w, publicerr.ContentUnavailable, nil)
		return
	}
	var excluded []corpus.CorpusExclusion
	counts, err := a.Content.CountFacets(ctx, scope, q, func() (content.FacetQuery, error) {
		var err error
		q, excluded, err = a.facetRoutes(ctx, scope, q)
		return q, err
	})
	if err != nil {
		writeError(w, err, publicerr.ContentUnavailable)
		return
	}
	if counts.AsOf != nil {
		utc := counts.AsOf.UTC()
		counts.AsOf = &utc
	}
	// Exact counts carry neither marker, so their answer is unchanged.
	send(w, 200, struct {
		Items          []content.Facet              `json:"items"`
		Excluded       *[]transport.CorpusExclusion `json:"excluded_corpora,omitempty"`
		AsOf           *time.Time                   `json:"as_of,omitempty"`
		Approximate    bool                         `json:"approximate,omitempty"`
		SampleFraction float64                      `json:"sample_fraction,omitempty"`
	}{counts.Items, exclusionsToTransport(excluded), counts.AsOf, counts.SampleFraction > 0, counts.SampleFraction})
}

func (a *API) facetRoutes(ctx context.Context, scope corpus.Scope, q content.FacetQuery) (content.FacetQuery, []corpus.CorpusExclusion, error) {
	if a.Retrieval.Routing == nil {
		return q, nil, publicerr.ContentUnavailable
	}
	var excluded []corpus.CorpusExclusion
	types := map[string]string{}
	for _, id := range q.Records.CorpusIDs {
		g, err := a.Retrieval.Routing.Generation(ctx, scope.Organization, id)
		if err != nil {
			return q, nil, publicerr.ContentUnavailable
		}
		filters, missing, err := corpus.ResolveFilters(q.Records.Metadata, g.Fields)
		if err != nil {
			return q, nil, err
		}
		declared := map[string]corpus.Field{}
		for _, f := range corpus.FilterFields(g.Fields) {
			declared[f.Name] = f
		}
		var snapshot []content.FacetField
		if q.Fast {
			for _, f := range corpus.FilterFields(g.Fields) {
				snapshot = append(snapshot, content.FacetField{Field: f.Name, Type: f.Type})
			}
		}
		absent := map[string]bool{}
		for _, f := range missing {
			absent[f] = true
		}
		for _, f := range q.Fields {
			if _, ok := declared[f.Field]; !ok {
				absent[f.Field] = true
			}
		}
		if len(absent) > 0 {
			fields := []string{}
			for f := range absent {
				fields = append(fields, f)
			}
			sort.Strings(fields)
			excluded = append(excluded, corpus.CorpusExclusion{CorpusID: id, Fields: fields})
			continue
		}
		if !g.MetadataProjected {
			return q, nil, publicerr.MetadataFilterUnavailable
		}
		for i, f := range q.Fields {
			typ := declared[f.Field].Type
			if previous, ok := types[f.Field]; ok && previous != typ {
				return q, nil, publicerr.InvalidQuery
			}
			if (typ == "datetime") != (f.Interval != "") {
				return q, nil, publicerr.InvalidQuery
			}
			types[f.Field] = typ
			q.Fields[i].Type = typ
		}
		q.Records.FilterRoutes = append(q.Records.FilterRoutes, content.CatalogFilterRoute{CorpusID: id, GenerationID: g.ID, Filters: filters})
		if q.Fast {
			if q.Declared == nil {
				q.Declared = map[string][]content.FacetField{}
			}
			q.Declared[id] = snapshot
		}
	}
	return q, excluded, nil
}
