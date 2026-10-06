package acceptance

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// The assembled text-monitoring journey (THE-662) composes the already-tested
// feature journeys into one public run over HTTP, SSE and raw webhook bytes, in
// its own Corpus. scripts/local.py runs it in three phases around a worker
// outage:
//
//	TestJourneyBeforeRestart  ingestion, lexical then vector search, Match and
//	                          signed delivery with a retry, correction, withdrawal
//	(harness stops the worker)
//	TestJourneyWorkerStopped  accepted work stays pending and readable
//	(harness restarts the worker)
//	TestJourneyAfterRestart   recovery, SSE replay/resume, cursor expiry and
//	                          catalog resync, rebuild
//
// Each feature keeps its own acceptance tests; this file only composes public
// behaviour they already prove. Search assertions run only after enrichment
// committed, so vector-dependent results are deterministic.

const (
	journeyNoMatch = "CALME" // fixture decision marker: no_match; anything else matches
	journeyQuery   = "Are passenger boats to Corsica running during the strike?"
)

// journeyState carries identities from one phase to the next.
type journeyState struct {
	Run, Corpus, Subscription, Start      string
	RecordA, VersionA, ReceiptA, ReceiptD string
	CommandA                              map[string]any
	Withdrawn                             string
	Records                               []string
}

func journeyStatePath() string {
	return filepath.Join(os.Getenv("QUIVR_TEST_CAPTURES"), "journey-state.json")
}

func saveJourney(t *testing.T, s journeyState) {
	t.Helper()
	b, _ := json.Marshal(s)
	if err := os.WriteFile(journeyStatePath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func loadJourney(t *testing.T) journeyState {
	t.Helper()
	raw, err := os.ReadFile(journeyStatePath())
	if err != nil {
		t.Skip("runs after TestJourneyBeforeRestart (scripts/local.py)")
	}
	var s journeyState
	if err = json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// journeySteps records each named step's duration and outcome in
// journey-<phase>.json, so a failed run names the step that failed.
type journeySteps struct {
	t     *testing.T
	phase string
	steps []map[string]any
}

func newJourneySteps(t *testing.T, phase string) *journeySteps {
	j := &journeySteps{t: t, phase: phase}
	t.Cleanup(func() {
		if dir := os.Getenv("QUIVR_TEST_CAPTURES"); dir != "" {
			status := "passed"
			if t.Failed() {
				status = "failed"
			}
			b, _ := json.MarshalIndent(map[string]any{"phase": phase, "status": status, "steps": j.steps}, "", "  ")
			_ = os.WriteFile(filepath.Join(dir, "journey-"+phase+".json"), b, 0o600)
		}
	})
	return j
}

// step runs one named part of the journey; a failure inside stays attributed to it.
func (j *journeySteps) step(name string, fn func()) {
	start := time.Now()
	entry := map[string]any{"step": name, "status": "failed"}
	j.steps = append(j.steps, entry)
	defer func() { entry["seconds"] = time.Since(start).Seconds() }()
	fn()
	entry["status"] = "passed"
}

// journeyNotice polls the Corpus feed from cursor for a monitoring notice of
// kind about recordID.
func journeyNotice(t *testing.T, corpusID, cursor, kind, recordID string) map[string]any {
	t.Helper()
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	deadline := time.Now().Add(monitoringWait)
	for {
		items, next := drain(t, admin, corpusID, cursor, 0)
		for _, item := range items {
			if item["type"] == kind && item["monitoring"] != nil && refsOf(item)["record_id"] == recordID {
				return item
			}
		}
		cursor = next
		if time.Now().After(deadline) {
			t.Fatalf("no %s notice for %s", kind, recordID)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// matchesFor counts a Subscription's Matches about one Record.
func matchesFor(t *testing.T, subscriptionID, recordID string) int {
	t.Helper()
	n := 0
	for _, item := range request(t, "GET", matchesPath(subscriptionID, "", 100), os.Getenv("QUIVR_TEST_ADMIN"), nil, 200)["items"].([]any) {
		if item.(map[string]any)["record_id"] == recordID {
			n++
		}
	}
	return n
}

// searchHits runs one public search over a Corpus.
func searchHits(t *testing.T, corpusID, query, mode string) []map[string]any {
	t.Helper()
	body := map[string]any{"query": query, "corpus_ids": []string{corpusID}, "mode": mode}
	var hits []map[string]any
	for _, item := range request(t, "POST", "/v0/search", os.Getenv("QUIVR_TEST_ADMIN"), body, 200)["items"].([]any) {
		hits = append(hits, item.(map[string]any))
	}
	return hits
}

func TestJourneyBeforeRestart(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	j := newJourneySteps(t, "before-restart")
	receiver := startReceiver(t)
	s := journeyState{Run: monitoringRun()}
	var created map[string]any

	j.step("corpus_and_subscription", func() {
		s.Corpus = request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Journey " + s.Run, "idempotency_key": "journey-" + s.Run}, 201)["corpus_id"].(string)
		s.Start = request(t, "GET", changesPath(s.Corpus, "", 0), admin, nil, 200)["next_cursor"].(string)
		query := request(t, "POST", "/v0/saved-queries", admin, savedQueryCommand("journey-"+s.Run, s.Corpus), 201)
		body := subscriptionCommand("journey-"+s.Run, query, destinationCapture)
		body["evaluator"].(map[string]any)["configuration"] = map[string]any{"decisions": map[string]any{journeyNoMatch: "no_match", "default": "match"}}
		s.Subscription = request(t, "POST", "/v0/subscriptions", admin, body, 201)["subscription_id"].(string)
		// The first notice fails once, then every attempt is acknowledged.
		receiver.scriptType(s.Subscription, "match.created", reply{status: 503}, reply{status: 204})
	})

	j.step("inline_ingestion_and_replay", func() {
		s.CommandA = inlineCommand(s.Corpus, "journey-a-"+s.Run, "journey-a", "Les ferries pour la Corse circulent normalement malgré la grève du port de Marseille.")
		accepted := request(t, "POST", "/v0/records", admin, s.CommandA, 202)
		if replay := request(t, "POST", "/v0/records", admin, s.CommandA, 202); replay["receipt_id"] != accepted["receipt_id"] {
			t.Fatal("replay created another Receipt", replay, accepted)
		}
		ready := awaitReady(t, accepted["receipt_id"].(string))
		s.ReceiptA, s.RecordA, s.VersionA = ready["receipt_id"].(string), ready["record_id"].(string), ready["version_id"].(string)
		s.Records = append(s.Records, s.RecordA)
	})

	j.step("lexical_then_vector", func() {
		awaitEnriched(t, admin, s.Corpus, s.Start, s.RecordA)
		feed, _ := drain(t, admin, s.Corpus, s.Start, 0)
		ready, enriched := eventPosition(feed, "record.retrieval_ready", s.RecordA), eventPosition(feed, "record.enrichment_available", s.RecordA)
		if ready < 0 || enriched < 0 || ready > enriched {
			t.Fatal("the Record must be lexically searchable before its embeddings are available", ready, enriched)
		}
		lexical := searchHits(t, s.Corpus, "ferries", "lexical")
		if len(lexical) != 1 || lexical[0]["record_id"] != s.RecordA || lexical[0]["version_id"] != s.VersionA {
			t.Fatal("lexical hit", lexical)
		}
		semantic := searchHits(t, s.Corpus, journeyQuery, "semantic")
		if len(semantic) == 0 || semantic[0]["record_id"] != s.RecordA || semantic[0]["embedding_artifact_id"] == nil || semantic[0]["vector_space_id"] == nil {
			t.Fatal("semantic hit", semantic)
		}
	})

	j.step("match_and_signed_delivery_with_retry", func() {
		created = journeyNotice(t, s.Corpus, s.Start, "match.created", s.RecordA)
		delivery := refsOf(created)["delivery_id"].(string)
		delivered := awaitDelivery(t, admin, delivery, func(d map[string]any) bool { return d["state"] == "delivered" })
		if got := attemptOutcomes(t, delivery); !reflect.DeepEqual(got, []string{"retryable_error", "acknowledged"}) {
			t.Fatal("attempt history", got)
		}
		captures := receiver.capturesOf(created["event_id"].(string))
		if len(captures) != 2 || captures[0].Status != 503 || captures[1].Status != 204 {
			t.Fatalf("receiver saw %d attempts", len(captures))
		}
		sameNotice(t, receiver, captures, delivered)
		if matchesFor(t, s.Subscription, s.RecordA) != 1 {
			t.Fatal("one Match per Record Version")
		}
	})

	j.step("batch_with_upload_and_structured_manifest", func() {
		entries := richEntries(t, s.Corpus, "journey-"+s.Run)
		outcomes := batchOutcomes(t, admin, entries)
		for i, want := range map[int]string{0: "Texte journey-" + s.Run + " 🌞", 1: "Blob journey-" + s.Run + " 📦", 2: "Titre journey-" + s.Run} {
			ready := awaitReady(t, receiptOf(t, outcomes[i])["receipt_id"].(string))
			if ready["outcome"] != "created" || firstPartText(t, ready) != want {
				t.Fatalf("batch entry %d: %v", i, ready)
			}
			s.Records = append(s.Records, ready["record_id"].(string))
		}
	})

	j.step("correction_no_longer_matches", func() {
		first := awaitReady(t, ingestCorrection(t, s.Corpus, "journey-b1-"+s.Run, "journey-b", "Dépêche crue de la Loire à Orléans"))
		recordB := first["record_id"].(string)
		s.Records = append(s.Records, recordB)
		createdB := journeyNotice(t, s.Corpus, s.Start, "match.created", recordB)
		deliveredAsPolled(t, receiver, createdB)
		correction := awaitReady(t, ingestCorrection(t, s.Corpus, "journey-b2-"+s.Run, "journey-b", "Dépêche "+journeyNoMatch+" : la crue de la Loire est terminée"))
		if correction["record_id"] != recordB || correction["version_id"] == first["version_id"] {
			t.Fatal("correction is not a new Version", correction)
		}
		notice := journeyNotice(t, s.Corpus, s.Start, "match.no_longer_matches", recordB)
		if refsOf(notice)["match_id"] != refsOf(createdB)["match_id"] || refsOf(notice)["record_version_id"] != correction["version_id"] {
			t.Fatal("match.no_longer_matches references", notice)
		}
		deliveredAsPolled(t, receiver, notice)
		if n := matchesFor(t, s.Subscription, recordB); n != 1 {
			t.Fatal("a correction that no longer matches must not create a Match", n)
		}
	})

	j.step("withdrawal_notice", func() {
		ready := awaitReady(t, ingest(t, s.Corpus, "journey-c-"+s.Run, "Dépêche éruption volcanique en Islande"))
		recordC := ready["record_id"].(string)
		s.Withdrawn = recordC
		createdC := journeyNotice(t, s.Corpus, s.Start, "match.created", recordC)
		deliveredAsPolled(t, receiver, createdC)
		awaitEnriched(t, admin, s.Corpus, s.Start, recordC)
		if hits := searchHits(t, s.Corpus, "volcanique", "lexical"); len(hits) != 1 || hits[0]["record_id"] != recordC {
			t.Fatal("alerted Record not searchable", hits)
		}
		accepted := request(t, "POST", "/v0/records/withdrawals", admin, withdrawalCommand(s.Corpus, "journey-c-withdraw-"+s.Run, "example-feed", "journey-c-"+s.Run, "source retraction"), 202)
		if resolved := awaitReceipt(t, accepted["receipt_id"].(string)); resolved["outcome"] != "withdrawal_applied" {
			t.Fatal(resolved)
		}
		if hits := searchHits(t, s.Corpus, "volcanique", "lexical"); len(hits) != 0 {
			t.Fatal("withdrawn Record still searchable", hits)
		}
		notice := journeyNotice(t, s.Corpus, s.Start, "match.withdrawn", recordC)
		if refsOf(notice)["match_id"] != refsOf(createdC)["match_id"] {
			t.Fatal("match.withdrawn references", notice)
		}
		deliveredAsPolled(t, receiver, notice)
	})
	saveJourney(t, s)
}

// TestJourneyWorkerStopped runs while scripts/local.py holds the worker
// stopped: the API keeps accepting and serving durable state; new work waits.
func TestJourneyWorkerStopped(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	s := loadJourney(t)
	j := newJourneySteps(t, "worker-stopped")
	j.step("accept_while_worker_stopped", func() {
		command := inlineCommand(s.Corpus, "journey-d-"+s.Run, "journey-d", "Dépêche séisme au large du Japon")
		accepted := request(t, "POST", "/v0/records", admin, command, 202)
		s.ReceiptD = accepted["receipt_id"].(string)
		// The harness has confirmed worker termination before this phase.
		if replay := request(t, "POST", "/v0/records", admin, command, 202); replay["receipt_id"] != s.ReceiptD {
			t.Fatal("replay during the outage diverged", replay)
		}
		pending := request(t, "GET", "/v0/ingestion-receipts/"+s.ReceiptD, admin, nil, 200)
		if pending["state"] != "pending" || pending["outcome"] != nil {
			t.Fatal("accepted work must wait durably for the worker", pending)
		}
	})
	j.step("reads_survive_worker_outage", func() {
		if hits := searchHits(t, s.Corpus, "ferries", "lexical"); len(hits) != 1 || hits[0]["record_id"] != s.RecordA {
			t.Fatal("search must not depend on the worker", hits)
		}
		request(t, "GET", "/v0/records/"+s.RecordA+"/versions/"+s.VersionA, admin, nil, 200)
	})
	saveJourney(t, s)
}

func TestJourneyAfterRestart(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	s := loadJourney(t)
	if s.ReceiptD == "" {
		t.Skip("runs after TestJourneyWorkerStopped (scripts/local.py)")
	}
	j := newJourneySteps(t, "after-restart")
	receiver := startReceiver(t)

	j.step("pending_work_recovers", func() {
		ready := awaitReady(t, s.ReceiptD)
		recordD := ready["record_id"].(string)
		s.Records = append(s.Records, recordD)
		created := journeyNotice(t, s.Corpus, s.Start, "match.created", recordD)
		deliveredAsPolled(t, receiver, created)
		if n := matchesFor(t, s.Subscription, recordD); n != 1 {
			t.Fatal("restart duplicated a Match", n)
		}
		if replay := request(t, "POST", "/v0/records", admin, s.CommandA, 202); replay["receipt_id"] != s.ReceiptA {
			t.Fatal("replay after restart created another Receipt", replay)
		}
	})

	j.step("sse_replays_and_resumes_like_polling", func() {
		polled, _ := drain(t, admin, s.Corpus, s.Start, 0)
		n := min(len(polled), 12)
		if n < 4 {
			t.Fatal("journey feed too short", len(polled))
		}
		base, path := os.Getenv("QUIVR_TEST_URL"), "/v0/changes/stream?corpus_id="+url.QueryEscape(s.Corpus)
		replay := openChangeStream(t, base, admin, path, s.Start)
		for i := 0; i < n; i++ {
			if _, event := replay.nextChange(t); event["event_id"] != polled[i]["event_id"] {
				t.Fatal("SSE replay and polling disagree at", i, event, polled[i])
			}
		}
		replay.close()
		k := n / 2
		resumed := openChangeStream(t, base, admin, path, polled[k-1]["cursor"].(string))
		if _, event := resumed.nextChange(t); event["event_id"] != polled[k]["event_id"] {
			t.Fatal("Last-Event-ID resume lost or invented an event", event, polled[k])
		}
	})

	j.step("cursor_expiry_and_catalog_resync", func() {
		short := os.Getenv("QUIVR_TEST_SHORT_RETENTION_URL")
		if short == "" {
			t.Fatal("make verify starts a short-retention API")
		}
		stale := openChangeStream(t, short, admin, changesPath(s.Corpus, s.Start, 0), "")
		var e map[string]any
		if err := json.NewDecoder(stale.body.Body).Decode(&e); err != nil || stale.body.StatusCode != 410 || e["code"] != "cursor_expired" || e["resync_url"] == nil {
			t.Fatal("expired cursor", stale.body.StatusCode, e, err)
		}
		stale.close()
		c := &resyncClient{t: t, base: os.Getenv("QUIVR_TEST_URL"), token: admin, corpus: s.Corpus, limit: 2}
		if err := c.scan(e["resync_url"].(string)); err != nil {
			t.Fatal(err)
		}
		converge(t, c)
		for _, id := range s.Records {
			if c.view[id] == nil {
				t.Fatal("resynchronized catalog lacks", id)
			}
		}
	})

	j.step("rebuild_preserves_search", func() {
		before := searchHits(t, s.Corpus, "ferries", "lexical")
		if len(before) != 1 {
			t.Fatal("baseline", before)
		}
		op, location := postRebuild(t, admin, s.Corpus, "journey-rebuild-"+s.Run, 202)
		done := awaitOperation(t, location)
		result, _ := done["result"].(map[string]any)
		if done["state"] != "succeeded" || result == nil || done["operation_id"] != op["operation_id"] {
			t.Fatal("rebuild outcome", done)
		}
		generation := result["projection_generation_id"].(string)
		after := searchHits(t, s.Corpus, "ferries", "lexical")
		if len(after) != 1 || after[0]["version_id"] != s.VersionA || after[0]["projection_generation_id"] != generation || generation == before[0]["projection_generation_id"] {
			t.Fatal("rebuilt lexical routing", after, generation)
		}
		if semantic := searchHits(t, s.Corpus, journeyQuery, "semantic"); len(semantic) == 0 || semantic[0]["record_id"] != s.RecordA {
			t.Fatal("rebuilt semantic routing", semantic)
		}
		if hits := searchHits(t, s.Corpus, "volcanique", "lexical"); len(hits) != 0 {
			t.Fatal("rebuild resurrected a withdrawn Record", hits)
		}
	})
}
