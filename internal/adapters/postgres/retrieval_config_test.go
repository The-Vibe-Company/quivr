package postgres_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
)

func titleConfig(pointer string) corpus.Retrieval {
	return corpus.Retrieval{Fields: []corpus.Field{{Name: "title", SourcePointer: pointer, Type: "string", Roles: []string{"search"}}}}
}

func (f controlFixture) configure(corpusID, key string, cfg corpus.Retrieval) (operations.Operation, error) {
	scope := corpus.Scope{Organization: f.org, Actions: []string{"corpora:write", "operations:write"}, Corpora: []string{"*"}}
	return operations.Service{Store: f.store}.ConfigureRetrieval(f.ctx, scope, corpusID, key, cfg)
}

func (f controlFixture) effective(corpusID string) corpus.Retrieval {
	f.t.Helper()
	c, err := postgres.Store{Pool: f.pool}.Read(f.ctx, f.org, corpusID)
	if err != nil {
		f.t.Fatal(err)
	}
	b, _ := json.Marshal(c.Retrieval)
	var out corpus.Retrieval
	if err = json.Unmarshal(b, &out); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f controlFixture) routedFields(corpusID string) []corpus.Field {
	f.t.Helper()
	g, err := f.store.Generation(f.ctx, f.org, corpusID)
	if err != nil {
		f.t.Fatal(err)
	}
	return g.Fields
}

// activate runs an empty Corpus's Operation to its activation decision.
func (f controlFixture) activate(id string) (bool, error) {
	f.t.Helper()
	target, err := f.store.BeginRebuild(f.ctx, f.org, id)
	if err != nil {
		f.t.Fatal(err)
	}
	if target.Operation.State != operations.StateRunning {
		return false, operations.ErrNotRunning
	}
	return f.store.ActivateRebuild(f.ctx, f.org, id)
}

// The effective configuration is the routed generation's pin: it changes only
// at validated cutover, survives failure, and routing serves the same fields
// that later ingestion publishes with.
func TestRetrievalConfigurationActivatesOnlyAtCutover(t *testing.T) {
	f, cancel := newControlFixture(t, "config")
	defer cancel()
	a, b := f.corpus("a"), f.corpus("b")
	if got := f.effective(a); got.Fields == nil || len(got.Fields) != 0 {
		t.Fatalf("creation config %+v", got)
	}
	headline := titleConfig("/extensions/example.editorial/data/headline")
	op, err := f.configure(a, "k1", headline)
	if err != nil || op.Kind != operations.KindRetrievalConfiguration || op.State != operations.StateQueued || op.CorpusID != a {
		t.Fatalf("accept %+v %v", op, err)
	}
	if again, err := f.configure(a, "k1", headline); err != nil || again.ID != op.ID || again.TargetGenerationID != op.TargetGenerationID {
		t.Fatalf("replay %+v %v", again, err)
	}
	if _, err = f.configure(a, "k1", titleConfig("/provenance/title")); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("changed request under same key: %v", err)
	}
	// Pending: the prior configuration stays effective and routed.
	if len(f.effective(a).Fields) != 0 || len(f.routedFields(a)) != 0 {
		t.Fatal("pending configuration became effective")
	}
	target, err := f.store.BeginRebuild(f.ctx, f.org, op.ID)
	if err != nil || len(target.Generation.Fields) != 1 || target.Generation.Fields[0].SourcePointer != "/extensions/example.editorial/data/headline" {
		t.Fatalf("target pin %+v %v", target.Generation, err)
	}
	if ok, err := f.store.ActivateRebuild(f.ctx, f.org, op.ID); !ok || err != nil {
		t.Fatalf("activate %v %v", ok, err)
	}
	if got := f.effective(a); len(got.Fields) != 1 || got.Fields[0].Name != "title" {
		t.Fatalf("effective after cutover %+v", got)
	}
	if fields := f.routedFields(a); len(fields) != 1 || f.routed(a) != op.TargetGenerationID {
		t.Fatalf("routing after cutover %+v", fields)
	}
	if len(f.effective(b).Fields) != 0 || len(f.routedFields(b)) != 0 {
		t.Fatal("neighbouring Corpus changed")
	}
	// Recovery after activation recognizes the same target.
	if ok, err := f.store.ActivateRebuild(f.ctx, f.org, op.ID); !ok || err != nil || f.state(op.ID) != operations.StateSucceeded {
		t.Fatalf("recovery %v %v", ok, err)
	}

	// Failure leaves the prior configuration effective and queryable.
	failing, err := f.configure(a, "k2", titleConfig("/provenance/title"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.BeginRebuild(f.ctx, f.org, failing.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.store.FailRebuild(f.ctx, f.org, failing.ID, operations.Error{Code: "segmentation_mismatch", Message: "x"}); err != nil {
		t.Fatal(err)
	}
	if got := f.effective(a); got.Fields[0].SourcePointer != "/extensions/example.editorial/data/headline" || f.routed(a) != op.TargetGenerationID {
		t.Fatalf("failure changed effective config %+v", got)
	}

	// A plain rebuild pins the effective configuration rather than reverting it.
	rebuild := f.rebuild(a, "plain")
	if ok, err := f.activate(rebuild.ID); !ok || err != nil {
		t.Fatalf("plain rebuild %v %v", ok, err)
	}
	if fields := f.routedFields(a); len(fields) != 1 || fields[0].SourcePointer != "/extensions/example.editorial/data/headline" {
		t.Fatalf("plain rebuild reverted mapping %+v", fields)
	}

	// Rerunning the failed configuration re-targets the same configuration.
	rerun, err := f.store.AcceptRerun(f.ctx, f.org, failing.ID, "again", []byte(`{"idempotency_key":"again","source_operation_id":"`+failing.ID+`"}`))
	if err != nil || rerun.Kind != operations.KindRetrievalConfiguration || rerun.TargetGenerationID == failing.TargetGenerationID {
		t.Fatalf("rerun %+v %v", rerun, err)
	}
	if ok, err := f.activate(rerun.ID); !ok || err != nil {
		t.Fatalf("rerun activation %v %v", ok, err)
	}
	if got := f.effective(a); got.Fields[0].SourcePointer != "/provenance/title" {
		t.Fatalf("rerun effective %+v", got)
	}
}

// The latest accepted configuration wins: it supersedes older pending
// configuration Operations, and no generation pinned to an older configuration
// can activate over a newer effective one.
func TestNewerRetrievalConfigurationSupersedesOlder(t *testing.T) {
	f, cancel := newControlFixture(t, "supersede")
	defer cancel()
	a := f.corpus("a")
	queued, err := f.configure(a, "queued", titleConfig("/provenance/one"))
	if err != nil {
		t.Fatal(err)
	}
	running, err := f.configure(a, "running", titleConfig("/provenance/two"))
	if err != nil {
		t.Fatal(err)
	}
	if f.state(queued.ID) != operations.StateCanceled {
		t.Fatalf("older queued configuration %s", f.state(queued.ID))
	}
	if _, err = f.store.BeginRebuild(f.ctx, f.org, running.ID); err != nil {
		t.Fatal(err)
	}
	stale := f.rebuild(a, "stale") // pins the configuration effective now (version 1)
	latest, err := f.configure(a, "latest", titleConfig("/provenance/three"))
	if err != nil {
		t.Fatal(err)
	}
	if f.state(running.ID) != operations.StateCancelRequested {
		t.Fatalf("older running configuration %s", f.state(running.ID))
	}
	if ok, err := f.activate(latest.ID); !ok || err != nil {
		t.Fatalf("latest %v %v", ok, err)
	}
	if ok, err := f.activate(stale.ID); ok || !errors.Is(err, operations.ErrNotRunning) {
		t.Fatalf("stale rebuild activated %v %v", ok, err)
	}
	op, err := f.store.Operation(f.ctx, f.org, stale.ID)
	if err != nil || op.State != operations.StateFailed || len(op.Errors) != 1 || op.Errors[0].Code != "retrieval_configuration_superseded" {
		t.Fatalf("stale outcome %+v %v", op, err)
	}
	if got := f.effective(a); got.Fields[0].SourcePointer != "/provenance/three" || f.routed(a) != latest.TargetGenerationID {
		t.Fatalf("effective %+v", got)
	}
	// Rerunning a superseded configuration cannot revert the newer one.
	if err = f.store.ConfirmCancel(f.ctx, f.org, running.ID); err != nil {
		t.Fatal(err)
	}
	rerun, err := f.store.AcceptRerun(f.ctx, f.org, running.ID, "again", []byte(`{"idempotency_key":"again","source_operation_id":"`+running.ID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := f.activate(rerun.ID); ok || !errors.Is(err, operations.ErrNotRunning) || f.state(rerun.ID) != operations.StateFailed {
		t.Fatalf("superseded rerun activated %v %v", ok, err)
	}
	if got := f.effective(a); got.Fields[0].SourcePointer != "/provenance/three" {
		t.Fatalf("rerun reverted %+v", got)
	}
}

// Corpus creation stores the resolved configuration as version 1.
func TestCorpusCreationResolvesProfile(t *testing.T) {
	f, cancel := newControlFixture(t, "profile")
	defer cancel()
	scope := corpus.Scope{Organization: f.org, Actions: []string{"corpora:write"}, Corpora: []string{"*"}}
	service := corpus.Service{Store: postgres.Store{Pool: f.pool}, Namespaces: func(ns string) bool { return ns == "example.editorial" }}
	c, _, err := service.Create(f.ctx, scope, corpus.CreateInput{Key: "profiled", Name: "Profiled", Retrieval: map[string]any{"plugin_profile": "example.editorial"}})
	if err != nil {
		t.Fatal(err)
	}
	got := f.effective(c.ID)
	if got.PluginProfile != "example.editorial" || len(got.Fields) != 1 || len(f.routedFields(c.ID)) != 1 {
		t.Fatalf("creation profile %+v", got)
	}
}
