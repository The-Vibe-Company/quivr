package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type archiveFixture struct {
	Config       map[string]any    `json:"config"`
	Credential   map[string]string `json:"credential"`
	LatestText   string            `json:"latest_text"`
	MembersTotal int               `json:"members_total"`
}
type archiveState struct{ Corpus, Cursor, Connector, Record, LatestVersion string }

func archiveInput(t *testing.T) (string, archiveFixture) {
	t.Helper()
	token := connectorToken(t)
	path := os.Getenv("QUIVR_TEST_ARCHIVE_SOURCE")
	if path == "" {
		t.Skip("archive_source.py seeds the real SeaweedFS source")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture archiveFixture
	if json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("invalid private archive fixture")
	}
	return token, fixture
}
func archiveStatePath() string {
	return filepath.Join(os.Getenv("QUIVR_TEST_CAPTURES"), "archive-source-state.json")
}
func archiveDone(h map[string]any) float64 {
	if d, ok := h["diagnostics"].(map[string]any); ok {
		n, _ := d["members_done"].(float64)
		return n
	}
	return -1
}

// Owns the real S3 -> upload grants -> checkpoint boundary. The two phases
// straddle a real plugin+worker restart and pause, in an isolated Corpus.
func TestArchiveSourceBeforeRestart(t *testing.T) {
	token, fixture := archiveInput(t)
	corpus, cursor := connectorCorpus(t, token, "archive-source")
	fixture.Config["batch_size"] = 1
	fixture.Config["concurrency"] = 4
	created := request(t, "POST", "/v0/connectors", token, map[string]any{"idempotency_key": "archive-create-" + connectorRun, "corpus_id": corpus, "source_namespace": "synthetic-archive", "kind": "object_storage_archive", "config": fixture.Config, "schedule": map[string]any{"interval_seconds": 3600}, "credential": map[string]any{"secret": fixture.Credential}}, 201)
	id := created["connector_id"].(string)
	ready := awaitHealth(t, token, id, func(h map[string]any) bool { return h["last_success_at"] != nil && archiveDone(h) == 10 })
	// A ten-page run must leave later archive members for the next run.
	if h := ready["health"].(map[string]any); h["last_error"] != nil {
		t.Fatalf("archive acquisition failed: %v", h)
	}
	raw, _ := json.Marshal(ready)
	for _, secret := range fixture.Credential {
		if strings.Contains(string(raw), secret) {
			t.Fatal("deposited storage credential appeared in connector response")
		}
	}
	// A page-limited run continues at once, so the restart may interrupt the
	// next run mid-attachment; its retry must resume (THE-1314). Disable is
	// terminal in the connector API and cannot represent a pause.
	keys := map[string]int{"ITEM7": 1}
	for i := 0; i < 9; i++ {
		keys[fmt.Sprintf("FILLER%d", i)] = 1
	}
	records, _ := recordsByKey(t, token, corpus, cursor, keys)
	awaitChange(t, token, corpus, cursor, "record.retrieval_ready", records["ITEM7"])
	r := request(t, "GET", "/v0/records/"+records["ITEM7"], token, nil, 200)
	state := archiveState{corpus, cursor, id, records["ITEM7"], r["current_version_id"].(string)}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(archiveStatePath(), data, 0600); err != nil {
		t.Fatal(err)
	}
}

// The older source revision deliberately appears in a later archive folder.
// Currentness and exact raw-source Blob bytes are public API oracles, independent
// of the plugin's mapping tests. Idempotent replay must not create extra versions.
func TestArchiveSourceAfterRestart(t *testing.T) {
	token, fixture := archiveInput(t)
	raw, err := os.ReadFile(archiveStatePath())
	if err != nil {
		t.Fatal(err)
	}
	var state archiveState
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	request(t, "POST", "/v0/connectors/"+state.Connector+"/runs", token, map[string]any{"idempotency_key": "archive-resume-" + connectorRun}, 202)
	// The last member page can commit before a subsequent fetch observes EOF.
	// Wait for both public progress conditions rather than racing that fetch.
	done := awaitHealth(t, token, state.Connector, func(h map[string]any) bool {
		d, _ := h["diagnostics"].(map[string]any)
		return archiveDone(h) == float64(fixture.MembersTotal) && d["archive_complete"] == true
	})
	d := done["health"].(map[string]any)["diagnostics"].(map[string]any)
	if d["members_left"] != float64(0) || d["archive_complete"] != true || d["progress_percent"] != float64(100) {
		t.Fatalf("archive progress did not complete: %v", d)
	}
	request(t, "POST", "/v0/connectors/"+state.Connector+"/disable", token, map[string]any{"idempotency_key": "archive-finish-" + connectorRun}, 200)
	expected := map[string]int{"ITEM7": 2, "ITEM9": 1}
	for i := 0; i < 10; i++ {
		expected[fmt.Sprintf("FILLER%d", i)] = 1
	}
	records, events := recordsByKey(t, token, state.Corpus, state.Cursor, expected)
	if records["ITEM7"] != state.Record || typed(events, "record.materialized", state.Record) != 2 {
		t.Fatalf("revisions did not reconcile to exactly one Record and two Versions: %v", records)
	}
	current := request(t, "GET", "/v0/records/"+state.Record, token, nil, 200)
	if current["current_version_id"] != state.LatestVersion {
		t.Fatalf("later archive member with older position replaced revision twelve: %v", current)
	}
	version := request(t, "GET", "/v0/records/"+state.Record+"/versions/"+state.LatestVersion, token, nil, 200)
	blobs := version["provenance"].(map[string]any)["source_blob_ids"].([]any)
	if len(blobs) != 1 {
		t.Fatalf("raw source Blob missing: %v", version)
	}
	blob := request(t, "GET", "/v0/blobs/"+blobs[0].(string), token, nil, 200)
	hash := sha256.Sum256([]byte(fixture.LatestText))
	if blob["sha256"] != hex.EncodeToString(hash[:]) || blob["size_bytes"] != float64(len(fixture.LatestText)) {
		t.Fatalf("raw member bytes changed: %v", blob)
	}
}
