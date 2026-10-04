// Package x is the single X API fake used by Go and Python test suites.
package x

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed fixtures/*.json
var examples embed.FS

func example(name string) map[string]any {
	raw, err := examples.ReadFile("fixtures/" + name + ".json")
	if err != nil {
		panic(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return out
}

type rule struct {
	ID    string `json:"id"`
	Value string `json:"value"`
	Tag   string `json:"tag"`
}
type webhook struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Valid bool   `json:"valid"`
}
type failure struct {
	Status  int    `json:"status"`
	Reset   int64  `json:"reset"`
	ResetIn *int64 `json:"reset_in"`
	Times   *int   `json:"times"`
}
type list struct {
	IDs     map[string]bool
	Members []string
	Fail    *failure
	Error   string
}

type Fake struct {
	mu                       sync.Mutex
	token                    string
	lists                    map[string]*list
	posts                    map[string]map[string]any
	gone                     map[string]string
	user                     map[string]any
	lang                     string
	media                    []any
	pageSize, memberPageSize int
	fail                     *failure
	secret                   string
	crcFails                 bool
	rules                    []rule
	webhooks                 []webhook
	linked                   map[string]bool
	nextID                   int
	requests                 []string
	callbacks                *http.Client
}

func New(token string) *Fake {
	template := example("timeline")
	return &Fake{token: token, lists: map[string]*list{}, posts: map[string]map[string]any{}, gone: map[string]string{},
		user: template["includes"].(map[string]any)["users"].([]any)[0].(map[string]any), lang: "en", pageSize: 5, memberPageSize: 100,
		secret: "x-test-consumer-not-real", linked: map[string]bool{}, callbacks: &http.Client{Timeout: 5 * time.Second,
			CheckRedirect: func(r *http.Request, via []*http.Request) error {
				if len(via) >= 10 || !loopback(r.URL) {
					return fmt.Errorf("callback must stay on loopback")
				}
				return nil
			}}}
}

func loopback(u *url.URL) bool {
	return (u.Scheme == "http" || u.Scheme == "https") && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback())
}
func (f *Fake) list(id string) *list {
	if f.lists[id] == nil {
		f.lists[id] = &list{IDs: map[string]bool{}}
	}
	return f.lists[id]
}
func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}
func decode(w http.ResponseWriter, r *http.Request, body any) bool {
	if err := json.NewDecoder(r.Body).Decode(body); err != nil {
		reply(w, 400, map[string]string{"title": "Invalid control or request body"})
		return false
	}
	return true
}
func (f *Fake) newID(prefix string) string { f.nextID++; return fmt.Sprintf("%s-%d", prefix, f.nextID) }

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/_control/") {
		f.control(w, r)
		return
	}
	f.mu.Lock()
	if r.Method == http.MethodGet {
		f.requests = append(f.requests, r.URL.Path+"?"+r.URL.Query().Encode())
	} else {
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	}
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || auth == "" || strings.HasPrefix(auth, "x-revoked") || (f.token != "" && auth != f.token) {
		f.mu.Unlock()
		reply(w, 401, map[string]string{"title": "Unauthorized"})
		return
	}
	if f.failure(w, f.fail, func() { f.fail = nil }) {
		f.mu.Unlock()
		return
	}
	f.mu.Unlock()
	if r.Method == http.MethodPost && r.URL.Path == "/2/webhooks" {
		f.createWebhook(w, r)
		return
	}
	if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/2/webhooks/") {
		f.validateWebhook(w, r)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/2/lists/") && strings.HasSuffix(path, "/tweets"):
		f.timeline(w, r)
	case r.Method == http.MethodGet && path == "/2/tweets":
		body := example("lookup")
		delete(body, "data")
		delete(body, "errors")
		var data, errors []any
		for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
			if reason := f.gone[id]; reason != "" || f.posts[id] == nil {
				if reason == "" {
					reason = "resource-not-found"
				}
				e := example("lookup")["errors"].([]any)[0].(map[string]any)
				e["value"], e["resource_id"], e["type"] = id, id, "https://api.twitter.com/2/problems/"+reason
				errors = append(errors, e)
			} else {
				data = append(data, f.posts[id])
			}
		}
		if len(data) > 0 {
			body["data"] = data
		}
		if len(errors) > 0 {
			body["errors"] = errors
		}
		reply(w, 200, body)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/2/lists/") && strings.HasSuffix(path, "/members"):
		members := f.list(strings.TrimSuffix(strings.TrimPrefix(path, "/2/lists/"), "/members")).Members
		start, end, ok := page(r, "m", f.memberPageSize, len(members))
		if !ok {
			reply(w, 400, map[string]string{"title": "Invalid Request"})
			return
		}
		users := []any{}
		for _, id := range members[start:end] {
			users = append(users, map[string]string{"id": id, "username": "user" + id})
		}
		meta := map[string]any{"result_count": end - start}
		if end < len(members) {
			meta["next_token"] = fmt.Sprintf("m%d", end)
		}
		reply(w, 200, map[string]any{"data": users, "meta": meta})
	case r.Method == http.MethodGet && path == "/2/tweets/search/stream/rules":
		reply(w, 200, map[string]any{"data": f.rules})
	case r.Method == http.MethodPost && path == "/2/tweets/search/stream/rules":
		var body struct {
			Add    []rule `json:"add"`
			Delete struct {
				IDs []string `json:"ids"`
			} `json:"delete"`
		}
		if !decode(w, r, &body) {
			return
		}
		for _, item := range body.Add {
			item.ID = f.newID("rule")
			f.rules = append(f.rules, item)
		}
		f.rules = slices.DeleteFunc(f.rules, func(item rule) bool { return slices.Contains(body.Delete.IDs, item.ID) })
		reply(w, 200, map[string]any{"meta": map[string]string{"sent": time.Now().UTC().Format(time.RFC3339)}})
	case r.Method == http.MethodGet && path == "/2/webhooks":
		reply(w, 200, map[string]any{"data": f.webhooks})
	case r.Method == http.MethodGet && path == "/2/tweets/search/webhooks":
		ids := []string{}
		for id := range f.linked {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		links := []any{}
		for _, id := range ids {
			links = append(links, map[string]string{"webhook_id": id})
		}
		reply(w, 200, map[string]any{"data": links})
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/2/tweets/search/webhooks/"):
		f.linked[strings.TrimPrefix(path, "/2/tweets/search/webhooks/")] = true
		reply(w, 200, map[string]any{"data": map[string]bool{"provisioned": true}})
	default:
		reply(w, 404, map[string]string{"title": "Not Found"})
	}
}

func page(r *http.Request, prefix string, size, total int) (int, int, bool) {
	q := r.URL.Query()
	start := 0
	if token := q.Get("pagination_token"); token != "" {
		if !strings.HasPrefix(token, prefix) {
			return 0, 0, false
		}
		n, err := strconv.Atoi(strings.TrimPrefix(token, prefix))
		if err != nil || n < 0 || n > total {
			return 0, 0, false
		}
		start = n
	}
	if max := q.Get("max_results"); max != "" {
		n, err := strconv.Atoi(max)
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		size = min(n, size)
	}
	return start, min(start+size, total), true
}

func (f *Fake) timeline(w http.ResponseWriter, r *http.Request) {
	lst := f.list(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/2/lists/"), "/tweets"))
	if f.failure(w, lst.Fail, func() { lst.Fail = nil }) {
		return
	}
	if lst.Error != "" {
		reply(w, 200, map[string]any{"errors": []any{map[string]string{"resource_type": "list", "type": "https://api.twitter.com/2/problems/" + lst.Error}}})
		return
	}
	if r.URL.Query().Get("expansions") == "" || !strings.Contains(r.URL.Query().Get("tweet.fields"), "edit_history_tweet_ids") {
		reply(w, 400, map[string]string{"title": "Invalid Request"})
		return
	}
	ids := []string{}
	for id := range lst.IDs {
		versions := history(f.posts[id])
		if f.gone[id] == "" && id == versions[len(versions)-1] {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return len(ids[i]) > len(ids[j]) || len(ids[i]) == len(ids[j]) && ids[i] > ids[j] })
	start, end, ok := page(r, "p", f.pageSize, len(ids))
	if !ok {
		reply(w, 400, map[string]string{"title": "Invalid Request"})
		return
	}
	body := example("timeline")
	delete(body, "data")
	if start < end {
		data := []any{}
		for _, id := range ids[start:end] {
			data = append(data, f.posts[id])
		}
		body["data"] = data
	}
	includes := map[string]any{"users": []any{f.user}}
	if len(f.media) > 0 {
		includes["media"] = f.media
	}
	body["includes"] = includes
	meta := map[string]any{"result_count": end - start}
	if end < len(ids) {
		meta["next_token"] = fmt.Sprintf("p%d", end)
	}
	body["meta"] = meta
	reply(w, 200, body)
}

func (f *Fake) failure(w http.ResponseWriter, fail *failure, clear func()) bool {
	if fail == nil || fail.Status == 0 {
		return false
	}
	if fail.Times != nil {
		*fail.Times--
		if *fail.Times <= 0 {
			clear()
		}
	}
	reset := fail.Reset
	if fail.ResetIn != nil {
		reset = time.Now().Unix() + *fail.ResetIn
	}
	if reset != 0 {
		w.Header().Set("x-rate-limit-reset", strconv.FormatInt(reset, 10))
	}
	kind := "failure"
	if fail.Status == 402 {
		kind = "credits-depleted"
	}
	reply(w, fail.Status, map[string]string{"title": "Failure", "type": "https://api.x.com/2/problems/" + kind})
	return true
}

func history(post map[string]any) []string {
	out := []string{}
	for _, id := range post["edit_history_tweet_ids"].([]any) {
		out = append(out, id.(string))
	}
	return out
}

func (f *Fake) control(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/_control/state":
			reply(w, 200, map[string]any{"posts": f.posts, "rules": f.rules, "webhooks": f.webhooks, "linked": f.linked})
		case "/_control/requests":
			requests := f.requests
			if requests == nil {
				requests = []string{}
			}
			f.requests = nil
			reply(w, 200, requests)
		default:
			reply(w, 404, nil)
		}
		return
	}
	if r.Method != http.MethodPost {
		reply(w, 405, nil)
		return
	}
	if r.URL.Path == "/_control/app" {
		var body struct {
			Secret         *string         `json:"consumer_secret"`
			CRCFails       *bool           `json:"crc_fails"`
			PageSize       *int            `json:"page_size"`
			MemberPageSize *int            `json:"member_page_size"`
			User           map[string]any  `json:"user"`
			Lang           *string         `json:"lang"`
			Media          []any           `json:"media"`
			Fail           json.RawMessage `json:"fail"`
		}
		if !decode(w, r, &body) {
			return
		}
		if body.PageSize != nil && *body.PageSize <= 0 || body.MemberPageSize != nil && *body.MemberPageSize <= 0 {
			reply(w, 400, nil)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if body.Secret != nil {
			f.secret = *body.Secret
		}
		if body.CRCFails != nil {
			f.crcFails = *body.CRCFails
		}
		if body.PageSize != nil {
			f.pageSize = *body.PageSize
		}
		if body.MemberPageSize != nil {
			f.memberPageSize = *body.MemberPageSize
		}
		if body.User != nil {
			f.user = body.User
		}
		if body.Lang != nil {
			f.lang = *body.Lang
		}
		f.media = append(f.media, body.Media...)
		if body.Fail != nil {
			if err := json.Unmarshal(body.Fail, &f.fail); err != nil {
				reply(w, 400, nil)
				return
			}
		}
		reply(w, 200, map[string]bool{"ok": true})
		return
	}
	if r.URL.Path == "/_control/webhooks" {
		var body struct {
			Invalidate bool `json:"invalidate"`
		}
		if !decode(w, r, &body) {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if body.Invalidate {
			for i := range f.webhooks {
				f.webhooks[i].Valid = false
			}
		}
		reply(w, 200, map[string]bool{"ok": true})
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/_control/lists/") {
		reply(w, 404, nil)
		return
	}
	var body struct {
		Posts     []map[string]any `json:"posts"`
		Delete    []string         `json:"delete"`
		Protect   []string         `json:"protect"`
		Members   *[]string        `json:"members"`
		Fail      json.RawMessage  `json:"fail"`
		ListError *string          `json:"list_error"`
	}
	if !decode(w, r, &body) {
		return
	}
	f.mu.Lock()
	lst := f.list(strings.TrimPrefix(r.URL.Path, "/_control/lists/"))
	var pushed []struct {
		post   map[string]any
		forged bool
	}
	for _, input := range body.Posts {
		id, ok := input["id"].(string)
		if !ok || id == "" {
			f.mu.Unlock()
			reply(w, 400, nil)
			return
		}
		post := example("timeline")["data"].([]any)[0].(map[string]any)
		post["author_id"], post["lang"] = f.user["id"], f.lang
		post["edit_history_tweet_ids"] = []any{id}
		for k, v := range input {
			if k != "push" {
				post[k] = v
			}
		}
		ids, ok := post["edit_history_tweet_ids"].([]any)
		if !ok || len(ids) == 0 {
			f.mu.Unlock()
			reply(w, 400, nil)
			return
		}
		for _, old := range ids {
			if _, ok := old.(string); !ok {
				f.mu.Unlock()
				reply(w, 400, nil)
				return
			}
		}
		push := input["push"]
		forged := push == "forged"
		if !forged {
			for _, old := range history(post)[:len(ids)-1] {
				delete(lst.IDs, old)
				if earlier := f.posts[old]; earlier != nil {
					earlier["edit_history_tweet_ids"] = ids
				}
			}
			f.posts[id] = post
			lst.IDs[id] = true
		}
		if push == true || forged {
			pushed = append(pushed, struct {
				post   map[string]any
				forged bool
			}{post, forged})
		}
	}
	for _, id := range body.Delete {
		f.gone[id] = "resource-not-found"
		delete(lst.IDs, id)
	}
	for _, id := range body.Protect {
		f.gone[id] = "not-authorized-for-resource"
		delete(lst.IDs, id)
	}
	if body.Members != nil {
		lst.Members = *body.Members
	}
	if body.ListError != nil {
		lst.Error = *body.ListError
	}
	if body.Fail != nil {
		if err := json.Unmarshal(body.Fail, &lst.Fail); err != nil {
			f.mu.Unlock()
			reply(w, 400, nil)
			return
		}
	}
	f.mu.Unlock()
	deliveries := []any{}
	for _, p := range pushed {
		deliveries = append(deliveries, f.deliver(p.post, p.forged)...)
	}
	reply(w, 200, map[string]any{"ok": true, "deliveries": deliveries})
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func (f *Fake) crc(target string) bool {
	u, err := url.Parse(target)
	if err != nil || !loopback(u) {
		return false
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return false
	}
	token := hex.EncodeToString(nonce[:])
	q := u.Query()
	q.Set("crc_token", token)
	q.Set("nonce", token)
	u.RawQuery = q.Encode()
	f.mu.Lock()
	secret, forced := f.secret, f.crcFails
	f.mu.Unlock()
	resp, err := f.callbacks.Get(u.String())
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var body struct {
		Token string `json:"response_token"`
	}
	return resp.StatusCode == 200 && json.NewDecoder(resp.Body).Decode(&body) == nil && !forced && hmac.Equal([]byte(body.Token), []byte(sign(secret, []byte(token))))
}

func (f *Fake) createWebhook(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !f.crc(body.URL) {
		reply(w, 400, map[string]any{"errors": []any{map[string]string{"message": "CRC validation failed"}}})
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	hook := webhook{f.newID("hook"), body.URL, true}
	f.webhooks = append(f.webhooks, hook)
	reply(w, 200, map[string]any{"data": hook})
}

func (f *Fake) validateWebhook(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/2/webhooks/")
	f.mu.Lock()
	var target string
	for _, hook := range f.webhooks {
		if hook.ID == id {
			target = hook.URL
		}
	}
	f.mu.Unlock()
	if target == "" {
		reply(w, 404, nil)
		return
	}
	valid := f.crc(target)
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.webhooks {
		if f.webhooks[i].ID == id {
			f.webhooks[i].Valid = valid
			reply(w, 200, map[string]any{"data": f.webhooks[i]})
			return
		}
	}
}

func (f *Fake) deliver(post map[string]any, forged bool) []any {
	f.mu.Lock()
	body := example("delivery")
	body["data"] = post
	body["includes"] = map[string]any{"users": []any{f.user}}
	matched := []any{}
	for _, rule := range f.rules {
		if slices.Contains(strings.Split(rule.Value, " OR "), "from:"+fmt.Sprint(post["author_id"])) {
			matched = append(matched, map[string]string{"id": rule.ID, "tag": rule.Tag})
		}
	}
	body["matching_rules"] = matched
	urls := []string{}
	if len(matched) > 0 {
		for _, hook := range f.webhooks {
			if hook.Valid && f.linked[hook.ID] {
				urls = append(urls, hook.URL)
			}
		}
	}
	secret := f.secret
	if forged {
		secret = "x-forged-secret-not-real"
	}
	raw, err := json.Marshal(body)
	f.mu.Unlock()
	if err != nil {
		return nil
	}
	results := []any{}
	for _, target := range urls {
		u, err := url.Parse(target)
		if err != nil || !loopback(u) {
			continue
		}
		req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(raw))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-twitter-webhooks-signature", sign(secret, raw))
		status := 0
		resp, err := f.callbacks.Do(req)
		if err == nil {
			status = resp.StatusCode
			resp.Body.Close()
		}
		results = append(results, map[string]any{"url": target, "status": status})
	}
	return results
}
