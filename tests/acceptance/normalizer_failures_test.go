package acceptance

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These scenarios observe external normalization failures through the public
// API only. scripts/normalizer_plugin.py drives the plugin processes around
// them: it stops the pinned plugin for the outage scenario, and for the
// failure scenario it pins a controllable test plugin that answers with the
// failure its Record Key names (text/x-fault is a required route,
// text/x-fault-optional an optional one).

const outageMarkdown = "# Lantern log\n\nThe relief keeper arrived by the evening ferry.\n"

type outageState struct{ Corpus, Receipt string }

func outageStatePath() string {
	return filepath.Join(os.Getenv("QUIVR_TEST_CAPTURES"), "normalizer-outage.json")
}

func probeStatus(t *testing.T, url string) int {
	t.Helper()
	res, err := (&http.Client{Timeout: 5 * time.Second}).Get(url)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

// TestNormalizerOutageKeepsThePlatformHealthy runs while the pinned plugin is
// stopped: a routed Blob reports the outage while the API, text ingestion and
// search keep working. Temporal's workflow test owns the retry lifetime.
func TestNormalizerOutageKeepsThePlatformHealthy(t *testing.T) {
	if os.Getenv("QUIVR_TEST_NORMALIZER_OUTAGE") == "" {
		t.Skip("scripts/normalizer_plugin.py runs it with the plugin stopped")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := time.Now().UTC().Format("20060102T150405.000000")
	corpusID := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Normalizer outage", "idempotency_key": "normalizer-outage-" + run}, 201)["corpus_id"].(string)
	blobID := uploadBlob(t, admin, []byte(outageMarkdown), "text/markdown")
	routed := request(t, "POST", "/v0/records", admin, map[string]any{
		"idempotency_key": "normalizer-outage-routed-" + run,
		"source":          map[string]any{"corpus_id": corpusID, "namespace": "logs", "record_key": "lantern"},
		"content":         map[string]any{"kind": "blob", "blob_id": blobID, "media_type": "text/markdown"},
	}, 202)["receipt_id"].(string)

	// Text ingestion and search keep working meanwhile.
	text := request(t, "POST", "/v0/records", admin, map[string]any{
		"idempotency_key": "normalizer-outage-text-" + run,
		"source":          map[string]any{"corpus_id": corpusID, "namespace": "logs", "record_key": "tides"},
		"content":         map[string]any{"kind": "text", "text": "Spring tides flood the causeway at noon."},
	}, 202)["receipt_id"].(string)
	awaitRetrievalReady(t, text)
	hits := request(t, "POST", "/v0/search", admin, map[string]any{"query": "causeway", "corpus_ids": []string{corpusID}, "mode": "lexical"}, 200)["items"].([]any)
	if len(hits) != 1 {
		t.Fatalf("search during the outage: %v", hits)
	}

	// Wait for the public outage diagnostic, then let the harness restart the
	// plugin immediately rather than holding it down for a retry interval.
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	for {
		r := request(t, "GET", "/v0/ingestion-receipts/"+routed, admin, nil, 200)
		if r["state"] != "pending" {
			t.Fatalf("an unavailable plugin resolved the receipt: %v", r)
		}
		diagnostics, _ := r["diagnostics"].([]any)
		if len(diagnostics) == 1 && diagnostics[0].(map[string]any)["code"] == "plugin_unavailable" && diagnostics[0].(map[string]any)["retryable"] == true {
			break
		}
		select {
		case <-deadline.C:
			t.Fatalf("receipt never reported the plugin outage: %v", r)
		case <-poll.C:
		}
	}
	for _, probe := range []string{"QUIVR_TEST_API_PROBE_URL", "QUIVR_TEST_WORKER_PROBE_URL"} {
		if status := probeStatus(t, os.Getenv(probe)+"/healthz"); status != 204 {
			t.Fatalf("%s /healthz answered %d during the plugin outage", probe, status)
		}
	}
	state, _ := json.Marshal(outageState{Corpus: corpusID, Receipt: routed})
	if err := os.WriteFile(outageStatePath(), state, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestNormalizerOutageRecovers runs once the plugin is back: the pending
// routed Version completes with the plugin's output.
func TestNormalizerOutageRecovers(t *testing.T) {
	raw, err := os.ReadFile(outageStatePath())
	if err != nil || os.Getenv("QUIVR_TEST_NORMALIZER_OUTAGE") == "" {
		t.Skip("runs after TestNormalizerOutageKeepsThePlatformHealthy and a plugin restart")
	}
	var state outageState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	ready := awaitRetrievalReady(t, state.Receipt)
	if ready["outcome"] != "created" {
		t.Fatal(ready)
	}
	version := request(t, "GET", "/v0/records/"+ready["record_id"].(string)+"/versions/"+ready["version_id"].(string), admin, nil, 200)
	n, _ := version["provenance"].(map[string]any)["normalization"].(map[string]any)
	key, _ := n["idempotency_key"].(string)
	if !strings.HasPrefix(key, "nk_") || n["fallback"] != nil {
		t.Fatalf("provenance %v", version["provenance"])
	}
	hits := request(t, "POST", "/v0/search", admin, map[string]any{"query": "ferry", "corpus_ids": []string{state.Corpus}, "mode": "lexical"}, 200)["items"].([]any)
	if len(hits) != 1 || hits[0].(map[string]any)["version_id"] != ready["version_id"] {
		t.Fatalf("search after recovery: %v", hits)
	}
}

// TestNormalizerFailures ingests one routed Blob per failure class of the
// controllable test plugin and observes each outcome publicly.
func TestNormalizerFailures(t *testing.T) {
	if os.Getenv("QUIVR_TEST_FAULTY_NORMALIZER") == "" {
		t.Skip("scripts/normalizer_plugin.py pins the controllable test plugin for it")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := time.Now().UTC().Format("20060102T150405.000000")
	corpusID := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Normalizer failures", "idempotency_key": "normalizer-failures-" + run}, 201)["corpus_id"].(string)
	_, cursor := drain(t, admin, corpusID, "", 0)
	ingest := func(recordKey, mediaType string) string {
		data := []byte(recordKey + "\n\nThe cormorant dries its wings on the breakwater.\n")
		blobID := uploadBlob(t, admin, data, mediaType)
		return request(t, "POST", "/v0/records", admin, map[string]any{
			"idempotency_key": "normalizer-failure-" + recordKey + "-" + run,
			"source":          map[string]any{"corpus_id": corpusID, "namespace": "faults", "record_key": recordKey},
			"content":         map[string]any{"kind": "blob", "blob_id": blobID, "media_type": mediaType},
		}, 202)["receipt_id"].(string)
	}
	expected := map[string]string{
		"terminal":             "normalizer_failed",
		"malformed-part":       "normalizer_invalid_output",
		"bad-checksum":         "normalizer_invalid_output",
		"undeclared-namespace": "normalizer_invalid_output",
		"large":                "normalizer_invalid_output",
		"slow":                 "normalizer_timeout",
		"retry":                "normalizer_retries_exhausted",
	}
	receipts := map[string]string{}
	for mode := range expected {
		receipts[mode] = ingest(mode+".1", "text/x-fault")
	}
	optional := ingest("terminal.optional", "text/x-fault-optional")

	var quarantined []string
	for mode, code := range expected {
		r := awaitReceipt(t, receipts[mode])
		availability, _ := r["availability"].(map[string]any)
		diagnostics, _ := r["diagnostics"].([]any)
		if r["outcome"] != "created" || availability["state"] != "quarantined" || availability["searchable"] != false || len(diagnostics) != 1 || diagnostics[0].(map[string]any)["code"] != code {
			t.Fatalf("%s: receipt %v", mode, r)
		}
		v := request(t, "GET", "/v0/records/"+r["record_id"].(string)+"/versions/"+r["version_id"].(string), admin, nil, 200)
		ds, _ := v["diagnostics"].([]any)
		if len(ds) != 1 {
			t.Fatalf("%s: version diagnostics %v", mode, v)
		}
		d := ds[0].(map[string]any)
		if d["code"] != code || d["message"] == "" || d["plugin"] == nil || d["contribution"] != "normalizer" || !strings.HasPrefix(d["invocation_id"].(string), "inv_") {
			t.Fatalf("%s: diagnostic %v", mode, d)
		}
		// Nothing from the plugin was published: only the submitted input Blob Part.
		parts := v["manifest"].(map[string]any)["parts"].([]any)
		if len(parts) != 1 || parts[0].(map[string]any)["content"].(map[string]any)["kind"] != "blob" || v["provenance"].(map[string]any)["normalization"] != nil {
			t.Fatalf("%s: published %v", mode, v)
		}
		quarantined = append(quarantined, r["record_id"].(string))
	}
	// Every quarantine is announced on the change feed.
	events, _ := drain(t, admin, corpusID, cursor, 0)
	for _, recordID := range quarantined {
		if typed(events, "record.quarantined", recordID) != 1 {
			t.Fatalf("record.quarantined for %s: %v", recordID, events)
		}
	}

	// The optional route falls back to the built-in text path and stays searchable.
	ready := awaitRetrievalReady(t, optional)
	v := request(t, "GET", "/v0/records/"+ready["record_id"].(string)+"/versions/"+ready["version_id"].(string), admin, nil, 200)
	n, _ := v["provenance"].(map[string]any)["normalization"].(map[string]any)
	fallback, _ := n["fallback"].(map[string]any)
	ds, _ := v["diagnostics"].([]any)
	if fallback["code"] != "normalizer_failed" || len(ds) != 1 || ds[0].(map[string]any)["invocation_id"] != n["invocation_id"] {
		t.Fatalf("fallback version %v", v)
	}
	hits := request(t, "POST", "/v0/search", admin, map[string]any{"query": "cormorant", "corpus_ids": []string{corpusID}, "mode": "lexical"}, 200)["items"].([]any)
	if len(hits) != 1 || hits[0].(map[string]any)["version_id"] != ready["version_id"] {
		t.Fatalf("only the fallback Version is searchable: %v", hits)
	}
}
