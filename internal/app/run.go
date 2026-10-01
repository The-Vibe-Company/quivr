package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	s3store "github.com/The-Vibe-Company/quivr-v2/internal/adapters/s3"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/tei"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/weaviate"
	"github.com/The-Vibe-Company/quivr-v2/internal/backfill"
	"github.com/The-Vibe-Company/quivr-v2/internal/changes"
	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/The-Vibe-Company/quivr-v2/internal/normalization"
	"github.com/The-Vibe-Company/quivr-v2/internal/observability"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	orchestration "github.com/The-Vibe-Company/quivr-v2/internal/orchestration/temporal"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	pluginregistry "github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr-v2/internal/processing"
	"github.com/The-Vibe-Company/quivr-v2/internal/quarantine"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/telemetry"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

type Config struct {
	// TEIURL encodes queries for generations built before the core.ingest
	// plugin (THE-777), which serve the legacy E5 space until rebuilt.
	TEIURL          string                  `json:"tei_url"`
	WeaviateURL     string                  `json:"weaviate_url"`
	TemporalAddress string                  `json:"temporal_address"`
	S3              s3store.Config          `json:"s3"`
	LogDirectory    string                  `json:"log_directory"`
	DatabaseURL     string                  `json:"database_url"`
	Listen          string                  `json:"listen"`
	ProbeListen     string                  `json:"probe_listen"`
	CursorKey       string                  `json:"cursor_key"`
	ChangeRetention string                  `json:"change_retention"`
	Keys            map[string]corpus.Scope `json:"keys"`
	// Destinations are deployment-configured webhook receivers. Real
	// deployments reference their signing secret through secret_env.
	Destinations map[string]monitoring.Destination `json:"destinations"`
	// Delivery overrides the webhook retry policy (worker only).
	Delivery DeliveryConfig `json:"delivery"`
	// CredentialKey encrypts Deposited Credentials at rest (32+ bytes). It is
	// optional: without it credential deposits and rotations are refused.
	CredentialKey string `json:"credential_key"`
	// ConnectorFixtures enables the deterministic fixture connector kind (local/CI only).
	ConnectorFixtures bool `json:"connector_fixtures"`
	// ConnectorMinInterval is the polling-interval floor (Go duration, default 30s).
	ConnectorMinInterval string `json:"connector_min_interval"`
	// PublicURL is the base URL where sources reach this deployment's API
	// (https://quivr.example.com). Instances of a kind that declares the push
	// mode get their webhook address from it; without it they only poll.
	PublicURL string `json:"public_url"`
	// M365 is refused: the m365_mail kind moved to the connector.m365_mail
	// plugin, whose pin configuration carries its endpoints.
	M365 json.RawMessage `json:"m365"`
	// Plugin pins one external plugin; it is shorthand for a one-item
	// Plugins list and may be combined with it (it comes first).
	Plugin *plugins.PinConfig `json:"plugin"`
	// Plugins pins external plugins together: normalizers are routed by Blob
	// media type, subscription evaluators by plugin id and version, connector
	// kinds by name beside the built-in kinds (a kind with two providers is a
	// conflict; a connector plugin needs https unless it is on loopback). API and
	// worker refuse to start on an invalid pin or a conflict between pins; an
	// unreachable plugin never prevents startup.
	Plugins []plugins.PinConfig `json:"plugins"`
	// MonitoringFixtureEvaluator installs the deterministic fixture evaluator
	// quivr.fixture@1 (local and CI test deployments only).
	MonitoringFixtureEvaluator bool `json:"monitoring_fixture_evaluator"`

	// ChangePrune tunes the worker's change-journal prune (THE-697).
	ChangePrune ChangePruneConfig `json:"change_prune"`
	// PluginPlanPoll is how often api and worker check whether the active
	// Pipeline Plan changed, to follow it without restart (Go duration,
	// default 2s).
	PluginPlanPoll string `json:"plugin_plan_poll"`
	// ChangeStreamPoll is how often an open change stream reads the journal
	// again (Go duration, default 250ms).
	ChangeStreamPoll string `json:"change_stream_poll"`
	// PinnedPluginAttempts is how many attempts work pinned to a Pipeline
	// Plan gets once a plugin of that plan has left the active plan and cannot
	// be reached, before the work stops with a pinned_plugin_unavailable
	// diagnostic (worker only; default 10).
	PinnedPluginAttempts int `json:"pinned_plugin_attempts"`
	// ProjectionPurgeGrace delays the physical purge of dead projection
	// objects (Go duration, default 1h; worker only).
	ProjectionPurgeGrace string `json:"projection_purge_grace"`
	// MigrationWait is how long the worker waits at startup for migrations
	// the api has not applied yet before it exits (Go duration, default 5m;
	// 0s exits at once; worker only).
	MigrationWait string `json:"migration_wait"`
	// Observability configures the plugin call, search and step counters
	// (THE-795): record_query_text (default false) also counts searches by
	// their normalized text, which then stays in PostgreSQL for 7 days, and
	// flush_interval (default 5s) is how often each process writes its
	// counts, so how much a crash can lose.
	Observability observability.Config `json:"observability"`
	// Backfill sets how backfills run (Spec 5): rate (Versions per second,
	// default 2) and poll (how often a paused backfill checks for resume,
	// default 5s) in the worker, and max_cost_without_confirmation (US
	// dollars, default 0) in the api: a dry run estimated above it needs
	// confirm_cost.
	Backfill BackfillConfig `json:"backfill"`
}

// connectorSealer builds the Deposited Credential sealer. credential_key is
// optional; a configured key shorter than 32 bytes is a startup error.
func (cfg Config) connectorSealer(logger *slog.Logger) (connectors.Sealer, error) {
	if cfg.CredentialKey == "" {
		logger.Info("credential deposits disabled", "reason", "credential_key not configured")
		return connectors.NewKeylessSealer(cfg.CursorKey)
	}
	return connectors.NewSealer(cfg.CredentialKey)
}

// ConfigEnv names the environment variable holding the path of the JSON
// configuration file every engine command reads.
const ConfigEnv = "QUIVR_CONFIG"

// Command is one engine process `quivr <name>` runs from the configuration
// file. The table is the source of the generated CLI reference.
type Command struct {
	Name    string
	Summary string
}

// Commands lists the engine commands in help order.
var Commands = []Command{
	{Name: "api", Summary: "Serve the public HTTP API until interrupted."},
	{Name: "worker", Summary: "Run the background work that processes content, pulls connectors and delivers events, until interrupted."},
	{Name: "migrate", Summary: "Prepare PostgreSQL, object storage and the search projections, then exit. Rerun it to finish a step whose dependency was not ready."},
}

// engineUsage is the engine usage line, built from Commands.
func engineUsage() string {
	names := make([]string, len(Commands))
	for i, c := range Commands {
		names[i] = c.Name
	}
	return "usage: quivr " + strings.Join(names, "|")
}

func isCommand(name string) bool {
	for _, c := range Commands {
		if c.Name == name {
			return true
		}
	}
	return false
}

func Run(command string) error {
	if !isCommand(command) {
		return errors.New(engineUsage())
	}
	b, err := os.ReadFile(os.Getenv(ConfigEnv))
	if err != nil {
		return errors.New("read QUIVR_CONFIG file failed")
	}
	var cfg Config
	if err = json.Unmarshal(b, &cfg); err != nil {
		return errors.New("invalid configuration JSON")
	}
	if len(cfg.M365) > 0 && string(cfg.M365) != "null" {
		return errors.New("m365 moved to the connector.m365_mail plugin's configuration; pin plugins/m365-mail with login_endpoint and graph_endpoint (https://docs.quivr.thevibecompany.co/guides/microsoft-365)")
	}
	// Validate the pins before logs move to files, so a refusal is reported on stderr.
	pins, err := cfg.loadPins(command)
	if err != nil {
		return err
	}
	// api and worker follow the active Pipeline Plan, resolved once the
	// database is reachable; until then, the pins. Its plugins own their
	// declared extension namespaces beside the built-in ones: clients cannot
	// write them, retrieval mappings may read them.
	live, err := plugins.NewLive("", pins)
	if err != nil {
		return err
	}
	if cfg.LogDirectory != "" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(&rotatingLog{path: filepath.Join(cfg.LogDirectory, command+".log")}, nil)))
	}
	if cfg.DatabaseURL == "" || len(cfg.CursorKey) < 32 || len(cfg.Keys) == 0 {
		return errors.New("database_url, cursor_key (32+ bytes) and keys required")
	}
	logger := slog.Default()
	if command == "migrate" {
		// Only api and worker handle credentials; migrate stays silent about them.
		logger = slog.New(slog.DiscardHandler)
	}
	sealer, err := cfg.connectorSealer(logger)
	if err != nil {
		return err
	}
	minInterval := connectors.DefaultMinInterval
	if cfg.ConnectorMinInterval != "" {
		if minInterval, err = time.ParseDuration(cfg.ConnectorMinInterval); err != nil || minInterval <= 0 {
			return errors.New("connector_min_interval must be a positive duration")
		}
	}
	var builtinKinds []connectors.Connector
	if cfg.ConnectorFixtures {
		builtinKinds = append(builtinKinds, connectors.Fixture{})
	}
	// Connector kinds of the plan's plugins resolve beside the enabled
	// built-in kinds; a kind with two providers refuses startup, and an
	// activation.
	kindsOf := func(set *plugins.PinSet) []connectors.Connector {
		return append(append([]connectors.Connector{}, builtinKinds...), pluginhttp.Connectors(set)...)
	}
	registry, err := connectors.NewRegistry(kindsOf(pins)...)
	if err != nil {
		return err
	}
	planPoll := 2 * time.Second
	if cfg.PluginPlanPoll != "" {
		if planPoll, err = time.ParseDuration(cfg.PluginPlanPoll); err != nil || planPoll <= 0 {
			return errors.New("plugin_plan_poll must be a positive duration")
		}
	}
	backfillSettings, err := cfg.Backfill.settings()
	if err != nil {
		return err
	}
	pinnedAttempts := 10
	switch {
	case cfg.PinnedPluginAttempts < 0:
		return errors.New("pinned_plugin_attempts must be a positive number")
	case cfg.PinnedPluginAttempts > 0:
		pinnedAttempts = cfg.PinnedPluginAttempts
	}
	if err := validPublicURL(cfg.PublicURL); err != nil {
		return err
	}
	for token, s := range cfg.Keys {
		if len(token) < 32 || s.Organization == "" || len(s.Actions) == 0 || len(s.Corpora) == 0 {
			return errors.New("invalid scoped credential configuration")
		}
	}
	if err := validateDestinations(cfg.Destinations, cfg.Delivery.AllowPrivateDestinations); err != nil {
		return err
	}
	retryPolicy, deliveryTimeout, err := cfg.Delivery.parse()
	if err != nil {
		return err
	}
	retention := changes.DefaultRetention
	if cfg.ChangeRetention != "" {
		if retention, err = time.ParseDuration(cfg.ChangeRetention); err != nil || retention <= 0 {
			return errors.New("change_retention must be a positive duration")
		}
	}
	streamPoll := httpapi.DefaultStreamPoll
	if cfg.ChangeStreamPoll != "" {
		if streamPoll, err = time.ParseDuration(cfg.ChangeStreamPoll); err != nil || streamPoll <= 0 {
			return errors.New("change_stream_poll must be a positive duration")
		}
	}
	prune, err := cfg.ChangePrune.parse(retention)
	if err != nil {
		return err
	}
	purgeGrace := retrieval.DefaultPurgeGrace
	if cfg.ProjectionPurgeGrace != "" {
		if purgeGrace, err = time.ParseDuration(cfg.ProjectionPurgeGrace); err != nil || purgeGrace <= 0 {
			return errors.New("projection_purge_grace must be a positive duration")
		}
	}
	schemaStartup := schemaWait{First: 250 * time.Millisecond, Max: 5 * time.Second}
	if command == "worker" {
		schemaStartup.Limit = defaultMigrationWait
		if cfg.MigrationWait != "" {
			if schemaStartup.Limit, err = time.ParseDuration(cfg.MigrationWait); err != nil || schemaStartup.Limit < 0 {
				return errors.New("migration_wait must be a Go duration, 0s or more")
			}
		}
	}
	if _, ok := cfg.Observability.Interval(); !ok {
		return errors.New("observability.flush_interval must be a positive Go duration")
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:8080"
	}
	if cfg.ProbeListen == "" {
		cfg.ProbeListen = "127.0.0.1:8081"
	}
	// The engine segments and embeds nothing itself (THE-777): api and worker
	// need a pinned ingestion plugin, normally the first-party core.ingest.
	if command != "migrate" && pins.Ingestion() == nil {
		return errors.New("no ingestion plugin pinned: pin plugins/core-ingest (core.ingest) or another ingestion plugin in `plugins` (plugins/core-ingest/README.md)")
	}
	// Nor does it rank search results itself (THE-779): the api needs a
	// pinned retrieval plugin, normally the first-party core.retrieve.
	if command == "api" && pins.Retrieval() == nil {
		return errors.New("no retrieval plugin pinned: pin plugins/core-retrieve (core.retrieve) or another retrieval plugin in `plugins` (plugins/core-retrieve/README.md)")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return errors.New("invalid database configuration")
	}
	defer pool.Close()
	if cfg.WeaviateURL == "" || cfg.TemporalAddress == "" || cfg.S3.Endpoint == "" || cfg.S3.Bucket == "" || cfg.S3.AccessKey == "" || cfg.S3.SecretKey == "" {
		return errors.New("Temporal and S3 configuration required")
	}
	blobs := s3store.New(cfg.S3)
	store := postgres.ContentStore{Pool: pool}
	// Plugin calls, searches, processing steps, documents received and
	// Matches are counted in memory and flushed as rollups; only the worker
	// deletes expired ones. The recorder runs once the plan is resolved.
	rollups := postgres.ObservabilityStore{Pool: pool}
	recorder := observability.NewRecorder(rollups, cfg.Observability, command == "worker")
	// Normalizer routes and extension namespaces follow the plan.
	contents := content.Service{Repository: store, Catalog: store, Blobs: blobs, Baseline: store, Embeddings: store, BlobSource: store, Relations: store, Extensions: live, Normalizations: store, Supersession: store, Routes: live,
		Received: recorder.Received}
	uploadService := uploads.Service{Store: store, Transfer: blobs}
	projection := weaviate.New(cfg.WeaviateURL)
	projection.LegacySpace = tei.Space().ID
	if command == "migrate" {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		// The PostgreSQL part runs first and alone needs no other dependency;
		// rerunning migrate completes the S3 and Weaviate steps.
		if err = BootstrapDatabase(ctx, pool, DeploymentSpaces(cfg.migrationPins())); err != nil {
			if errors.Is(err, content.ErrSpaceOwner) || errors.Is(err, content.ErrSpaceChanged) {
				return err
			}
			return errors.New("migration failed; check database connectivity and schema")
		}
		for {
			if err = blobs.Bootstrap(ctx); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return errors.New("S3 bootstrap deadline exceeded")
			case <-time.After(200 * time.Millisecond):
			}
		}
		for {
			if err = projection.Bootstrap(ctx, weaviate.InitialCollection); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return errors.New("projection bootstrap deadline exceeded")
			case <-time.After(200 * time.Millisecond):
			}
		}
		slog.Info("migrations complete")
		return nil
	}
	connectorStore := postgres.ConnectorStore{ContentStore: store}
	acquisition := &orchestration.Connectors{Scheduler: connectorStore, Acquirer: connectors.Acquirer{PublicURL: cfg.PublicURL, Store: connectorStore, Registry: registry, Sealer: sealer, Ingest: contents, Blobs: uploadService, Receipts: store}}
	var runtime atomic.Pointer[orchestration.Runtime]
	schemaReady := func(ctx context.Context) error { return postgres.SchemaReady(ctx, pool) }
	// The api is ready once its query encoding is warm, or warmBound passed.
	settled := make(chan struct{})
	close(settled)
	var warming <-chan struct{} = settled
	ready := func(ctx context.Context) error {
		if !warmed(warming) {
			return errWarming
		}
		err := schemaReady(ctx)
		if err == nil && command == "worker" {
			rt := runtime.Load()
			if rt == nil {
				return errors.New("worker dependencies unavailable")
			}
			if _, err = rt.Client.CheckHealth(ctx, nil); err == nil {
				err = blobs.Ready(ctx)
				if err == nil {
					err = projection.Ready(ctx)
				}
			}
		}
		return err
	}
	// The api migrates before it serves; a worker started first waits for it.
	if err = awaitSchema(ctx, schemaReady, schemaStartup); err != nil {
		if ctx.Err() != nil {
			return nil // stopped while waiting
		}
		return err
	}
	// The registry checks registered plugins with the Contract Runner (api)
	// and records activations with the spaces they register, refusing one
	// that breaks a startup rule of this engine.
	pluginRegistry := pluginregistry.Service{Store: postgres.PluginStore{Pool: pool}, Spaces: DeploymentSpaces, Wake: make(chan struct{}, 1),
		Validate: func(set *plugins.PinSet) error { _, err := connectors.NewRegistry(kindsOf(set)...); return err }}
	planID, resolved, err := applyPluginConfiguration(ctx, pluginRegistry, pins)
	if err != nil {
		return err
	}
	if resolved.Ingestion() == nil {
		return errors.New("the active pipeline plan has no ingestion plugin: pin plugins/core-ingest (core.ingest) or another ingestion plugin in `plugins` (plugins/core-ingest/README.md)")
	}
	if command == "api" && resolved.Retrieval() == nil {
		return errors.New("the active pipeline plan has no retrieval plugin: pin plugins/core-retrieve (core.retrieve) or another retrieval plugin in `plugins` (plugins/core-retrieve/README.md)")
	}
	if err = registry.Replace(kindsOf(resolved)...); err != nil {
		return fmt.Errorf("pipeline plan %s: %w", planID, err)
	}
	if err = live.Store(planID, resolved); err != nil {
		return fmt.Errorf("pipeline plan %s: %w", planID, err)
	}
	if command == "api" {
		// The first search must not pay the ingestion plugin's first-use
		// loading (THE-813); the worker never encodes a query.
		warming = warmQueries(ctx, pluginhttp.Ingestor{Pin: resolved.Ingestion()}.Warm, warmBound, warmRetry)
	}
	follower := &planFollower{store: pluginRegistry.Store, live: live, apply: func(set *plugins.PinSet) error { return registry.Replace(kindsOf(set)...) }}
	// Work pinned to an earlier plan resolves its plugins in that plan.
	planStore := postgres.PluginStore{Pool: pool}
	live.Resolve = func(ctx context.Context, plan string) (*plugins.PinSet, error) {
		return resolvePlanByID(ctx, planStore, plan)
	}
	workPinner := workPins{store: planStore, live: live, budget: pinnedAttempts}
	acquisition.Acquirer.Kinds = (&planKinds{live: live, current: registry, kindsOf: kindsOf}).at
	// The registry records each space's owner and this deployment's roles; a
	// space claimed by another owner, or changed under the same version,
	// refuses startup. New Corpora then start on the registered spaces.
	register, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = store.RegisterSpaces(register, DeploymentSpaces(resolved))
	if err == nil {
		err = alignDefaultGeneration(register, store)
	}
	cancel()
	if err != nil {
		return fmt.Errorf("vector space registry: %w", err)
	}
	pluginhttp.Observe(func(c pluginhttp.Call) {
		recorder.PluginCall(observability.PluginCall{Organization: c.Organization, Plugin: c.PluginID, Version: c.Version, Operation: c.Operation, Duration: c.Duration, ErrorCode: c.ErrorCode})
	})
	recorderDone := make(chan struct{})
	go func() {
		defer close(recorderDone)
		recorder.Run(ctx)
	}()
	// Registered before the loops below, so it runs after their shutdown and
	// flushes the counts they recorded last.
	defer func() {
		stop()
		select {
		case <-recorderDone:
		case <-time.After(5 * time.Second):
		}
	}()
	// Subscription evaluators are resolved at startup: an activation never
	// switches one (registry.PlanActivation).
	evaluators := cfg.evaluators(resolved)
	embedding := tei.Encoder{Endpoint: cfg.TEIURL}
	// Coverage counts read every current segment of a Corpus; a search sees
	// them at most 10 s old.
	search := retrieval.Service{Embedder: embedding, Routing: store, Registry: store, Coverage: &retrieval.CoverageCache{TTL: 10 * time.Second}, Projection: projection, Content: contents}
	// External normalization runs in the worker only, before publication.
	normalizer := normalization.Service{Content: contents, Store: store, Signer: blobs, Pin: live}
	processor := processing.Service{Content: contents, Retrieval: search, Enrichment: search, Normalizer: normalizer, Routing: store, LegacySpace: tei.Space().ID}
	rebuilder := retrieval.Rebuilder{Store: store, Content: contents, Projection: projection, Routing: store}
	// The plan's ingestion plugin segments and embeds every Version, encodes
	// the queries of its spaces and derives rebuild targets; each call
	// resolves the plugin the plan names at that moment.
	ingestor := pluginhttp.LiveIngestor{Live: live}
	deriver := &processing.PluginDeriver{Content: contents, Plugin: ingestor}
	search.Spaces = ingestor
	processor.Retrieval, processor.Enrichment = search, search
	processor.Plugin = deriver
	rebuilder.Plugin = deriver
	// Backfills fill spaces through the plan each one is pinned to, paced
	// below live ingestion on their own task queue.
	pinnedIngestion := planIngestion{store: planStore, live: live}
	backfiller := &backfill.Backfiller{Store: store, Content: contents, Plugin: deriver, Projection: projection, Pinned: pinnedIngestion, Steps: recorder, Settings: backfillSettings}
	// Quarantine reprocesses rerun normalization and processing through the
	// plan each one is pinned to, paced like backfills on their queue. The
	// processor is copied before the worker gives it its observer: a
	// reprocessed Version must not count as days from acceptance to
	// searchable.
	reprocessor := &quarantine.Reprocessor{Store: store, Normalizer: normalizer, Publisher: contents, Processor: processor,
		Settings: quarantine.Settings{Rate: backfillSettings.Rate, Poll: backfillSettings.Poll}}
	// The retrieval plugin, normally core.retrieve, answers every search: it
	// requests candidates, which search serves after authorization and
	// hydration, and ranks them. The api refuses to start without one.
	if resolved.Retrieval() != nil {
		search.Ranker = pluginhttp.LiveRetriever{Live: live, Started: resolved.Retrieval()}
	}
	// The api that served an activation follows it at once; new Corpora
	// start on the spaces it registered.
	pluginRegistry.Activated = func(ctx context.Context) {
		follower.Refresh(ctx)
		if err := alignDefaultGeneration(ctx, store); err != nil {
			slog.Error("new Corpora stay on the previous vector spaces until the next start", "error", err)
		}
	}
	go follower.Run(ctx, planPoll)
	probes := http.NewServeMux()
	probes.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	probes.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := ready(ctx); errors.Is(err, errWarming) {
			http.Error(w, err.Error(), 503)
			return
		} else if err != nil {
			http.Error(w, "database/schema unavailable", 503)
			return
		}
		w.WriteHeader(204)
	})
	deliveryStore := postgres.DeliveryStore{ContentStore: store}
	deliveryMetrics := &monitoring.DeliveryMetrics{}
	commands := telemetry.NewCommands()
	pruneMetrics := &telemetry.ChangePrune{}
	evaluationMetrics := &monitoring.EvaluationMetrics{}
	purgeMetrics := retrieval.NewPurgeMetrics()
	if command == "worker" {
		// Delivery attempt outcomes and admissible backlog, processing outcomes and
		// acceptance-to-searchable durations, in Prometheus text format.
		processingMetrics := telemetry.NewProcessing()
		processor.Observer = processingObserver{metrics: processingMetrics, store: store, steps: recorder}
		deliveryMetrics.Extra = func(w io.Writer) {
			processingMetrics.Write(w)
			pruneMetrics.Write(w)
			purgeMetrics.Write(w)
			evaluationMetrics.Write(w)
			recorder.WriteMetrics(w)
		}
		probes.Handle("GET /metrics", deliveryMetrics.Handler(deliveryStore.DeliveryBacklog))
		slog.Info("plugins pinned", "plan", planID, "plugins", resolved.Describe(), "evaluators", len(evaluators))
	} else {
		// Accepted durable commands and the ingestion backlog: what the API committed
		// and how much of it still waits for the worker.
		probes.Handle("GET /metrics", apiMetrics(commands, store.IngestionBacklog, recorder.WriteMetrics))
	}
	servers := []*http.Server{{Addr: cfg.ProbeListen, Handler: probes, ReadHeaderTimeout: 5 * time.Second}}
	if command == "api" {
		// The api checks registered plugins with the Contract Runner in
		// process; a check a restart interrupted runs again after its lease.
		go pluginRegistry.RunChecks(ctx, 5*time.Second, 2*time.Minute)
		// Subscription previews call the subscription plugins from the API.
		previews := postgres.EvaluationStore{ContentStore: store}
		handler, err := httpapi.New(postgres.Store{Pool: pool}, contents, search, uploadService, cfg.Keys, []byte(cfg.CursorKey), httpapi.WithChanges(changes.Service{Journal: store, Key: []byte(cfg.CursorKey), Retention: retention}, streamPoll), httpapi.WithMonitoring(monitoring.Service{Store: store, Corpora: store, Destinations: cfg.Destinations, Profiles: search, MatchStore: store, Evaluators: evaluators, Recent: previews, Versions: versionParts{content: contents, metadata: previews}}), httpapi.WithOperations(operations.Service{Store: store}),
			httpapi.WithConnectors(connectors.Service{Store: connectorStore, Registry: registry, Sealer: sealer, MinInterval: minInterval, PublicURL: cfg.PublicURL}), httpapi.WithCommands(commands), httpapi.WithVectorSpaces(store),
			// Operators register, check and activate plugins (plugins:admin).
			httpapi.WithPlugins(pluginRegistry),
			// Operators backfill past Versions and promote vector spaces (plugins:admin).
			httpapi.WithBackfills(backfill.Service{Store: store, Plans: pinnedIngestion, Throughput: backfillThroughput{reader: observability.Reader{Store: rollups}}, Settings: backfillSettings}, backfill.Promotions{Store: store}),
			// Operators list the Versions stuck in quarantine and reprocess them (plugins:admin).
			httpapi.WithQuarantine(quarantine.Service{Store: store}),
			// Operators follow documents through their steps (observability:read).
			httpapi.WithActivity(content.Activities{Store: store}),
			// Searches are counted, and the rollups read back (observability:read).
			httpapi.WithObservability(recorder, observability.Reader{Store: rollups, RecordQueryText: cfg.Observability.RecordQueryText}),
			// Push deliveries are relayed by the API, which the source reaches.
			httpapi.WithRelay(connectors.Relay{Store: connectorStore, Registry: registry, Sealer: sealer, Ingest: contents}))
		if err != nil {
			return fmt.Errorf("compile public request schema: %w", err)
		}
		servers = append(servers, &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384})
	} else {
		workerDone := make(chan struct{})
		defer func() {
			stop()
			select {
			case <-workerDone:
			case <-time.After(5 * time.Second):
			}
		}()
		evaluationDone := make(chan struct{})
		defer func() {
			stop()
			select {
			case <-evaluationDone:
			case <-time.After(5 * time.Second):
			}
		}()
		// Monitoring evaluation keeps its durable state in PostgreSQL and runs
		// independently of Temporal availability.
		go func() {
			defer close(evaluationDone)
			evaluation := postgres.EvaluationStore{ContentStore: store}
			monitoring.Engine{Store: evaluation, Versions: versionParts{content: contents, metadata: evaluation}, Evaluators: evaluators, Workers: 4, Lease: time.Minute, Metrics: evaluationMetrics, Matched: recorder.Matched}.Run(ctx)
		}()
		deliveryDone := make(chan struct{})
		defer func() {
			stop()
			select {
			case <-deliveryDone:
			case <-time.After(15 * time.Second):
			}
		}()
		// Webhook delivery is a separate PostgreSQL-leased loop: admission and
		// outcome facts commit around, never inside, the network attempt.
		go func() {
			defer close(deliveryDone)
			monitoring.Deliverer{Store: deliveryStore, Destinations: cfg.Destinations, Workers: 2, Lease: time.Minute, Timeout: deliveryTimeout, Retry: retryPolicy, Metrics: deliveryMetrics, AllowPrivateAddresses: cfg.Delivery.AllowPrivateDestinations}.Run(ctx)
		}()
		pruneDone := make(chan struct{})
		defer func() {
			stop()
			select {
			case <-pruneDone:
			case <-time.After(5 * time.Second):
			}
		}()
		// The change-journal prune is a bounded PostgreSQL loop beside
		// evaluation and delivery; its watermark keeps cursor expiry exact.
		go func() {
			defer close(pruneDone)
			changes.Pruner{Store: store, Retention: prune.Retention, Interval: prune.Interval, Organizations: prune.Organizations, Metrics: pruneMetrics}.Run(ctx)
		}()
		// Projection purge (THE-698): a bounded PostgreSQL-leased sweep that
		// deletes objects no route or current Version can serve again.
		purgeDone := make(chan struct{})
		defer func() {
			stop()
			select {
			case <-purgeDone:
			case <-time.After(5 * time.Second):
			}
		}()
		go func() {
			defer close(purgeDone)
			retrieval.Purger{Store: store, Projection: projection, Grace: purgeGrace, Interval: time.Minute, Batch: 100, Metrics: purgeMetrics}.Run(ctx)
		}()
		go func() {
			defer close(workerDone)
			for ctx.Err() == nil {
				rt, err := orchestration.Start(ctx, cfg.TemporalAddress, processor, rebuilder, store, acquisition, backfiller, reprocessor, workPinner)
				if err == nil {
					runtime.Store(rt)
					<-ctx.Done()
					rt.Close()
					return
				}
				slog.Warn("worker dependencies unavailable; retrying")
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
			}
		}()
	}
	failures := make(chan error, len(servers))
	for _, server := range servers {
		listener, err := net.Listen("tcp", server.Addr)
		if err != nil {
			return errors.New("listen failed")
		}
		go func(s *http.Server, l net.Listener) { failures <- s.Serve(l) }(server, listener)
	}
	slog.Info("process ready", "component", command)
	select {
	case <-ctx.Done():
	case err = <-failures:
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.New("HTTP server stopped")
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(shutdown)
	}
	return nil
}

// validPublicURL accepts an empty public_url or an absolute http(s) URL
// without query or fragment.
func validPublicURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return errors.New("public_url must be an absolute http(s) URL without credentials, query or fragment, such as https://quivr.example.com")
	}
	return nil
}
