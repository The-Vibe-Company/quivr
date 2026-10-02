package httpapi

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

// recordPage is the signed payload of a Record catalog page cursor. It binds
// the Corpus filter and authorization scope; its signature domain differs from
// Change Cursors and Corpus page cursors, so neither is accepted in its place.
type recordPage struct {
	Version int    `json:"v"`
	Corpus  string `json:"c"`
	Scope   string `json:"s"`
	After   string `json:"a"`
}

var errPageScope = errors.New("cursor_scope_changed")

func (a *API) encodeRecordPage(p recordPage) string {
	b, _ := json.Marshal(p)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(a.signCursor(recordPageDomain, b))
}

func (a *API) decodeRecordPage(token, corpusID string, s corpus.Scope) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", errors.New("invalid_cursor")
	}
	b, e1 := base64.RawURLEncoding.DecodeString(parts[0])
	sig, e2 := base64.RawURLEncoding.DecodeString(parts[1])
	var p recordPage
	if e1 != nil || e2 != nil || !hmac.Equal(sig, a.signCursor(recordPageDomain, b)) || json.Unmarshal(b, &p) != nil || p.Version != 1 {
		return "", errors.New("invalid_cursor")
	}
	if p.Corpus != corpusID || p.Scope != scopeDigest(s) {
		return "", errPageScope
	}
	return p.After, nil
}

// listRecords serves the authorized Record catalog of one Corpus in stable key
// order, withdrawn Records included. It is the resynchronization entry point
// named by resync_url; pages are independent reads, not a snapshot.
func (a *API) listRecords(w http.ResponseWriter, r *http.Request, s corpus.Scope) {
	q := r.URL.Query()
	for k, v := range q {
		if (k != "corpus_id" && k != "page_cursor" && k != "limit") || len(v) != 1 {
			writeError(w, publicerr.InvalidQuery, nil)
			return
		}
	}
	corpusID := q.Get("corpus_id")
	if corpusID == "" {
		writeError(w, publicerr.InvalidQuery, nil)
		return
	}
	if q.Has("page_cursor") && q.Get("page_cursor") == "" {
		writeError(w, publicerr.InvalidCursor, nil)
		return
	}
	limit, ok := pageLimit(w, q, 100, 100)
	if !ok {
		return
	}
	if !s.Allows("content:read") {
		writeError(w, publicerr.Forbidden, nil)
		return
	}
	if !s.Contains(corpusID) {
		writeError(w, publicerr.NotFound, nil)
		return
	}
	if _, err := a.Service.Store.Read(r.Context(), s.Organization, corpusID); err != nil {
		writeError(w, err, publicerr.ContentUnavailable)
		return
	}
	after := ""
	if q.Has("page_cursor") {
		var err error
		if after, err = a.decodeRecordPage(q.Get("page_cursor"), corpusID, s); errors.Is(err, errPageScope) {
			writeError(w, publicerr.CursorScopeChanged, nil, corpusID)
			return
		} else if err != nil {
			writeError(w, publicerr.InvalidCursor, nil)
			return
		}
	}
	records, err := a.Content.Records(r.Context(), s, corpusID, after, limit+1)
	if err != nil {
		writeError(w, err, publicerr.ContentUnavailable)
		return
	}
	page := transport.RecordPage{Items: make([]transport.Record, 0, min(len(records), limit))}
	for i, record := range records {
		if i == limit {
			next := a.encodeRecordPage(recordPage{Version: 1, Corpus: corpusID, Scope: scopeDigest(s), After: records[limit-1].ID})
			page.NextPageCursor = &next
			break
		}
		page.Items = append(page.Items, recordToTransport(record))
	}
	send(w, 200, page)
}
