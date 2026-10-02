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
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr-v2/contracts"
	"github.com/The-Vibe-Company/quivr-v2/internal/backfill"
	"github.com/The-Vibe-Company/quivr-v2/internal/changes"
	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/The-Vibe-Company/quivr-v2/internal/observability"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
	"github.com/The-Vibe-Company/quivr-v2/internal/quarantine"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/telemetry"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type API struct {
	pushProxyCIDRs []string
	Content        content.Service
	Retrieval      retrieval.Service
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
	// evaluatorMigrationSchema validates the alert-rule migration command.
	evaluatorMigrationSchema   *jsonschema.Schema
	evaluationRetirementSchema *jsonschema.Schema
	// Activity serves the operator reads of document activity.
	Activity content.Activities
	// Recorder counts searches; nil counts nothing.
	Recorder *observability.Recorder
	// Stats serves the admin stats reads; without a Store they answer 404.
	Stats observability.Reader
	// Backfills and Promotions serve the backfill and vector space promotion
	// commands; nil answers 404.
	Backfills       *backfill.Service
	Promotions      *backfill.Promotions
	backfillSchema  *jsonschema.Schema
	promotionSchema *jsonschema.Schema
	// Quarantine serves the quarantine listing and reprocess command; nil
	// answers 404.
	Quarantine      *quarantine.Service
	reprocessSchema *jsonschema.Schema
	// changePoll is how often an open change stream reads the journal again.
	changePoll time.Duration
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
	evaluatorMigrationSchema, err := compiler.Compile(contracts.HTTPSchema("SubscriptionEvaluatorMigrationRequest"))
	if err != nil {
		return nil, err
	}
	evaluationRetirementSchema, err := compiler.Compile(contracts.HTTPSchema("EvaluationRetirementRequest"))
	if err != nil {
		return nil, err
	}
	backfillSchema, err := compiler.Compile(contracts.HTTPSchema("BackfillRequest"))
	if err != nil {
		return nil, err
	}
	promotionSchema, err := compiler.Compile(contracts.HTTPSchema("VectorSpacePromotionRequest"))
	if err != nil {
		return nil, err
	}
	reprocessSchema, err := compiler.Compile(contracts.HTTPSchema("QuarantineReprocessRequest"))
	if err != nil {
		return nil, err
	}
	a := &API{backfillSchema: backfillSchema, promotionSchema: promotionSchema, reprocessSchema: reprocessSchema, pluginSchema: pluginSchema, pluginRollbackSchema: pluginRollbackSchema, evaluatorMigrationSchema: evaluatorMigrationSchema, monitoringSchemas: monitored, actionSchema: monitored.action, connectorSchema: connectorSchema, credentialSchema: credentialSchema, scheduleSchema: scheduleSchema, Retrieval: search, searchSchema: searchSchema, Content: contents, ingestSchema: ingestSchema, Uploads: uploadService, uploadSchema: uploadSchema, withdrawSchema: withdrawSchema, batchSchema: batchSchema, configSchema: configSchema, Service: corpus.Service{Store: store, Namespaces: contents.ExtensionDeclared}, Keys: keys, CursorKey: cursorKey, schema: schema}
	a.evaluationRetirementSchema = evaluationRetirementSchema
	a.Content.Corpora = store
	for _, option := range options {
		option(a)
	}
	a.Changes.Corpora = store
	for _, raw := range a.pushProxyCIDRs {
		if _, err := netip.ParsePrefix(raw); err != nil {
			return nil, errors.New("trusted push proxies require CIDRs")
		}
	}
	return http.HandlerFunc(a.servePushAudited), nil
}

const (
	// maxRequestBytes bounds one single-command request body.
	maxRequestBytes = 1 << 20
	// maxBatchBytes and maxBatchEntries bound one ingestion batch envelope.
	maxBatchBytes   = 10 << 20
	maxBatchEntries = 100
	requestTimeout  = 5 * time.Second
	// Leave response headroom within the server write timeout.
	batchTimeout = 8 * time.Second
)

func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// pageLimit reads a list's optional limit parameter. Absent, it is def; any
// present value outside 1..max, the empty one included, is 422 invalid_limit
// on every route.
func pageLimit(w http.ResponseWriter, q url.Values, def, max int) (int, bool) {
	if !q.Has("limit") {
		return def, true
	}
	n, err := strconv.Atoi(q.Get("limit"))
	if err != nil || n < 1 || n > max {
		writeError(w, publicerr.InvalidLimit, nil)
		return 0, false
	}
	return n, true
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
	if a.connectorAPIRoute(w, r) {
		return
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		writeError(w, publicerr.InvalidApiKey, nil)
		return
	}
	scope, ok := a.Keys[strings.TrimPrefix(auth, "Bearer ")]
	if !ok {
		writeError(w, publicerr.InvalidApiKey, nil)
		return
	}
	if r.URL.Path == "/v0/changes/stream" && r.Method == "GET" {
		a.streamChanges(w, r, scope)
		return
	}
	// A search answered by a retrieval plugin runs under its profile's hard
	// bound (plugins.RetrievalProfile.Deadline) instead of the API's request
	// deadline.
	if r.Method == "POST" && r.URL.Path == "/v0/search" && (a.Retrieval.Ranker != nil || a.Retrieval.ProfilesRouter != nil) {
		a.search(w, r, scope)
		return
	}
	timeout := requestTimeout
	isBatch := r.Method == "POST" && r.URL.Path == "/v0/records/batch"
	if isBatch {
		timeout = batchTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	r = r.WithContext(ctx)
	if isBatch {
		// A context deadline alone cannot interrupt a blocked request-body read.
		deadline, _ := ctx.Deadline()
		_ = http.NewResponseController(w).SetReadDeadline(deadline)
	}
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
	if a.evaluatorMigrationRoutes(w, r, scope) {
		return
	}
	if a.evaluationAdministrationRoutes(w, r, scope) {
		return
	}
	if a.backfillRoutes(w, r, scope) {
		return
	}
	if a.quarantineRoutes(w, r, scope) {
		return
	}
	if a.adminDocumentRoutes(w, r, scope) {
		return
	}
	if a.activePluginRoutes(w, r, scope) {
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
	if a.connectorTokenRoutes(w, r, scope) {
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
			a.create(w, r, scope)
		case "GET":
			a.list(w, r, scope)
		default:
			writeError(w, publicerr.MethodNotAllowed, nil)
		}
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v0/corpora/") && !strings.Contains(strings.TrimPrefix(r.URL.Path, "/v0/corpora/"), "/") && r.Method == "GET" {
		id := strings.TrimPrefix(r.URL.Path, "/v0/corpora/")
		c, err := a.Service.Read(ctx, scope, id)
		if err != nil {
			writeError(w, err, publicerr.StorageUnavailable)
		} else {
			send(w, 200, c)
		}
		return
	}
	writeError(w, publicerr.NotFound, nil)
}
func (a *API) create(w http.ResponseWriter, r *http.Request, s corpus.Scope) {
	var input transport.CorpusRequest
	c, conflict, err := a.Service.Create(r.Context(), s, corpus.CreateInput{}, func() (corpus.CreateInput, error) {
		raw, ok := decodeRequest(w, r, a.schema)
		if !ok {
			return corpus.CreateInput{}, errResponseWritten
		}
		data := raw.(map[string]any)
		if _, ok := data["retrieval"]; !ok {
			data["retrieval"] = map[string]any{}
		}
		canonical, err := json.Marshal(data)
		if err != nil {
			writeError(w, publicerr.InvalidSchema, nil)
			return corpus.CreateInput{}, errResponseWritten
		}
		if err := json.Unmarshal(canonical, &input); err != nil {
			writeError(w, publicerr.InvalidSchema, nil)
			return corpus.CreateInput{}, errResponseWritten
		}

		return corpus.CreateInput{Key: input.IdempotencyKey, Name: input.Name, Retrieval: data["retrieval"].(map[string]any)}, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
	} else if conflict {
		writeError(w, publicerr.IdempotencyConflict, nil)
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
	// quarantinePageDomain signs the admin list of quarantined Versions.
	quarantinePageDomain = "quarantine-page"
)

// signCursor is the only signer for CursorKey tokens; the domain is required.
func (a *API) signCursor(domain string, b []byte) []byte {
	h := hmac.New(sha256.New, a.CursorKey)
	h.Write([]byte(domain + "\x00"))
	h.Write(b)
	return h.Sum(nil)
}

func (a *API) list(w http.ResponseWriter, r *http.Request, s corpus.Scope) {
	var limit int
	var ok bool
	var scope string
	items, err := a.Service.List(r.Context(), s, "", 0, func() (string, int, error) {
		q := r.URL.Query()
		for k, v := range q {
			if (k != "limit" && k != "page_cursor") || len(v) != 1 {
				writeError(w, publicerr.InvalidQuery, nil)
				return "", 0, errResponseWritten
			}
		}
		limit, ok = pageLimit(w, q, 100, 100)
		if !ok {
			return "", 0, errResponseWritten
		}
		after := ""
		scope = scopeDigest(s)
		if q.Has("page_cursor") {
			parts := strings.Split(q.Get("page_cursor"), ".")
			if len(parts) != 2 {
				writeError(w, publicerr.InvalidCursor, nil)
				return "", 0, errResponseWritten
			}
			b, e1 := base64.RawURLEncoding.DecodeString(parts[0])
			sig, e2 := base64.RawURLEncoding.DecodeString(parts[1])
			var c cursor
			if e1 != nil || e2 != nil || !hmac.Equal(sig, a.signCursor(corpusPageDomain, b)) || json.Unmarshal(b, &c) != nil || c.Scope != scope {
				writeError(w, publicerr.InvalidCursor, nil)
				return "", 0, errResponseWritten
			}
			after = c.After
		}

		return after, limit + 1, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
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
		writeError(w, publicerr.InvalidSchema, nil)
		return nil, false
	}
	return raw, true
}

// readJSON reads one bounded UTF-8 JSON document without interpreting its
// shape, returning it decoded and as the raw payload.
func readJSON(w http.ResponseWriter, r *http.Request, limit int64) (any, []byte, bool) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, publicerr.UnsupportedMediaType, nil)
		return nil, nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		var large *http.MaxBytesError
		deadline, hasDeadline := r.Context().Deadline()
		if errors.As(err, &large) {
			writeError(w, publicerr.RequestTooLarge, nil)
		} else if r.Method == "POST" && r.URL.Path == "/v0/records/batch" &&
			(r.Context().Err() != nil || hasDeadline && !time.Now().Before(deadline)) {
			// The connection's read deadline may fire before the context timer.
			writeError(w, publicerr.ContentUnavailable, nil)
		} else {
			writeError(w, publicerr.MalformedJson, nil)
		}
		return nil, nil, false
	}
	if !utf8.Valid(payload) {
		writeError(w, publicerr.MalformedJson, nil)
		return nil, nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var raw any
	if err := decoder.Decode(&raw); err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			writeError(w, publicerr.RequestTooLarge, nil)
		} else {
			writeError(w, publicerr.MalformedJson, nil)
		}
		return nil, nil, false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, publicerr.MalformedJson, nil)
		return nil, nil, false
	}
	return raw, payload, true
}
