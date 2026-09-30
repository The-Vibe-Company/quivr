package postgres_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

// TestStepTimesAreWrittenOnceByTheirStep owns the stored step times and the
// latest-documents read: each step's time appears with the commit of that
// step, a retried step never moves it, and the list pages the newest
// accepted Versions of the Organization across its Corpora.
func TestStepTimesAreWrittenOnceByTheirStep(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	run := fmt.Sprint(time.Now().UnixNano())
	org := "adapter-steps-" + run
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	corpora := corpus.Service{Store: postgres.Store{Pool: pool}}
	a, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "a", Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "b", Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.ContentStore{Pool: pool}
	contents := content.Service{Repository: store, Baseline: store}
	accept := func(corpusID, key string, manifest *content.Manifest) (content.Command, content.Work) {
		t.Helper()
		cmd := content.Command{Key: key, Source: content.Source{CorpusID: corpusID, Namespace: "steps", RecordKey: key}, Content: content.Text{Kind: "text", Text: "Dépêche " + key}}
		if manifest != nil {
			cmd = content.Command{Key: key, Source: cmd.Source, Content: content.Text{Kind: "manifest"}, Manifest: manifest}
		}
		r, err := contents.Accept(ctx, scope, cmd)
		if err != nil {
			t.Fatal(err)
		}
		work, _, err := store.Work(ctx, org, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		return cmd, work
	}
	publish := func(key string, work content.Work) {
		t.Helper()
		text := content.Blob{Key: "fixture/" + key, SHA256: "text-" + run + key, Size: 10}
		published := publication(text, content.Blob{Key: "fixture/m-" + key, SHA256: "manifest-" + run + key, Size: 2})
		// One text Part per Manifest Part, so segments can reference them.
		published.Parts = append(published.Parts, content.PartBlob{Key: "title", Role: "title", Blob: text})
		if err := store.Publish(ctx, work, published); err != nil {
			t.Fatal(err)
		}
		// The fixture blobs are not in S3: keep this Receipt away from a live worker.
		if _, err := pool.Exec(ctx, `UPDATE ingestion_outbox SET dispatched=true WHERE organization=$1`, org); err != nil {
			t.Fatal(err)
		}
	}
	read := func(versionID string) content.Activity {
		t.Helper()
		activity, err := store.VersionActivity(ctx, org, versionID)
		if err != nil {
			t.Fatal(err)
		}
		return activity
	}
	same := func(step string, before, after *time.Time) {
		t.Helper()
		if before == nil || after == nil || !before.Equal(*after) {
			t.Fatalf("%s: %v then %v, want one time written once", step, before, after)
		}
	}
	long := strings.Repeat("é", 250)
	titled := &content.Manifest{Parts: []content.Part{{Key: "title", Role: "title", Content: content.Text{Kind: "text", Text: "  " + long + "  "}}, {Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: "Le port rouvre."}}}}
	cmd, searchable := accept(a.ID, "searchable", titled)
	_, held := accept(a.ID, "held", nil)
	_, other := accept(b.ID, "other", nil)

	received := read(searchable.VersionID)
	if received.State != content.ActivityReceived || received.Steps.Accepted == nil || received.Steps.Materialized != nil || received.Title != strings.Repeat("é", 200) {
		t.Fatalf("received document: %+v", received)
	}

	// The latest documents of the Organization, newest first, one per page.
	var listed []string
	var after *content.ActivityCursor
	for i := 0; i < 4; i++ {
		items, err := store.LatestActivity(ctx, org, after, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) == 0 {
			break
		}
		listed = append(listed, items[0].VersionID)
		after = &content.ActivityCursor{AcceptedAt: *items[0].Steps.Accepted, VersionID: items[0].VersionID}
	}
	if got, want := strings.Join(listed, ","), strings.Join([]string{other.VersionID, held.VersionID, searchable.VersionID}, ","); got != want {
		t.Fatalf("latest documents: %s, want %s", got, want)
	}

	publish("searchable", searchable)
	materialized := read(searchable.VersionID)
	if materialized.State != "materialized" || materialized.Steps.Materialized == nil || materialized.Steps.Materialized.Before(*materialized.Steps.Accepted) || materialized.Steps.Segmented != nil {
		t.Fatalf("materialized document: %+v", materialized.Steps)
	}
	v := content.Version{ID: searchable.VersionID, RecordID: searchable.RecordID, Manifest: content.ManifestFor(cmd)}
	seg, err := wholeParts(org, v)
	if err != nil {
		t.Fatal(err)
	}
	g, err := store.Generation(ctx, org, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A retried segmentation or promotion keeps the first time.
	var first content.Activity
	for i := 0; i < 2; i++ {
		if err = contents.SaveSegmentation(ctx, org, v, seg); err != nil {
			t.Fatal(err)
		}
		if err = contents.Promote(ctx, org, seg, g); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = read(searchable.VersionID)
		}
	}
	ready := read(searchable.VersionID)
	same("materialized", materialized.Steps.Materialized, ready.Steps.Materialized)
	same("segmented", first.Steps.Segmented, ready.Steps.Segmented)
	same("retrieval_ready", first.Steps.RetrievalReady, ready.Steps.RetrievalReady)
	if ready.State != "retrieval_ready" || !ready.Current || ready.Steps.Segmented == nil || ready.Steps.RetrievalReady == nil || ready.Steps.RetrievalReady.Before(*ready.Steps.Segmented) {
		t.Fatalf("searchable document: %+v %+v", ready, ready.Steps)
	}
	if ready.Ingestion == nil || ready.Ingestion.ID != "adapter.fixture" {
		t.Fatalf("ingestion plugin of the served segmentation: %+v", ready.Ingestion)
	}
	// The public Version reads the same times.
	stored, err := store.Version(ctx, org, searchable.RecordID, searchable.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	same("version retrieval_ready", ready.Steps.RetrievalReady, stored.Steps.RetrievalReady)
	same("version accepted", ready.Steps.Accepted, stored.Steps.Accepted)

	// A quarantine is recorded once, even when the baseline reports it again.
	publish("held", held)
	for i := 0; i < 2; i++ {
		if err = contents.BaselineProgress(ctx, org, held.VersionID, "blocked", "short_text_limit", true); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = read(held.VersionID)
		}
	}
	quarantined := read(held.VersionID)
	same("quarantined", first.Steps.Quarantined, quarantined.Steps.Quarantined)
	if quarantined.State != "quarantined" || quarantined.Current {
		t.Fatalf("quarantined document: %+v", quarantined)
	}

	// A Version materialized before step times were recorded, simulated by
	// clearing its time, gets no later step: a rebuild re-running its steps
	// would date them wrongly.
	publish("other", other)
	if _, err = pool.Exec(ctx, `UPDATE record_versions SET materialized_at=NULL WHERE organization=$1 AND id=$2`, org, other.VersionID); err != nil {
		t.Fatal(err)
	}
	ov := content.Version{ID: other.VersionID, RecordID: other.RecordID, Manifest: content.ManifestFor(content.Command{Content: content.Text{Kind: "text", Text: "Dépêche other"}})}
	oseg, err := wholeParts(org, ov)
	if err != nil {
		t.Fatal(err)
	}
	og, err := store.Generation(ctx, org, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = contents.SaveSegmentation(ctx, org, ov, oseg); err != nil {
		t.Fatal(err)
	}
	if err = contents.Promote(ctx, org, oseg, og); err != nil {
		t.Fatal(err)
	}
	if legacy := read(other.VersionID); legacy.State != "retrieval_ready" || legacy.Steps.Segmented != nil || legacy.Steps.RetrievalReady != nil {
		t.Fatalf("legacy Version dated by a re-run: %+v %+v", legacy, legacy.Steps)
	}

	// A withdrawal dates every Version of its Record, once.
	for _, key := range []string{"withdraw-1", "withdraw-2"} {
		if _, err = contents.Withdraw(ctx, scope, content.Withdrawal{Key: key, Source: cmd.Source}); err != nil {
			t.Fatal(err)
		}
		if key == "withdraw-1" {
			first = read(searchable.VersionID)
		}
	}
	withdrawn := read(searchable.VersionID)
	same("withdrawn", first.Steps.Withdrawn, withdrawn.Steps.Withdrawn)
	if withdrawn.State != content.ActivityWithdrawn || withdrawn.Current {
		t.Fatalf("withdrawn document: %+v", withdrawn)
	}
}
