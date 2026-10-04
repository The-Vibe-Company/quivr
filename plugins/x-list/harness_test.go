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
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
	"github.com/The-Vibe-Company/quivr/tests/fakes/process"
)

var base = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

const testToken = "x-test-token"

// fakeX is a control/observation client; provider replies come from cmd/x.
type fakeX struct {
	t   *testing.T
	url string
}

func newFakeX(t *testing.T) (*fakeX, *process.Server) {
	t.Helper()
	srv := process.Start(t, "x", "-token", testToken)
	f := &fakeX{t: t, url: srv.URL}
	f.control("/_control/app", map[string]any{"page_size": 3, "member_page_size": 2, "consumer_secret": testConsumerSecret,
		"user": map[string]string{"id": "42", "username": "newsdesk", "name": "News Desk"}, "lang": "fr"})
	return f, srv
}

func (f *fakeX) call(method, path string, body, out any) {
	f.t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, f.url+path, bytes.NewReader(raw))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		f.t.Fatalf("%s %s: HTTP%d %s", method, path, resp.StatusCode, raw)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			f.t.Fatal(err)
		}
	}
}
func (f *fakeX) control(path string, body any) { f.call(http.MethodPost, path, body, nil) }
func (f *fakeX) list(body any)                 { f.control("/_control/lists/77", body) }
func (f *fakeX) post(id, text string, at int64, history []string, extra map[string]any) {
	post := map[string]any{"id": id, "text": text, "created_at": base.Add(time.Duration(at) * time.Second).Format(time.RFC3339)}
	if len(history) > 0 {
		post["edit_history_tweet_ids"] = history
	}
	for k, v := range extra {
		post[k] = v
	}
	f.list(map[string]any{"posts": []any{post}})
}
func (f *fakeX) take() []string {
	var out []string
	f.call(http.MethodGet, "/_control/requests", nil, &out)
	return out
}

type xState struct {
	Posts    map[string]map[string]any `json:"posts"`
	Rules    []streamRule              `json:"rules"`
	Webhooks []xWebhook                `json:"webhooks"`
	Linked   map[string]bool           `json:"linked"`
}

func (f *fakeX) state() xState {
	var out xState
	f.call(http.MethodGet, "/_control/state", nil, &out)
	return out
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

func newInstance(t *testing.T, srv *process.Server, config string) *instance {
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
