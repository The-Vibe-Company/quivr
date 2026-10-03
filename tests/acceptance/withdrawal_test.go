package acceptance

import (
	"os"
	"strings"
	"testing"
	"time"
)

func withdrawalCommand(corpus, key, namespace, record, reason string) map[string]any {
	return map[string]any{
		"idempotency_key": key,
		"source":          map[string]any{"corpus_id": corpus, "namespace": namespace, "record_key": record},
		"reason":          reason,
	}
}

func TestWithdrawalFencesRecord(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	c := ingestionCorpus(t)
	source := request(t, "POST", "/v0/records", admin, inlineCommand(c, "withdraw-source", "withdraw-1", "Ancienne comète 🌠"), 202)
	source = awaitReceipt(t, source["receipt_id"].(string))
	awaitSearchable(t, source)
	query := map[string]any{"query": "comète", "corpus_ids": []string{c}, "mode": "lexical"}
	if len(request(t, "POST", "/v0/search", admin, query, 200)["items"].([]any)) != 1 {
		t.Fatal("source never searchable")
	}

	withdrawal := withdrawalCommand(c, "withdraw-1", "example-feed", "withdraw-1", "source retraction")
	accepted := request(t, "POST", "/v0/records/withdrawals", admin, withdrawal, 202)
	resolved := awaitReceipt(t, accepted["receipt_id"].(string))
	if resolved["state"] != "resolved" || resolved["outcome"] != "withdrawal_applied" || resolved["record_id"] != source["record_id"] {
		t.Fatal(resolved)
	}
	if _, ok := resolved["version_id"]; ok {
		t.Fatal("withdrawal Receipt linked a Version", resolved)
	}
	if replay := request(t, "POST", "/v0/records/withdrawals", admin, withdrawal, 202); replay["receipt_id"] != accepted["receipt_id"] {
		t.Fatal("withdrawal replay diverged")
	}
	request(t, "POST", "/v0/records/withdrawals", admin, withdrawalCommand(c, "withdraw-1", "example-feed", "withdraw-1", "different reason"), 409)

	record := request(t, "GET", "/v0/records/"+source["record_id"].(string), admin, nil, 200)
	if record["withdrawn"] != true {
		t.Fatal("Record not withdrawn", record)
	}
	// Canonical exclusion hides the surviving projection object immediately.
	if len(request(t, "POST", "/v0/search", admin, query, 200)["items"].([]any)) != 0 {
		t.Fatal("withdrawn Record still searchable")
	}
	version := request(t, "GET", "/v0/records/"+source["record_id"].(string)+"/versions/"+source["version_id"].(string), admin, nil, 200)
	availability := version["availability"].(map[string]any)
	if availability["searchable"] != false || availability["is_current"] != false {
		t.Fatal("withdrawn Version stayed eligible", availability)
	}
	request(t, "POST", "/v0/records", admin, inlineCommand(c, "post-withdraw", "withdraw-1", "Ressusciter"), 409)
	request(t, "POST", "/v0/records/withdrawals", os.Getenv("QUIVR_TEST_READER"), withdrawalCommand(c, "withdraw-reader", "example-feed", "withdraw-1", "x"), 403)
	request(t, "POST", "/v0/records/withdrawals", os.Getenv("QUIVR_TEST_OTHER"), withdrawalCommand(c, "withdraw-other", "example-feed", "withdraw-1", "x"), 404)
}

func TestWithdrawalFencesUnmaterializedIdentity(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	c := ingestionCorpus(t)
	accepted := request(t, "POST", "/v0/records/withdrawals", admin, withdrawalCommand(c, "fence-alone", "example-feed", "fence-alone", "pre-emptive fence"), 202)
	resolved := awaitReceipt(t, accepted["receipt_id"].(string))
	if resolved["outcome"] != "withdrawal_applied" || resolved["record_id"] == nil {
		t.Fatal(resolved)
	}
	// The fenced identity cannot be introduced later.
	request(t, "POST", "/v0/records", admin, inlineCommand(c, "fence-after", "fence-alone", "Après fence"), 409)
}

func TestWithdrawalDoesNotCascadeToRelatedRecords(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	c := ingestionCorpus(t)
	photo := request(t, "POST", "/v0/records", admin, inlineCommand(c, "shared-photo", "photo-shared", "Légende partagée 🌞"), 202)
	photo = awaitReceipt(t, photo["receipt_id"].(string))
	awaitSearchable(t, photo)
	relation := []any{map[string]any{"type": "illustrated_by", "target": map[string]any{"corpus_id": c, "namespace": "example-feed", "record_key": "photo-shared"}}}
	command := manifestCommandBody(c, "withdraw-relation-source", "example-feed", "dispatch-rel", []any{manifestPart("body", "body", "text", "Dépêche reliée à la photo")}, relation, nil)
	source := request(t, "POST", "/v0/records", admin, command, 202)
	source = awaitReceipt(t, source["receipt_id"].(string))
	awaitSearchable(t, source)

	resolved := awaitReceipt(t, request(t, "POST", "/v0/records/withdrawals", admin, withdrawalCommand(c, "withdraw-relation-wd", "example-feed", "dispatch-rel", "retrait"), 202)["receipt_id"].(string))
	if resolved["outcome"] != "withdrawal_applied" {
		t.Fatal(resolved)
	}
	// The shared target Record is untouched and still searchable.
	target := request(t, "GET", "/v0/records/"+photo["record_id"].(string), admin, nil, 200)
	if target["withdrawn"] != false || target["current_version_id"] != photo["version_id"] {
		t.Fatal("withdrawal cascaded to the shared related Record", target)
	}
	photoQuery := map[string]any{"query": "partagée", "corpus_ids": []string{c}, "mode": "lexical"}
	if len(request(t, "POST", "/v0/search", admin, photoQuery, 200)["items"].([]any)) != 1 {
		t.Fatal("shared related Record disappeared from search")
	}
	sourceQuery := map[string]any{"query": "reliée", "corpus_ids": []string{c}, "mode": "lexical"}
	if len(request(t, "POST", "/v0/search", admin, sourceQuery, 200)["items"].([]any)) != 0 {
		t.Fatal("withdrawn source still searchable")
	}
}

func TestCorrectionKeepsPriorCurrentUntilSuccessorReady(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	c := ingestionCorpus(t)
	first := request(t, "POST", "/v0/records", admin, inlineCommand(c, "correction-first", "correction-1", "Ancienne comète"), 202)
	first = awaitReceipt(t, first["receipt_id"].(string))
	awaitSearchable(t, first)

	// A successor whose lexical baseline can never build must not replace the
	// current Version.
	blocked := inlineCommand(c, "correction-blocked", "correction-1", strings.Repeat("x", 262145))
	blocked["source_revision"] = "2"
	blocked = request(t, "POST", "/v0/records", admin, blocked, 202)
	blocked = awaitReceipt(t, blocked["receipt_id"].(string))
	deadline := time.Now().Add(30 * time.Second)
	for {
		view := request(t, "GET", "/v0/ingestion-receipts/"+blocked["receipt_id"].(string), admin, nil, 200)
		if view["processing"].(map[string]any)["state"] == "blocked" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("successor never observably blocked", view)
		}
		time.Sleep(100 * time.Millisecond)
	}
	record := request(t, "GET", "/v0/records/"+first["record_id"].(string), admin, nil, 200)
	if record["current_version_id"] != first["version_id"] {
		t.Fatal("an unready successor replaced the current Version", record)
	}
	query := map[string]any{"query": "comète", "corpus_ids": []string{c}, "mode": "lexical"}
	items := request(t, "POST", "/v0/search", admin, query, 200)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["version_id"] != first["version_id"] {
		t.Fatal("prior current Version was not searchable during the correction", items)
	}

	// A later ready correction wins the guarded promotion.
	next := inlineCommand(c, "correction-ready", "correction-1", "Nouvelle galaxie")
	next["source_revision"] = "3"
	next = request(t, "POST", "/v0/records", admin, next, 202)
	next = awaitReceipt(t, next["receipt_id"].(string))
	awaitSearchable(t, next)
	record = request(t, "GET", "/v0/records/"+first["record_id"].(string), admin, nil, 200)
	if record["current_version_id"] != next["version_id"] {
		t.Fatal("ready successor did not win the guarded promotion", record)
	}
	if len(request(t, "POST", "/v0/search", admin, query, 200)["items"].([]any)) != 0 {
		t.Fatal("superseded Version still returned by search")
	}
	nextQuery := map[string]any{"query": "galaxie", "corpus_ids": []string{c}, "mode": "lexical"}
	items = request(t, "POST", "/v0/search", admin, nextQuery, 200)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["version_id"] != next["version_id"] {
		t.Fatal("successor not searchable", items)
	}
}

func TestWithdrawalRacesLateIndexingWithoutResurrection(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	c := ingestionCorpus(t)
	accepted := request(t, "POST", "/v0/records", admin, inlineCommand(c, "race-source", "race-1", "Fugace éclair"), 202)
	resolved := awaitReceipt(t, request(t, "POST", "/v0/records/withdrawals", admin, withdrawalCommand(c, "race-wd", "example-feed", "race-1", "retrait immédiat"), 202)["receipt_id"].(string))
	if resolved["outcome"] != "withdrawal_applied" {
		t.Fatal(resolved)
	}
	ingestion := awaitReceipt(t, accepted["receipt_id"].(string))
	if ingestion["outcome"] != "created" && ingestion["outcome"] != "conflict" {
		t.Fatal(ingestion)
	}
	if ingestion["outcome"] == "created" && ingestion["availability"].(map[string]any)["searchable"] != false {
		t.Fatal("withdrawn Version became searchable", ingestion)
	}
	if ingestion["outcome"] == "created" {
		// A withdrawn Version becomes idle when late indexing has settled.
		path := "/v0/records/" + ingestion["record_id"].(string) + "/versions/" + ingestion["version_id"].(string)
		deadline := time.Now().Add(10 * time.Second)
		for {
			version := request(t, "GET", path, admin, nil, 200)
			if version["processing"].(map[string]any)["state"] == "idle" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("withdrawn Version never finished processing", version)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	query := map[string]any{"query": "éclair", "corpus_ids": []string{c}, "mode": "lexical"}
	if items := request(t, "POST", "/v0/search", admin, query, 200)["items"].([]any); len(items) != 0 {
		t.Fatal("withdrawn Record resurrected in search", items)
	}
	record := request(t, "GET", "/v0/records/"+ingestion["record_id"].(string), admin, nil, 200)
	if record["withdrawn"] != true {
		t.Fatal("withdrawal did not stick", record)
	}
}
