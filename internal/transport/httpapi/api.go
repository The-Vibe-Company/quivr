package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	contract "github.com/The-Vibe-Company/quivr-v2/contracts/http/v0"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

type API struct {
	Content      content.Service
	Retrieval    retrieval.Service
	searchSchema *jsonschema.Schema
	ingestSchema *jsonschema.Schema
	Service      corpus.Service
	Keys         map[string]corpus.Scope
	CursorKey    []byte
	schema       *jsonschema.Schema
}

func New(store corpus.Store, contents content.Service, search retrieval.Service, keys map[string]corpus.Scope, cursorKey []byte) (http.Handler, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(contract.OpenAPI, &doc); err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("https://quivr.invalid/openapi", doc); err != nil {
		return nil, err
	}
	schema, err := compiler.Compile("https://quivr.invalid/openapi#/components/schemas/CorpusRequest")
	if err != nil {
		return nil, err
	}
	ingestSchema, err := compiler.Compile("https://quivr.invalid/openapi#/components/schemas/IngestCommand")
	if err != nil {
		return nil, err
	}
	searchSchema, err := compiler.Compile("https://quivr.invalid/openapi#/components/schemas/SearchRequest")
	if err != nil {
		return nil, err
	}
	a := &API{Retrieval: search, searchSchema: searchSchema, Content: contents, ingestSchema: ingestSchema, Service: corpus.Service{Store: store}, Keys: keys, CursorKey: cursorKey, schema: schema}
	return http.HandlerFunc(a.serve), nil
}
func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, status int, code string) {
	send(w, status, transport.Error{Code: code, Message: strings.ReplaceAll(code, "_", " "), Retryable: status == 503})
}
func (a *API) serve(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var requestID [16]byte
	_, _ = rand.Read(requestID[:])
	id := hex.EncodeToString(requestID[:])
	w.Header().Set("X-Request-ID", id)
	observed := &responseWriter{ResponseWriter: w}
	w = observed
	defer func() {
		slog.Info("http request", "method", r.Method, "request_id", id, "status", observed.status, "duration_ms", time.Since(start).Milliseconds())
	}()
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		failure(w, 401, "invalid_api_key")
		return
	}
	scope, ok := a.Keys[strings.TrimPrefix(auth, "Bearer ")]
	if !ok {
		failure(w, 401, "invalid_api_key")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	if r.Method == "POST" && r.URL.Path == "/v0/search" {
		a.search(w, r, scope)
		return
	}
	if a.contentRoutes(w, r, scope) {
		return
	}
	if r.URL.Path == "/v0/corpora" {
		switch r.Method {
		case "POST":
			if !scope.Allows("corpora:write") || !scope.AllCorpora() {
				failure(w, 403, "forbidden")
				return
			}
			a.create(w, r, scope)
		case "GET":
			if !scope.Allows("corpora:read") {
				failure(w, 403, "forbidden")
				return
			}
			a.list(w, r, scope)
		default:
			failure(w, 405, "method_not_allowed")
		}
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v0/corpora/") && !strings.Contains(strings.TrimPrefix(r.URL.Path, "/v0/corpora/"), "/") && r.Method == "GET" {
		if !scope.Allows("corpora:read") {
			failure(w, 403, "forbidden")
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v0/corpora/")
		if !scope.Contains(id) {
			failure(w, 404, "not_found")
			return
		}
		c, err := a.Service.Read(ctx, scope, id)
		if errors.Is(err, corpus.ErrNotFound) {
			failure(w, 404, "not_found")
		} else if err != nil {
			failure(w, 503, "storage_unavailable")
		} else {
			send(w, 200, c)
		}
		return
	}
	failure(w, 404, "not_found")
}
func (a *API) create(w http.ResponseWriter, r *http.Request, s corpus.Scope) {
	raw, ok := decodeRequest(w, r, a.schema)
	if !ok {
		return
	}
	data := raw.(map[string]any)
	if _, ok := data["retrieval"]; !ok {
		data["retrieval"] = map[string]any{}
	}
	canonical, err := json.Marshal(data)
	if err != nil {
		failure(w, 422, "invalid_schema")
		return
	}
	var input transport.CorpusRequest
	if err := json.Unmarshal(canonical, &input); err != nil {
		failure(w, 422, "invalid_schema")
		return
	}
	c, conflict, err := a.Service.Create(r.Context(), s, corpus.CreateInput{Key: input.IdempotencyKey, Name: input.Name, Retrieval: data["retrieval"].(map[string]any)})
	if errors.Is(err, corpus.ErrInvalidMapping) || errors.Is(err, corpus.ErrUnsupportedProfile) {
		failure(w, 422, err.Error())
	} else if errors.Is(err, corpus.ErrForbidden) {
		failure(w, 403, "forbidden")
	} else if err != nil {
		failure(w, 503, "storage_unavailable")
	} else if conflict {
		failure(w, 409, "idempotency_conflict")
	} else {
		send(w, 201, c)
	}
}

type cursor struct {
	After string `json:"after"`
	Scope string `json:"scope"`
}

func scopeDigest(s corpus.Scope) string {
	s.Actions = append([]string{}, s.Actions...)
	s.Corpora = append([]string{}, s.Corpora...)
	sort.Strings(s.Actions)
	sort.Strings(s.Corpora)
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(h[:])
}
func (a *API) sign(b []byte) []byte {
	h := hmac.New(sha256.New, a.CursorKey)
	h.Write(b)
	return h.Sum(nil)
}
func (a *API) list(w http.ResponseWriter, r *http.Request, s corpus.Scope) {
	q := r.URL.Query()
	for k, v := range q {
		if (k != "limit" && k != "page_cursor") || len(v) != 1 {
			failure(w, 422, "invalid_query")
			return
		}
	}
	limit := 100
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n < 1 || n > 100 {
			failure(w, 422, "invalid_limit")
			return
		}
		limit = n
	}
	after := ""
	scope := scopeDigest(s)
	if q.Has("page_cursor") {
		parts := strings.Split(q.Get("page_cursor"), ".")
		if len(parts) != 2 {
			failure(w, 422, "invalid_cursor")
			return
		}
		b, e1 := base64.RawURLEncoding.DecodeString(parts[0])
		sig, e2 := base64.RawURLEncoding.DecodeString(parts[1])
		var c cursor
		if e1 != nil || e2 != nil || !hmac.Equal(sig, a.sign(b)) || json.Unmarshal(b, &c) != nil || c.Scope != scope {
			failure(w, 422, "invalid_cursor")
			return
		}
		after = c.After
	}
	items, err := a.Service.List(r.Context(), s, after, limit+1)
	if err != nil {
		failure(w, 503, "storage_unavailable")
		return
	}
	page := map[string]any{"items": items}
	if len(items) > limit {
		page["items"] = items[:limit]
		b, _ := json.Marshal(cursor{items[limit-1].ID, scope})
		page["next_page_cursor"] = base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(a.sign(b))
	}
	send(w, 200, page)
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func decodeRequest(w http.ResponseWriter, r *http.Request, schema *jsonschema.Schema) (any, bool) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		failure(w, 415, "unsupported_media_type")
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			failure(w, 413, "request_too_large")
		} else {
			failure(w, 400, "malformed_json")
		}
		return nil, false
	}
	if !utf8.Valid(payload) {
		failure(w, 400, "malformed_json")
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var raw any
	if err := decoder.Decode(&raw); err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			failure(w, 413, "request_too_large")
		} else {
			failure(w, 400, "malformed_json")
		}
		return nil, false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		failure(w, 400, "malformed_json")
		return nil, false
	}
	if err := schema.Validate(raw); err != nil {
		failure(w, 422, "invalid_schema")
		return nil, false
	}
	return raw, true
}
