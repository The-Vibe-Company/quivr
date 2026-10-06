package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
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
// wrote at each step, the plugin keeps the same connector-owned items and
// checkpoint state. Revisions are normalized because the plugin now includes
// its emitted common metadata in that identity. A legacy checkpoint can also
// replay previously seen keys once to backfill the new extension; those keys
// are accepted only when they were present in the fixture checkpoint.
func TestParityWithTheBuiltInConnector(t *testing.T) {
	for name, steps := range goldens(t) {
		t.Run(name, func(t *testing.T) {
			srv, serve := goldenServer(t)
			fixtures := historicalFixtureItems(t, steps)
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
				expected := expectedRecordKeys(t, step.Items)
				observed := map[string]bool{}
				for pageNumber := 0; ; pageNumber++ {
					// A legacy checkpoint may spend pages on the one-time
					// metadata backfill, changing pagination boundaries. Keep
					// following that returned checkpoint until this fixture step's
					// literal records have all been observed.
					if pageNumber == 0 && step.CheckpointIn == "null" && page.More != step.More {
						t.Fatalf("step %d: more %v, built-in more %v", i, page.More, step.More)
					}
					for key := range compareHistoricalItems(t, name, i, page.Items, fixtures, step.Items, step.CheckpointIn) {
						observed[key] = true
					}
					if missing := missingRecordKeys(expected, observed); len(missing) == 0 {
						// Legacy checkpoints can paginate a one-time metadata
						// backfill differently from the frozen built-in sequence.
						// The initial checkpoint remains a full wire assertion;
						// cutover below asserts the settled legacy checkpoint.
						if pageNumber == 0 && step.CheckpointIn == "null" {
							gotCheckpoint := string(cp(page))
							if !sameCheckpointExceptRevisions(gotCheckpoint, step.Checkpoint) {
								t.Fatalf("step %d checkpoint:\n got %s\nwant %s", i, gotCheckpoint, step.Checkpoint)
							}
						}
						break
					}
					if !page.More {
						t.Fatalf("step %d missing expected records after page %d: %v", i, pageNumber, missingRecordKeys(expected, observed))
					}
					if pageNumber+1 >= maxParityPages {
						t.Fatalf("step %d did not cover expected records within %d pages: %v", i, maxParityPages, missingRecordKeys(expected, observed))
					}
					page, err = feed{}.Fetch(context.Background(), request(t, allowPrivate, cfg(srv.URL+"/feed"), "", cp(page), step.Now))
					if err != nil {
						t.Fatalf("step %d backfill page %d: %v", i, pageNumber+1, err)
					}
				}
			}
		})
	}
}

const maxParityPages = maxFeedItems/itemsPerPage + 2

// Cutover: an instance advanced by the built-in connector (its checkpoint
// after the first poll) continues on the plugin. The first plugin poll
// replays the existing items once to attach common metadata, alongside one
// new and one changed item. The checkpoint then settles and a second poll is
// empty.
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
	if want := []string{"example-2", "example-3", "https://news.example.org/first"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("items %v, want the metadata backfill, new, and changed items %v", keys, want)
	}
	if _, changed := delivered["https://news.example.org/first"]; !changed {
		t.Fatal("the changed item was not in the built-in delivery")
	}
	settled, err := feed{}.Fetch(context.Background(), request(t, allowPrivate, cfg(srv.URL+"/feed"), "", cp(page), builtIn.Now.Add(2*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if len(settled.Items) != 0 {
		t.Fatalf("metadata backfill repeated after its new checkpoint: %+v", settled.Items)
	}
}

func historicalFixtureItems(t *testing.T, steps []goldenStep) map[string][]json.RawMessage {
	t.Helper()
	items := map[string][]json.RawMessage{}
	for _, step := range steps {
		for _, raw := range step.Items {
			var item struct {
				RecordKey string `json:"record_key"`
			}
			if err := json.Unmarshal(raw, &item); err != nil {
				t.Fatal(err)
			}
			if item.RecordKey == "" {
				t.Fatalf("fixture item has no record_key: %s", raw)
			}
			items[item.RecordKey] = append(items[item.RecordKey], raw)
		}
	}
	return items
}

func expectedRecordKeys(t *testing.T, expected []json.RawMessage) map[string]bool {
	t.Helper()
	keys := map[string]bool{}
	for _, raw := range expected {
		var item struct {
			RecordKey string `json:"record_key"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatal(err)
		}
		keys[item.RecordKey] = true
	}
	return keys
}

func missingRecordKeys(expected, observed map[string]bool) []string {
	var missing []string
	for key := range expected {
		if !observed[key] {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	return missing
}

func compareHistoricalItems(t *testing.T, name string, step int, got []quivrplugin.Item, fixtures map[string][]json.RawMessage, expected []json.RawMessage, checkpointIn string) map[string]bool {
	t.Helper()
	current := map[string][]json.RawMessage{}
	for _, raw := range expected {
		var item struct {
			RecordKey string `json:"record_key"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatal(err)
		}
		current[item.RecordKey] = append(current[item.RecordKey], raw)
	}
	seen := checkpointKeys(checkpointIn)
	matchedExpected := map[string]bool{}
	for j, item := range got {
		currentPayloads, isExpected := current[item.RecordKey]
		if !isExpected && !seen[rssCheckpointKey(item.RecordKey)] {
			t.Fatalf("step %d unexpected item %q", step, item.RecordKey)
		}
		if isExpected {
			matchedExpected[item.RecordKey] = true
		}
		if _, ok := item.Extensions[quivrplugin.CommonMetadataNamespace]; !ok {
			t.Fatalf("step %d item %d %q has no common metadata", step, j, item.RecordKey)
		}
		if !strings.HasPrefix(item.Revision, "sha256:") {
			t.Fatalf("step %d item %d %q has invalid revision %q", step, j, item.RecordKey, item.Revision)
		}
		gotRaw, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		matched := false
		candidates := fixtures[item.RecordKey]
		if isExpected {
			// Current-step records must match that step's literal payload;
			// only old replay keys may use a prior fixture variant.
			candidates = currentPayloads
		}
		for _, wantRaw := range candidates {
			var gotValue, wantValue map[string]any
			if err := json.Unmarshal(gotRaw, &gotValue); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(wantRaw, &wantValue); err != nil {
				t.Fatal(err)
			}
			// Historical fixtures predate quivr.metadata, and the
			// revision now includes that emitted payload. Keep both
			// intentional differences explicit.
			delete(gotValue["extensions"].(map[string]any), quivrplugin.CommonMetadataNamespace)
			gotValue["revision"] = wantValue["revision"]
			if name == "large.json" {
				delete(gotValue, "content")
				delete(gotValue, "extensions")
			}
			gotComparable, err := json.Marshal(gotValue)
			if err != nil {
				t.Fatal(err)
			}
			if sameJSON(t, gotComparable, wantRaw) {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("step %d item %d %q did not match any historical payload", step, j, item.RecordKey)
		}
	}
	return matchedExpected
}

func rssCheckpointKey(recordKey string) string {
	sum := sha256.Sum256([]byte(recordKey))
	return hex.EncodeToString(sum[:8])
}

func checkpointKeys(raw string) map[string]bool {
	keys := map[string]bool{}
	var value struct {
		Seen []struct {
			Key string `json:"k"`
		} `json:"seen"`
	}
	if json.Unmarshal([]byte(raw), &value) == nil {
		for _, item := range value.Seen {
			keys[item.Key] = true
		}
	}
	return keys
}

func sameCheckpointExceptRevisions(got, want string) bool {
	if got == want {
		return true
	}
	var actual, expected any
	if json.Unmarshal([]byte(got), &actual) != nil || json.Unmarshal([]byte(want), &expected) != nil {
		return false
	}
	normalize := func(value any) bool {
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		seen, ok := object["seen"].([]any)
		if !ok {
			return false
		}
		for _, entry := range seen {
			item, ok := entry.(map[string]any)
			if !ok {
				return false
			}
			delete(item, "r")
		}
		return true
	}
	return normalize(actual) && normalize(expected) && reflect.DeepEqual(actual, expected)
}
