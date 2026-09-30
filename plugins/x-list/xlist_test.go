package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/sdks/go/quivrplugin"
)

func TestTheRecheckSetIsBoundedAndFitsTheDeclaredCheckpointLimit(t *testing.T) {
	cp := checkpoint{Floor: base, Watermark: "7000000000000009999", Sweep: &sweep{Top: "7000000000000009999", Token: "7140000000000000000000000000000000"}, ListReadDay: "2026-09-28"}
	for i := 0; i < maxRecheckPosts+5; i++ {
		id := fmt.Sprintf("%d", 7000000000000000000+i)
		cp.track(id, id, base.Add(time.Duration(i)*time.Second), "2026-09-28")
	}
	if len(cp.Recent) != maxRecheckPosts || cp.Dropped != 5 || cp.Recent[0].Root != "7000000000000000005" {
		t.Fatalf("bounded %d dropped %d first %s", len(cp.Recent), cp.Dropped, cp.Recent[0].Root)
	}
	// Tracking an edit updates the entry instead of adding one.
	cp.track("7000000000000000005", "7100000000000000000", base, "2026-09-28")
	if len(cp.Recent) != maxRecheckPosts || cp.Recent[0].Latest != "7100000000000000000" {
		t.Fatalf("edit tracking %+v", cp.Recent[0])
	}
	// A full recheck set must fit the max_checkpoint_bytes the manifest declares.
	started := base
	cp.LastRecheck, cp.Recheck = &started, &recheck{Started: base, After: "7000000000000001000"}
	raw, _ := json.Marshal(cp)
	p, err := quivrplugin.New("quivr-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if limit := p.Manifest().Connector.Limits.MaxCheckpointBytes; len(raw) > limit || limit <= quivrplugin.MaxCheckpointBytes {
		t.Fatalf("a full checkpoint is %d bytes; the manifest declares %d", len(raw), limit)
	}
}

// backfill_since reaches at most 7 days before the first poll. The API cannot
// ask a plugin at creation, so the plugin refuses it at the first fetch and
// the checkpoint does not move.
func TestABackfillOlderThanSevenDaysIsRefusedAtTheFirstPoll(t *testing.T) {
	_, srv := newFakeX(t)
	for _, since := range []time.Duration{-8 * 24 * time.Hour, time.Hour} {
		in := newInstance(t, srv, `{"list_id":"77","backfill_since":"`+base.Add(since).Format(time.RFC3339)+`"}`)
		if e := runError(in.run()); e == nil || e.Class != "source" || e.Code != "invalid_config" || in.checkpoint != nil {
			t.Fatalf("backfill %s: %+v checkpoint %s", since, e, in.checkpoint)
		}
	}
	in := newInstance(t, srv, `{"list_id":"77","backfill_since":"`+base.Add(-6*24*time.Hour).Format(time.RFC3339)+`"}`)
	if e := runError(in.run()); e != nil {
		t.Fatalf("a 6-day backfill: %+v", e)
	}
}

// A recheck interval under 60 s is refused at the first poll unless the pin
// sets allow_short_recheck, which holds only for a loopback api_endpoint (a
// test fake), never for X. A refused run leaves the checkpoint where it was.
func TestARecheckUnderAMinuteNeedsTheTestOnlyPinGuard(t *testing.T) {
	_, srv := newFakeX(t)
	for _, c := range []struct {
		name, api, code string
		guard           bool
	}{
		{"no guard", srv.URL, "invalid_config", false},
		{"guard on a loopback fake", srv.URL, "", true},
		{"guard against X", DefaultAPI, "invalid_configuration", true},
	} {
		in := newInstance(t, srv, `{"list_id":"77","recheck_interval_seconds":1}`)
		in.api, in.shortCheck = c.api, c.guard
		e := runError(in.run())
		if c.code == "" && e != nil || c.code != "" && (e == nil || e.Class != "source" || e.Code != c.code || in.checkpoint != nil) {
			t.Fatalf("%s: got %+v checkpoint %s, want code %q", c.name, e, in.checkpoint, c.code)
		}
	}
}

// An instance advanced by the built-in kind (an interrupted sweep, tracked
// posts, a deleted post pending recheck) continues on the plugin: the sweep
// resumes from its token, no post is collected twice and the deleted post is
// withdrawn. Then the plugin keeps going from its own checkpoint.
func TestAnInstanceOfTheBuiltinKindContinuesOnThePlugin(t *testing.T) {
	var golden struct {
		Runs []builtinRun `json:"runs"`
	}
	readJSON(t, filepath.Join("testdata", "builtin", "cutover_mid_life.json"), &golden)
	var before []string
	for _, r := range golden.Runs[:len(golden.Runs)-1] {
		for _, p := range r.Pages {
			for _, item := range p.Items {
				before = append(before, item["record_key"].(string))
			}
		}
	}
	last := golden.Runs[len(golden.Runs)-1]
	f, srv := newFakeX(t)
	for i := 1; i <= 10; i++ {
		f.post(fmt.Sprintf("90000000000000000%02d", i), fmt.Sprintf("Post %d", i), int64(i), nil, nil)
	}
	f.deleted["9000000000000000002"] = true
	in := newInstance(t, srv, `{"list_id":"77","recheck_interval_seconds":60}`)
	in.checkpoint, in.reads, in.now = last.Checkpoint, last.ReadsToday, base.Add(3*time.Minute)
	got := keys(in.run())
	if !slices.Contains(got, "-9000000000000000002") {
		t.Fatalf("the pending deletion was not withdrawn: %v", got)
	}
	for _, k := range got {
		if slices.Contains(before, k) {
			t.Fatalf("%s was collected again after the cutover: %v", k, got)
		}
	}
	if want := []string{"9000000000000000007", "9000000000000000006", "9000000000000000005", "-9000000000000000002"}; !slices.Equal(got, want) {
		t.Fatalf("after the cutover %v, want %v", got, want)
	}
	in.now = in.now.Add(2 * time.Minute)
	if again := keys(in.run()); len(again) != 0 {
		t.Fatalf("the next run collected %v", again)
	}
}
