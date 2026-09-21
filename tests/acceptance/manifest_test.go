package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func manifestPart(key, role, kind, value string) map[string]any {
	if kind == "blob" {
		return map[string]any{"key": key, "role": role, "content": map[string]any{"kind": "blob", "blob_id": value, "media_type": "application/xml"}}
	}
	return map[string]any{"key": key, "role": role, "content": map[string]any{"kind": "text", "text": value}}
}

func editorialExtension() map[string]any {
	return map[string]any{"example.editorial": map[string]any{
		"schema_version": "1",
		"data": map[string]any{
			"headline": "Titre",
			"subjects": []any{map[string]any{"code": "science", "score": 0.75}},
			"flags":    map[string]any{"urgent": true},
			"extra":    nil,
		},
	}}
}

func xmlUpload(text string) map[string]any {
	sum := sha256.Sum256([]byte(text))
	return map[string]any{"size_bytes": len([]byte(text)), "sha256": hex.EncodeToString(sum[:]), "media_type": "application/xml"}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func manifestCommandBody(corpusID, key, namespace, record string, parts, relations []any, extensions map[string]any) map[string]any {
	content := map[string]any{"kind": "manifest", "parts": parts}
	if relations != nil {
		content["relations"] = relations
	}
	command := map[string]any{
		"idempotency_key": key,
		"source":          map[string]any{"corpus_id": corpusID, "namespace": namespace, "record_key": record},
		"content":         content,
	}
	if extensions != nil {
		command["extensions"] = extensions
	}
	return command
}

func TestStructuredManifestRelationsAndSearch(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	other := os.Getenv("QUIVR_TEST_OTHER")
	corpusID := ingestionCorpus(t)

	// The Record a Relation resolves to.
	target := request(t, "POST", "/v0/records", admin, inlineCommand(corpusID, "manifest-photo", "photo-1", "Légende photo 🌞"), 202)
	target = awaitReceipt(t, target["receipt_id"].(string))
	awaitSearchable(t, target)

	// Another Organization's Record must never resolve across the ownership boundary.
	otherCorpus := request(t, "POST", "/v0/corpora", other, map[string]any{"name": "Manifest other", "idempotency_key": "manifest-other"}, 201)["corpus_id"].(string)
	request(t, "POST", "/v0/records", other, inlineCommand(otherCorpus, "manifest-cross", "cross-org", "Autre organisation"), 202)

	// A verified non-text Blob Part is preserved structurally, never extracted.
	raw := "<dispatch><title/></dispatch>"
	created := request(t, "POST", "/v0/uploads", admin, xmlUpload(raw), 201)
	uploadID := created["upload_id"].(string)
	if status := rawTransfer(t, created["upload_url"].(string), uploadHeaders(created), []byte(raw)); status/100 != 2 {
		t.Fatalf("signed transfer refused: %d", status)
	}
	blobID := request(t, "POST", "/v0/uploads/"+uploadID+"/confirm", admin, nil, 202)["blob_id"].(string)

	relations := []any{
		map[string]any{"type": "illustrated_by", "target": map[string]any{"corpus_id": corpusID, "namespace": "example-feed", "record_key": "photo-1"}, "source_target_revision": "1"},
		map[string]any{"type": "illustrated_by", "target": map[string]any{"corpus_id": corpusID, "namespace": "manifest", "record_key": "missing"}},
		map[string]any{"type": "illustrated_by", "target": map[string]any{"corpus_id": otherCorpus, "namespace": "example-feed", "record_key": "cross-org"}},
	}
	body := manifestPart("body", "body", "text", "Corps de dépêche manifeste.")
	body["extensions"] = editorialExtension()
	parts := []any{
		manifestPart("title", "title", "text", "Grand titre 🌞"),
		body,
		manifestPart("source", "source", "blob", blobID),
	}
	command := manifestCommandBody(corpusID, "manifest-struct-1", "manifest", "dispatch-1", parts, relations, editorialExtension())
	command["provenance"] = map[string]any{"source_blob_ids": []string{blobID}, "producer": "example.plugin", "producer_version": "sha256:pinned"}

	accepted := request(t, "POST", "/v0/records", admin, command, 202)
	resolved := awaitReceipt(t, accepted["receipt_id"].(string))
	if resolved["outcome"] != "created" {
		t.Fatal(resolved)
	}
	version := awaitSearchable(t, resolved)

	manifest := version["manifest"].(map[string]any)
	manifestParts := manifest["parts"].([]any)
	if len(manifestParts) != 3 {
		t.Fatalf("Manifest Parts changed: %v", manifestParts)
	}
	if manifestParts[0].(map[string]any)["role"] != "title" || manifestParts[1].(map[string]any)["role"] != "body" {
		t.Fatal("Part roles lost", manifestParts)
	}
	blobPart := manifestParts[2].(map[string]any)["content"].(map[string]any)
	if blobPart["kind"] != "blob" || blobPart["blob_id"] != blobID || blobPart["media_type"] != "application/xml" {
		t.Fatal("verified Blob Part not preserved", blobPart)
	}
	if relations, ok := manifest["relations"].([]any); !ok || len(relations) != 3 {
		t.Fatal("source Relations not preserved", manifest)
	}
	partExtension := manifestParts[1].(map[string]any)["extensions"].(map[string]any)["example.editorial"].(map[string]any)
	if partExtension["schema_version"] != "1" || partExtension["data"].(map[string]any)["headline"] != "Titre" {
		t.Fatal("Part extensions not preserved", manifestParts[1])
	}
	extension := version["extensions"].(map[string]any)["example.editorial"].(map[string]any)
	if extension["schema_version"] != "1" || extension["data"].(map[string]any)["headline"] != "Titre" {
		t.Fatal("Version extensions not preserved", version)
	}
	provenance := version["provenance"].(map[string]any)
	if provenance["producer"] != "example.plugin" || provenance["source_blob_ids"].([]any)[0] != blobID {
		t.Fatal("provenance not preserved", provenance)
	}

	resolvedRelations := version["relations"].([]any)
	if len(resolvedRelations) != 3 {
		t.Fatalf("resolved relations missing: %v", resolvedRelations)
	}
	available := resolvedRelations[0].(map[string]any)
	if available["status"] != "available" || available["target_record_id"] != target["record_id"] || available["target_version_id"] != target["version_id"] {
		t.Fatal("eligible target did not resolve", available)
	}
	if available["source_reference"].(map[string]any)["source_target_revision"] != "1" {
		t.Fatal("source reference revision lost", available)
	}
	for _, index := range []int{1, 2} {
		relation := resolvedRelations[index].(map[string]any)
		if relation["status"] != "unavailable" {
			t.Fatal("unavailable relation resolved", relation)
		}
		if _, ok := relation["target_record_id"]; ok {
			t.Fatal("unavailable relation leaked a target Record", relation)
		}
		if _, ok := relation["target_version_id"]; ok {
			t.Fatal("unavailable relation leaked a target Version", relation)
		}
	}

	query := map[string]any{"query": "manifeste", "corpus_ids": []string{corpusID}, "mode": "lexical"}
	deadline := time.Now().Add(30 * time.Second)
	for {
		items := request(t, "POST", "/v0/search", admin, query, 200)["items"].([]any)
		if len(items) > 0 {
			hit := items[0].(map[string]any)
			if hit["record_id"] != resolved["record_id"] || hit["part_key"] != "body" {
				t.Fatal("wrong Manifest Part provenance", hit)
			}
			if hit["excerpt"].(map[string]any)["text"] != "Corps de dépêche manifeste." {
				t.Fatal("noncanonical Manifest excerpt", hit)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Manifest body never searchable")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Resolving a newer target Version must not mutate the source Manifest.
	before := mustJSON(t, manifest)
	correction := inlineCommand(corpusID, "manifest-photo-2", "photo-1", "Légende photo corrigée 🌞")
	correction["source_revision"] = "2"
	correction = request(t, "POST", "/v0/records", admin, correction, 202)
	correction = awaitReceipt(t, correction["receipt_id"].(string))
	awaitSearchable(t, correction)
	again := request(t, "GET", "/v0/records/"+resolved["record_id"].(string)+"/versions/"+resolved["version_id"].(string), admin, nil, 200)
	if mustJSON(t, again["manifest"].(map[string]any)) != before {
		t.Fatal("relation resolution mutated the source Manifest")
	}
	refreshed := again["relations"].([]any)[0].(map[string]any)
	if refreshed["status"] != "available" || refreshed["target_version_id"] != correction["version_id"] {
		t.Fatal("resolution did not follow the newer target Version", refreshed)
	}
	targetRecord := request(t, "GET", "/v0/records/"+target["record_id"].(string), admin, nil, 200)
	if targetRecord["current_version_id"] != correction["version_id"] {
		t.Fatal("target correction did not become current", targetRecord)
	}
}

func TestManifestExtensionsAndStructureRejections(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	c := ingestionCorpus(t)
	base := func(key string) map[string]any {
		return manifestCommandBody(c, key, "manifest-invalid", key, []any{manifestPart("body", "body", "text", "Corps valide")}, nil, nil)
	}

	valid := base("manifest-valid")
	valid["extensions"] = editorialExtension()
	request(t, "POST", "/v0/records", admin, valid, 202)

	unknownNamespace := base("manifest-unknown-namespace")
	unknownNamespace["extensions"] = map[string]any{"uninstalled": map[string]any{"schema_version": "1", "data": map[string]any{}}}
	request(t, "POST", "/v0/records", admin, unknownNamespace, 422)

	unknownVersion := base("manifest-unknown-version")
	unknownVersion["extensions"] = map[string]any{"example.editorial": map[string]any{"schema_version": "9", "data": map[string]any{}}}
	request(t, "POST", "/v0/records", admin, unknownVersion, 422)

	badData := base("manifest-bad-data")
	badData["extensions"] = map[string]any{"example.editorial": map[string]any{"schema_version": "1", "data": map[string]any{"headline": 42}}}
	request(t, "POST", "/v0/records", admin, badData, 422)

	badPartExtension := base("manifest-part-extension")
	part := manifestPart("body", "body", "text", "Corps")
	part["extensions"] = map[string]any{"uninstalled": map[string]any{"schema_version": "1", "data": map[string]any{}}}
	badPartExtension["content"] = map[string]any{"kind": "manifest", "parts": []any{part}}
	request(t, "POST", "/v0/records", admin, badPartExtension, 422)

	duplicate := base("manifest-duplicate")
	duplicate["content"] = map[string]any{"kind": "manifest", "parts": []any{manifestPart("body", "body", "text", "Un"), manifestPart("body", "body", "text", "Deux")}}
	request(t, "POST", "/v0/records", admin, duplicate, 422)

	unknownParent := base("manifest-parent")
	unknownParent["content"] = map[string]any{"kind": "manifest", "parts": []any{map[string]any{"key": "body", "role": "body", "parent_key": "missing", "content": map[string]any{"kind": "text", "text": "Corps"}}}}
	request(t, "POST", "/v0/records", admin, unknownParent, 422)

	cycle := base("manifest-cycle")
	cycle["content"] = map[string]any{"kind": "manifest", "parts": []any{
		map[string]any{"key": "a", "role": "body", "parent_key": "b", "content": map[string]any{"kind": "text", "text": "A"}},
		map[string]any{"key": "b", "role": "body", "parent_key": "a", "content": map[string]any{"kind": "text", "text": "B"}},
	}}
	request(t, "POST", "/v0/records", admin, cycle, 422)

	unverifiedBlob := base("manifest-blob")
	unverifiedBlob["content"] = map[string]any{"kind": "manifest", "parts": []any{manifestPart("body", "body", "text", "Corps"), manifestPart("source", "source", "blob", "blob_missing")}}
	request(t, "POST", "/v0/records", admin, unverifiedBlob, 422)

	unknownCore := base("manifest-core")
	unknownCore["unexpected"] = true
	request(t, "POST", "/v0/records", admin, unknownCore, 422)

	forgedProvenance := base("manifest-provenance")
	forgedProvenance["provenance"] = map[string]any{"source_blob_ids": []string{"blob_never_uploaded"}}
	request(t, "POST", "/v0/records", admin, forgedProvenance, 422)
}
