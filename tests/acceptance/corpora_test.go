package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"
)

func request(t *testing.T, method, path, token string, body any, want int) map[string]any {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return rawRequest(t, method, path, token, data, want)
}

// rawRequest sends exact JSON bytes, including envelopes no typed body can express.
func rawRequest(t *testing.T, method, path, token string, data []byte, want int) map[string]any {
	t.Helper()
	req, err := http.NewRequest(method, os.Getenv("QUIVR_TEST_URL")+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var result map[string]any
	if err = json.NewDecoder(res.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != want {
		t.Fatalf("%s %s: got %d want %d: %v", method, path, res.StatusCode, want, result)
	}
	if res.Header.Get("X-Request-ID") == "" {
		t.Fatal("missing correlation ID")
	}
	if dir := os.Getenv("QUIVR_TEST_CAPTURES"); dir != "" {
		f, e := os.CreateTemp(dir, "response-*.json")
		if e != nil {
			t.Fatal(e)
		}
		defer f.Close()
		_ = json.NewEncoder(f).Encode(map[string]any{"path": req.URL.Path, "method": method, "status": res.StatusCode, "body": result})
	}
	return result
}
func TestCorpusPersistsAndReplays(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("run make verify for real HTTP/PostgreSQL acceptance")
	}
	token := os.Getenv("QUIVR_TEST_ADMIN")
	body := map[string]any{"name": "Actualités 🌍", "idempotency_key": "persistent-corpus"}
	first := request(t, "POST", "/v0/corpora", token, body, 201)
	again := request(t, "POST", "/v0/corpora", token, body, 201)
	if first["corpus_id"] != again["corpus_id"] {
		t.Fatal("replay changed Corpus")
	}
	got := request(t, "GET", "/v0/corpora/"+first["corpus_id"].(string), token, nil, 200)
	if got["name"] != "Actualités 🌍" {
		t.Fatal(got)
	}
	page := request(t, "GET", "/v0/corpora", token, nil, 200)
	if len(page["items"].([]any)) != 1 {
		t.Fatal(page)
	}
}

func TestAuthorization(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	other := os.Getenv("QUIVR_TEST_OTHER")
	reader := os.Getenv("QUIVR_TEST_READER")
	scoped := os.Getenv("QUIVR_TEST_SCOPED")
	denied := os.Getenv("QUIVR_TEST_DENIED")
	page := request(t, "GET", "/v0/corpora", admin, nil, 200)
	id := page["items"].([]any)[0].(map[string]any)["corpus_id"].(string)
	request(t, "GET", "/v0/corpora/"+id, scoped, nil, 200)
	request(t, "GET", "/v0/corpora/"+id, other, nil, 404)
	request(t, "GET", "/v0/corpora/absent", other, nil, 404)
	request(t, "GET", "/v0/corpora", denied, nil, 403)
	request(t, "GET", "/v0/corpora", "invalid", nil, 401)
	body := map[string]any{"name": "Denied", "idempotency_key": "denied"}
	request(t, "POST", "/v0/corpora", reader, body, 403)
	request(t, "POST", "/v0/corpora", scoped, body, 403)
	newCorpus := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Private", "idempotency_key": "private"}, 201)
	request(t, "GET", "/v0/corpora/"+newCorpus["corpus_id"].(string), scoped, nil, 404)
	scopedPage := request(t, "GET", "/v0/corpora", scoped, nil, 200)
	if len(scopedPage["items"].([]any)) != 1 {
		t.Fatal(scopedPage)
	}
	foreign := request(t, "POST", "/v0/corpora", other, map[string]any{"name": "Other", "idempotency_key": "persistent-corpus"}, 201)
	if foreign["corpus_id"] == id {
		t.Fatal("Organization identity collision")
	}
}
func TestValidation(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Changed", "idempotency_key": "persistent-corpus"}, 409)
	for _, v := range []any{nil, []any{}, map[string]any{"name": ""}, map[string]any{"name": "A", "idempotency_key": "bad", "organization": "org_b"}} {
		request(t, "POST", "/v0/corpora", admin, v, 422)
	}
	// Pointers address the canonical source view (manifest, declared
	// extensions, provenance); other roots are undeclared raw mappings.
	for _, pointer := range []string{"/provenance/~2bad", "/provenance/trailing~", "/metadata/~0tilde/~1slash", "/provenance/~0tilde/~1slash"} {
		status := 422
		if pointer == "/provenance/~0tilde/~1slash" {
			status = 201
		}
		request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Mapping", "idempotency_key": pointer, "retrieval": map[string]any{"fields": []any{map[string]any{"name": "headline", "source_pointer": pointer, "type": "string", "roles": []string{"search"}}}}}, status)
	}
	for _, q := range []string{"limit=0", "limit=101", "limit=a", "limit=1&limit=2", "page_cursor=", "page_cursor=tampered", "organization=org_b"} {
		request(t, "GET", "/v0/corpora?"+q, admin, nil, 422)
	}
	req, _ := http.NewRequest("POST", os.Getenv("QUIVR_TEST_URL")+"/v0/corpora", bytes.NewBufferString(`{"name":"Wrong media","idempotency_key":"media"}`))
	req.Header.Set("Authorization", "Bearer "+admin)
	req.Header.Set("Content-Type", "text/plain")
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 415 {
		t.Fatalf("unsupported media got %d want 415", res.StatusCode)
	}
}
func TestPagination(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	for i := 0; i < 3; i++ {
		request(t, "POST", "/v0/corpora", admin, map[string]any{"name": fmt.Sprintf("Page %d", i), "idempotency_key": fmt.Sprintf("page-%d", i)}, 201)
	}
	seen := map[string]bool{}
	path := "/v0/corpora?limit=1"
	cursor := ""
	for i := 0; i < 20; i++ {
		page := request(t, "GET", path, admin, nil, 200)
		for _, v := range page["items"].([]any) {
			id := v.(map[string]any)["corpus_id"].(string)
			if seen[id] {
				t.Fatal("duplicate page entry")
			}
			seen[id] = true
		}
		next, ok := page["next_page_cursor"].(string)
		if !ok {
			break
		}
		cursor = next
		path = "/v0/corpora?limit=1&page_cursor=" + url.QueryEscape(next)
	}
	full := request(t, "GET", "/v0/corpora", admin, nil, 200)
	if len(seen) != len(full["items"].([]any)) {
		t.Fatal("pagination omitted entries")
	}
	if cursor == "" {
		t.Fatal("missing cursor")
	}
	request(t, "GET", "/v0/corpora?page_cursor="+url.QueryEscape(cursor), os.Getenv("QUIVR_TEST_OTHER"), nil, 422)
	request(t, "GET", "/v0/corpora?page_cursor="+url.QueryEscape(cursor), os.Getenv("QUIVR_TEST_SCOPED"), nil, 422)
	request(t, "GET", "/v0/corpora?page_cursor="+url.QueryEscape(cursor+"x"), admin, nil, 422)
}
func TestConcurrentCreation(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Concurrent", "idempotency_key": "concurrent"}, 201)
			ids <- c["corpus_id"].(string)
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first != "" && first != id {
			t.Fatal("concurrent replay duplicated Corpus")
		}
		first = id
	}
	// JSON key order and explicit empty defaults must not change canonical identity.
	request(t, "POST", "/v0/corpora", admin, map[string]any{"retrieval": map[string]any{}, "idempotency_key": "concurrent", "name": "Concurrent"}, 201)
}
