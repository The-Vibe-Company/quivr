package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/internal/backfill"
	"github.com/The-Vibe-Company/quivr/internal/changes"
	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/The-Vibe-Company/quivr/internal/observability"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	"github.com/The-Vibe-Company/quivr/internal/quarantine"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
	"github.com/The-Vibe-Company/quivr/internal/transport/routing"
	"github.com/The-Vibe-Company/quivr/internal/uploads"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type API struct {
	draining       func() bool
	processWork    context.Context
	router         *routing.Mux
	routes         http.Handler
	pushProxyCIDRs []string
	Content        content.Service
	Retrieval      retrieval.Service
	// Spaces lists a Corpus's vector spaces; nil answers 404.
	Spaces     SpaceRegistry
	Uploads    uploads.Service
	Changes    changes.Service
	Monitoring monitoring.Service
	Operations operations.Service

	Service   corpus.Service
	Keys      map[string]corpus.Scope
	CursorKey []byte

	schemas    map[string]*jsonschema.Schema
	Connectors connectors.Service

	// Commands counts accepted durable commands; the zero value ignores them.
	Commands telemetry.Commands
	// Relay serves the public webhook routes of push Connector Instances.
	Relay *connectors.Relay
	// Plugins serves the operator routes of the plugin registry.
	Plugins registry.Service

	// Activity serves the operator reads of document activity.
	Activity content.Activities
	// Recorder counts searches; nil counts nothing.
	Recorder *observability.Recorder
	// Stats serves the admin stats reads; without a Store they answer 404.
	Stats observability.Reader
	// Backfills and Promotions serve the backfill and vector space promotion
	// commands; nil answers 404.
	Backfills  *backfill.Service
	Promotions *backfill.Promotions

	// Quarantine serves the quarantine listing and reprocess command; nil
	// answers 404.
	Quarantine *quarantine.Service

	// changePoll is how often an open change stream reads the journal again.
	changePoll time.Duration
}

func New(store corpus.Store, contents content.Service, search retrieval.Service, uploadService uploads.Service, keys map[string]corpus.Scope, cursorKey []byte, options ...Option) (http.Handler, error) {
	contract, err := loadContract()
	if err != nil {
		return nil, err
	}
	a := &API{schemas: contract.schemas, Retrieval: search, Content: contents, Uploads: uploadService, Service: corpus.Service{Store: store, Namespaces: contents.ExtensionDeclared}, Keys: keys, CursorKey: cursorKey}
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
	mux := routing.New(a.routeFallback)
	a.routes = transport.HandlerWithOptions(transport.NewStrictHandler(a, nil), transport.StdHTTPServerOptions{BaseRouter: mux})
	for alias, target := range contract.aliases {
		if err := mux.HandleAlias(alias, target); err != nil {
			return nil, err
		}
	}
	a.router = mux
	return http.HandlerFunc(a.serveAccess), nil
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
	if isWebhookRoute(r.URL.Path) || isConnectorAPIPath(r.URL.Path) {
		a.routes.ServeHTTP(w, r)
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
	r = r.WithContext(context.WithValue(r.Context(), scopeContextKey{}, scope))
	// Streams and routed searches own their service deadlines.
	if r.Method == "GET" && r.URL.Path == "/v0/changes/stream" ||
		r.Method == "POST" && r.URL.Path == "/v0/search" && (a.Retrieval.Ranker != nil || a.Retrieval.ProfilesRouter != nil) {
		a.routes.ServeHTTP(w, r)
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
		deadline, _ := ctx.Deadline()
		_ = http.NewResponseController(w).SetReadDeadline(deadline)
	}
	a.routes.ServeHTTP(w, r)
}

// API implements every operation in the generated contract. Regeneration of a
// missing/renamed operation fails compilation rather than leaving a dead route.
var _ transport.StrictServerInterface = (*API)(nil)

type scopeContextKey struct{}

func requestScope(ctx context.Context) corpus.Scope {
	scope, _ := ctx.Value(scopeContextKey{}).(corpus.Scope)
	return scope
}

// Connector API authentication belongs to the declared plugin route. Malformed
// addresses in this namespace retain their public 404 before authentication.
func isConnectorAPIPath(path string) bool {
	if !strings.HasPrefix(path, "/v0/connectors/") {
		return false
	}
	parts := strings.SplitN(strings.TrimPrefix(path, "/v0/connectors/"), "/", 3)
	return len(parts) >= 2 && parts[1] == "api"
}

func (a *API) create(w http.ResponseWriter, r *http.Request, s corpus.Scope) {
	var input transport.CorpusRequest
	c, conflict, err := a.Service.Create(r.Context(), s, corpus.CreateInput{}, func() (corpus.CreateInput, error) {
		raw, ok := decodeRequest(w, r, a.schemas["CorpusRequest"])
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
			var c cursor
			if a.decodePage(corpusPageDomain, q.Get("page_cursor"), &c) != nil || c.Scope != scope {
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
		page["next_page_cursor"] = a.encodePage(corpusPageDomain, cursor{items[limit-1].ID, scope})
	}
	send(w, 200, page)
}

// writeDecodeError preserves the preview deadline when decoding finishes late.
// Size and header errors keep their own precedence before JSON decoding.
func writeDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	if r.Method == "POST" && r.URL.Path == "/v0/subscription-previews" {
		deadline, hasDeadline := r.Context().Deadline()
		if errors.Is(r.Context().Err(), context.DeadlineExceeded) || hasDeadline && !time.Now().Before(deadline) {
			err = publicerr.PreviewDeadlineExceeded
		}
	}
	writeError(w, err, nil)
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
			writeDecodeError(w, r, publicerr.MalformedJson)
		}
		return nil, nil, false
	}
	if !utf8.Valid(payload) {
		writeDecodeError(w, r, publicerr.MalformedJson)
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
			writeDecodeError(w, r, publicerr.MalformedJson)
		}
		return nil, nil, false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeDecodeError(w, r, publicerr.MalformedJson)
		return nil, nil, false
	}
	return raw, payload, true
}
