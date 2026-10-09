package acceptance

import (
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// These scenarios reprocess Versions stuck in quarantine through the operator
// API. scripts/normalizer_plugin.py runs the normalization one beside the
// controllable test normalizer, and scripts/ingestion_plugin.py the ingestion
// one after its rollback scenario stopped a Record.

// quarantined lists the stuck Versions of a Corpus with the reason code.
func quarantined(t *testing.T, token, corpusID, code string) []map[string]any {
	t.Helper()
	q := url.Values{"corpus_id": {corpusID}, "code": {code}}
	var out []map[string]any
	for _, item := range request(t, "GET", "/v0/admin/quarantine?"+q.Encode(), token, nil, 200)["items"].([]any) {
		out = append(out, item.(map[string]any))
	}
	return out
}

// reprocess checks that a dry run is required, dry-runs the reprocess of a
// Corpus's Versions quarantined with code, expecting versions, then starts it
// and waits for its end.
func reprocess(t *testing.T, token, key, corpusID, code string, versions int, fromStage ...string) (map[string]any, map[string]any) {
	t.Helper()
	body := map[string]any{"idempotency_key": key, "corpus_id": corpusID, "code": code, "dry_run": false}
	if len(fromStage) > 0 {
		body["from_stage"] = fromStage[0]
	}
	if refused := request(t, "POST", "/v0/admin/quarantine/reprocess", token, body, 409); refused["code"] != "dry_run_required" {
		t.Fatalf("a reprocess without a dry run: %v", refused)
	}
	body["dry_run"] = true
	if estimate := request(t, "POST", "/v0/admin/quarantine/reprocess", token, body, 200); estimate["versions"] != float64(versions) || estimate["codes"].(map[string]any)[code] != float64(versions) {
		t.Fatalf("dry run %v, want %d Versions quarantined with %s", estimate, versions, code)
	}
	body["dry_run"] = false
	op := request(t, "POST", "/v0/admin/quarantine/reprocess", token, body, 202)
	location := "/v0/operations/" + op["operation_id"].(string)
	return body, awaitBackfill(t, token, location, func(op map[string]any) bool {
		return op["state"] == "succeeded" || op["state"] == "failed" || op["state"] == "canceled"
	})
}

// quarantineFixtureAlert subscribes the fixture evaluator, which matches every
// Version of the Corpus, from now on.
func quarantineFixtureAlert(t *testing.T, key, corpusID string) string {
	t.Helper()
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	query := request(t, "POST", "/v0/saved-queries", admin, savedQueryCommand("quarantine-query-"+key, corpusID), 201)
	sub := request(t, "POST", "/v0/subscriptions", admin, subscriptionCommand("quarantine-subscription-"+key, query, destinationCapture), 201)
	return sub["subscription_id"].(string)
}

// TestQuarantineReprocessNormalization quarantines a routed Blob whose
// normalizer fails, under an alert on its Corpus. Reprocessed while the
// normalizer still fails, the Version stays quarantined with the new
// invocation and nothing alerts. Once a fixed version of the normalizer is
// registered and activated, a second reprocess publishes the Version with its
// normalized Manifest: it becomes current and searchable, the change feed
// announces it and the alert matches it once. The plan is rolled back after.
func TestQuarantineReprocessNormalization(t *testing.T) {
	endpoint, operator, backfiller := os.Getenv("QUIVR_TEST_FIXED_NORMALIZER_ENDPOINT"), os.Getenv("QUIVR_TEST_OPERATOR"), os.Getenv("QUIVR_TEST_BACKFILLER")
	if endpoint == "" || operator == "" || backfiller == "" {
		t.Skip("scripts/normalizer_plugin.py runs a fixed version of the test normalizer beside the faulty one")
	}
	fixed, err := os.ReadFile(os.Getenv("QUIVR_TEST_FIXED_NORMALIZER_MANIFEST"))
	if err != nil {
		t.Fatal(err)
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := time.Now().UTC().Format("20060102T150405.000000")
	corpusID := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Quarantine reprocess", "idempotency_key": "quarantine-normalization-" + run}, 201)["corpus_id"].(string)
	subscription := quarantineFixtureAlert(t, "normalization-"+run, corpusID)
	start := request(t, "GET", changesPath(corpusID, "", 0), admin, nil, 200)["next_cursor"].(string)
	blobID := uploadBlob(t, admin, []byte("terminal\n\nThe pilot boat left before dawn.\n"), "text/x-fault")
	receipt := awaitReceipt(t, request(t, "POST", "/v0/records", admin, map[string]any{
		"idempotency_key": "quarantine-normalization-record-" + run,
		"source":          map[string]any{"corpus_id": corpusID, "namespace": "faults", "record_key": "terminal.reprocess"},
		"content":         map[string]any{"kind": "blob", "blob_id": blobID, "media_type": "text/x-fault"},
	}, 202)["receipt_id"].(string))
	version := "/v0/records/" + receipt["record_id"].(string) + "/versions/" + receipt["version_id"].(string)
	first := request(t, "GET", version, admin, nil, 200)["diagnostics"].([]any)[0].(map[string]any)

	items := quarantined(t, backfiller, corpusID, "normalizer_failed")
	if len(items) != 1 || items[0]["version_id"] != receipt["version_id"] || items[0]["stage"] != "normalization" || items[0]["reason"].(map[string]any)["plugin"] != "quivr-test.faulty" {
		t.Fatalf("stuck Versions %v, want the Blob quarantined at normalization", items)
	}

	// The normalizer still fails: the Version stays quarantined with the new invocation.
	_, done := reprocess(t, backfiller, "quarantine-again-"+run, corpusID, "normalizer_failed", 1)
	again := request(t, "GET", version, admin, nil, 200)
	d := again["diagnostics"].([]any)[0].(map[string]any)
	if done["state"] != "succeeded" || counter(done, "versions_quarantined") != 1 || again["availability"].(map[string]any)["state"] != "quarantined" || d["code"] != "normalizer_failed" || d["invocation_id"] == first["invocation_id"] {
		t.Fatalf("reprocessed while the normalizer fails: %v, version diagnostics %v", done, again["diagnostics"])
	}

	// The fixed normalizer is registered and activated; the second reprocess recovers the Version.
	registration := request(t, "POST", "/v0/admin/plugins", operator, map[string]any{"idempotency_key": "fixed-normalizer-" + run, "endpoint": endpoint, "manifest": string(fixed),
		"routes": []map[string]any{{"media_type": "text/x-fault", "mode": "required"}, {"media_type": "text/x-fault-optional", "mode": "optional"}}}, 202)
	if checked := awaitCheck(t, operator, "/v0/admin/plugins/"+registration["registration_id"].(string)); checked["state"] != "validated" {
		t.Fatalf("the fixed normalizer: %v", checked)
	}
	routingRequest(t, "POST", "/v0/admin/plugins/"+registration["registration_id"].(string)+"/activate", operator, map[string]any{}, 200)
	// The harness restores its own pins afterwards: leave the plan they seeded.
	t.Cleanup(func() {
		routingRequest(t, "POST", "/v0/admin/plugins/plan/rollback", operator, map[string]any{"idempotency_key": "fixed-normalizer-back-" + run}, 200)
	})
	body, done := reprocess(t, backfiller, "quarantine-fixed-"+run, corpusID, "normalizer_failed", 1)
	if done["state"] != "succeeded" || counter(done, "versions_recovered") != 1 {
		t.Fatalf("reprocessed after the fix: %v", done)
	}
	if replay := request(t, "POST", "/v0/admin/quarantine/reprocess", backfiller, body, 202); replay["operation_id"] != done["operation_id"] {
		t.Fatalf("the same key again: %v, want %v", replay["operation_id"], done["operation_id"])
	}
	recovered := request(t, "GET", version, admin, nil, 200)
	availability := recovered["availability"].(map[string]any)
	diagnostics, _ := recovered["diagnostics"].([]any)
	if availability["searchable"] != true || availability["is_current"] != true || recovered["provenance"].(map[string]any)["normalization"] == nil || len(diagnostics) != 0 {
		t.Fatalf("the recovered Version %v", recovered)
	}
	if parts := recovered["manifest"].(map[string]any)["parts"].([]any); len(parts) != 1 || parts[0].(map[string]any)["content"].(map[string]any)["kind"] != "text" {
		t.Fatalf("the recovered Version publishes %v, want the normalized text", parts)
	}
	seen, created := awaitMatches(t, admin, corpusID, start, 1)
	recordID := receipt["record_id"].(string)
	if typed(seen, "record.materialized", recordID) != 2 || typed(seen, "record.retrieval_ready", recordID) != 1 || typed(seen, "record.quarantined", recordID) != 1 {
		t.Fatalf("change feed %v, want the first publication, its republication and one retrieval_ready", seen)
	}
	if matches := matchCreatedFor(created, subscription); len(matches) != 1 || matches[0]["monitoring"].(map[string]any)["record_id"] != recordID {
		t.Fatalf("matches %v, want one for the recovered Version", created)
	}
}

// TestQuarantineReprocessIngestion runs after TestRollback stopped a Record
// pinned to the bad release: its Version is listed with the plan and plugin
// version that stopped it, then reprocessed through the plan now active. It
// becomes current and searchable by meaning, and its alert matches.
func TestQuarantineReprocessIngestion(t *testing.T) {
	_, _, _, corpusID, run := rollbackSetup(t)
	backfiller := os.Getenv("QUIVR_TEST_BACKFILLER")
	if backfiller == "" {
		t.Skip("make verify gives the scenario an operator key of the Corpus's Organization")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	stopped := awaitReceipt(t, request(t, "POST", "/v0/records", admin, inlineCommand(corpusID, "stopped-"+run, "stopped", "The ferry waited for the tide to turn."), 202)["receipt_id"].(string))
	items := quarantined(t, backfiller, corpusID, "pinned_plan_stopped")
	if len(items) != 1 || items[0]["version_id"] != stopped["version_id"] || items[0]["stage"] != "ingestion" || items[0]["reason"].(map[string]any)["plugin_version"] != "0.2.0" {
		t.Fatalf("stuck Versions %v, want the Record the rollback stopped", items)
	}
	subscription := quarantineFixtureAlert(t, "ingestion-"+run, corpusID)
	start := request(t, "GET", changesPath(corpusID, "", 0), admin, nil, 200)["next_cursor"].(string)

	_, done := reprocess(t, backfiller, "quarantine-ingestion-"+run, corpusID, "pinned_plan_stopped", 1)
	if done["state"] != "succeeded" || counter(done, "versions_recovered") != 1 {
		t.Fatalf("reprocess %v", done)
	}
	recordID := stopped["record_id"].(string)
	awaitEnriched(t, admin, corpusID, start, recordID)
	version := request(t, "GET", "/v0/records/"+recordID+"/versions/"+stopped["version_id"].(string), admin, nil, 200)
	if a := version["availability"].(map[string]any); a["searchable"] != true || a["is_current"] != true {
		t.Fatalf("the reprocessed Version %v", version)
	}
	if hits := semanticHits(t, corpusID, "ferry waited for the tide"); len(hits) == 0 || hits[0]["version_id"] != stopped["version_id"] {
		t.Fatalf("search by meaning %v, want the reprocessed Version first", hits)
	}
	if _, created := awaitMatches(t, admin, corpusID, start, 1); len(matchCreatedFor(created, subscription)) != 1 {
		t.Fatalf("matches %v, want one for the reprocessed Version", created)
	}
	if left := quarantined(t, backfiller, corpusID, "pinned_plan_stopped"); len(left) != 0 {
		t.Fatalf("still stuck %v", left)
	}
}

// TestQuarantineRenormalizesIngestion recovers an ingestion refusal caused by
// successful but unusable normalization, without replacing a newer revision.
func TestQuarantineRenormalizesIngestion(t *testing.T) {
	endpoint, operator, backfiller := os.Getenv("QUIVR_TEST_FIXED_NORMALIZER_ENDPOINT"), os.Getenv("QUIVR_TEST_OPERATOR"), os.Getenv("QUIVR_TEST_BACKFILLER")
	if endpoint == "" || operator == "" || backfiller == "" {
		t.Skip("scripts/normalizer_plugin.py supplies the faulty and fixed normalizers")
	}
	fixed, err := os.ReadFile(os.Getenv("QUIVR_TEST_FIXED_NORMALIZER_MANIFEST"))
	if err != nil {
		t.Fatal(err)
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := monitoringRun()
	corpusID := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Normalization restart", "idempotency_key": "restart-" + run}, 201)["corpus_id"].(string)
	blobID := uploadBlob(t, admin, []byte("The ferry crossed the harbour.\n"), "text/x-fault")
	accept := func(key string) map[string]any {
		receipt := awaitReceipt(t, request(t, "POST", "/v0/records", admin, map[string]any{
			"idempotency_key": key + run,
			"source":          map[string]any{"corpus_id": corpusID, "namespace": "faults", "record_key": key},
			"content":         map[string]any{"kind": "blob", "blob_id": blobID, "media_type": "text/x-fault"},
		}, 202)["receipt_id"].(string))
		path := "/v0/records/" + receipt["record_id"].(string) + "/versions/" + receipt["version_id"].(string)
		deadline, poll := time.NewTimer(10*time.Second), time.NewTicker(20*time.Millisecond)
		defer deadline.Stop()
		defer poll.Stop()
		for {
			v := request(t, "GET", path, admin, nil, 200)
			if v["availability"].(map[string]any)["state"] == "quarantined" {
				d := v["diagnostics"].([]any)[0].(map[string]any)
				if d["code"] != "ingestion_refused" || !strings.Contains(d["message"].(string), "segmentation_limit") {
					t.Fatalf("quarantine reason %v, want the ingestion recipe's two-title refusal", d)
				}
				return receipt
			}
			select {
			case <-deadline.C:
				t.Fatalf("Version never quarantined: %v", v)
			case <-poll.C:
			}
		}
	}
	stuck, older := accept("two-titles.recover"), accept("two-titles.replaced")
	versionPath := "/v0/records/" + stuck["record_id"].(string) + "/versions/" + stuck["version_id"].(string)
	before := request(t, "GET", versionPath, admin, nil, 200)
	if items := quarantined(t, backfiller, corpusID, "ingestion_refused"); len(items) != 2 || items[0]["stage"] != "ingestion" || items[1]["stage"] != "ingestion" {
		t.Fatalf("ingestion quarantines %v, want both schema-valid two-title Manifests", items)
	}
	oldNormalization := before["provenance"].(map[string]any)["normalization"].(map[string]any)
	if oldNormalization["plugin_version"] != "0.1.0" || len(before["manifest"].(map[string]any)["parts"].([]any)) != 3 {
		t.Fatalf("original normalization %v", before)
	}

	registration := request(t, "POST", "/v0/admin/plugins", operator, map[string]any{
		"idempotency_key": "restart-fixed-" + run, "endpoint": endpoint, "manifest": string(fixed),
		"routes": []map[string]any{{"media_type": "text/x-fault", "mode": "required"}, {"media_type": "text/x-fault-optional", "mode": "optional"}},
	}, 202)
	if checked := awaitCheck(t, operator, "/v0/admin/plugins/"+registration["registration_id"].(string)); (checked["state"] != "validated" && checked["state"] != "inactive") || checked["check"].(map[string]any)["certified"] != true {
		t.Fatalf("fixed normalizer validation %v", checked)
	}
	routingRequest(t, "POST", "/v0/admin/plugins/"+registration["registration_id"].(string)+"/activate", operator, map[string]any{}, 200)
	t.Cleanup(func() {
		routingRequest(t, "POST", "/v0/admin/plugins/plan/rollback", operator, map[string]any{"idempotency_key": "restart-rollback-" + run}, 200)
	})

	// The default still segments the old output after the fixed plugin is active.
	_, done := reprocess(t, backfiller, "restart-default-"+run, corpusID, "ingestion_refused", 2)
	if counter(done, "versions_quarantined") != 2 || counter(done, "versions_recovered") != 0 {
		t.Fatalf("default retry %v, want the unchanged stale Manifests refused again", done)
	}
	// A newer accepted revision is excluded from recovery of the older Version.
	replacement := awaitReady(t, request(t, "POST", "/v0/records", admin, map[string]any{
		"idempotency_key": "replacement-" + run,
		"source":          map[string]any{"corpus_id": corpusID, "namespace": "faults", "record_key": "two-titles.replaced"},
		"content":         map[string]any{"kind": "text", "text": "The replacement ferry timetable."},
	}, 202)["receipt_id"].(string))
	body, done := reprocess(t, backfiller, "restart-normalization-"+run, corpusID, "ingestion_refused", 1, "normalization")
	if done["state"] != "succeeded" || counter(done, "versions_recovered") != 1 || done["quarantine_reprocess"].(map[string]any)["from_stage"] != "normalization" {
		t.Fatalf("normalization restart %v", done)
	}
	if replay := request(t, "POST", "/v0/admin/quarantine/reprocess", backfiller, body, 202); replay["operation_id"] != done["operation_id"] {
		t.Fatalf("restart replay %v, want %v", replay, done)
	}
	recovered := request(t, "GET", versionPath, admin, nil, 200)
	normalized := recovered["provenance"].(map[string]any)["normalization"].(map[string]any)
	availability := recovered["availability"].(map[string]any)
	if availability["is_current"] != true || availability["searchable"] != true || normalized["plugin_version"] != "0.2.0" || normalized["input_sha256"] != oldNormalization["input_sha256"] || len(recovered["manifest"].(map[string]any)["parts"].([]any)) != 1 {
		t.Fatalf("recovered same source Version %v", recovered)
	}
	if hits := lexicalHits(t, []string{corpusID}, "echo"); len(hits) != 1 || hits[stuck["version_id"].(string)] == nil {
		t.Fatalf("recovered search results %v", hits)
	}
	old := request(t, "GET", "/v0/records/"+older["record_id"].(string)+"/versions/"+older["version_id"].(string), admin, nil, 200)
	if a := old["availability"].(map[string]any); a["is_current"] != false || a["searchable"] != false {
		t.Fatalf("superseded Version became current or searchable: %v", old)
	}
	current := request(t, "GET", "/v0/records/"+replacement["record_id"].(string)+"/versions/"+replacement["version_id"].(string), admin, nil, 200)
	if current["availability"].(map[string]any)["is_current"] != true {
		t.Fatalf("replacement lost currentness: %v", current)
	}
}
