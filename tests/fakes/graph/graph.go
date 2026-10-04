// Package graph implements the shared, local Microsoft Graph test subset.
package graph

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed fixtures/*.json
var examples embed.FS

func example(name string) map[string]any {
	b, err := examples.ReadFile("fixtures/" + name + ".json")
	if err != nil {
		panic(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return out
}

type app struct {
	Secret      string `json:"secret"`
	Expired     bool   `json:"expired"`
	Certificate bool   `json:"certificate"`
	TokenError  string `json:"token_error"`
}
type failure struct {
	Status     int    `json:"status"`
	Code       string `json:"code"`
	RetryAfter any    `json:"retry_after"`
	Path       string `json:"path"`
}
type change struct {
	seq int
	id  string
}
type attachment struct {
	meta map[string]any
	data []byte
}
type mailbox struct {
	messages        map[string]map[string]any
	order           []string
	changes         []change
	seq             int
	files           map[string]map[string]attachment
	fail            []failure
	extra           []any
	expire          bool
	delay           time.Duration
	pageSize        int
	attachmentsNext string
	stats           map[string]int
}

// Server owns isolated applications, bearer tokens and mailbox change logs.
type Server struct {
	mu       sync.Mutex
	apps     map[string]app
	tokens   map[string]string
	issued   int
	issuedBy map[string]int
	forms    map[string]url.Values
	boxes    map[string]*mailbox
}

func New() *Server {
	return &Server{apps: map[string]app{}, tokens: map[string]string{}, issuedBy: map[string]int{}, forms: map[string]url.Values{}, boxes: map[string]*mailbox{}}
}
func (s *Server) box(name string) *mailbox {
	name = strings.ToLower(name)
	b := s.boxes[name]
	if b == nil {
		b = &mailbox{messages: map[string]map[string]any{}, files: map[string]map[string]attachment{}, stats: map[string]int{"delta": 0, "attachments": 0, "value": 0, "resyncs": 0}}
		s.boxes[name] = b
	}
	return b
}
func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func graphError(w http.ResponseWriter, status int, code string) {
	out := example("error")
	out["error"].(map[string]any)["code"] = code
	reply(w, status, out)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/_fake/") {
		s.control(w, r)
		return
	}
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token") {
		s.token(w, r)
		return
	}
	if r.Method != http.MethodGet {
		graphError(w, 404, "ResourceNotFound")
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "v1.0" || parts[1] != "users" {
		graphError(w, 404, "ResourceNotFound")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || s.tokens[strings.TrimPrefix(auth, "Bearer ")] == "" {
		graphError(w, 401, "InvalidAuthenticationToken")
		return
	}
	if strings.HasPrefix(strings.ToLower(parts[2]), "unknown") {
		graphError(w, 404, "ErrorInvalidUser")
		return
	}
	b := s.box(parts[2])
	for i, f := range b.fail {
		if strings.Contains(r.URL.Path, f.Path) {
			b.fail = append(b.fail[:i], b.fail[i+1:]...)
			if f.RetryAfter != nil {
				w.Header().Set("Retry-After", fmt.Sprint(f.RetryAfter))
			}
			graphError(w, f.Status, f.Code)
			return
		}
	}
	rest := parts[3:]
	if len(rest) == 4 && rest[0] == "mailFolders" && rest[2] == "messages" && rest[3] == "delta" {
		b.stats["delta"]++
		// The restart harness deliberately holds a page in flight. Do not hold
		// the state lock; controls and other mailboxes remain usable.
		if delay := b.delay; delay > 0 {
			s.mu.Unlock()
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-r.Context().Done():
			}
			timer.Stop()
			s.mu.Lock()
			if r.Context().Err() != nil {
				return
			}
		}
		s.delta(w, r, b)
		return
	}
	if len(rest) >= 2 && rest[0] == "messages" {
		msg := b.messages[rest[1]]
		if msg == nil {
			graphError(w, 404, "ErrorItemNotFound")
			return
		}
		if len(rest) == 2 {
			reply(w, 200, msg)
			return
		}
		files := b.files[rest[1]]
		if len(rest) == 3 && rest[2] == "attachments" {
			b.stats["attachments"]++
			ids := make([]string, 0, len(files))
			for id := range files {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			value := []any{}
			for _, id := range ids {
				value = append(value, files[id].meta)
			}
			out := example("attachments")
			out["value"] = value
			if b.attachmentsNext != "" {
				out["@odata.nextLink"] = b.attachmentsNext
			}
			reply(w, 200, out)
			return
		}
		if len(rest) == 5 && rest[2] == "attachments" && rest[4] == "$value" {
			a, ok := files[rest[3]]
			if !ok {
				graphError(w, 404, "ErrorItemNotFound")
				return
			}
			if a.meta["@odata.type"] == "#microsoft.graph.referenceAttachment" {
				graphError(w, 405, "ErrorInvalidRequest")
				return
			}
			b.stats["value"]++
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(200)
			_, _ = w.Write(a.data)
			return
		}
	}
	graphError(w, 404, "ResourceNotFound")
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		graphError(w, 400, "ErrorInvalidRequest")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	client := r.PostForm.Get("client_id")
	s.forms[client] = r.PostForm
	a, ok := s.apps[client]
	code := a.TokenError
	status := 401
	if !ok {
		code = "700016"
		status = 400
	} else if a.Expired {
		code = "7000222"
	}
	valid := r.PostForm.Get("client_secret") == a.Secret && a.Secret != ""
	if a.Certificate {
		valid = r.PostForm.Get("client_assertion_type") == "urn:ietf:params:oauth:client-assertion-type:jwt-bearer" && len(strings.Split(r.PostForm.Get("client_assertion"), ".")) == 3
	}
	if code == "" && !valid {
		code = "7000215"
	}
	if code != "" {
		n, _ := strconv.Atoi(code)
		out := example("token-error")
		out["error_codes"] = []int{n}
		out["error_description"] = "AADSTS" + code + ": test refusal"
		if !ok {
			out["error"] = "unauthorized_client"
		}
		reply(w, status, out)
		return
	}
	s.issued++
	value := fmt.Sprintf("fake-access-%d", s.issued)
	s.tokens[value] = client
	s.issuedBy[client]++
	out := example("token")
	out["access_token"] = value
	reply(w, 200, out)
}

func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method == http.MethodGet && r.URL.Path == "/_fake/stats" {
		q := r.URL.Query()
		b := s.box(q.Get("mailbox"))
		out := map[string]any{}
		for k, v := range b.stats {
			out[k] = v
		}
		out["tokens_issued"] = s.issuedBy[q.Get("client_id")]
		out["token_form"] = s.forms[q.Get("client_id")]
		reply(w, 200, out)
		return
	}
	if r.Method != http.MethodPost {
		graphError(w, 404, "ResourceNotFound")
		return
	}
	var d struct {
		ClientID string `json:"client_id"`
		app
		Mailbox           string         `json:"mailbox"`
		ID                string         `json:"id"`
		Received          string         `json:"received"`
		Subject           string         `json:"subject"`
		HTML              *string        `json:"html"`
		Text              string         `json:"text"`
		InternetMessageID string         `json:"internet_message_id"`
		Message           map[string]any `json:"message"`
		Fields            map[string]any `json:"fields"`
		Attachments       []struct {
			ID          string         `json:"id"`
			Name        string         `json:"name"`
			Type        string         `json:"type"`
			ContentType string         `json:"content_type"`
			Size        int            `json:"size"`
			Data        string         `json:"data_b64"`
			Meta        map[string]any `json:"meta"`
		} `json:"attachments"`
		failure
		Count           *int    `json:"count"`
		Seconds         float64 `json:"seconds"`
		PageSize        int     `json:"page_size"`
		AttachmentsNext string  `json:"attachments_next"`
	}
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		graphError(w, 400, "ErrorInvalidRequest")
		return
	}
	b := s.box(d.Mailbox)
	switch r.URL.Path {
	case "/_fake/apps":
		if d.ClientID == "" {
			graphError(w, 400, "ErrorInvalidRequest")
			return
		}
		s.apps[d.ClientID] = d.app
	case "/_fake/mailbox":
		if d.PageSize < 0 {
			graphError(w, 400, "ErrorInvalidRequest")
			return
		}
		b.pageSize = d.PageSize
		b.attachmentsNext = d.AttachmentsNext
	case "/_fake/messages":
		msg := d.Message
		if msg == nil {
			msg = example("message")
			msg["id"] = d.ID
			msg["subject"] = d.Subject
			msg["internetMessageId"] = "<" + d.ID + "@example.org>"
			if d.InternetMessageID != "" {
				msg["internetMessageId"] = d.InternetMessageID
			}
			body := msg["body"].(map[string]any)
			body["contentType"] = "text"
			body["content"] = d.Text
			if d.HTML != nil {
				body["contentType"] = "html"
				body["content"] = *d.HTML
			}
			msg["toRecipients"] = []any{map[string]any{"emailAddress": map[string]any{"name": "Monitoring", "address": d.Mailbox}}}
			msg["sentDateTime"] = d.Received
			msg["receivedDateTime"] = d.Received
			msg["conversationId"] = "conv-" + d.ID
			msg["hasAttachments"] = len(d.Attachments) > 0
		}
		id, _ := msg["id"].(string)
		if id == "" {
			graphError(w, 400, "ErrorInvalidRequest")
			return
		}
		files := map[string]attachment{}
		for _, a := range d.Attachments {
			data, err := base64.StdEncoding.DecodeString(a.Data)
			if err != nil {
				graphError(w, 400, "ErrorInvalidRequest")
				return
			}
			meta := a.Meta
			if meta == nil {
				meta = example("attachments")["value"].([]any)[0].(map[string]any)
				kind := map[string]string{"": "fileAttachment", "file": "fileAttachment", "item": "itemAttachment", "reference": "referenceAttachment"}[a.Type]
				if kind == "" {
					graphError(w, 400, "ErrorInvalidRequest")
					return
				}
				meta["@odata.type"] = "#microsoft.graph." + kind
				meta["id"] = a.ID
				meta["name"] = a.Name
				if a.Name == "" {
					meta["name"] = a.ID
				}
				meta["contentType"] = a.ContentType
				if a.ContentType == "" {
					meta["contentType"] = "application/octet-stream"
				}
				meta["size"] = a.Size
				if a.Size == 0 {
					meta["size"] = len(data)
				}
			}
			aid, _ := meta["id"].(string)
			if aid == "" {
				graphError(w, 400, "ErrorInvalidRequest")
				return
			}
			files[aid] = attachment{meta, data}
		}
		if b.messages[id] == nil {
			b.order = append(b.order, id)
		}
		b.messages[id] = msg
		b.files[id] = files
		b.seq++
		b.changes = append(b.changes, change{b.seq, id})
	case "/_fake/update":
		msg := b.messages[d.ID]
		if msg == nil {
			graphError(w, 404, "ErrorItemNotFound")
			return
		}
		for k, v := range d.Fields {
			msg[k] = v
		}
		b.seq++
		b.changes = append(b.changes, change{b.seq, d.ID})
	case "/_fake/delete":
		delete(b.messages, d.ID)
		b.seq++
		b.changes = append(b.changes, change{b.seq, d.ID})
	case "/_fake/partial":
		b.extra = append(b.extra, map[string]any{"id": d.ID})
	case "/_fake/removed":
		b.extra = append(b.extra, map[string]any{"id": d.ID, "@removed": map[string]any{"reason": "deleted"}})
	case "/_fake/delete-attachment":
		messageID, ok := d.Fields["message_id"].(string)
		if !ok {
			graphError(w, 400, "ErrorInvalidRequest")
			return
		}
		delete(b.files[messageID], d.ID)
	case "/_fake/fail":
		count := 1
		if d.Count != nil {
			count = *d.Count
		}
		if d.Status < 400 || d.Status > 599 || count < 0 {
			graphError(w, 400, "ErrorInvalidRequest")
			return
		}
		for i := 0; i < count; i++ {
			b.fail = append(b.fail, d.failure)
		}
	case "/_fake/clear-failures":
		b.fail = nil
	case "/_fake/expire-delta":
		b.expire = true
	case "/_fake/delay":
		if d.Seconds < 0 || d.Seconds > 30 {
			graphError(w, 400, "ErrorInvalidRequest")
			return
		}
		b.delay = time.Duration(d.Seconds * float64(time.Second))
	default:
		graphError(w, 404, "ResourceNotFound")
		return
	}
	reply(w, 200, map[string]any{"ok": true})
}

func (s *Server) delta(w http.ResponseWriter, r *http.Request, b *mailbox) {
	q := r.URL.Query()
	size := 10
	if prefer := r.Header.Get("Prefer"); strings.Contains(prefer, "odata.maxpagesize=") {
		v := strings.Split(strings.SplitN(prefer, "odata.maxpagesize=", 2)[1], ",")[0]
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n <= 0 {
			graphError(w, 400, "ErrorInvalidRequest")
			return
		}
		size = n
	}
	if b.pageSize > 0 && b.pageSize < size {
		size = b.pageSize
	}
	mode := "i"
	start := b.seq
	offset := 0
	if token := q.Get("$skiptoken"); token != "" {
		p := strings.Split(token, ":")
		if len(p) != 3 || (p[0] != "i" && p[0] != "d") {
			graphError(w, 400, "ErrorInvalidRequest")
			return
		}
		mode = p[0]
		var err error
		start, err = strconv.Atoi(p[1])
		if err != nil {
			graphError(w, 400, "ErrorInvalidRequest")
			return
		}
		offset, err = strconv.Atoi(p[2])
		if err != nil {
			graphError(w, 400, "ErrorInvalidRequest")
			return
		}
	} else if token := q.Get("$deltatoken"); token != "" {
		if b.expire {
			b.expire = false
			b.stats["resyncs"]++
			graphError(w, 410, "syncStateNotFound")
			return
		}
		mode = "d"
		var err error
		start, err = strconv.Atoi(token)
		if err != nil {
			graphError(w, 400, "ErrorInvalidRequest")
			return
		}
	}
	if start < 0 || start > b.seq || offset < 0 {
		graphError(w, 400, "ErrorInvalidRequest")
		return
	}
	since := time.Time{}
	filter := q.Get("$filter")
	if filter != "" {
		v, ok := strings.CutPrefix(filter, "receivedDateTime ge ")
		var err error
		since, err = time.Parse(time.RFC3339, v)
		if !ok || err != nil {
			graphError(w, 400, "ErrorInvalidRequest")
			return
		}
	}
	ids := b.order
	if mode == "d" {
		ids = nil
		seen := map[string]bool{}
		for _, c := range b.changes {
			if c.seq > start && !seen[c.id] {
				ids = append(ids, c.id)
				seen[c.id] = true
			}
		}
	}
	entries := []any{}
	for _, id := range ids {
		msg := b.messages[id]
		if msg == nil {
			if mode == "d" {
				entries = append(entries, map[string]any{"id": id, "@removed": map[string]any{"reason": "deleted"}})
			}
			continue
		}
		stamp, _ := msg["receivedDateTime"].(string)
		received, _ := time.Parse(time.RFC3339, stamp)
		if !received.Before(since) {
			entries = append(entries, msg)
		}
	}
	// Explicit partial/tombstone scenarios supplement a single returned page.
	// They keep message hydration/deletion owners independent of polling state.
	if offset > len(entries) {
		graphError(w, 400, "ErrorInvalidRequest")
		return
	}
	end := offset + size
	if end > len(entries) {
		end = len(entries)
	}
	value := append([]any{}, entries[offset:end]...)
	value = append(value, b.extra...)
	b.extra = nil
	out := example("delta")
	delete(out, "@odata.nextLink")
	out["value"] = value
	keep := url.Values{}
	if filter != "" {
		keep.Set("$filter", filter)
	}
	base := "http://" + r.Host + r.URL.Path + "?"
	if end < len(entries) {
		keep.Set("$skiptoken", fmt.Sprintf("%s:%d:%d", mode, start, end))
		out["@odata.nextLink"] = base + keep.Encode()
	} else {
		if mode == "d" {
			start = b.seq
		}
		keep.Set("$deltatoken", strconv.Itoa(start))
		out["@odata.deltaLink"] = base + keep.Encode()
	}
	reply(w, 200, out)
}
