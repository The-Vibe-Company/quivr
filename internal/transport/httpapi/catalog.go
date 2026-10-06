package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
)

// recordPage binds the Corpus, authorization scope, order and time-bound
// instants. Version 1 ID cursors remain valid for the original unbounded catalog.
type recordPage struct {
	Version         int                 `json:"v"`
	Corpus          string              `json:"c"`
	Scope           string              `json:"s"`
	After           string              `json:"a"`
	Order           content.RecordOrder `json:"o,omitempty"`
	AcceptedAfter   *time.Time          `json:"lo,omitempty"`
	AcceptedBefore  *time.Time          `json:"hi,omitempty"`
	AfterAcceptedAt *time.Time          `json:"at,omitempty"`
	FilterScope     string              `json:"f,omitempty"`
}

var errPageScope = errors.New("cursor_scope_changed")

func (a *API) encodeRecordPage(p recordPage) string { return a.encodePage(recordPageDomain, p) }

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func (a *API) decodeRecordPage(token, corpusID string, s corpus.Scope, q content.RecordQuery) (content.RecordQuery, error) {
	var p recordPage
	if a.decodePage(recordPageDomain, token, &p) != nil || (p.Version != 1 && p.Version != 2 && p.Version != 3) || p.After == "" {
		return q, errors.New("invalid_cursor")
	}
	order := p.Order
	if order == "" {
		order = content.RecordIDOrder
	}
	if p.Corpus != corpusID || p.Scope != scopeDigest(s) || order != q.Order || !sameTime(p.AcceptedAfter, q.AcceptedAfter) || !sameTime(p.AcceptedBefore, q.AcceptedBefore) || p.FilterScope != catalogFilterScope(q) {
		return q, errPageScope
	}
	q.AfterID, q.AfterAcceptedAt = p.After, p.AfterAcceptedAt
	return q, nil
}

func (a *API) recordQuery(w http.ResponseWriter, r *http.Request, count bool) (string, content.RecordQuery, bool) {
	values := r.URL.Query()
	q := content.RecordQuery{Order: content.RecordIDOrder}
	for k, v := range values {
		allowed := k == "corpus_id" || k == "accepted_after" || k == "accepted_before" || (!count && (k == "order" || k == "page_cursor" || k == "limit" || k == "corpus_ids" || k == "metadata"))
		if !allowed || len(v) != 1 {
			writeError(w, publicerr.InvalidQuery, nil)
			return "", q, false
		}
	}
	corpusID := values.Get("corpus_id")
	if !count && values.Has("corpus_ids") {
		if values.Has("corpus_id") {
			writeError(w, publicerr.InvalidQuery, nil)
			return "", q, false
		}
		q.CorpusIDs = strings.Split(values.Get("corpus_ids"), ",")
		if len(q.CorpusIDs) > 16 {
			writeError(w, publicerr.InvalidQuery, nil)
			return "", q, false
		}
		sort.Strings(q.CorpusIDs)
		for i, id := range q.CorpusIDs {
			if id == "" || (i > 0 && id == q.CorpusIDs[i-1]) {
				writeError(w, publicerr.InvalidQuery, nil)
				return "", q, false
			}
		}
		corpusID = strings.Join(q.CorpusIDs, ",")
	}
	if corpusID == "" {
		writeError(w, publicerr.InvalidQuery, nil)
		return "", q, false
	}
	if len(q.CorpusIDs) == 0 {
		q.CorpusIDs = []string{corpusID}
	}
	if !count && values.Has("metadata") {
		raw := values.Get("metadata")
		decoder := json.NewDecoder(bytes.NewBufferString(raw))
		decoder.DisallowUnknownFields()
		if len(raw) > 262144 || decoder.Decode(&q.Metadata) != nil || len(q.Metadata) == 0 || corpus.ValidateFilters(q.Metadata) != nil {
			writeError(w, publicerr.InvalidQuery, nil)
			return "", q, false
		}
		var predicates []any
		if json.Unmarshal([]byte(raw), &predicates) != nil {
			writeError(w, publicerr.InvalidQuery, nil)
			return "", q, false
		}
		for _, predicate := range predicates {
			if a.schemas["MetadataFilter"].Validate(predicate) != nil {
				writeError(w, publicerr.InvalidQuery, nil)
				return "", q, false
			}
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			writeError(w, publicerr.InvalidQuery, nil)
			return "", q, false
		}
	}
	if values.Has("order") {
		q.Order = content.RecordOrder(values.Get("order"))
		if q.Order != content.RecordIDOrder && q.Order != content.AcceptedAtDesc {
			writeError(w, publicerr.InvalidQuery, nil)
			return "", q, false
		}
	}
	var ok bool
	if q.AcceptedAfter, ok = recordTime(w, values, "accepted_after"); !ok {
		return "", q, false
	}
	if q.AcceptedBefore, ok = recordTime(w, values, "accepted_before"); !ok {
		return "", q, false
	}
	if q.AcceptedAfter != nil && q.AcceptedBefore != nil && q.AcceptedAfter.After(*q.AcceptedBefore) {
		writeError(w, publicerr.InvalidQuery, nil)
		return "", q, false
	}
	return corpusID, q, true
}

// Go's layout parser also accepts a one-digit hour, comma fractions and
// out-of-range zone offsets. Require the RFC3339 shape before calendar parsing.
var recordTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]([01]\d|2[0-3]):[0-5]\d)$`)

func recordTime(w http.ResponseWriter, values url.Values, key string) (*time.Time, bool) {
	if !values.Has(key) {
		return nil, true
	}
	value := values.Get(key)
	if !recordTimestamp.MatchString(value) {
		writeError(w, publicerr.InvalidQuery, nil)
		return nil, false
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		writeError(w, publicerr.InvalidQuery, nil)
		return nil, false
	}
	return &t, true
}

// listRecords serves independent authorized catalog pages. The default ID
// order is the resynchronization entry point; withdrawn Records are included.
func (a *API) listRecords(w http.ResponseWriter, r *http.Request, s corpus.Scope) {
	corpusID, query, ok := a.recordQuery(w, r, false)
	if !ok {
		return
	}
	values := r.URL.Query()
	if values.Has("page_cursor") && values.Get("page_cursor") == "" {
		writeError(w, publicerr.InvalidCursor, nil)
		return
	}
	limit, ok := pageLimit(w, values, 100, 100)
	if !ok {
		return
	}
	query.Limit = limit + 1
	var exclusions []corpus.CorpusExclusion
	records, err := a.Content.RecordsAcross(r.Context(), s, query.CorpusIDs, query, func() (content.RecordQuery, error) {
		var filterErr error
		query, exclusions, filterErr = a.catalogFilters(r.Context(), s, query)
		if filterErr != nil {
			return query, filterErr
		}
		if !values.Has("page_cursor") {
			return query, nil
		}
		decoded, err := a.decodeRecordPage(values.Get("page_cursor"), corpusID, s, query)
		if errors.Is(err, errPageScope) {
			writeCatalogScopeError(w, query.CorpusIDs)
			return query, errResponseWritten
		}
		if err != nil {
			writeError(w, publicerr.InvalidCursor, nil)
			return query, errResponseWritten
		}
		return decoded, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.ContentUnavailable)
		return
	}
	page := transport.RecordPage{ExcludedCorpora: exclusionsToTransport(exclusions), Items: make([]transport.Record, 0, min(len(records), limit))}
	for i, record := range records {
		if i == limit {
			last := records[limit-1]
			p := recordPage{Version: 3, FilterScope: catalogFilterScope(query), Corpus: corpusID, Scope: scopeDigest(s), After: last.ID, Order: query.Order, AcceptedAfter: query.AcceptedAfter, AcceptedBefore: query.AcceptedBefore}
			if query.Order == content.AcceptedAtDesc {
				p.AfterAcceptedAt = last.CurrentAcceptedAt
			}
			next := a.encodeRecordPage(p)
			page.NextPageCursor = &next
			break
		}
		page.Items = append(page.Items, recordToTransport(record))
	}
	send(w, 200, page)
}

func (a *API) countRecords(w http.ResponseWriter, r *http.Request, s corpus.Scope) {
	corpusID, query, ok := a.recordQuery(w, r, true)
	if !ok {
		return
	}
	count, err := a.Content.CountRecords(r.Context(), s, corpusID, query)
	if err != nil {
		writeError(w, err, publicerr.ContentUnavailable)
		return
	}
	send(w, 200, transport.RecordCount{Count: count})
}

func catalogFilterScope(q content.RecordQuery) string {
	if len(q.Metadata) == 0 {
		return ""
	}
	raw, _ := json.Marshal(struct {
		Metadata []corpus.MetadataFilter
		Routes   []content.CatalogFilterRoute
	}{q.Metadata, q.FilterRoutes})
	return content.Hash(raw)
}
func (a *API) catalogFilters(ctx context.Context, s corpus.Scope, q content.RecordQuery) (content.RecordQuery, []corpus.CorpusExclusion, error) {
	if len(q.Metadata) == 0 {
		return q, nil, nil
	}
	var excluded []corpus.CorpusExclusion
	for _, id := range q.CorpusIDs {
		g, err := a.Retrieval.Routing.Generation(ctx, s.Organization, id)
		if err != nil {
			return q, nil, publicerr.ContentUnavailable
		}
		filters, missing, err := corpus.ResolveFilters(q.Metadata, g.Fields)
		if err != nil {
			return q, nil, err
		}
		if len(missing) > 0 {
			excluded = append(excluded, corpus.CorpusExclusion{CorpusID: id, Fields: missing})
			continue
		}
		if !g.MetadataProjected {
			return q, nil, publicerr.MetadataFilterUnavailable
		}
		q.FilterRoutes = append(q.FilterRoutes, content.CatalogFilterRoute{CorpusID: id, GenerationID: g.ID, Filters: filters})
	}
	return q, excluded, nil
}
func exclusionsToTransport(excluded []corpus.CorpusExclusion) *[]transport.CorpusExclusion {
	if len(excluded) == 0 {
		return nil
	}
	out := make([]transport.CorpusExclusion, len(excluded))
	for i, e := range excluded {
		out[i] = transport.CorpusExclusion{CorpusId: e.CorpusID, Fields: e.Fields}
	}
	return &out
}

func writeCatalogScopeError(w http.ResponseWriter, ids []string) {
	status, body := errorResponse(publicerr.CursorScopeChanged, nil)
	values := url.Values{}
	if len(ids) == 1 {
		values.Set("corpus_id", ids[0])
	} else {
		values.Set("corpus_ids", strings.Join(ids, ","))
	}
	resync := "/v0/records?" + values.Encode()
	body.ResyncUrl = &resync
	body.Message += "; resynchronize"
	send(w, status, body)
}
