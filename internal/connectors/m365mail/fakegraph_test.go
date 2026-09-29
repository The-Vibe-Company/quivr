package m365mail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGraph is a minimal in-process Microsoft Graph used by unit tests: a
// token endpoint, per-folder message delta with page and delta tokens,
// attachment listing and raw attachment bytes, and scripted failures.
type fakeGraph struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	messages []map[string]any
	removed  []string
	files    map[string]map[string]fakeAttachment // message id -> attachment id
	// tokens counts token requests; forms keeps the last token form.
	tokens   int
	form     url.Values
	secret   string
	tokenErr string // AADSTS code returned by the token endpoint
	// fail maps a path fragment to the statuses to return, one per request.
	fail      map[string][]failure
	pageSize  int
	expireTok bool // next delta with a delta token fails with syncStateNotFound
}

type failure struct {
	status     int
	code       string
	retryAfter string
}

type fakeAttachment struct {
	meta  map[string]any
	bytes string
}

func newFakeGraph(t *testing.T) *fakeGraph {
	g := &fakeGraph{t: t, files: map[string]map[string]fakeAttachment{}, fail: map[string][]failure{}, secret: "test-secret-not-real", pageSize: 2}
	g.server = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.server.Close)
	return g
}

func (g *fakeGraph) connector() *Connector {
	c := New(g.server.URL, g.server.URL+"/v1.0", g.server.Client())
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func (g *fakeGraph) addMessage(id, received string, attachments ...fakeAttachment) {
	g.mu.Lock()
	defer g.mu.Unlock()
	msg := map[string]any{"id": id, "internetMessageId": "<" + id + "@example.org>", "subject": "Subject " + id,
		"body":         map[string]any{"contentType": "html", "content": "<html><body><p>Hello <b>" + id + "</b></p><script>x()</script></body></html>"},
		"from":         map[string]any{"emailAddress": map[string]any{"name": "Desk", "address": "desk@example.org"}},
		"toRecipients": []any{map[string]any{"emailAddress": map[string]any{"name": "Monitor", "address": "monitoring@example.org"}}},
		"sentDateTime": received, "receivedDateTime": received, "conversationId": "conv-" + id, "hasAttachments": len(attachments) > 0}
	g.messages = append(g.messages, msg)
	files := map[string]fakeAttachment{}
	for _, a := range attachments {
		files[a.meta["id"].(string)] = a
	}
	g.files[id] = files
}

func fileAttachment(id, name, contentType, bytes string, size int) fakeAttachment {
	if size == 0 {
		size = len(bytes)
	}
	return fakeAttachment{meta: map[string]any{"@odata.type": "#microsoft.graph.fileAttachment", "id": id, "name": name, "contentType": contentType, "size": size, "isInline": false, "lastModifiedDateTime": "2026-09-28T10:00:00Z"}, bytes: bytes}
}

func (g *fakeGraph) failNext(fragment string, f ...failure) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fail[fragment] = append(g.fail[fragment], f...)
}

func (g *fakeGraph) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for fragment, list := range g.fail {
		if len(list) > 0 && strings.Contains(r.URL.Path, fragment) {
			g.fail[fragment] = list[1:]
			f := list[0]
			if f.retryAfter != "" {
				w.Header().Set("Retry-After", f.retryAfter)
			}
			w.WriteHeader(f.status)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": f.code, "message": "scripted"}})
			return
		}
	}
	if strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token") {
		g.token(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer token-"+strconv.Itoa(g.tokens) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/messages/delta"):
		g.delta(w, r)
	case strings.HasSuffix(r.URL.Path, "/$value"):
		parts := strings.Split(r.URL.Path, "/")
		msg, att := parts[len(parts)-4], parts[len(parts)-2]
		a, ok := g.files[msg][att]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(a.bytes))
	case strings.HasSuffix(r.URL.Path, "/attachments"):
		parts := strings.Split(r.URL.Path, "/")
		msg := parts[len(parts)-2]
		files, ok := g.files[msg]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "ErrorItemNotFound"}})
			return
		}
		list := []any{}
		for _, a := range files {
			list = append(list, a.meta)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"value": list})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (g *fakeGraph) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	g.form = r.PostForm
	if g.tokenErr != "" || (r.PostForm.Get("client_secret") != "" && r.PostForm.Get("client_secret") != g.secret) {
		code := g.tokenErr
		if code == "" {
			code = "7000215"
		}
		w.WriteHeader(http.StatusUnauthorized)
		n, _ := strconv.Atoi(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_client", "error_description": "AADSTS" + code + ": scripted", "error_codes": []int{n}})
		return
	}
	g.tokens++
	_ = json.NewEncoder(w).Encode(map[string]any{"token_type": "Bearer", "expires_in": 3599, "access_token": "token-" + strconv.Itoa(g.tokens)})
}

// delta pages over messages in arrival order. A skip token is the next index;
// a delta token is the count of messages seen at the end of the round.
func (g *fakeGraph) delta(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	start := 0
	if s := q.Get("$skiptoken"); s != "" {
		start, _ = strconv.Atoi(s)
	} else if d := q.Get("$deltatoken"); d != "" {
		if g.expireTok {
			g.expireTok = false
			w.WriteHeader(http.StatusGone)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "syncStateNotFound"}})
			return
		}
		start, _ = strconv.Atoi(d)
	}
	since := time.Time{}
	if f := q.Get("$filter"); f != "" {
		since, _ = time.Parse(time.RFC3339, strings.TrimPrefix(f, "receivedDateTime ge "))
	}
	if q.Get("$filter") == "" && q.Get("$skiptoken") == "" && q.Get("$deltatoken") == "" {
		g.t.Errorf("initial delta without a receivedDateTime filter: %s", r.URL.RawQuery)
	}
	base := "http://" + r.Host + r.URL.Path + "?"
	keep := url.Values{}
	if f := q.Get("$filter"); f != "" {
		keep.Set("$filter", f)
	}
	value := []any{}
	i := start
	for ; i < len(g.messages) && len(value) < g.pageSize; i++ {
		received, _ := time.Parse(time.RFC3339, g.messages[i]["receivedDateTime"].(string))
		if received.Before(since) {
			continue
		}
		value = append(value, g.messages[i])
	}
	for _, id := range g.removed {
		value = append(value, map[string]any{"id": id, "@removed": map[string]any{"reason": "deleted"}})
	}
	g.removed = nil
	out := map[string]any{"value": value}
	if i < len(g.messages) {
		keep.Set("$skiptoken", strconv.Itoa(i))
		out["@odata.nextLink"] = base + keep.Encode()
	} else {
		keep.Set("$deltatoken", strconv.Itoa(i))
		out["@odata.deltaLink"] = base + keep.Encode()
	}
	_ = json.NewEncoder(w).Encode(out)
}
