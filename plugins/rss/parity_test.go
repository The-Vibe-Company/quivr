package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

// The golden files in testdata/golden were written by the RSS connector built
// into the core, before it moved here (THE-715): each step's feed, its ETag,
// the core's clock, the checkpoint it started from, and the items (in the
// connector wire shape) and checkpoint it returned.
type goldenStep struct {
	Feed         string            `json:"feed"`
	ETag         string            `json:"etag"`
	Now          time.Time         `json:"now"`
	CheckpointIn string            `json:"checkpoint_in"`
	NotDue       bool              `json:"not_due"`
	Items        []json.RawMessage `json:"items"`
	More         bool              `json:"more"`
	Checkpoint   string            `json:"checkpoint"`
}

func goldens(t *testing.T) map[string][]goldenStep {
	t.Helper()
	files, _ := filepath.Glob("testdata/golden/*.json")
	if len(files) == 0 {
		t.Fatal("no golden files")
	}
	out := map[string][]goldenStep{}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct{ Steps []goldenStep }
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(file)] = doc.Steps
	}
	return out
}

// goldenServer serves the step's feed with its ETag and answers a matching
// If-None-Match with 304, as the generator's server did.
func goldenServer(t *testing.T) (*httptest.Server, func(body []byte, etag string)) {
	var mu sync.Mutex
	var body []byte
	var etag string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Last-Modified", "Tue, 01 Sep 2026 10:00:00 GMT")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, func(b []byte, e string) { mu.Lock(); body, etag = b, e; mu.Unlock() }
}

// sameJSON compares two JSON documents as values.
func sameJSON(t *testing.T, got, want []byte) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &b); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(a, b)
}

// Parity with the built-in connector: from the checkpoint the built-in code
// wrote at each step, the plugin returns the same items and a byte-identical
// checkpoint. Record Keys and revisions are equal, so a re-fetched item
// replays its Receipt instead of conflicting.
func TestParityWithTheBuiltInConnector(t *testing.T) {
	for name, steps := range goldens(t) {
		t.Run(name, func(t *testing.T) {
			srv, serve := goldenServer(t)
			for i, step := range steps {
				serve([]byte(testdata(t, step.Feed)), step.ETag)
				req := request(t, allowPrivate, cfg(srv.URL+"/feed"), "", json.RawMessage(step.CheckpointIn), step.Now)
				page, err := feed{}.Fetch(context.Background(), req)
				if step.NotDue {
					if !errors.Is(err, quivrplugin.ErrNotDue) {
						t.Fatalf("step %d: want not_due, got %v", i, err)
					}
					continue
				}
				if err != nil {
					t.Fatalf("step %d: %v", i, err)
				}
				if page.More != step.More || len(page.Items) != len(step.Items) {
					t.Fatalf("step %d: more %v items %d, built-in more %v items %d", i, page.More, len(page.Items), step.More, len(step.Items))
				}
				for j, item := range page.Items {
					// Historical fixtures predate the shared common metadata
					// namespace; keep this parity assertion focused on the
					// connector-owned wire shape.
					delete(item.Extensions, quivrplugin.CommonMetadataNamespace)
					if name == "large.json" {
						// The large feed's golden keeps identities only.
						item.Content, item.Extensions = nil, nil
					}
					got, _ := json.Marshal(item)
					if !sameJSON(t, got, step.Items[j]) {
						t.Fatalf("step %d item %d:\n got %s\nwant %s", i, j, got, step.Items[j])
					}
				}
				if got := string(cp(page)); got != step.Checkpoint {
					t.Fatalf("step %d checkpoint:\n got %s\nwant %s", i, got, step.Checkpoint)
				}
			}
		})
	}
}

// Cutover: an instance advanced by the built-in connector (its checkpoint
// after the first poll) continues on the plugin. The next feed brings one new
// and one changed item; the plugin returns exactly those, and no item the
// built-in code already delivered unchanged.
func TestCutoverFromABuiltInCheckpoint(t *testing.T) {
	steps := goldens(t)["sequence.json"]
	builtIn := steps[0]
	srv, serve := goldenServer(t)
	serve([]byte(testdata(t, "rss2-v2.xml")), `"v2"`)
	page, err := feed{}.Fetch(context.Background(), request(t, allowPrivate, cfg(srv.URL+"/feed"), "", json.RawMessage(builtIn.Checkpoint), builtIn.Now.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	delivered := map[string]string{}
	for _, raw := range builtIn.Items {
		var it quivrplugin.Item
		_ = json.Unmarshal(raw, &it)
		delivered[it.RecordKey] = it.Revision
	}
	var keys []string
	for _, it := range page.Items {
		if revision, ok := delivered[it.RecordKey]; ok && revision == it.Revision {
			t.Fatalf("duplicate: %s was already delivered unchanged", it.RecordKey)
		}
		keys = append(keys, it.RecordKey)
	}
	sort.Strings(keys)
	if want := []string{"example-3", "https://news.example.org/first"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("items %v, want the new and the changed item %v", keys, want)
	}
	if _, changed := delivered["https://news.example.org/first"]; !changed {
		t.Fatal("the changed item was not in the built-in delivery")
	}
}
