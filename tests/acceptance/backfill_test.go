package acceptance

import (
	"os"
	"testing"
	"time"
)

// backfillSetup is what scripts/ingestion_plugin.py gives the backfill
// scenario once the rollback one ran: 0.1.0 serves the plan at its address
// with the small space served and the large one for evaluation, the
// deployment backfills one Version per second, and an operator key of the
// Corpus's Organization may backfill.
func backfillSetup(t *testing.T) (operator, backfiller, endpoint, run string) {
	t.Helper()
	operator, endpoint, _, _, run = rollbackSetup(t)
	backfiller = os.Getenv("QUIVR_TEST_BACKFILLER")
	if backfiller == "" {
		t.Skip("make verify gives the backfill scenario an operator key of the Corpus's Organization")
	}
	return operator, backfiller, endpoint, run
}

// backfillArticles are ingested while 0.1.0 enables only its small space; the
// backfill's window holds the last three.
var backfillArticles = []struct{ key, text string }{
	{"lighthouse", "The lighthouse keeper logged the storm."},
	{"ferry", "The ferry crossed at dawn with three cars."},
	{"oysters", "Oyster farmers counted the spring spat."},
	{"regatta", "The regatta started after the fog lifted."},
	{"mussels", "Mussel beds recovered after the cold winter."},
}

// backfillCorpus is the scenario's Corpus, created on first use.
func backfillCorpus(t *testing.T, run string) string {
	t.Helper()
	return request(t, "POST", "/v0/corpora", os.Getenv("QUIVR_TEST_ADMIN"), map[string]any{"name": "Backfill", "idempotency_key": "backfill-" + run}, 201)["corpus_id"].(string)
}

// article replays an article's ingestion and returns its Version.
func article(t *testing.T, corpusID, run string, i int) map[string]any {
	t.Helper()
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	a := backfillArticles[i]
	receipt := awaitReceipt(t, request(t, "POST", "/v0/records", admin, inlineCommand(corpusID, "backfill-"+a.key+"-"+run, a.key, a.text), 202)["receipt_id"].(string))
	return request(t, "GET", "/v0/records/"+receipt["record_id"].(string)+"/versions/"+receipt["version_id"].(string), admin, nil, 200)
}

// backfillBody is the backfill command: the Corpus from the third article's
// acceptance on.
func backfillBody(t *testing.T, corpusID, run string) map[string]any {
	t.Helper()
	return map[string]any{"idempotency_key": "backfill-" + run, "corpus_id": corpusID, "accepted_after": article(t, corpusID, run, 2)["accepted_at"], "dry_run": false, "confirm_cost": true}
}

func semanticHits(t *testing.T, corpusID, query string) []map[string]any {
	t.Helper()
	items := request(t, "POST", "/v0/search", os.Getenv("QUIVR_TEST_ADMIN"), map[string]any{"query": query, "corpus_ids": []string{corpusID}, "mode": "semantic"}, 200)["items"].([]any)
	hits := []map[string]any{}
	for _, item := range items {
		hits = append(hits, item.(map[string]any))
	}
	return hits
}

// awaitBackfill polls a backfill Operation until done reports it settled.
func awaitBackfill(t *testing.T, token, location string, done func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		op := request(t, "GET", location, token, nil, 200)
		if done(op) {
			return op
		}
		if time.Now().After(deadline) {
			t.Fatalf("backfill %v never reached the expected state", op)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func counter(op map[string]any, name string) float64 {
	n, _ := op["counters"].(map[string]any)[name].(float64)
	return n
}

// TestBackfillStarts builds a Corpus that predates the large space: 0.1.0 is
// activated with its small space alone, the Corpus ingests five articles,
// and a rollback enables the large space again, which new Corpora carry but
// this one does not. The dry run of a window counts only its articles and
// prices the large space; the backfill then needs confirm_cost, starts, and
// is paused once it has filled an article.
func TestBackfillStarts(t *testing.T) {
	operator, backfiller, endpoint, run := backfillSetup(t)
	pinned, err := os.ReadFile(os.Getenv("QUIVR_TEST_ACTIVATION_PINNED_MANIFEST"))
	if err != nil {
		t.Fatal(err)
	}
	both := request(t, "GET", "/v0/admin/plugins/plan", operator, nil, 200)
	small := request(t, "POST", "/v0/admin/plugins", operator, map[string]any{"idempotency_key": "small-only-" + run, "endpoint": endpoint, "manifest": string(pinned), "spaces": map[string]string{"example.hash_embedder.small": "served"}}, 202)
	if checked := awaitCheck(t, operator, "/v0/admin/plugins/"+small["registration_id"].(string)); checked["state"] != "validated" {
		t.Fatalf("0.1.0 with its small space alone: %v", checked)
	}
	request(t, "POST", "/v0/admin/plugins/"+small["registration_id"].(string)+"/activate", operator, map[string]any{}, 200)
	corpusID := backfillCorpus(t, run)
	for _, a := range backfillArticles {
		ingestEnriched(t, corpusID, "backfill-"+a.key+"-"+run, a.key, a.text)
	}
	back := request(t, "POST", "/v0/admin/plugins/plan/rollback", operator, map[string]any{"idempotency_key": "backfill-both-" + run}, 200)
	if planRoles(back)["ingestion"] != planRoles(both)["ingestion"] {
		t.Fatalf("the rollback to both spaces: %v", back)
	}
	if spaces, _ := vectorSpaces(t, corpusID); len(spaces) != 1 || spaces[pluginServedSpace] == nil {
		t.Fatalf("the Corpus predates the large space: %v", spaces)
	}

	body := backfillBody(t, corpusID, run)
	body["dry_run"], body["confirm_cost"] = true, false
	estimate := request(t, "POST", "/v0/admin/backfills", backfiller, body, 200)
	if estimate["versions"] != float64(3) || estimate["segments"] != float64(3) || estimate["spaces"].([]any)[0] != pluginEvaluationSpace || estimate["estimated_cost_usd"] == nil || estimate["confirmation_required"] != true {
		t.Fatalf("dry run %v, want the window's 3 articles in the large space, priced above the threshold", estimate)
	}
	body["dry_run"] = false
	if refused := request(t, "POST", "/v0/admin/backfills", backfiller, body, 409); refused["code"] != "cost_confirmation_required" {
		t.Fatalf("an unconfirmed cost: %v", refused)
	}
	body["confirm_cost"] = true
	op := request(t, "POST", "/v0/admin/backfills", backfiller, body, 202)
	location := "/v0/operations/" + op["operation_id"].(string)
	// One article a second: it is paused while it still has articles to fill.
	awaitBackfill(t, backfiller, location, func(op map[string]any) bool { return counter(op, "versions_done") >= 1 })
	paused := request(t, "POST", location+"/pause", backfiller, map[string]any{"idempotency_key": "pause-" + run}, 202)
	if paused["state"] != "paused" || counter(paused, "versions_done") >= counter(paused, "versions_in_scope") {
		t.Fatalf("paused %v, want it paused with articles left", paused)
	}
}

// TestBackfillResumes runs once the harness restarted the worker. The
// backfill resumes from its checkpoint and fills the window without redoing
// an article, while the articles before the window get no vector in the
// large space. Promoting the large space is refused while coverage is
// incomplete; forced, search uses it, and promoting the small space back
// restores it.
func TestBackfillResumes(t *testing.T) {
	_, backfiller, _, run := backfillSetup(t)
	corpusID := backfillCorpus(t, run)
	// The same key and scope replay the backfill.
	op := request(t, "POST", "/v0/admin/backfills", backfiller, backfillBody(t, corpusID, run), 202)
	location := "/v0/operations/" + op["operation_id"].(string)
	if op["state"] != "paused" {
		t.Fatalf("after the restart %v, want it still paused", op)
	}
	request(t, "POST", location+"/resume", backfiller, map[string]any{"idempotency_key": "resume-" + run}, 202)
	done := awaitBackfill(t, backfiller, location, func(op map[string]any) bool { return op["state"] != "running" && op["state"] != "paused" })
	if done["state"] != "succeeded" || counter(done, "versions_in_scope") != 3 || counter(done, "versions_done") != 3 || counter(done, "versions_skipped") != 0 {
		t.Fatalf("resumed backfill %v, want each of the window's 3 articles filled once", done)
	}
	spaces, total := vectorSpaces(t, corpusID)
	if large := spaces[pluginEvaluationSpace]; total != 5 || large == nil || large["role"] != "evaluation" || coverage(large) != 3 || coverage(spaces[pluginServedSpace]) != 5 {
		t.Fatalf("after the backfill %v of %v segments, want the large space on the window's 3 only", spaces, total)
	}

	promote := func(space string, force bool, want int) map[string]any {
		t.Helper()
		return request(t, "POST", "/v0/admin/spaces/"+space+"/promote", backfiller, map[string]any{"force": force}, want)
	}
	if refused := promote(pluginEvaluationSpace, false, 409); refused["code"] != "coverage_incomplete" {
		t.Fatalf("promoting an incomplete space: %v", refused)
	}
	if p := promote(pluginEvaluationSpace, true, 200); p["served_space_id"] != pluginEvaluationSpace || p["previous_space_id"] != pluginServedSpace {
		t.Fatalf("forced promotion %v", p)
	}
	regatta := article(t, corpusID, run, 3)["version_id"]
	if hits := semanticHits(t, corpusID, "regatta fog lifted"); len(hits) == 0 || hits[0]["version_id"] != regatta || hits[0]["vector_space_id"] != pluginEvaluationSpace {
		t.Fatalf("after the promotion %v, want the regatta found in the large space", hits)
	}
	if p := promote(pluginServedSpace, true, 200); p["served_space_id"] != pluginServedSpace {
		t.Fatalf("promoting back %v", p)
	}
	lighthouse := article(t, corpusID, run, 0)["version_id"]
	if hits := semanticHits(t, corpusID, "lighthouse keeper storm"); len(hits) == 0 || hits[0]["version_id"] != lighthouse || hits[0]["vector_space_id"] != pluginServedSpace {
		t.Fatalf("after promoting back %v, want the lighthouse found in the small space", hits)
	}
}
