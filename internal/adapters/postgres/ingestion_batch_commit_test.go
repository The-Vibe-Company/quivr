package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/processing"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"reflect"
	"testing"
	"time"
)

// A later member's real hash-insert barrier must hold the entire group's
// visibility while another group in the same organization can commit. This
// owns the batch atomicity and commit-order contract; single-writer owners
// cannot catch a loop which commits each member separately.
func TestPreparedPublicationGroupsKeepAtomicOrderedFeed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	org := fmt.Sprintf("adapter-publication-groups-%d", time.Now().UnixNano())
	c, _, err := (postgres.Store{Pool: pool}).Create(ctx, org, corpus.CreateInput{Key: "groups", Name: "Groups"})
	if err != nil {
		t.Fatal(err)
	}
	stores := contentStores(pool)
	g, err := stores.Generation(ctx, org, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]content.PublicationCommit, 4)
	for i := range entries {
		key := fmt.Sprintf("document-%d", i)
		cmd := content.Command{Key: key, Source: content.Source{CorpusID: c.ID, Namespace: "groups", RecordKey: key}, Content: content.Text{Kind: "text", Text: key}}
		r, err := stores.Accept(ctx, corpus.Scope{Organization: org}, cmd)
		if err != nil {
			t.Fatal(err)
		}
		w, _, err := stores.Work(ctx, org, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		v := content.Version{ID: w.VersionID, RecordID: w.RecordID, Manifest: content.ManifestFor(cmd)}
		seg, err := wholeParts(org, v)
		if err != nil {
			t.Fatal(err)
		}
		entries[i] = content.PublicationCommit{Work: w, Publication: publication(content.Blob{Key: key, SHA256: key, Size: int64(len(key))}, content.Blob{Key: key + "/manifest", SHA256: key + "/manifest", Size: 2}), Segmentation: seg, Generation: g}
	}
	head, err := stores.ReadChanges(ctx, org, c.ID, 0, 100, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(context.WithoutCancel(ctx))
	b := entries[1].Publication.Normalized
	if _, err = barrier.Exec(ctx, `INSERT INTO content_blobs VALUES($1,$2,$3,$4,$5)`, org, content.StableID("blob", org, b.SHA256), b.Key, b.SHA256, b.Size); err != nil {
		t.Fatal(err)
	}
	cfg := pool.Config()
	cfg.ConnConfig.RuntimeParams["application_name"] = org
	blocked, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Close()
	defer barrier.Rollback(context.WithoutCancel(ctx))
	done := make(chan error, 1)
	go func() { done <- commitPrepared(ctx, postgres.MaterializationStore{Pool: blocked}, entries[:2]) }()
	for {
		var waiting bool
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock')`, org).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("group escaped insert barrier: %v", err)
		default:
		}
	}
	// Progress by independent writers under injected insert latency, without
	// a flaky elapsed-time ratio in CI.
	if err = commitPrepared(ctx, stores, entries[2:]); err != nil {
		t.Fatal(err)
	}
	window, err := stores.ReadChanges(ctx, org, c.ID, head.Head, 100, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Events) != 6 {
		t.Fatalf("visible events=%d, want only independent group's 6: %+v", len(window.Events), window.Events)
	}
	if err = barrier.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	final, err := stores.ReadChanges(ctx, org, c.ID, head.Head, 100, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Events) != 12 || !reflect.DeepEqual(window.Events, final.Events[:len(window.Events)]) {
		t.Fatalf("unstable committed window: before=%+v after=%+v", window, final)
	}
	for i, event := range final.Events {
		if event.Position != head.Head+int64(i)+1 {
			t.Fatalf("gap at %d: %+v", i, event)
		}
		member := 2 + i/3
		if member >= 4 {
			member -= 4
		}
		want := entries[member].Work.RecordID
		if i%3 == 1 {
			want = entries[member].Work.ReceiptID
		}
		if event.ResourceID != want {
			t.Fatalf("commit order at %d: got %+v want %s", i, event, want)
		}
	}
	t.Run("failed event insertion rolls back the whole group and isolates poison", func(t *testing.T) {
		extra := make([]content.PublicationCommit, 2)
		for i := range extra {
			key := fmt.Sprintf("rollback-%d", i)
			cmd := content.Command{Key: key, Source: content.Source{CorpusID: c.ID, Namespace: "groups", RecordKey: key}, Content: content.Text{Kind: "text", Text: key}}
			r, err := stores.Accept(ctx, corpus.Scope{Organization: org}, cmd)
			if err != nil {
				t.Fatal(err)
			}
			w, _, err := stores.Work(ctx, org, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			seg, err := wholeParts(org, content.Version{ID: w.VersionID, RecordID: w.RecordID, Manifest: content.ManifestFor(cmd)})
			if err != nil {
				t.Fatal(err)
			}
			extra[i] = content.PublicationCommit{Work: w, Publication: publication(content.Blob{Key: key, SHA256: key, Size: int64(len(key))}, content.Blob{Key: key + "/manifest", SHA256: key + "/manifest", Size: 2}), Segmentation: seg, Generation: g}
		}
		before, err := stores.ReadChanges(ctx, org, c.ID, 0, 100, 24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		name := pgx.Identifier{"fail_group_" + content.Hash([]byte(org))[:12]}.Sanitize()
		_, err = pool.Exec(ctx, `CREATE FUNCTION `+name+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.organization=TG_ARGV[0] AND (TG_ARGV[1]='*' OR NEW.resource_id=TG_ARGV[1]) AND NEW.event_type='record.retrieval_ready' THEN RAISE EXCEPTION 'injected group event failure'; END IF;
 RETURN NEW; END $$`)
		if err != nil {
			t.Fatal(err)
		}
		_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON change_events FOR EACH ROW EXECUTE FUNCTION %s('%s','%s')`, name, name, org, "*"))
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Exec(context.WithoutCancel(ctx), `DROP TRIGGER `+name+` ON change_events;DROP FUNCTION `+name+`()`)
		if err = commitPrepared(ctx, stores, extra); err == nil {
			t.Fatal("event failure did not abort the batch")
		}
		after, err := stores.ReadChanges(ctx, org, c.ID, 0, 100, 24*time.Hour)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("failed group changed feed: before=%+v after=%+v err=%v", before, after, err)
		}
		for _, e := range extra {
			r, err := stores.Receipt(ctx, org, e.Work.ReceiptID)
			if err != nil || r.State != "pending" {
				t.Fatalf("failed group resolved member: %+v %v", r, err)
			}
		}
		if _, err = pool.Exec(ctx, `DROP TRIGGER `+name+` ON change_events`); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON change_events FOR EACH ROW EXECUTE FUNCTION %s('%s','%s')`, name, name, org, extra[1].Work.RecordID)); err != nil {
			t.Fatal(err)
		}
		commits := make([]content.IngestionCommit, 2)
		for i, e := range extra {
			commits[i] = content.IngestionCommit{Context: ctx, Kind: content.CommitPublication, Organization: org, RecordID: e.Work.RecordID, Publication: e}
		}
		outcomes := stores.CommitIngestion(ctx, commits)
		if outcomes[0] != nil || outcomes[1] == nil {
			t.Fatalf("poison isolation: %v", outcomes)
		}
		isolated, err := stores.ReadChanges(ctx, org, c.ID, before.Head, 100, 24*time.Hour)
		if err != nil || len(isolated.Events) != 3 {
			t.Fatalf("healthy group member did not commit: %+v %v", isolated, err)
		}
		for i, event := range isolated.Events {
			if event.Position != before.Head+int64(i+1) {
				t.Fatalf("rollback consumed journal positions: %+v", isolated)
			}
		}
	})
	t.Run("grouped enrichment is durable and replay does not allocate positions", func(t *testing.T) {
		space := content.VectorSpace{ID: g.SpaceID}
		if err = pool.QueryRow(ctx, `SELECT manifest FROM vector_spaces WHERE id=$1`, space.ID).Scan(&space.Manifest); err != nil {
			t.Fatal(err)
		}
		commits := make([]content.IngestionCommit, len(entries))
		for i, e := range entries {
			v := content.Version{ID: e.Work.VersionID, RecordID: e.Work.RecordID, Manifest: content.ManifestFor(e.Work.Command)}
			artifact := content.EmbeddingInput(org, c.ID, v, e.Segmentation, e.Segmentation.Segments[0], space, "example")
			artifact.ID = content.StableID("artifact", org, v.ID)
			if err = stores.SaveEmbedding(ctx, artifact, space); err != nil {
				t.Fatal(err)
			}
			commits[i] = content.IngestionCommit{Context: ctx, Kind: content.CommitVectors, Organization: org, RecordID: v.RecordID, Segmentation: e.Segmentation, Generation: g, Artifacts: []content.Embedding{artifact}}
		}
		before, err := stores.ReadChanges(ctx, org, c.ID, 0, 100, 24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if err = errors.Join(stores.CommitIngestion(ctx, commits)...); err != nil {
			t.Fatal(err)
		}
		after, err := stores.ReadChanges(ctx, org, c.ID, before.Head, 100, 24*time.Hour)
		if err != nil || len(after.Events) != 4 {
			t.Fatalf("grouped enrichment feed: %+v %v", after, err)
		}
		for i, event := range after.Events {
			if event.Position != before.Head+int64(i+1) || event.Type != "record.enrichment_available" || event.ResourceID != entries[i].Work.RecordID {
				t.Fatalf("unordered enrichment group: %+v", after)
			}
			activity, err := stores.VersionActivity(ctx, org, entries[i].Work.VersionID)
			if err != nil || activity.Steps.Enriched == nil {
				t.Fatalf("enrichment success preceded durability: %+v %v", activity, err)
			}
		}
		if err = errors.Join(stores.CommitIngestion(ctx, commits)...); err != nil {
			t.Fatal(err)
		}
		replay, err := stores.ReadChanges(ctx, org, c.ID, before.Head, 100, 24*time.Hour)
		if err != nil || !reflect.DeepEqual(after, replay) {
			t.Fatalf("enrichment replay changed cursor window: %+v %v", replay, err)
		}
	})

}

// This integration owns the preparation/fallback lifecycle, rather than the
// SQL group's feed contract above: a provider outage still materializes the
// accepted receipt, and a healthy provider publishes keywords before vectors.
func TestBulkPreparedPublicationKeepsMaterializationFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	org := fmt.Sprintf("adapter-prepared-processing-%d", time.Now().UnixNano())
	c, _, err := (postgres.Store{Pool: pool}).Create(ctx, org, corpus.CreateInput{Key: "prepared", Name: "Prepared"})
	if err != nil {
		t.Fatal(err)
	}
	stores := contentStores(pool)
	g, err := stores.Generation(ctx, org, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	objects := &objectMemory{objects: map[string][]byte{}}
	contents := content.Service{Materialization: stores, Receipts: stores, Versions: stores, RecordStore: stores, Baseline: stores, Blobs: objects, Supersession: stores}
	batch, closeBatch := contents.BeginIngestionBatch(ctx)
	defer closeBatch()
	for _, scenario := range []struct{ fails, race, deadline bool }{{}, {fails: true}, {race: true}, {deadline: true}} {
		fails := scenario.fails
		key := fmt.Sprintf("provider-fails-%v-race-%v-deadline-%v", fails, scenario.race, scenario.deadline)
		r, err := stores.Accept(ctx, corpus.Scope{Organization: org}, content.Command{Key: key, Source: content.Source{CorpusID: c.ID, Namespace: "prepared", RecordKey: key}, Content: content.Text{Kind: "text", Text: "Keyword availability"}})
		if err != nil {
			t.Fatal(err)
		}
		provider := &preparedProvider{space: g.SpaceID, fails: fails, arrived: make(chan struct{}, 1), release: make(chan struct{})}
		if scenario.deadline {
			provider.deadlineFailures = 2
		}
		search := retrieval.Service{Routing: stores, Content: contents, Projection: preparedProjection{}}
		processor := processing.Service{Content: contents, Routing: stores, Retrieval: search, Plugin: &processing.PluginDeriver{Content: contents, Plugin: provider}}
		done := make(chan error, 1)
		go func() { done <- processor.Run(batch, org, r.ID) }()
		select {
		case <-provider.arrived:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		before, err := stores.Receipt(ctx, org, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if before.State != "pending" {
			close(provider.release)
			<-done
			t.Fatalf("receipt became visible before prepared baseline: %+v", before)
		}
		if scenario.race {
			// A legacy or replayed worker may materialize this Version while
			// preparation is in flight. Resolving the receipt alone must never
			// be mistaken for the prepared keyword commit.
			if err = contents.Materialize(ctx, org, r.ID); err != nil {
				close(provider.release)
				<-done
				t.Fatal(err)
			}
		}
		close(provider.release)
		err = <-done
		if (fails || scenario.deadline) && err == nil {
			t.Fatal("provider outage lost its retry outcome")
		}
		if !fails && !scenario.deadline && err != nil {
			t.Fatal(err)
		}
		after, readErr := stores.Receipt(ctx, org, r.ID)
		if readErr != nil || after.State != "resolved" || after.Outcome != "created" {
			t.Fatalf("materialization not durable: %+v %v", after, readErr)
		}
		v, readErr := contents.ProcessingVersion(ctx, org, r.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if fails || scenario.deadline {
			if v.Steps.Materialized == nil || v.Steps.RetrievalReady != nil {
				t.Fatalf("failed baseline discarded materialization: %+v", v)
			}
		} else if v.Availability.State != "retrieval_ready" || v.Steps.Enriched != nil {
			t.Fatalf("healthy baseline not ready before vectors: %+v", v)
		}
		if scenario.deadline {
			// The dependency reports budget exhaustion in both prepared and
			// ordinary calls. Durable materialization must make the next
			// attempt bypass preparation and retain its full parent deadline.
			if err = processor.Run(batch, org, r.ID); err != nil {
				t.Fatal(err)
			}
			v, readErr = contents.ProcessingVersion(ctx, org, r.ID)
			if readErr != nil || v.Availability.State != "retrieval_ready" {
				t.Fatalf("deadline retry did not publish keywords: %+v %v", v, readErr)
			}
			parentDeadline, _ := batch.Deadline()
			if len(provider.deadlines) != 3 || !provider.deadlines[0].Before(parentDeadline) || !provider.deadlines[1].Equal(parentDeadline) || !provider.deadlines[2].Equal(parentDeadline) {
				t.Fatalf("retry retained prepared deadline: calls=%v parent=%v", provider.deadlines, parentDeadline)
			}
		}
	}
}

type preparedProvider struct {
	deadlineFailures int
	deadlines        []time.Time
	space            string
	fails            bool
	arrived          chan struct{}
	release          chan struct{}
}

func (p *preparedProvider) Descriptor() processing.IngestionDescriptor {
	return processing.IngestionDescriptor{Recipe: "plugin:core.ingest@1.0.0", Producer: "plugin:core.ingest@1.0.0", Provenance: json.RawMessage(`{"producer":"example"}`), Spaces: []string{p.space}, VectorSpaces: map[string]content.VectorSpace{p.space: {ID: p.space}}, SegmentsOnly: true}
}
func (p *preparedProvider) SegmentAndEmbed(ctx context.Context, _, _ string, v content.Version, spaces []string) ([]processing.PluginSegment, error) {
	select {
	case p.arrived <- struct{}{}:
	default:
	}
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	deadline, _ := ctx.Deadline()
	p.deadlines = append(p.deadlines, deadline)
	if p.deadlineFailures > 0 {
		p.deadlineFailures--
		return nil, context.DeadlineExceeded
	}
	if p.fails || len(spaces) > 0 {
		return nil, errors.New("provider unavailable")
	}
	return []processing.PluginSegment{{SegmentInput: content.SegmentInput{PartKey: "body", End: len([]rune(v.Manifest.Parts[0].Content.Text))}}}, nil
}

type preparedProjection struct{}

func (preparedProjection) Search(context.Context, []retrieval.Route, corpus.Scope, retrieval.Request) ([]content.Candidate, error) {
	return nil, errors.New("search dependency disabled")
}

func (preparedProjection) Publish(context.Context, content.Generation, string, string, string, content.Version, content.Segmentation) error {
	return nil
}
func (preparedProjection) PublishEmbeddings(context.Context, content.Generation, string, []content.EmbeddingData) error {
	return nil
}

func commitPrepared(ctx context.Context, store content.IngestionCommitStore, entries []content.PublicationCommit) error {
	commits := make([]content.IngestionCommit, len(entries))
	for i, e := range entries {
		commits[i] = content.IngestionCommit{Context: ctx, Kind: content.CommitPublication, Organization: e.Work.Organization, RecordID: e.Work.RecordID, Publication: e}
	}
	return errors.Join(store.CommitIngestion(ctx, commits)...)
}
