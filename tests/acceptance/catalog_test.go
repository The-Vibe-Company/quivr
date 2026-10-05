package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"
)

// This file runs the published resynchronization procedure
// (docs/dated/design/quivr-v2-ingestion-contracts.md, "Change feed and resynchronization")
// as a minimal reference client. It is test code, not an SDK.

// catalogView is a client's local current view: Record ID to Record resource.
type catalogView map[string]map[string]any

// restartError carries the error that discarded a partial view.
type restartError struct{ body map[string]any }

func (e restartError) Error() string { return fmt.Sprint("restart: ", e.body) }

// refresh treats invalidated Record IDs as a coalesced set, rereading each
// current resource once and sequentially so no stale concurrent read can
// overwrite a newer one. An absent or inaccessible Record leaves the view.
func refresh(view catalogView, ids []string, read func(id string) (int, map[string]any)) error {
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		switch status, body := read(id); status {
		case http.StatusOK:
			view[id] = body
		case http.StatusNotFound:
			delete(view, id)
		default:
			return fmt.Errorf("reread %s: %d %v", id, status, body)
		}
	}
	return nil
}

type resyncClient struct {
	t                   *testing.T
	base, token, corpus string
	limit               int
	view                catalogView
	cursor              string
	restarts            []map[string]any
	// onPage runs after each catalog page of the current attempt with that
	// page's Records, so a test can mutate content during traversal.
	onPage func(attempt, page int, items []map[string]any)
	// started is the Change Cursor the current scan captured before its first page.
	started string
}

func (c *resyncClient) get(path string) (int, map[string]any) {
	c.t.Helper()
	req, _ := http.NewRequest("GET", c.base+path, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	if err = json.NewDecoder(res.Body).Decode(&body); err != nil {
		c.t.Fatal(err)
	}
	if dir := os.Getenv("QUIVR_TEST_CAPTURES"); dir != "" {
		f, e := os.CreateTemp(dir, "response-*.json")
		if e != nil {
			c.t.Fatal(e)
		}
		defer f.Close()
		_ = json.NewEncoder(f).Encode(map[string]any{"path": req.URL.Path, "method": "GET", "status": res.StatusCode, "body": body})
	}
	return res.StatusCode, body
}

// check turns expiry and scope changes into a restart; anything else unexpected fails.
func (c *resyncClient) check(status int, body map[string]any) error {
	c.t.Helper()
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusGone, http.StatusConflict:
		return restartError{body}
	default:
		c.t.Fatalf("unexpected %d %v", status, body)
		return nil
	}
}

// scan captures a start-now Change Cursor, then reads every catalog page from
// first into a fresh view. The view is installed only when the scan completes.
func (c *resyncClient) scan(first string) error {
	status, start := c.get(changesPath(c.corpus, "", 0))
	if err := c.check(status, start); err != nil {
		return err
	}
	c.started = start["next_cursor"].(string)
	view := catalogView{}
	path := first + "&limit=" + fmt.Sprint(c.limit)
	for page := 0; ; page++ {
		status, body := c.get(path)
		if err := c.check(status, body); err != nil {
			return err
		}
		var items []map[string]any
		for _, item := range body["items"].([]any) {
			record := item.(map[string]any)
			view[record["record_id"].(string)] = record
			items = append(items, record)
		}
		if c.onPage != nil {
			c.onPage(len(c.restarts), page, items)
		}
		next, ok := body["next_page_cursor"].(string)
		if !ok {
			break
		}
		path = recordsPath(c.corpus, next, c.limit)
	}
	c.view, c.cursor = view, c.started
	return nil
}

// catchUp consumes changes after the captured cursor as invalidations.
func (c *resyncClient) catchUp() error {
	for {
		status, page := c.get(changesPath(c.corpus, c.cursor, 0))
		if err := c.check(status, page); err != nil {
			return err
		}
		var ids []string
		for _, item := range page["items"].([]any) {
			if resource := item.(map[string]any)["resource"].(map[string]any); resource["kind"] == "record" {
				ids = append(ids, resource["id"].(string))
			}
		}
		if err := refresh(c.view, ids, func(id string) (int, map[string]any) { return c.get("/v0/records/" + id) }); err != nil {
			c.t.Fatal(err)
		}
		c.cursor = page["next_cursor"].(string)
		if page["has_more"] != true {
			return nil
		}
	}
}

// sync runs the whole procedure, discarding the partial view and restarting
// from the error's resync reference whenever a cursor expires or its scope changes.
func (c *resyncClient) sync() {
	c.t.Helper()
	first := recordsPath(c.corpus, "", 0)
	for attempt := 0; attempt < 3; attempt++ {
		err := c.scan(first)
		if err == nil {
			err = c.catchUp()
		}
		var restart restartError
		if !errors.As(err, &restart) {
			return
		}
		c.view, c.cursor = nil, ""
		c.restarts = append(c.restarts, restart.body)
		first = restart.body["resync_url"].(string)
	}
	c.t.Fatal("resynchronization kept restarting", c.restarts)
}

func recordsPath(corpusID, pageCursor string, limit int) string {
	q := url.Values{"corpus_id": {corpusID}}
	if pageCursor != "" {
		q.Set("page_cursor", pageCursor)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	return "/v0/records?" + q.Encode()
}

// freshScan reads the whole current catalog without a client view.
func freshScan(t *testing.T, base, token, corpusID string) catalogView {
	t.Helper()
	c := &resyncClient{t: t, base: base, token: token, corpus: corpusID, limit: 100}
	if err := c.scan(recordsPath(corpusID, "", 0)); err != nil {
		t.Fatal(err)
	}
	return c.view
}

// converge keeps consuming changes until the client's view equals a fresh scan.
func converge(t *testing.T, c *resyncClient) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := c.catchUp(); err != nil {
			t.Fatal("catch-up restarted while converging", err)
		}
		fresh := freshScan(t, c.base, c.token, c.corpus)
		if reflect.DeepEqual(c.view, fresh) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("view never converged:\nclient %v\nfresh  %v", c.view, fresh)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// derivedRecordID mirrors the stable public Record identity so a test can
// choose a source key whose Record sorts before a traversal position.
func derivedRecordID(organization, corpusID, namespace, key string) string {
	b, _ := json.Marshal([]string{organization, corpusID, namespace, key})
	sum := sha256.Sum256(b)
	return "record_" + hex.EncodeToString(sum[:])
}

func seedCatalog(t *testing.T, admin, corpusID, prefix string, n int) map[string]string {
	t.Helper()
	keys := map[string]string{}
	for i := range n {
		key := fmt.Sprint(prefix, i)
		r := request(t, "POST", "/v0/records", admin, inlineCommand(corpusID, "catalog-"+corpusID+"-"+key, key, fmt.Sprint("Dépêche initiale ", i)), 202)
		keys[awaitReceipt(t, r["receipt_id"].(string))["record_id"].(string)] = key
	}
	return keys
}

func TestCatalogRefreshRemovesAbsentRecords(t *testing.T) {
	view := catalogView{"record_a": {"record_id": "record_a"}, "record_b": {"record_id": "record_b"}}
	var reads []string
	err := refresh(view, []string{"record_a", "record_b", "record_a", "record_c"}, func(id string) (int, map[string]any) {
		reads = append(reads, id)
		if id == "record_b" {
			return http.StatusNotFound, map[string]any{"code": "not_found"}
		}
		return http.StatusOK, map[string]any{"record_id": id, "withdrawn": true}
	})
	if err != nil || fmt.Sprint(reads) != "[record_a record_b record_c]" {
		t.Fatal("invalidations were not coalesced into one sequential reread each", reads, err)
	}
	if _, ok := view["record_b"]; ok || view["record_a"]["withdrawn"] != true || view["record_c"] == nil {
		t.Fatal("absent Record kept or current state not applied", view)
	}
}

func TestCatalogResyncConvergesUnderMutation(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin, base := os.Getenv("QUIVR_TEST_ADMIN"), os.Getenv("QUIVR_TEST_URL")
	c := changeCorpus(t, "catalog-converge")
	seeded := seedCatalog(t, admin, c, "seed-", 4)
	for id, key := range seeded {
		if derivedRecordID("org_a", c, "example-feed", key) != id {
			t.Fatal("test cannot derive the public Record identity; update derivedRecordID")
		}
	}

	var missed, corrected, correction, scannedWithdrawn, unscannedWithdrawn string
	scanned := map[string]bool{}
	client := &resyncClient{t: t, base: base, token: admin, corpus: c, limit: 2}
	client.onPage = func(_, page int, items []map[string]any) {
		for _, item := range items {
			scanned[item["record_id"].(string)] = true
		}
		if page != 0 {
			return
		}
		// After the first page: correct and withdraw scanned Records, withdraw an
		// unscanned one, and insert a Record keyed behind the traversal position.
		position := items[len(items)-1]["record_id"].(string)
		corrected, scannedWithdrawn = items[0]["record_id"].(string), position
		for id := range seeded {
			if id > position {
				unscannedWithdrawn = id
			}
		}
		for i := 0; missed == ""; i++ {
			if i == 1000 {
				t.Fatal("no source key sorts before the traversal position")
			}
			key := fmt.Sprint("missed-", i)
			if id := derivedRecordID("org_a", c, "example-feed", key); id < position {
				r := request(t, "POST", "/v0/records", admin, inlineCommand(c, "catalog-"+c+"-"+key, key, "Insérée derrière le curseur"), 202)
				if r["record_id"] != id {
					t.Fatal("derived identity mismatch", r)
				}
				missed = id
			}
		}
		revision := inlineCommand(c, "catalog-"+c+"-correction", seeded[corrected], "Dépêche corrigée")
		revision["source_revision"] = "2"
		correction = request(t, "POST", "/v0/records", admin, revision, 202)["receipt_id"].(string)
		for _, id := range []string{scannedWithdrawn, unscannedWithdrawn} {
			key := seeded[id]
			request(t, "POST", "/v0/records/withdrawals", admin, withdrawalCommand(c, "catalog-"+c+"-withdraw-"+key, "example-feed", key, "retraction"), 202)
		}
	}
	if err := client.scan(recordsPath(c, "", 0)); err != nil {
		t.Fatal(err)
	}
	if scanned[missed] || client.view[missed] != nil {
		t.Fatal("keyset traversal was expected to miss the Record inserted behind its position")
	}
	if len(client.view) != len(seeded) {
		t.Fatal("traversal lost or duplicated Records", client.view)
	}
	current := awaitReceipt(t, correction)["version_id"]
	deadline := time.Now().Add(60 * time.Second)
	for request(t, "GET", "/v0/records/"+corrected, admin, nil, 200)["current_version_id"] != current {
		if time.Now().After(deadline) {
			t.Fatal("correction never became current")
		}
		time.Sleep(200 * time.Millisecond)
	}
	converge(t, client)
	if client.view[missed] == nil || client.view[corrected]["current_version_id"] != current {
		t.Fatal("catch-up did not add the missed Record or apply the correction", client.view)
	}
	if client.view[scannedWithdrawn]["withdrawn"] != true || client.view[unscannedWithdrawn]["withdrawn"] != true || len(client.view) != len(seeded)+1 {
		t.Fatal("withdrawal state did not converge", client.view)
	}
}

// TestCatalogResyncRestartsAfterExpiry lets the captured Change Cursor expire
// during traversal on the short-retention API; the client discards its partial
// view, restarts from resync_url and converges.
func TestCatalogResyncRestartsAfterExpiry(t *testing.T) {
	short := os.Getenv("QUIVR_TEST_SHORT_RETENTION_URL")
	if short == "" {
		t.Skip("make verify starts a short-retention API")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	c := changeCorpus(t, "catalog-expiry")
	seeded := seedCatalog(t, admin, c, "expiry-", 3)
	late := ""
	client := &resyncClient{t: t, base: short, token: admin, corpus: c, limit: 1}
	client.onPage = func(attempt, page int, _ []map[string]any) {
		if attempt == 0 && page == 0 {
			late = request(t, "POST", "/v0/records", admin, inlineCommand(c, "catalog-"+c+"-late", "late", "Arrivée pendant le parcours"), 202)["record_id"].(string)
			awaitExpired(t, short, admin, changesPath(c, client.started, 0))
		}
	}
	client.sync()
	resync := "/v0/records?" + url.Values{"corpus_id": {c}}.Encode()
	if len(client.restarts) != 1 || client.restarts[0]["code"] != "cursor_expired" || client.restarts[0]["resync_url"] != resync {
		t.Fatal("expiry did not restart from the documented resync reference", client.restarts)
	}
	converge(t, client)
	if client.view[late] == nil || len(client.view) != len(seeded)+1 {
		t.Fatal("restarted view missed content", client.view)
	}
}
