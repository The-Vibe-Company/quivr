package main

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type scenarioStep struct {
	Post *struct {
		ID      string   `json:"id"`
		Text    string   `json:"text"`
		At      int64    `json:"at"`
		History []string `json:"history"`
	} `json:"post"`
	Media   map[string]any `json:"media"`
	Delete  string         `json:"delete"`
	Protect string         `json:"protect"`
	Fail    *struct {
		Status int   `json:"status"`
		Reset  int64 `json:"reset"`
	} `json:"fail"`
	ListError *string `json:"list_error"`
	Run       *struct {
		Now      int64           `json:"now"`
		Config   json.RawMessage `json:"config"`
		Token    string          `json:"token"`
		MaxPages int             `json:"max_pages"`
	} `json:"run"`
}

// builtinRun is one run of the former built-in kind, captured before it was
// deleted: its input checkpoint and day's reads, then each page or the error.
type builtinRun struct {
	Step       int             `json:"step"`
	Checkpoint json.RawMessage `json:"checkpoint"`
	ReadsToday int64           `json:"reads_today"`
	Pages      []struct {
		PageInRun   int              `json:"page_in_run"`
		XRequests   []string         `json:"x_requests"`
		Items       []map[string]any `json:"items"`
		Checkpoint  json.RawMessage  `json:"checkpoint"`
		More        bool             `json:"more"`
		Reads       int64            `json:"reads"`
		Notice      string           `json:"notice"`
		Diagnostics map[string]any   `json:"diagnostics"`
	} `json:"pages"`
	Error *struct {
		Class             string   `json:"error_class"`
		Code              string   `json:"code"`
		RetryAfterSeconds int64    `json:"retry_after_seconds"`
		PageInRun         int      `json:"page_in_run"`
		XRequests         []string `json:"x_requests"`
	} `json:"error"`
}

func plain(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Every run of every scenario returns what the built-in kind returned: the
// same X requests, items (so the same Receipts, Records and Tombstones),
// checkpoints, billed reads, notices, diagnostics and errors. Each run starts
// from the checkpoint the built-in kind had at that point, so each run is
// also a cutover from the built-in kind to the plugin.
func TestThePluginReturnsWhatTheBuiltinKindReturned(t *testing.T) {
	var doc struct {
		Scenarios []struct {
			Name  string            `json:"name"`
			Steps []json.RawMessage `json:"steps"`
		} `json:"scenarios"`
	}
	readJSON(t, filepath.Join("testdata", "scenarios.json"), &doc)
	if len(doc.Scenarios) == 0 {
		t.Fatal("no scenarios")
	}
	for _, sc := range doc.Scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			var golden struct {
				Runs []builtinRun `json:"runs"`
			}
			readJSON(t, filepath.Join("testdata", "builtin", sc.Name+".json"), &golden)
			f, srv := newFakeX(t)
			in := newInstance(t, srv, `{}`)
			runs := 0
			for i, raw := range sc.Steps {
				var step scenarioStep
				if err := json.Unmarshal(raw, &step); err != nil {
					t.Fatal(err)
				}
				switch {
				case step.Post != nil:
					var generic struct {
						Post map[string]any `json:"post"`
					}
					_ = json.Unmarshal(raw, &generic)
					for _, k := range []string{"id", "text", "at", "history"} {
						delete(generic.Post, k)
					}
					f.post(step.Post.ID, step.Post.Text, step.Post.At, step.Post.History, generic.Post)
				case step.Media != nil:
					f.control("/_control/app", map[string]any{"media": []any{step.Media}})
				case step.Delete != "":
					f.list(map[string]any{"delete": []string{step.Delete}})
				case step.Protect != "":
					f.list(map[string]any{"protect": []string{step.Protect}})
				case step.Fail != nil:
					reset := int64(0)
					if step.Fail.Reset != 0 {
						reset = base.Add(time.Duration(step.Fail.Reset) * time.Second).Unix()
					}
					f.control("/_control/app", map[string]any{"fail": map[string]any{"status": step.Fail.Status, "reset": reset}})
				case step.ListError != nil:
					f.list(map[string]any{"list_error": *step.ListError})
				case step.Run != nil:
					if runs >= len(golden.Runs) || golden.Runs[runs].Step != i {
						t.Fatalf("step %d has no captured built-in run", i)
					}
					want := golden.Runs[runs]
					runs++
					in.checkpoint, in.reads = want.Checkpoint, want.ReadsToday
					if string(in.checkpoint) == "null" {
						in.checkpoint = nil
					}
					in.config, in.now, in.token, in.maxPages = string(step.Run.Config), base.Add(time.Duration(step.Run.Now)*time.Second), step.Run.Token, step.Run.MaxPages
					if in.token == "" {
						in.token = testToken
					}
					if in.maxPages == 0 {
						in.maxPages = 10
					}
					f.take()
					got := in.run()
					compareRun(t, i, want, got, f)
				}
			}
			if runs != len(golden.Runs) {
				t.Fatalf("%d runs replayed, %d captured", runs, len(golden.Runs))
			}
		})
	}
}

func compareRun(t *testing.T, step int, want builtinRun, got []page, f *fakeX) {
	t.Helper()
	pages := got
	var failure *envelope
	if e := runError(got); e != nil {
		pages, failure = got[:len(got)-1], e
	}
	if len(pages) != len(want.Pages) {
		t.Fatalf("step %d: %d pages %v, the built-in kind returned %d", step, len(pages), keys(pages), len(want.Pages))
	}
	// The X requests of the whole run, in order.
	var wantRequests []string
	for i, w := range want.Pages {
		g := pages[i]
		wantRequests = append(wantRequests, w.XRequests...)
		if !reflect.DeepEqual(plain(t, g.Items), plain(t, w.Items)) {
			t.Fatalf("step %d page %d items:\ngot  %v\nwant %v", step, i, plain(t, g.Items), plain(t, w.Items))
		}
		if !reflect.DeepEqual(plain(t, g.Checkpoint), plain(t, w.Checkpoint)) {
			t.Fatalf("step %d page %d checkpoint:\ngot  %s\nwant %s", step, i, g.Checkpoint, w.Checkpoint)
		}
		if g.More != w.More || g.Reads != w.Reads || g.Notice != w.Notice || !reflect.DeepEqual(plain(t, g.Diagnostics), plain(t, w.Diagnostics)) {
			t.Fatalf("step %d page %d: more %v reads %d notice %q diagnostics %v, want %v %d %q %v", step, i, g.More, g.Reads, g.Notice, g.Diagnostics, w.More, w.Reads, w.Notice, w.Diagnostics)
		}
	}
	switch {
	case (failure == nil) != (want.Error == nil):
		t.Fatalf("step %d: error %+v, the built-in kind returned %+v", step, failure, want.Error)
	case failure != nil:
		wantRequests = append(wantRequests, want.Error.XRequests...)
		if failure.Class != want.Error.Class || failure.Code != want.Error.Code || failure.RetryAfterSeconds != want.Error.RetryAfterSeconds {
			t.Fatalf("step %d: error %+v, want %+v", step, failure, want.Error)
		}
	}
	if gotRequests := f.take(); !reflect.DeepEqual(gotRequests, orEmpty(wantRequests)) {
		t.Fatalf("step %d X requests:\ngot  %v\nwant %v", step, gotRequests, wantRequests)
	}
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
