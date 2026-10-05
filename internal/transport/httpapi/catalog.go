package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
)

// recordPage binds the Corpus, authorization scope, order and normalized time
// bounds. Version 1 ID cursors remain valid for the original unbounded catalog.
type recordPage struct {
	Version         int                 `json:"v"`
	Corpus          string              `json:"c"`
	Scope           string              `json:"s"`
	After           string              `json:"a"`
	Order           content.RecordOrder `json:"o,omitempty"`
	AcceptedAfter   *time.Time          `json:"lo,omitempty"`
	AcceptedBefore  *time.Time          `json:"hi,omitempty"`
	AfterAcceptedAt *time.Time          `json:"at,omitempty"`
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
	if a.decodePage(recordPageDomain, token, &p) != nil || (p.Version != 1 && p.Version != 2) || p.After == "" {
		return q, errors.New("invalid_cursor")
	}
	order := p.Order
	if order == "" {
		order = content.RecordIDOrder
	}
	if p.Corpus != corpusID || p.Scope != scopeDigest(s) || order != q.Order || !sameTime(p.AcceptedAfter, q.AcceptedAfter) || !sameTime(p.AcceptedBefore, q.AcceptedBefore) {
		return q, errPageScope
	}
	q.AfterID, q.AfterAcceptedAt = p.After, p.AfterAcceptedAt
	return q, nil
}

func recordQuery(w http.ResponseWriter, r *http.Request, count bool) (string, content.RecordQuery, bool) {
	values := r.URL.Query()
	q := content.RecordQuery{Order: content.RecordIDOrder}
	for k, v := range values {
		allowed := k == "corpus_id" || k == "accepted_after" || k == "accepted_before" || (!count && (k == "order" || k == "page_cursor" || k == "limit"))
		if !allowed || len(v) != 1 {
			writeError(w, publicerr.InvalidQuery, nil)
			return "", q, false
		}
	}
	corpusID := values.Get("corpus_id")
	if corpusID == "" {
		writeError(w, publicerr.InvalidQuery, nil)
		return "", q, false
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
	t = t.UTC()
	return &t, true
}

// listRecords serves independent authorized catalog pages. The default ID
// order is the resynchronization entry point; withdrawn Records are included.
func (a *API) listRecords(w http.ResponseWriter, r *http.Request, s corpus.Scope) {
	corpusID, query, ok := recordQuery(w, r, false)
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
	records, err := a.Content.Records(r.Context(), s, corpusID, query, func() (content.RecordQuery, error) {
		if !values.Has("page_cursor") {
			return query, nil
		}
		decoded, err := a.decodeRecordPage(values.Get("page_cursor"), corpusID, s, query)
		if errors.Is(err, errPageScope) {
			writeError(w, publicerr.CursorScopeChanged, nil, corpusID)
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
	page := transport.RecordPage{Items: make([]transport.Record, 0, min(len(records), limit))}
	for i, record := range records {
		if i == limit {
			last := records[limit-1]
			p := recordPage{Version: 2, Corpus: corpusID, Scope: scopeDigest(s), After: last.ID, Order: query.Order, AcceptedAfter: query.AcceptedAfter, AcceptedBefore: query.AcceptedBefore}
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
	corpusID, query, ok := recordQuery(w, r, true)
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
