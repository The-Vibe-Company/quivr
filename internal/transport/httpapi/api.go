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

	"github.com/The-Vibe-Company/quivr-v2/contracts"
	"github.com/The-Vibe-Company/quivr-v2/internal/changes"
	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/The-Vibe-Company/quivr-v2/internal/observability"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/telemetry"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type API struct {
	Content   content.Service
	Retrieval retrieval.Service
	// Spaces lists a Corpus's vector spaces; nil answers 404.
	Spaces         SpaceRegistry
	Uploads        uploads.Service
	Changes        changes.Service
	Monitoring     monitoring.Service
	Operations     operations.Service
	actionSchema   *jsonschema.Schema
	configSchema   *jsonschema.Schema
	searchSchema   *jsonschema.Schema
	ingestSchema   *jsonschema.Schema
	uploadSchema   *jsonschema.Schema
	withdrawSchema *jsonschema.Schema
	batchSchema    *jsonschema.Schema
	Service        corpus.Service
	Keys           map[string]corpus.Scope
	CursorKey      []byte
	schema         *jsonschema.Schema
	monitoringSchemas
	Connectors       connectors.Service
	connectorSchema  *jsonschema.Schema
	scheduleSchema   *jsonschema.Schema
	credentialSchema *jsonschema.Schema
	// Commands counts accepted durable commands; the zero value ignores them.
	Commands telemetry.Commands
	// Relay serves the public webhook routes of push Connector Instances.
	Relay *connectors.Relay
	// Plugins serves the operator routes of the plugin registry.
	Plugins      registry.Service
	pluginSchema *jsonschema.Schema
	// pluginRollbackSchema validates the plan rollback command.
	pluginRollbackSchema *jsonschema.Schema
	// Activity serves the operator reads of document activity.
	Activity content.Activities
	// Recorder counts searches; nil counts nothing.
	Recorder *observability.Recorder
	// Stats serves the admin stats reads; without a Store they answer 404.
	Stats observability.Reader
}

func New(store corpus.Store, contents content.Service, search retrieval.Service, uploadService uploads.Service, keys map[string]corpus.Scope, cursorKey []byte, options ...Option) (http.Handler, error) {
	// The OpenAPI document references the shared Manifest schema, so compile
	// every contract resource together.
	compiler, err := contracts.NewCompiler()
	if err != nil {
		return nil, err
	}
	schema, err := compiler.Compile(contracts.HTTPSchema("CorpusRequest"))
	if err != nil {
		return nil, err
	}
	ingestSchema, err := compiler.Compile(contracts.HTTPSchema("IngestCommand"))
	if err != nil {
		return nil, err
	}
	searchSchema, err := compiler.Compile(contracts.HTTPSchema("SearchRequest"))
	if err != nil {
		return nil, err
	}
	uploadSchema, err := compiler.Compile(contracts.HTTPSchema("UploadRequest"))
	if err != nil {
		return nil, err
	}
	withdrawSchema, err := compiler.Compile(contracts.HTTPSchema("WithdrawalCommand"))
	if err != nil {
		return nil, err
	}
	batchSchema, err := compiler.Compile(contracts.HTTPSchema("BatchRequest"))
	if err != nil {
		return nil, err
	}
	configSchema, err := compiler.Compile(contracts.HTTPSchema("ConfigUpdate"))
	if err != nil {
		return nil, err
	}
	var monitored monitoringSchemas
	for name, target := range map[string]**jsonschema.Schema{"SavedQueryCreate": &monitored.savedQuery, "SavedQueryVersionCreate": &monitored.savedQueryVersion, "SubscriptionCreate": &monitored.subscription, "SubscriptionVersionCreate": &monitored.subscriptionVersion, "ActionRequest": &monitored.action, "SubscriptionPreviewRequest": &monitored.preview, "RenameRequest": &monitored.rename} {
		if *target, err = compiler.Compile(contracts.HTTPSchema(name)); err != nil {
			return nil, err
		}
	}
	connectorSchema, err := compiler.Compile(contracts.HTTPSchema("ConnectorCreate"))
	if err != nil {
		return nil, err
	}
	credentialSchema, err := compiler.Compile(contracts.HTTPSchema("CredentialReplace"))
	if err != nil {
		return nil, err
	}
	scheduleSchema, err := compiler.Compile(contracts.HTTPSchema("ScheduleChange"))
	if err != nil {
		return nil, err
	}
	pluginSchema, err := compiler.Compile(contracts.HTTPSchema("PluginRegistrationRequest"))
	if err != nil {
		return nil, err
	}
	pluginRollbackSchema, err := compiler.Compile(contracts.HTTPSchema("PipelinePlanRollbackRequest"))
	if err != nil {
		return nil, err
	}
	a := &API{pluginSchema: pluginSchema, pluginRollbackSchema: pluginRollbackSchema, monitoringSchemas: monitored, actionSchema: monitored.action, connectorSchema: connectorSchema, credentialSchema: credentialSchema, scheduleSchema: scheduleSchema, Retrieval: search, searchSchema: searchSchema, Content: contents, ingestSchema: ingestSchema, Uploads: uploadService, uploadSchema: uploadSchema, withdrawSchema: withdrawSchema, batchSchema: batchSchema, configSchema: configSchema, Service: corpus.Service{Store: store, Namespaces: contents.ExtensionDeclared}, Keys: keys, CursorKey: cursorKey, schema: schema}
	for _, option := range options {
		option(a)
	}
	return http.HandlerFunc(a.serve), nil
}

const (
	// maxRequestBytes bounds one single-command request body.
	maxRequestBytes = 1 << 20
	// maxBatchBytes and maxBatchEntries bound one ingestion batch envelope.
	maxBatchBytes   = 10 << 20
	maxBatchEntries = 100
)

func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func apiError(status int, code string) transport.Error {
	return transport.Error{Code: code, Message: strings.ReplaceAll(code, "_", " "), Retryable: status == 503}
}
func failure(w http.ResponseWriter, status int, code string) {
	send(w, status, apiError(status, code))
}

// publicCode returns the stable public code carried by err, never its text,
// so detail a domain adds to an error cannot change the API contract.
// fallback applies only to an error that carries no code. Domains wrap at most
// one coded sentinel per error, so the code matches the sentinel the caller
// selected the status from.
func publicCode(err error, fallback string) string {
	if code, ok := publicerr.Code(err); ok {
		return code
	}
	return fallback
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
	if isWebhookRoute(r.URL.Path) {
		// A source authenticates to the connector plugin, not with an API key.
		a.relayDelivery(w, r)
		return
	}
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
	if r.URL.Path == "/v0/changes/stream" && r.Method == "GET" {
		a.streamChanges(w, r, scope)
		return
	}
	// A search answered by a retrieval plugin runs under its profile's
	// max_latency_ms instead of the API's request deadline.
	if r.Method == "POST" && r.URL.Path == "/v0/search" && a.Retrieval.Ranker != nil {
		a.search(w, r, scope)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	if r.Method == "GET" && r.URL.Path == "/v0/changes" {
		a.pollChanges(w, r, scope)
		return
	}
	if r.Method == "POST" && r.URL.Path == "/v0/search" {
		a.search(w, r, scope)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/v0/search/profiles" {
		a.searchProfiles(w, scope)
		return
	}
	if a.contentRoutes(w, r, scope) {
		return
	}
	if a.uploadRoutes(w, r, scope) {
		return
	}
	if a.monitoringRoutes(w, r, scope) {
		return
	}
	if a.pluginRoutes(w, r, scope) {
		return
	}
	if a.adminDocumentRoutes(w, r, scope) {
		return
	}
	if a.statsRoutes(w, r, scope) {
		return
	}
	if a.operationRoutes(w, r, scope) {
		return
	}
	if a.matchRoutes(w, r, scope) {
		return
	}
	if a.connectorRoutes(w, r, scope) {
		return
	}
	if id, ok := vectorSpaceRoute(r); ok {
		a.listVectorSpaces(w, r, scope, id)
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
		failure(w, 422, publicCode(err, "invalid_mapping"))
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

// Page cursor signing domains. Every token signed with CursorKey names its
// domain so one kind is never accepted in place of another; the Change Cursor
// uses its own "change-cursor" domain in internal/changes.
const (
	corpusPageDomain    = "corpus-page"
	recordPageDomain    = "record-page"
	connectorPageDomain = "connector-page"
	matchPageDomain     = "match-page"
	attemptPageDomain   = "attempt-page"
	// subscriptionPageDomain signs the Subscription listing by owner.
	subscriptionPageDomain = "subscription-page"
	// documentPageDomain signs the admin list of latest documents.
	documentPageDomain = "document-page"
)

// signCursor is the only signer for CursorKey tokens; the domain is required.
func (a *API) signCursor(domain string, b []byte) []byte {
	h := hmac.New(sha256.New, a.CursorKey)
	h.Write([]byte(domain + "\x00"))
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
		if e1 != nil || e2 != nil || !hmac.Equal(sig, a.signCursor(corpusPageDomain, b)) || json.Unmarshal(b, &c) != nil || c.Scope != scope {
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
		page["next_page_cursor"] = base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(a.signCursor(corpusPageDomain, b))
	}
	send(w, 200, page)
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func decodeRequest(w http.ResponseWriter, r *http.Request, schema *jsonschema.Schema) (any, bool) {
	raw, _, ok := readJSON(w, r, maxRequestBytes)
	if !ok {
		return nil, false
	}
	if err := schema.Validate(raw); err != nil {
		failure(w, 422, "invalid_schema")
		return nil, false
	}
	return raw, true
}

// readJSON reads one bounded UTF-8 JSON document without interpreting its
// shape, returning it decoded and as the raw payload.
func readJSON(w http.ResponseWriter, r *http.Request, limit int64) (any, []byte, bool) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		failure(w, 415, "unsupported_media_type")
		return nil, nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			failure(w, 413, "request_too_large")
		} else {
			failure(w, 400, "malformed_json")
		}
		return nil, nil, false
	}
	if !utf8.Valid(payload) {
		failure(w, 400, "malformed_json")
		return nil, nil, false
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
		return nil, nil, false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		failure(w, 400, "malformed_json")
		return nil, nil, false
	}
	return raw, payload, true
}
