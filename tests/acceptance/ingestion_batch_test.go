package acceptance

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// batchOutcomes submits one batch and returns exactly one outcome per entry.
func batchOutcomes(t *testing.T, token string, items []any) []map[string]any {
	t.Helper()
	return indexed(t, request(t, "POST", "/v0/records/batch", token, map[string]any{"items": items}, 200), len(items))
}

func indexed(t *testing.T, result map[string]any, want int) []map[string]any {
	t.Helper()
	raw, _ := result["items"].([]any)
	if len(raw) != want {
		t.Fatalf("%d outcomes for %d entries: %v", len(raw), want, result)
	}
	outcomes := make([]map[string]any, want)
	for i, v := range raw {
		outcomes[i] = v.(map[string]any)
		if outcomes[i]["index"] != float64(i) {
			t.Fatalf("outcome %d correlated as %v", i, outcomes[i]["index"])
		}
	}
	return outcomes
}

func receiptOf(t *testing.T, outcome map[string]any) map[string]any {
	t.Helper()
	r, ok := outcome["receipt"].(map[string]any)
	if !ok {
		t.Fatalf("entry %v rejected: %v", outcome["index"], outcome["error"])
	}
	return r
}

func errorCode(t *testing.T, outcome map[string]any) string {
	t.Helper()
	e, ok := outcome["error"].(map[string]any)
	if !ok {
		t.Fatalf("entry %v accepted, want a rejection: %v", outcome["index"], outcome["receipt"])
	}
	return e["code"].(string)
}

func receiptIDs(t *testing.T, outcomes []map[string]any) []string {
	t.Helper()
	ids := make([]string, len(outcomes))
	for i, outcome := range outcomes {
		ids[i] = receiptOf(t, outcome)["receipt_id"].(string)
	}
	return ids
}

// verifiedBlob uploads text and returns its verified Blob ID.
func verifiedBlob(t *testing.T, text string) string {
	t.Helper()
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	body, _, _ := uploadRequest(text)
	created := request(t, "POST", "/v0/uploads", admin, body, 201)
	if status := rawTransfer(t, created["upload_url"].(string), uploadHeaders(created), []byte(text)); status/100 != 2 {
		t.Fatalf("signed transfer refused: %d", status)
	}
	confirmed := request(t, "POST", "/v0/uploads/"+created["upload_id"].(string)+"/confirm", admin, nil, 202)
	if confirmed["state"] != "verified" {
		t.Fatal(confirmed)
	}
	return confirmed["blob_id"].(string)
}

// richEntries returns a text, a verified Blob and a structured Manifest entry.
func richEntries(t *testing.T, corpusID, prefix string) []any {
	t.Helper()
	text := inlineCommand(corpusID, prefix+"-text", prefix+"-text", "Texte "+prefix+" 🌞")
	blob := inlineCommand(corpusID, prefix+"-blob", prefix+"-blob", "")
	blob["content"] = map[string]any{"kind": "blob", "blob_id": verifiedBlob(t, "Blob "+prefix+" 📦"), "media_type": "text/plain"}
	relation := []any{map[string]any{"type": "illustrated_by", "target": map[string]any{"corpus_id": corpusID, "namespace": "example-feed", "record_key": prefix + "-text"}}}
	manifest := manifestCommandBody(corpusID, prefix+"-manifest", "example-feed", prefix+"-manifest", []any{manifestPart("title", "title", "text", "Titre "+prefix), manifestPart("body", "body", "text", "Corps "+prefix)}, relation, editorialExtension())
	return []any{text, blob, manifest}
}

// firstPartText reads the first Part of a resolved Receipt's immutable Version.
func firstPartText(t *testing.T, resolved map[string]any) string {
	t.Helper()
	version := request(t, "GET", "/v0/records/"+resolved["record_id"].(string)+"/versions/"+resolved["version_id"].(string), os.Getenv("QUIVR_TEST_ADMIN"), nil, 200)
	return version["manifest"].(map[string]any)["parts"].([]any)[0].(map[string]any)["content"].(map[string]any)["text"].(string)
}

func TestBatchAcceptsValidPeersIndependently(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	c := ingestionCorpus(t)
	rich := richEntries(t, c, "batch-peers")
	single := inlineCommand(c, "batch-single-first", "batch-single", "Soumis seul d'abord")
	first := request(t, "POST", "/v0/records", admin, single, 202)
	conflicting := inlineCommand(c, "batch-single-first", "batch-single", "Autre contenu sous la même clé")
	unverified := inlineCommand(c, "batch-unverified", "batch-unverified", "")
	unverified["content"] = map[string]any{"kind": "blob", "blob_id": "blob_never_verified", "media_type": "text/plain"}
	unknown := inlineCommand(c, "batch-unknown", "batch-unknown", "Champ inconnu")
	unknown["atomic"] = true
	missing := map[string]any{"source": rich[0].(map[string]any)["source"]}

	outcomes := batchOutcomes(t, admin, []any{
		rich[0], 42, missing, rich[1], rich[2], single, conflicting, rich[0],
		inlineCommand("corpus_absent", "batch-absent", "batch-absent", "Corpus inconnu"), unverified, unknown,
	})
	for i, code := range map[int]string{1: "invalid_schema", 2: "invalid_schema", 6: "idempotency_conflict", 8: "not_found", 9: "unverified_blob", 10: "invalid_schema"} {
		if got := errorCode(t, outcomes[i]); got != code {
			t.Fatalf("entry %d: got %s want %s", i, got, code)
		}
	}
	if receiptOf(t, outcomes[5])["receipt_id"] != first["receipt_id"] {
		t.Fatal("batch replay of a single submission created another Receipt")
	}
	if receiptOf(t, outcomes[7])["receipt_id"] != receiptOf(t, outcomes[0])["receipt_id"] {
		t.Fatal("a key repeated within one batch created another Receipt")
	}
	// Valid peers are durably accepted and published despite the rejected entries.
	for i, want := range map[int]string{0: "Texte batch-peers 🌞", 3: "Blob batch-peers 📦", 4: "Titre batch-peers"} {
		resolved := awaitReceipt(t, receiptOf(t, outcomes[i])["receipt_id"].(string))
		if resolved["outcome"] != "created" || firstPartText(t, resolved) != want {
			t.Fatalf("entry %d: %v", i, resolved)
		}
	}
	// Replays converge across endpoints and positions; conflicting reuse fails alone too.
	if replay := request(t, "POST", "/v0/records", admin, rich[0], 202); replay["receipt_id"] != receiptOf(t, outcomes[0])["receipt_id"] {
		t.Fatal("single replay of a batch entry diverged")
	}
	request(t, "POST", "/v0/records", admin, conflicting, 409)
	again := batchOutcomes(t, admin, []any{rich[2], rich[1]})
	if receiptOf(t, again[0])["receipt_id"] != receiptOf(t, outcomes[4])["receipt_id"] || receiptOf(t, again[1])["receipt_id"] != receiptOf(t, outcomes[3])["receipt_id"] {
		t.Fatal("batch replay at new positions diverged")
	}
}

func TestBatchEnvelopeRejectionAcceptsNothing(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	c := ingestionCorpus(t)
	// Different content under the same key is accepted only if no Receipt exists.
	neverAccepted := func(key string) {
		t.Helper()
		request(t, "POST", "/v0/records", admin, inlineCommand(c, key, key, "Contenu ultérieur"), 202)
	}
	tooMany := []any{inlineCommand(c, "envelope-count", "envelope-count", "Première entrée")}
	for i := 1; i <= 100; i++ {
		key := fmt.Sprintf("envelope-count-%d", i)
		tooMany = append(tooMany, inlineCommand(c, key, key, "Entrée"))
	}
	if e := request(t, "POST", "/v0/records/batch", admin, map[string]any{"items": tooMany}, 413); e["code"] != "batch_too_large" {
		t.Fatal(e)
	}
	neverAccepted("envelope-count")
	envelope := map[string]any{"items": []any{inlineCommand(c, "envelope-field", "envelope-field", "Enveloppe invalide")}, "atomic": true}
	if e := request(t, "POST", "/v0/records/batch", admin, envelope, 422); e["code"] != "invalid_schema" {
		t.Fatal(e)
	}
	neverAccepted("envelope-field")
	request(t, "POST", "/v0/records/batch", admin, map[string]any{"items": []any{}}, 422)
	if e := rawRequest(t, "POST", "/v0/records/batch", admin, []byte(`{"items":[`), 400); e["code"] != "malformed_json" {
		t.Fatal(e)
	}
	// An entry above the single-request bound is rejected alone; its peer is accepted.
	outcomes := batchOutcomes(t, admin, []any{
		inlineCommand(c, "entry-bound-peer", "entry-bound-peer", "Pair accepté"),
		inlineCommand(c, "entry-bound", "entry-bound", strings.Repeat("y", 1<<20)),
	})
	awaitReceipt(t, receiptOf(t, outcomes[0])["receipt_id"].(string))
	if code := errorCode(t, outcomes[1]); code != "entry_too_large" {
		t.Fatal(code)
	}
	neverAccepted("entry-bound")
}

func TestBatchAuthorization(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	c := ingestionCorpus(t)
	entry := []any{inlineCommand(c, "batch-auth", "batch-auth", "Autorisation")}
	request(t, "POST", "/v0/records/batch", "not-a-key", map[string]any{"items": entry}, 401)
	request(t, "POST", "/v0/records/batch", os.Getenv("QUIVR_TEST_READER"), map[string]any{"items": entry}, 403)
	for _, key := range []string{"OTHER", "SCOPED"} {
		if code := errorCode(t, batchOutcomes(t, os.Getenv("QUIVR_TEST_"+key), entry)[0]); code != "not_found" {
			t.Fatalf("%s: %s", key, code)
		}
	}
	written := receiptOf(t, batchOutcomes(t, os.Getenv("QUIVR_TEST_WRITER"), []any{inlineCommand(c, "batch-write-only", "batch-write-only", "Écriture seule")})[0])
	if _, ok := written["record_id"]; ok {
		t.Fatal("write-only batch acceptance leaked the linked read view", written)
	}
}

// sendAndLose writes one batch request on a raw connection and reads only its
// status line: the server has processed the whole batch, but the client never
// learns any entry's outcome.
func sendAndLose(t *testing.T, token string, items []any) {
	t.Helper()
	target, err := url.Parse(os.Getenv("QUIVR_TEST_URL"))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", target.Host, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	head := fmt.Sprintf("POST /v0/records/batch HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", target.Host, token, len(payload))
	if _, err = conn.Write(append([]byte(head), payload...)); err != nil {
		t.Fatal(err)
	}
	if err = conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.HasPrefix(status, "HTTP/1.1 200 ") {
		t.Fatalf("batch whose response was lost was not processed: %q %v", status, err)
	}
}

func TestBatchLostResponseReplaysOneCanonicalResultPerEntry(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	c := ingestionCorpus(t)
	items := richEntries(t, c, "batch-lost")
	for i := 0; i < 3; i++ {
		key := fmt.Sprintf("batch-lost-%d", i)
		items = append(items, inlineCommand(c, key, key, fmt.Sprintf("Entrée perdue %d", i)))
	}
	// Two keys were already accepted alone before the batch.
	for _, seeded := range []any{items[0], items[4]} {
		request(t, "POST", "/v0/records", admin, seeded, 202)
	}
	// The whole batch is processed in another order, but its response is lost.
	sendAndLose(t, admin, []any{items[5], items[2], items[0], items[4], items[1], items[3]})
	// Every key is now durable: reusing it with other content conflicts and creates nothing.
	for i, item := range items {
		conflicting := map[string]any{}
		for k, v := range item.(map[string]any) {
			conflicting[k] = v
		}
		conflicting["content"] = map[string]any{"kind": "text", "text": "Contenu divergent"}
		if e := request(t, "POST", "/v0/records", admin, conflicting, 409); e["code"] != "idempotency_conflict" {
			t.Fatalf("entry %d was not durably accepted: %v", i, e)
		}
	}
	// Replaying the same keys, in the original order and alone, only returns existing Receipts.
	ids := receiptIDs(t, batchOutcomes(t, admin, items))
	if single := request(t, "POST", "/v0/records", admin, items[1], 202); single["receipt_id"] != ids[1] {
		t.Fatal("single replay diverged from the batch Receipt")
	}
	// Each entry resolved once as created, on its own Record, whose current Version is that entry's.
	records := map[string]bool{}
	for i, id := range ids {
		resolved := awaitReceipt(t, id)
		recordID, _ := resolved["record_id"].(string)
		if resolved["outcome"] != "created" || records[recordID] {
			t.Fatalf("entry %d did not converge on one canonical result: %v", i, resolved)
		}
		records[recordID] = true
		awaitSearchable(t, resolved)
		if record := request(t, "GET", "/v0/records/"+recordID, admin, nil, 200); record["current_version_id"] != resolved["version_id"] {
			t.Fatalf("entry %d: current Version is not the accepted one: %v", i, record)
		}
	}
	for i, want := range []string{"Texte batch-lost 🌞", "Blob batch-lost 📦", "Titre batch-lost"} {
		if got := firstPartText(t, awaitReceipt(t, ids[i])); got != want {
			t.Fatalf("rich entry %d published %q", i, got)
		}
	}
}

// postBatch is safe to call from several goroutines; callers assert afterwards.
func postBatch(token string, items []any) (int, map[string]any, error) {
	payload, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequest("POST", os.Getenv("QUIVR_TEST_URL")+"/v0/records/batch", bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	var result map[string]any
	err = json.NewDecoder(res.Body).Decode(&result)
	return res.StatusCode, result, err
}

func TestBatchConcurrentReplaysConverge(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	c := ingestionCorpus(t)
	items := []any{}
	for i := 0; i < 6; i++ {
		key := fmt.Sprintf("batch-concurrent-%d", i)
		items = append(items, inlineCommand(c, key, key, fmt.Sprintf("Rejeu concurrent %d", i)))
	}
	// A retry racing the still-running original must not create a second Receipt.
	type response struct {
		status int
		body   map[string]any
		err    error
	}
	responses := make(chan response, 4)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, body, err := postBatch(admin, items)
			responses <- response{status, body, err}
		}()
	}
	wg.Wait()
	close(responses)
	var first []string
	for r := range responses {
		if r.err != nil || r.status != 200 {
			t.Fatalf("concurrent batch got %d: %v %v", r.status, r.err, r.body)
		}
		ids := receiptIDs(t, indexed(t, r.body, len(items)))
		if first == nil {
			first = ids
		} else if strings.Join(ids, ",") != strings.Join(first, ",") {
			t.Fatal("concurrent replays diverged")
		}
	}
	for _, id := range first {
		if resolved := awaitReceipt(t, id); resolved["outcome"] != "created" {
			t.Fatal(resolved)
		}
	}
}
