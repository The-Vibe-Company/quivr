package acceptance

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// TestChangePruneExpiresCursorsAndResyncConverges runs on org_r, the only
// Organization the harness worker prunes (2 s retention, 1 s interval). Once
// the journal behind a captured cursor is physically pruned, the default
// seven-day API refuses that cursor (polling and SSE) instead of resuming past
// the gap; the catalog still holds every Record, resynchronization converges,
// and a fresh cursor then continues without loss.
func TestChangePruneExpiresCursorsAndResyncConverges(t *testing.T) {
	token, probe := os.Getenv("QUIVR_TEST_RETENTION"), os.Getenv("QUIVR_TEST_WORKER_PROBE_URL")
	if token == "" || probe == "" {
		t.Skip("make verify prunes org_r's change journal")
	}
	run := fmt.Sprint(time.Now().UnixNano())
	c := request(t, "POST", "/v0/corpora", token, map[string]any{"name": "Prune " + run, "idempotency_key": "prune-" + run}, 201)["corpus_id"].(string)
	cursor := request(t, "GET", changesPath(c, "", 0), token, nil, 200)["next_cursor"].(string)
	records := map[string]bool{}
	for i := range 3 {
		key := fmt.Sprint("prune-", run, "-", i)
		receipt := request(t, "POST", "/v0/records", token, inlineCommand(c, key, key, fmt.Sprint("Dépêche élaguée ", i)), 202)["receipt_id"].(string)
		records[awaitReceiptAs(t, token, receipt)["record_id"].(string)] = true
	}

	// The seven-day API would keep this cursor valid by age alone; physical
	// pruning must still refuse it with the documented restart.
	resync := "/v0/records?" + url.Values{"corpus_id": {c}}.Encode()
	deadline := time.Now().Add(60 * time.Second)
	for {
		status, body := getAs(t, token, changesPath(c, cursor, 0))
		if status == http.StatusGone {
			if body["code"] != "cursor_expired" || body["resync_url"] != resync {
				t.Fatal("pruned cursor without the documented restart", body)
			}
			break
		}
		if status != http.StatusOK || time.Now().After(deadline) {
			t.Fatal("the worker never pruned the journal behind the cursor", status, body)
		}
		time.Sleep(500 * time.Millisecond)
	}
	s := openChangeStream(t, os.Getenv("QUIVR_TEST_URL"), token, "/v0/changes/stream?corpus_id="+url.QueryEscape(c), cursor)
	var e map[string]any
	if err := json.NewDecoder(s.body.Body).Decode(&e); err != nil || s.body.StatusCode != http.StatusGone || e["code"] != "cursor_expired" {
		t.Fatal("pruned stream cursor before headers", s.body.StatusCode, e, err)
	}
	s.close()
	// The cursor expires when the prune batch commits; the worker counts the
	// pass only after its remaining batches return, so wait for the counter.
	deadline = time.Now().Add(30 * time.Second)
	for pruned := prunedEvents(t, probe); pruned <= 0; pruned = prunedEvents(t, probe) {
		if time.Now().After(deadline) {
			t.Fatal("worker metrics did not count pruned events", pruned)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Pruning removed journal events only: the catalog restart sees every Record.
	client := &resyncClient{t: t, base: os.Getenv("QUIVR_TEST_URL"), token: token, corpus: c, limit: 2}
	client.sync()
	converge(t, client)
	for id := range records {
		if client.view[id] == nil {
			t.Fatal("resynchronized view lost a Record", id, client.view)
		}
	}
	// The resynchronized cursor stands at or after the watermark and loses nothing.
	late := request(t, "POST", "/v0/records", token, inlineCommand(c, "prune-"+run+"-late", "prune-"+run+"-late", "Arrivée après l'élagage"), 202)["record_id"].(string)
	converge(t, client)
	if client.view[late] == nil || len(client.view) != len(records)+1 {
		t.Fatal("fresh cursor missed a change after the prune", late, client.view)
	}
}

func getAs(t *testing.T, token, path string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("GET", os.Getenv("QUIVR_TEST_URL")+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	if err = json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, body
}

func awaitReceiptAs(t *testing.T, token, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		r := request(t, "GET", "/v0/ingestion-receipts/"+id, token, nil, 200)
		if r["state"] == "resolved" {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("Receipt never resolved: %v", r)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

var prunedTotal = regexp.MustCompile(`(?m)^quivr_change_events_pruned_total (\d+)$`)

func prunedEvents(t *testing.T, probe string) int {
	t.Helper()
	res, err := (&http.Client{Timeout: 10 * time.Second}).Get(probe + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	m := prunedTotal.FindSubmatch(b)
	if m == nil {
		t.Fatal("metrics without quivr_change_events_pruned_total", string(b))
	}
	n, _ := strconv.Atoi(string(m[1]))
	return n
}
