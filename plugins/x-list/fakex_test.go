package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/sdks/go/quivrplugin"
)

// base is time 0 of testdata/scenarios.json.
var base = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

const testToken = "x-test-token"

// fakeX is a minimal X API v2 serving one list timeline (newest first, three
// posts per page, paged by opaque tokens) and post lookup by ids, with
// switchable failures. It records every request.
type fakeX struct {
	mu        sync.Mutex
	posts     map[string]map[string]any
	media     []any
	deleted   map[string]bool
	protected map[string]bool
	status    int
	reset     int64
	listError string
	requests  []string

	// Filtered Stream webhook state: the list members, the stream rules, the
	// registered webhooks and the ones linked to the stream. crcFails makes
	// every CRC check of a webhook fail.
	members  []string
	rules    []streamRule
	webhooks []xWebhook
	linked   map[string]bool
	crcFails bool
	nextID   int
}

func newFakeX(t *testing.T) (*fakeX, *httptest.Server) {
	t.Helper()
	f := &fakeX{posts: map[string]map[string]any{}, deleted: map[string]bool{}, protected: map[string]bool{}, linked: map[string]bool{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func TestFakeXKeepsEditedPostsAvailableByEarlierIDs(t *testing.T) {
	f := &fakeX{posts: map[string]map[string]any{}}
	f.post("100", "Original", 0, nil, nil)
	for _, history := range [][]string{{"100", "101"}, {"100", "101", "102"}} {
		latest := history[len(history)-1]
		f.post(latest, "Edited", 1, history, nil)
		for _, path := range []string{
			"/2/tweets?ids=" + strings.Join(history, ","),
			"/2/lists/77/tweets?expansions=author_id&tweet.fields=edit_history_tweet_ids",
		} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer "+testToken)
			rec := httptest.NewRecorder()
			f.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: HTTP %d, body %s", path, rec.Code, rec.Body)
			}
			var body struct {
				Data []struct {
					ID      string   `json:"id"`
					History []string `json:"edit_history_tweet_ids"`
				} `json:"data"`
				Errors []json.RawMessage `json:"errors"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			wantIDs := history
			if strings.HasPrefix(path, "/2/lists/") {
				wantIDs = []string{latest}
			}
			var gotIDs []string
			for _, p := range body.Data {
				gotIDs = append(gotIDs, p.ID)
				if !slices.Equal(p.History, history) {
					t.Fatalf("%s: post %s history %v, want %v", path, p.ID, p.History, history)
				}
			}
			if len(body.Errors) != 0 || !slices.Equal(gotIDs, wantIDs) {
				t.Fatalf("%s: IDs %v, errors %s; want IDs %v without errors", path, gotIDs, body.Errors, wantIDs)
			}
		}
	}
}

// post publishes a post created at seconds after base; a post with an edit
// history replaces its earlier versions in timelines but keeps them for lookup.
func (f *fakeX) post(id, text string, at int64, history []string, extra map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(history) == 0 {
		history = []string{id}
	}
	p := map[string]any{"id": id, "text": text, "created_at": base.Add(time.Duration(at) * time.Second).Format(time.RFC3339), "author_id": "42", "lang": "fr", "edit_history_tweet_ids": history}
	for k, v := range extra {
		p[k] = v
	}
	for _, old := range history[:len(history)-1] {
		if earlier := f.posts[old]; earlier != nil {
			earlier["edit_history_tweet_ids"] = history
		}
	}
	f.posts[id] = p
}

func (f *fakeX) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	if r.Method == http.MethodGet {
		f.requests = append(f.requests, r.URL.Path+"?"+q.Encode())
	} else {
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	}
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		w.WriteHeader(401)
		return
	}
	if f.status != 0 {
		if f.reset != 0 {
			w.Header().Set("x-rate-limit-reset", strconv.FormatInt(f.reset, 10))
		}
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(`{"title":"failure","type":"https://api.x.com/2/problems/credits-depleted"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if f.serveStream(w, r) {
		return
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/2/lists/") && strings.HasSuffix(r.URL.Path, "/tweets"):
		if f.listError != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"resource_type": "list", "type": "https://api.twitter.com/2/problems/" + f.listError}}})
			return
		}
		if q.Get("expansions") == "" || !strings.Contains(q.Get("tweet.fields"), "edit_history_tweet_ids") {
			w.WriteHeader(400)
			return
		}
		var list []map[string]any
		for id, p := range f.posts {
			history := p["edit_history_tweet_ids"].([]string)
			if !f.deleted[id] && !f.protected[id] && id == history[len(history)-1] {
				list = append(list, p)
			}
		}
		sort.Slice(list, func(i, j int) bool { return idAfter(list[i]["id"].(string), list[j]["id"].(string)) })
		start := 0
		if tok := q.Get("pagination_token"); tok != "" {
			n, err := strconv.Atoi(strings.TrimPrefix(tok, "p"))
			if err != nil || !strings.HasPrefix(tok, "p") {
				w.WriteHeader(400)
				return
			}
			start = n
		}
		end := min(start+3, len(list))
		includes := map[string]any{"users": []any{map[string]any{"id": "42", "username": "newsdesk", "name": "News Desk"}}}
		if len(f.media) > 0 {
			includes["media"] = f.media
		}
		body := map[string]any{"data": list[start:end], "includes": includes, "meta": map[string]any{"result_count": end - start}}
		if end < len(list) {
			body["meta"].(map[string]any)["next_token"] = fmt.Sprintf("p%d", end)
		}
		_ = json.NewEncoder(w).Encode(body)
	case r.URL.Path == "/2/tweets":
		var data, errs []any
		for _, id := range strings.Split(q.Get("ids"), ",") {
			switch {
			case f.deleted[id] || f.posts[id] == nil:
				errs = append(errs, map[string]any{"resource_id": id, "value": id, "resource_type": "tweet", "type": "https://api.twitter.com/2/problems/resource-not-found"})
			case f.protected[id]:
				errs = append(errs, map[string]any{"resource_id": id, "value": id, "resource_type": "tweet", "type": "https://api.twitter.com/2/problems/not-authorized-for-resource"})
			default:
				data = append(data, map[string]any{"id": id, "edit_history_tweet_ids": f.posts[id]["edit_history_tweet_ids"]})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "errors": errs})
	default:
		w.WriteHeader(404)
	}
}

// serveStream serves list members (two per page), stream rules, webhooks and
// their links to the stream. A webhook passes its CRC check unless crcFails.
func (f *fakeX) serveStream(w http.ResponseWriter, r *http.Request) bool {
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/2/lists/") && strings.HasSuffix(path, "/members"):
		start, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Query().Get("pagination_token"), "m"))
		end := min(start+2, len(f.members))
		var users []any
		for _, id := range f.members[start:end] {
			users = append(users, map[string]any{"id": id, "username": "user" + id})
		}
		meta := map[string]any{"result_count": end - start}
		if end < len(f.members) {
			meta["next_token"] = fmt.Sprintf("m%d", end)
		}
		reply(map[string]any{"data": users, "meta": meta})
	case path == "/2/tweets/search/stream/rules" && r.Method == http.MethodGet:
		reply(map[string]any{"data": f.rules})
	case path == "/2/tweets/search/stream/rules" && r.Method == http.MethodPost:
		var body struct {
			Add    []streamRule `json:"add"`
			Delete struct {
				IDs []string `json:"ids"`
			} `json:"delete"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, rule := range body.Add {
			f.nextID++
			rule.ID = fmt.Sprintf("rule-%d", f.nextID)
			f.rules = append(f.rules, rule)
		}
		kept := f.rules[:0]
		for _, rule := range f.rules {
			if !slicesContains(body.Delete.IDs, rule.ID) {
				kept = append(kept, rule)
			}
		}
		f.rules = kept
		reply(map[string]any{"meta": map[string]any{"sent": "now"}})
	case path == "/2/webhooks" && r.Method == http.MethodGet:
		reply(map[string]any{"data": f.webhooks})
	case path == "/2/webhooks" && r.Method == http.MethodPost:
		var body struct {
			URL string `json:"url"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if f.crcFails {
			w.WriteHeader(400)
			reply(map[string]any{"errors": []any{map[string]any{"message": "CRC validation failed"}}})
			return true
		}
		f.nextID++
		hook := xWebhook{ID: fmt.Sprintf("hook-%d", f.nextID), URL: body.URL, Valid: true}
		f.webhooks = append(f.webhooks, hook)
		reply(map[string]any{"data": hook})
	case strings.HasPrefix(path, "/2/webhooks/") && r.Method == http.MethodPut:
		id := strings.TrimPrefix(path, "/2/webhooks/")
		for i := range f.webhooks {
			if f.webhooks[i].ID == id {
				f.webhooks[i].Valid = !f.crcFails
				reply(map[string]any{"data": f.webhooks[i]})
				return true
			}
		}
		w.WriteHeader(404)
	case path == "/2/tweets/search/webhooks" && r.Method == http.MethodGet:
		var links []any
		for id := range f.linked {
			links = append(links, map[string]any{"webhook_id": id})
		}
		reply(map[string]any{"data": links})
	case strings.HasPrefix(path, "/2/tweets/search/webhooks/") && r.Method == http.MethodPost:
		f.linked[strings.TrimPrefix(path, "/2/tweets/search/webhooks/")] = true
		reply(map[string]any{"data": map[string]any{"provisioned": true}})
	default:
		return false
	}
	return true
}

func (f *fakeX) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.requests
	f.requests = nil
	if out == nil {
		out = []string{}
	}
	return out
}

func (f *fakeX) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.requests {
		if strings.HasPrefix(p, prefix) {
			n++
		}
	}
	return n
}

// instance drives the plugin's HTTP handler like the engine's Acquirer drives
// a Connector Instance: pages from the checkpoint, each returned checkpoint
// fed back and the reads added to the day's count, until more is false,
// maxPages or an error. The instance writes to corpus_1, Source Namespace x.
type instance struct {
	t          *testing.T
	h          http.Handler
	api        string
	shortCheck bool // the pin's allow_short_recheck
	config     string
	token      string
	webhookURL string
	checkpoint json.RawMessage
	reads      int64
	now        time.Time
	maxPages   int
}

func newInstance(t *testing.T, srv *httptest.Server, config string) *instance {
	t.Helper()
	p, err := quivrplugin.New("quivr-plugin.yaml", quivrplugin.WithLogger(discard()))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Connector("x_list", XList{}); err != nil {
		t.Fatal(err)
	}
	h, err := p.Handler()
	if err != nil {
		t.Fatal(err)
	}
	return &instance{t: t, h: h, api: srv.URL, config: config, token: testToken, now: base, maxPages: 10}
}

// page is one answered fetch: a page, or the error envelope that ended the run.
type page struct {
	Items       []map[string]any `json:"items"`
	Checkpoint  json.RawMessage  `json:"checkpoint"`
	More        bool             `json:"more"`
	Reads       int64            `json:"reads"`
	Notice      string           `json:"notice"`
	Diagnostics map[string]any   `json:"diagnostics"`
	Push        map[string]any   `json:"push"`
	Error       *envelope        `json:"-"`
}

type envelope struct {
	Class             string `json:"error_class"`
	Code              string `json:"code"`
	RetryAfterSeconds int64  `json:"retry_after_seconds"`
}

func (in *instance) fetch(pageInRun int) page {
	in.t.Helper()
	checkpoint := in.checkpoint
	if checkpoint == nil {
		checkpoint = json.RawMessage("null")
	}
	body, _ := json.Marshal(map[string]any{
		"invocation_id": fmt.Sprintf("test-%d", pageInRun), "contribution": "connector", "organization_id": "org_a",
		"configuration": map[string]any{"api_endpoint": in.api, "allow_short_recheck": in.shortCheck},
		"connector":     in.connector(),
		"credential":    map[string]any{"bearer_token": in.token}, "checkpoint": checkpoint,
		"now": in.now.Format(time.RFC3339), "page_in_run": pageInRun, "reads_today": in.reads,
	})
	rec := httptest.NewRecorder()
	in.h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v0/contributions/connector/fetch", bytes.NewReader(body)))
	var out page
	if rec.Code != 200 {
		out.Error = &envelope{}
		if err := json.Unmarshal(rec.Body.Bytes(), out.Error); err != nil {
			in.t.Fatalf("HTTP %d without an envelope: %s", rec.Code, rec.Body)
		}
		return out
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		in.t.Fatal(err)
	}
	return out
}

func (in *instance) connector() map[string]any {
	c := map[string]any{"instance_id": "connector_x", "kind": "x_list", "corpus_id": "corpus_1", "source_namespace": "x", "config": json.RawMessage(in.config)}
	if in.webhookURL != "" {
		c["webhook_url"] = in.webhookURL
	}
	return c
}

// run is one acquisition run; it returns its pages (the last one may be an error).
func (in *instance) run() []page {
	in.t.Helper()
	var pages []page
	for i := 0; i < in.maxPages; i++ {
		p := in.fetch(i)
		pages = append(pages, p)
		if p.Error != nil {
			break
		}
		in.checkpoint = p.Checkpoint
		in.reads += p.Reads
		if !p.More {
			break
		}
	}
	return pages
}

func keys(pages []page) []string {
	var out []string
	for _, p := range pages {
		for _, item := range p.Items {
			k := item["record_key"].(string)
			if item["withdraw"] == true {
				k = "-" + k
			}
			out = append(out, k)
		}
	}
	return out
}

func runError(pages []page) *envelope {
	if len(pages) == 0 {
		return nil
	}
	return pages[len(pages)-1].Error
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}
