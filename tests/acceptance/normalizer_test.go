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
	"time"
)

// The local harness pins the `quivr plugin init` template normalizer for
// text/markdown (scripts/normalizer_plugin.py). These scenarios observe it
// through the public API only.

const normalizerMarkdown = "# Harbour guide\n\nTides turn twice a day.\n\n## Night watch\n\nThe keeper trims the phosphorescent wick at dusk.\n"

type normalizerState struct {
	Corpus, Record, Version, Invocation, PartKey string
}

func normalizerStatePath() string {
	return filepath.Join(os.Getenv("QUIVR_TEST_CAPTURES"), "normalizer-state.json")
}

// uploadBlob uploads bytes of a media type through an Upload Session and
// returns the verified Blob ID.
func uploadBlob(t *testing.T, token string, data []byte, mediaType string) string {
	t.Helper()
	sum := sha256.Sum256(data)
	created := request(t, "POST", "/v0/uploads", token, map[string]any{"size_bytes": len(data), "sha256": hex.EncodeToString(sum[:]), "media_type": mediaType}, 201)
	if status := rawTransfer(t, created["upload_url"].(string), uploadHeaders(created), data); status/100 != 2 {
		t.Fatalf("transfer refused: %d", status)
	}
	confirmed := request(t, "POST", "/v0/uploads/"+created["upload_id"].(string)+"/confirm", token, nil, 202)
	if confirmed["state"] != "verified" {
		t.Fatal(confirmed)
	}
	return confirmed["blob_id"].(string)
}

func phosphorescentHit(t *testing.T, corpusID string) map[string]any {
	t.Helper()
	result := request(t, "POST", "/v0/search", os.Getenv("QUIVR_TEST_ADMIN"), map[string]any{"query": "phosphorescent", "corpus_ids": []string{corpusID}, "mode": "lexical"}, 200)
	items := result["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("search hits %v", result)
	}
	return items[0].(map[string]any)
}

// TestNormalizerMakesRoutedBlobsSearchable uploads a Markdown Blob, ingests it
// by reference and finds text the pinned normalizer split into its own Part.
func TestNormalizerMakesRoutedBlobsSearchable(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := time.Now().UTC().Format("20060102T150405.000000")
	corpusID := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Normalizer", "idempotency_key": "normalizer-" + run}, 201)["corpus_id"].(string)
	data := []byte(normalizerMarkdown)
	sum := sha256.Sum256(data)
	blobID := uploadBlob(t, admin, data, "text/markdown")

	command := map[string]any{
		"idempotency_key": "normalizer-markdown-" + run,
		"source":          map[string]any{"corpus_id": corpusID, "namespace": "guides", "record_key": "harbour"},
		"content":         map[string]any{"kind": "blob", "blob_id": blobID, "media_type": "text/markdown"},
		"provenance":      map[string]any{"producer": "acceptance-client"},
	}
	accepted := request(t, "POST", "/v0/records", admin, command, 202)
	ready := awaitRetrievalReady(t, accepted["receipt_id"].(string))
	if ready["outcome"] != "created" {
		t.Fatal(ready)
	}

	hit := phosphorescentHit(t, corpusID)
	partKey, _ := hit["part_key"].(string)
	if hit["version_id"] != ready["version_id"] || !strings.HasPrefix(partKey, "section-") {
		t.Fatalf("hit %v does not come from a plugin-produced Part", hit)
	}

	version := request(t, "GET", "/v0/records/"+ready["record_id"].(string)+"/versions/"+ready["version_id"].(string), admin, nil, 200)
	roles := map[string]bool{}
	for _, raw := range version["manifest"].(map[string]any)["parts"].([]any) {
		part := raw.(map[string]any)
		roles[part["role"].(string)] = true
		if part["key"] == partKey && !strings.Contains(part["content"].(map[string]any)["text"].(string), "phosphorescent") {
			t.Fatalf("Part %s does not hold the matched text: %v", partKey, part)
		}
	}
	if !roles["title"] || !roles["body"] {
		t.Fatalf("the published Manifest is not the normalizer's: %v", version["manifest"])
	}
	provenance := version["provenance"].(map[string]any)
	normalization, _ := provenance["normalization"].(map[string]any)
	invocation, _ := normalization["invocation_id"].(string)
	if invocation == "" || normalization["input_sha256"] != hex.EncodeToString(sum[:]) || normalization["contribution"] != "normalizer" || normalization["plugin_id"] == nil || normalization["idempotency_key"] == nil || normalization["plugin_api"] == nil {
		t.Fatalf("normalization provenance %v", provenance)
	}
	// The acquirer and the input Blob keep their meaning.
	if provenance["producer"] != "acceptance-client" || provenance["source_blob_ids"].([]any)[0] != blobID {
		t.Fatalf("provenance %v", provenance)
	}

	// The same input replays to the same Receipt and Version.
	if replay := request(t, "POST", "/v0/records", admin, command, 202); replay["receipt_id"] != accepted["receipt_id"] {
		t.Fatal("replay created another Receipt", replay)
	}

	// Normalization provenance is engine-owned.
	forged := map[string]any{
		"idempotency_key": "normalizer-forged-" + run,
		"source":          map[string]any{"corpus_id": corpusID, "namespace": "guides", "record_key": "forged"},
		"content":         map[string]any{"kind": "text", "text": "forged"},
		"provenance":      map[string]any{"normalization": normalization},
	}
	if rejected := request(t, "POST", "/v0/records", admin, forged, 422); rejected["code"] != "invalid_schema" {
		t.Fatal(rejected)
	}

	// An unrouted non-text Blob is still rejected at acceptance.
	octets := uploadBlob(t, admin, []byte("\x00\x01binary"), "application/octet-stream")
	unrouted := map[string]any{
		"idempotency_key": "normalizer-unrouted-" + run,
		"source":          map[string]any{"corpus_id": corpusID, "namespace": "guides", "record_key": "binary"},
		"content":         map[string]any{"kind": "blob", "blob_id": octets, "media_type": "application/octet-stream"},
	}
	if rejected := request(t, "POST", "/v0/records", admin, unrouted, 422); rejected["code"] != "unverified_blob" {
		t.Fatal(rejected)
	}

	state, _ := json.Marshal(normalizerState{Corpus: corpusID, Record: ready["record_id"].(string), Version: ready["version_id"].(string), Invocation: invocation, PartKey: partKey})
	if err := os.WriteFile(normalizerStatePath(), state, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestNormalizerExtensionsFeedRetrievalMappings ingests a routed Blob whose
// pinned normalizer returns an outline in the plugin's own extension
// namespace: the Version publishes it, a retrieval mapping projects it into
// search once activated, and clients cannot write that namespace.
func TestNormalizerExtensionsFeedRetrievalMappings(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := time.Now().UTC().Format("20060102T150405.000000")
	corpusID := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Normalizer extensions", "idempotency_key": "normalizer-extensions-" + run}, 201)["corpus_id"].(string)
	// Distinct bytes: the same upload request would replay the earlier, already
	// verified Upload Session, which offers no transfer.
	blobID := uploadBlob(t, admin, []byte(normalizerMarkdown+"\nLogged on run "+run+".\n"), "text/markdown")
	accepted := request(t, "POST", "/v0/records", admin, map[string]any{
		"idempotency_key": "normalizer-extensions-" + run,
		"source":          map[string]any{"corpus_id": corpusID, "namespace": "guides", "record_key": "harbour"},
		"content":         map[string]any{"kind": "blob", "blob_id": blobID, "media_type": "text/markdown"},
	}, 202)
	ready := awaitRetrievalReady(t, accepted["receipt_id"].(string))
	version := request(t, "GET", "/v0/records/"+ready["record_id"].(string)+"/versions/"+ready["version_id"].(string), admin, nil, 200)
	plugin, _ := version["provenance"].(map[string]any)["normalization"].(map[string]any)["plugin_id"].(string)
	namespace := plugin + ".outline"
	outline, _ := version["extensions"].(map[string]any)[namespace].(map[string]any)
	data, _ := outline["data"].(map[string]any)
	if plugin == "" || outline["schema_version"] != "1" || data["heading_count"] != float64(2) || fmt.Sprint(data["heading_levels"]) != "[h1 h2]" {
		t.Fatalf("plugin extension not published on the Version: %v", version["extensions"])
	}

	// "h2" appears only in the plugin's outline, never in the canonical text.
	if hits := lexicalHits(t, []string{corpusID}, "h2"); len(hits) != 0 {
		t.Fatalf("unmapped plugin extension was searchable: %v", hits)
	}
	mapping := map[string]any{"fields": []any{map[string]any{"name": "heading_levels", "source_pointer": "/extensions/" + namespace + "/data/heading_levels", "type": "string_array", "roles": []string{"search"}}}}
	op := request(t, "PUT", "/v0/corpora/"+corpusID+"/retrieval", admin, map[string]any{"idempotency_key": "outline-" + run, "retrieval": mapping}, 202)
	if done := awaitOperation(t, "/v0/operations/"+op["operation_id"].(string)); done["state"] != "succeeded" {
		t.Fatalf("configuration outcome %v", done)
	}
	if hits := lexicalHits(t, []string{corpusID}, "h2"); len(hits) != 1 || hits[ready["version_id"].(string)] == nil {
		t.Fatalf("mapped plugin extension not searchable: %v", hits)
	}

	// Clients cannot write the plugin-owned namespace, single or batch; a
	// built-in namespace still works.
	write := func(key, ns string, data map[string]any) map[string]any {
		c := inlineCommand(corpusID, key+"-"+run, key, "Forged outline")
		c["extensions"] = map[string]any{ns: map[string]any{"schema_version": "1", "data": data}}
		return c
	}
	forged := write("forged", namespace, map[string]any{"heading_count": 9, "heading_levels": []string{"h1"}})
	if rejected := request(t, "POST", "/v0/records", admin, forged, 422); rejected["code"] != "extension_namespace_owned" {
		t.Fatalf("client write to a plugin namespace: %v", rejected)
	}
	batch := request(t, "POST", "/v0/records/batch", admin, map[string]any{"items": []any{forged, write("builtin", "example.editorial", map[string]any{"headline": "Allowed"})}}, 200)
	items := batch["items"].([]any)
	if e, _ := items[0].(map[string]any)["error"].(map[string]any); e["code"] != "extension_namespace_owned" {
		t.Fatalf("batch write to a plugin namespace: %v", items[0])
	}
	if _, ok := items[1].(map[string]any)["receipt"]; !ok {
		t.Fatalf("batch write to a built-in namespace: %v", items[1])
	}
}

// TestNormalizerRebuildWithoutPlugin runs after scripts/local.py stopped the
// plugin and restarted API and worker: a projection rebuild of the Corpus
// reads the durable Manifest and never needs the plugin.
func TestNormalizerRebuildWithoutPlugin(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	raw, err := os.ReadFile(normalizerStatePath())
	if err != nil {
		t.Skip("runs after TestNormalizerMakesRoutedBlobsSearchable and a plugin stop (scripts/local.py)")
	}
	var state normalizerState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	_, location := postRebuild(t, admin, state.Corpus, "normalizer-rebuild-"+state.Version, 202)
	if op := awaitOperation(t, location); op["state"] != "succeeded" {
		t.Fatalf("rebuild without the plugin: %v", op)
	}
	hit := phosphorescentHit(t, state.Corpus)
	if hit["version_id"] != state.Version || hit["part_key"] != state.PartKey {
		t.Fatalf("hit after rebuild %v", hit)
	}
	version := request(t, "GET", "/v0/records/"+state.Record+"/versions/"+state.Version, admin, nil, 200)
	if n := version["provenance"].(map[string]any)["normalization"].(map[string]any); n["invocation_id"] != state.Invocation {
		t.Fatalf("provenance changed after rebuild: %v", n)
	}
}
