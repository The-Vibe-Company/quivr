package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	s3store "github.com/The-Vibe-Company/quivr/internal/adapters/s3"
	"github.com/The-Vibe-Company/quivr/internal/adapters/tei"
	"github.com/The-Vibe-Company/quivr/internal/adapters/weaviate"
	"github.com/The-Vibe-Company/quivr/internal/backfill"
	"github.com/The-Vibe-Company/quivr/internal/buildinfo"
	"github.com/The-Vibe-Company/quivr/internal/changes"
	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/The-Vibe-Company/quivr/internal/normalization"
	"github.com/The-Vibe-Company/quivr/internal/observability"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	orchestration "github.com/The-Vibe-Company/quivr/internal/orchestration/temporal"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost"
	pluginregistry "github.com/The-Vibe-Company/quivr/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr/internal/processing"
	"github.com/The-Vibe-Company/quivr/internal/quarantine"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	Postgres         PostgresConfig              `json:"postgres"`
	Worker           workqueue.Config            `json:"worker"`
	QueueObservation workqueue.ObservationConfig `json:"queue_observation"`
	TLS              TLSConfig                   `json:"tls"`
	Telemetry        telemetry.Config            `json:"telemetry"`
	// TEIURL encodes queries for generations built before the core.ingest
	// plugin (THE-777), which serve the legacy E5 space until rebuilt.
	TEIURL                  string                  `json:"tei_url"`
	WeaviateURL             string                  `json:"weaviate_url"`
	TemporalAddress         string                  `json:"temporal_address"`
	S3                      s3store.Config          `json:"s3"`
	LogLevel                string                  `json:"log_level"`
	Instance                string                  `json:"instance"`
	Environment             string                  `json:"environment"`
	ShutdownGrace           string                  `json:"shutdown_grace"`
	LogDirectory            string                  `json:"log_directory"`
	DatabaseURL             string                  `json:"database_url"`
	Listen                  string                  `json:"listen"`
	ProbeListen             string                  `json:"probe_listen"`
	CursorKey               string                  `json:"cursor_key"`
	RetainImportAuditDetail bool                    `json:"retain_import_audit_detail"`
	AuditRetentionMonths    int                     `json:"audit_retention_months"`
	ChangeRetention         string                  `json:"change_retention"`
	Keys                    map[string]corpus.Scope `json:"keys"`
	// Destinations are deployment-configured webhook receivers. Real
	// deployments reference their signing secret through secret_env.
	Destinations map[string]monitoring.Destination `json:"destinations"`
	// Delivery overrides the webhook retry policy (worker only).
	Delivery DeliveryConfig `json:"delivery"`
	// CredentialKey encrypts Deposited Credentials at rest (32+ bytes). It is
	// optional: without it credential deposits and rotations are refused.
	CredentialKey string `json:"credential_key"`
	// ConnectorMinInterval is the polling-interval floor (Go duration, default 30s).
	ConnectorMinInterval string                `json:"connector_min_interval"`
	ConnectorPush        connectors.PushConfig `json:"connector_push"`
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
	// kinds by name (a kind with two providers is a
	// conflict; a connector plugin needs https unless it is on loopback). API and
	// worker refuse to start on an invalid pin or a conflict between pins; an
	// unreachable plugin never prevents startup.
	Plugins []plugins.PinConfig `json:"plugins"`
	// Ingestion routes accepted source media types to pinned ingestion owners.
	Ingestion plugins.IngestionRouting `json:"ingestion"`
	// IngestionEvaluationConcurrency bounds optional activity slots per worker.
	// Zero selects four; one restores serial evaluation. Served slots are separate.
	IngestionEvaluationConcurrency int `json:"ingestion_evaluation_concurrency"`
	// Retrieval maps deployment short names to installed plugin/profile names.
	Retrieval RetrievalConfig `json:"retrieval"`
	// VectorIndex sets how the search index stores vector spaces.
	VectorIndex VectorIndexConfig `json:"vector_index"`

	// ChangePrune tunes the worker's change-journal prune (THE-697).
	ChangePrune ChangePruneConfig `json:"change_prune"`
	// PluginPlanPoll is how often api and worker check whether the active
	// Pipeline Plan changed, to follow it without restart (Go duration,
	// default 2s).
	PluginPlanPoll string `json:"plugin_plan_poll"`
	// ChangeStreamPoll is how often an open change stream reads the journal
	// again (Go duration, default 250ms).
	ChangeStreamPoll string `json:"change_stream_poll"`
	// PinnedPluginAttempts bounds unavailable plugins of older plans for
	// operations, and incompatible owners or invocation deadlines for live imports.
	// Live import reachability outages keep retrying (worker only; default 10).
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
	// Rebuild bounds parallel Version coverage inside each rebuild activity.
	Rebuild RebuildConfig `json:"rebuild"`
}

// RetrievalConfig names the search profiles selected by this deployment.
type RetrievalConfig struct {
	Profiles map[string]string `json:"profiles"`
	// CoverageRefresh paces background snapshots (Go duration, default 10s).
	CoverageRefresh string `json:"coverage_refresh"`
}

func (c RetrievalConfig) coverageRefresh() (time.Duration, error) {
	if c.CoverageRefresh == "" {
		return 10 * time.Second, nil
	}
	refresh, err := time.ParseDuration(c.CoverageRefresh)
	if err != nil || refresh <= 0 {
		return 0, badConfig(configInvalid, "retrieval.coverage_refresh", "retrieval.coverage_refresh must be a positive duration")
	}
	return refresh, nil
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
	{Name: "storage", Summary: "Check PostgreSQL schema readiness and report compact storage."},
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

func Run(command string, args ...string) error {
	contract := command == "migrate" && len(args) == 1 && args[0] == "--contract"
	if len(args) != 0 && !contract && command != "storage" {
		return errors.New(engineUsage())
	}
	if !isCommand(command) {
		return errors.New(engineUsage())
	}
	b, err := os.ReadFile(os.Getenv(ConfigEnv))
	if err != nil {
		if os.Getenv(ConfigEnv) == "" {
			return badConfig(configMissing, ConfigEnv, "QUIVR_CONFIG must name a configuration file")
		}
		return badConfig(configInvalid, ConfigEnv, "read QUIVR_CONFIG file failed")
	}
	var cfg Config
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&cfg); err != nil {
		field := ConfigEnv
		// encoding/json builds Field from declared struct fields, never map
		// keys or supplied values. Other decode errors identify the whole file.
		var typeError *json.UnmarshalTypeError
		if errors.As(err, &typeError) && typeError.Field != "" {
			field = typeError.Field
		}
		return invalidConfig(field, "invalid configuration JSON", err)
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return badConfig(configInvalid, ConfigEnv, "invalid configuration JSON: expected a single object")
	}
	if command == "storage" {
		return runStorage(cfg, args, os.Stdout)
	}
	if len(cfg.M365) > 0 && string(cfg.M365) != "null" {
		return badConfig(configInvalid, "m365", "m365 moved to the connector.m365_mail plugin's configuration; pin plugins/m365-mail with login_endpoint and graph_endpoint (https://docs.quivr.thevibecompany.co/guides/microsoft-365)")
	}
	grace, err := cfg.configureProcess(command)
	if err != nil {
		return err
	}
	if cfg.Telemetry.ResourceAttributes == nil {
		cfg.Telemetry.ResourceAttributes = map[string]string{}
	}
	for key, value := range map[string]string{"service.name": "quivr." + command, "service.version": buildinfo.Version, "service.instance.id": cfg.Instance, "deployment.environment.name": cfg.Environment} {
		if _, ok := cfg.Telemetry.ResourceAttributes[key]; !ok && value != "" {
			cfg.Telemetry.ResourceAttributes[key] = value
		}
	}
	telemetryRuntime, err := telemetry.Init(context.Background(), cfg.Telemetry)
	if err != nil {
		return invalidConfig("telemetry", "invalid telemetry settings", err)
	}

	events := newProcessEvents(slog.Default(), cfg.processSummary(grace))
	graceExpired := false
	defer func() {
		deadline, cancel := shutdownDeadline(events.shutdownStart(), grace)
		defer cancel()
		events.stop(deadline, graceExpired)
	}()
	defer func() {
		deadline, cancel := shutdownDeadline(events.shutdownStart(), grace)
		defer cancel()
		if telemetryRuntime.Shutdown(deadline) != nil {
			slog.Warn("telemetry shutdown failed", "event", "quivr.telemetry.shutdown_failed")
		}
	}()
	slog.Debug("debug logging enabled", "event", "quivr.debug")
	workerSettings, err := cfg.Worker.Resolve()
	if err != nil {
		return invalidConfig("worker", "invalid worker queues or slots", err)
	}
	queueRefreshInterval, err := cfg.QueueObservation.Resolve()
	if err != nil {
		return invalidConfig("queue_observation", "invalid queue refresh interval", err)
	}
	if err = cfg.VectorIndex.Validate(); err != nil {
		return invalidConfig("vector_index", "invalid vector index settings", err)
	}
	tlsSettings, err := cfg.validateTLS()
	if err != nil {
		return err
	}
	// Refusals use the configured stdout logger.
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
	switch {
	case cfg.DatabaseURL == "":
		return badConfig(configMissing, "database_url", "database_url is required")
	case cfg.CursorKey == "":
		return badConfig(configMissing, "cursor_key", "cursor_key is required")
	case len(cfg.CursorKey) < 32:
		return badConfig(configInvalid, "cursor_key", "cursor_key must have at least 32 bytes")
	case len(cfg.Keys) == 0:
		return badConfig(configMissing, "keys", "keys must contain at least one scoped API key")
	}
	logger := slog.Default()
	if command == "migrate" {
		// Only api and worker handle credentials; migrate stays silent about them.
		logger = slog.New(slog.DiscardHandler)
	}
	sealer, err := cfg.connectorSealer(logger)
	if err != nil {
		return invalidConfig("credential_key", "credential_key must have at least 32 bytes", err)
	}
	minInterval := connectors.DefaultMinInterval
	if cfg.ConnectorMinInterval != "" {
		if minInterval, err = time.ParseDuration(cfg.ConnectorMinInterval); err != nil || minInterval <= 0 {
			return badConfig(configInvalid, "connector_min_interval", "connector_min_interval must be a positive duration")
		}
	}
	pushConfig, err := cfg.ConnectorPush.Resolve()
	if err != nil {
		return invalidConfig("connector_push", "invalid connector_push settings", err)
	}
	// Every connector kind is supplied by a pinned plugin.
	kindsOf := pluginhttp.Connectors
	registry, err := connectors.NewRegistry(kindsOf(pins)...)
	if err != nil {
		return err
	}
	planPoll := 2 * time.Second
	if cfg.PluginPlanPoll != "" {
		if planPoll, err = time.ParseDuration(cfg.PluginPlanPoll); err != nil || planPoll <= 0 {
			return badConfig(configInvalid, "plugin_plan_poll", "plugin_plan_poll must be a positive duration")
		}
	}
	rebuildConcurrency, err := cfg.Rebuild.concurrency()
	if err != nil {
		return err
	}
	coverageRefresh, err := cfg.Retrieval.coverageRefresh()
	if err != nil {
		return err
	}
	backfillSettings, err := cfg.Backfill.settings()
	if err != nil {
		return err
	}
	if cfg.IngestionEvaluationConcurrency < 0 || cfg.IngestionEvaluationConcurrency > 32 {
		return badConfig(configInvalid, "ingestion_evaluation_concurrency", "ingestion_evaluation_concurrency must be between 1 and 32 (or 0 for the default 4)")
	}
	pinnedAttempts := 10
	switch {
	case cfg.PinnedPluginAttempts < 0:
		return badConfig(configInvalid, "pinned_plugin_attempts", "pinned_plugin_attempts must be a positive number")
	case cfg.PinnedPluginAttempts > 0:
		pinnedAttempts = cfg.PinnedPluginAttempts
	}
	if err := validPublicURL(cfg.PublicURL); err != nil {
		return err
	}
	for token, s := range cfg.Keys {
		if len(token) < 32 || s.Organization == "" || len(s.Actions) == 0 || len(s.Corpora) == 0 {
			return badConfig(configInvalid, "keys", "invalid scoped credential configuration")
		}
	}
	if err := validateDestinations(cfg.Destinations, cfg.Delivery.AllowPrivateDestinations); err != nil {
		return err
	}
	retryPolicy, deliveryTimeout, err := cfg.Delivery.parse()
	if err != nil {
		return err
	}
	auditMonths, err := auditRetentionMonths(cfg.AuditRetentionMonths)
	if err != nil {
		return err
	}
	retention := changes.DefaultRetention
	if cfg.ChangeRetention != "" {
		if retention, err = time.ParseDuration(cfg.ChangeRetention); err != nil || retention <= 0 {
			return badConfig(configInvalid, "change_retention", "change_retention must be a positive duration")
		}
	}
	streamPoll := httpapi.DefaultStreamPoll
	if cfg.ChangeStreamPoll != "" {
		if streamPoll, err = time.ParseDuration(cfg.ChangeStreamPoll); err != nil || streamPoll <= 0 {
			return badConfig(configInvalid, "change_stream_poll", "change_stream_poll must be a positive duration")
		}
	}
	prune, err := cfg.ChangePrune.parse(retention)
	if err != nil {
		return err
	}
	if cfg.Delivery.AllowPrivateDestinations {
		slog.Warn("delivery.allow_private_destinations is enabled: webhooks may reach internal networks; this increases SSRF exposure")
	}
	if cfg.ChangePrune.AllowShortRetention {
		slog.Warn("change_prune.allow_short_retention is enabled: pruning may remove change events before API cursors expire; this can cause data loss for consumers", "retention", prune.Retention, "change_retention", retention)
	}
	purgeGrace := retrieval.DefaultPurgeGrace
	if cfg.ProjectionPurgeGrace != "" {
		if purgeGrace, err = time.ParseDuration(cfg.ProjectionPurgeGrace); err != nil || purgeGrace <= 0 {
			return badConfig(configInvalid, "projection_purge_grace", "projection_purge_grace must be a positive duration")
		}
	}
	schemaStartup := schemaWait{First: 250 * time.Millisecond, Max: 5 * time.Second}
	if command == "worker" {
		schemaStartup.Limit = defaultMigrationWait
		if cfg.MigrationWait != "" {
			if schemaStartup.Limit, err = time.ParseDuration(cfg.MigrationWait); err != nil || schemaStartup.Limit < 0 {
				return badConfig(configInvalid, "migration_wait", "migration_wait must be a Go duration, 0s or more")
			}
		}
	}
	if _, ok := cfg.Observability.Interval(); !ok {
		return badConfig(configInvalid, "observability.flush_interval", "observability.flush_interval must be a positive Go duration")
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
		return badConfig(configMissing, "plugins", "no ingestion plugin pinned: pin plugins/core-ingest (core.ingest) or another ingestion plugin in `plugins` (plugins/core-ingest/README.md)")
	}
	// Nor does it rank search results itself (THE-779): the api needs a
	// pinned retrieval plugin, normally the first-party core.retrieve.
	if command == "api" && len(pins.Retrievals()) == 0 {
		return badConfig(configMissing, "plugins", "no retrieval plugin pinned: pin plugins/core-retrieve (core.retrieve) or another retrieval plugin in `plugins` (plugins/core-retrieve/README.md)")
	}
	signals, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	loops := lifecycle.New()
	ctx := loops.Context()
	beginDrain := func() { events.drain(loops) }

	go func() {
		select {
		case <-signals.Done():
			beginDrain()
		case <-ctx.Done():
		}
	}()
	var servers []*http.Server
	var flush = func(context.Context) {}
	var closeResources = func(context.Context) {}
	defer func() {
		beginDrain()
		deadline, cancel := shutdownDeadline(events.shutdownStart(), grace)
		defer cancel()
		drainProcess(deadline, loops, servers, flush)
		closeResources(deadline)
		graceExpired = deadline.Err() != nil
	}()
	poolConfig, err := cfg.poolConfig()
	if err != nil {
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return badConfig(configInvalid, "database_url", "invalid database configuration")
	}
	auditStore := postgres.AuditStore{Pool: pool}
	closeResources = func(deadline context.Context) {
		done := make(chan struct{})
		go func() { pool.Close(); close(done) }()
		select {
		case <-done:
		case <-deadline.Done():
		}
	}

	for _, required := range []struct{ field, value string }{
		{"weaviate_url", cfg.WeaviateURL}, {"temporal_address", cfg.TemporalAddress},
		{"s3.endpoint", cfg.S3.Endpoint}, {"s3.bucket", cfg.S3.Bucket},
		{"s3.access_key", cfg.S3.AccessKey}, {"s3.secret_key", cfg.S3.SecretKey},
	} {
		if required.value == "" {
			return badConfig(configMissing, required.field, "required setting is missing")
		}
	}
	blobs, err := s3store.NewWithTLS(cfg.S3, cfg.TLS.S3)
	if err != nil {
		return invalidConfig("s3", "invalid object storage settings", err)
	}
	devhost.SetTransport(tlsSettings.plugins)
	materialization := postgres.MaterializationStore{Pool: pool}
	normalizations := postgres.NormalizationStore{Pool: pool, Blobs: blobs, RetainImportAuditDetail: cfg.RetainImportAuditDetail}
	baseline := postgres.ProjectionStore{Pool: pool}
	embeddings := postgres.EmbeddingStore{Pool: pool}
	spaces := postgres.SpaceStore{Pool: pool}
	backfills := postgres.BackfillStore{Pool: pool}
	operationStore := postgres.OperationStore{Pool: pool}
	rebuilds := postgres.RebuildStore{Pool: pool}
	quarantines := postgres.QuarantineStore{Pool: pool}
	journal := postgres.ChangeStore{Pool: pool}
	activity := postgres.ActivityStore{Pool: pool}
	uploadStore := postgres.UploadStore{Pool: pool}
	monitor := postgres.MonitoringStore{Pool: pool}
	matches := postgres.MatchStore{Pool: pool}
	purges := postgres.PurgeStore{Pool: pool}
	ingestionEvaluations := postgres.IngestionEvaluationStore{Pool: pool}
	servingProjections := postgres.ServingProjectionStore{Pool: pool}
	// Plugin calls, searches, processing steps, documents received and
	// Matches are counted in memory and flushed as rollups; only the worker
	// deletes expired ones. The recorder runs once the plan is resolved.
	rollups := postgres.ObservabilityStore{Pool: pool}
	recorder := observability.NewRecorder(rollups, cfg.Observability, command == "worker")
	// Normalizer routes and extension namespaces follow the plan.
	submissions := postgres.SubmissionStore{Pool: pool, RetainImportAuditDetail: cfg.RetainImportAuditDetail}
	receipts := postgres.ReceiptStore{Pool: pool}
	records := postgres.RecordStore{Pool: pool}
	versions := postgres.VersionStore{Pool: pool}
	contents := content.Service{Submissions: submissions, Receipts: receipts, RecordStore: records, Versions: versions, Materialization: materialization, Catalog: records, Facets: records, Blobs: blobs, Baseline: baseline, Embeddings: embeddings, BlobSource: uploadStore, Relations: records, Extensions: live, Normalizations: normalizations, Supersession: normalizations, Routes: live,
		Received: recorder.Received}
	uploadService := uploads.Service{Store: uploadStore, Transfer: blobs}
	projection, err := weaviate.NewWithTLS(cfg.WeaviateURL, cfg.TLS.Weaviate)
	if err != nil {
		return err
	}
	projection.LegacySpace = tei.Space().ID
	if command == "migrate" {
		if contract {
			contractCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err = postgres.MigrateContracts(contractCtx, pool)
			cancel()
			if err != nil {
				if errors.Is(err, postgres.ErrMigrationBusy) {
					return err
				}
				return errors.New("contract migration failed; check database connectivity and schema")
			}
		}
		// Required PostgreSQL setup runs first and needs no other dependency.
		if err = BootstrapDatabase(ctx, pool, cfg.DeploymentSpaces(cfg.migrationPins())); err != nil {
			if errors.Is(err, content.ErrSpaceOwner) || errors.Is(err, content.ErrSpaceChanged) || errors.Is(err, postgres.ErrIndexSetup) || errors.Is(err, postgres.ErrMigrationBusy) {
				return err
			}
			return errors.New("migration failed; check database connectivity and schema")
		}
		// Keep required dependency setup bounded.
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
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
		events.ready(ctx, "migrations complete")
		return nil
	}
	connectorStore := postgres.ConnectorStore{Pool: pool}
	acquisition := &orchestration.Connectors{Scheduler: connectorStore, Acquirer: connectors.Acquirer{PublicURL: cfg.PublicURL, Store: connectorStore, Registry: registry, Sealer: sealer, Ingest: contents, Blobs: uploadService, Receipts: receipts}}
	var runtime atomic.Pointer[orchestration.Runtime]
	schemaReady := func(ctx context.Context) error { return postgres.SchemaReady(ctx, pool) }
	// The api is ready once its query encoding is warm, or warmBound passed.
	settled := make(chan struct{})
	close(settled)
	var warming <-chan struct{} = settled
	ready := func(ctx context.Context) error {
		if loops.Draining() {
			return errors.New("process draining")
		}
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
	indexes := &IndexMaintenance{Pool: pool}
	loops.Go(indexes.Run)
	queueSnapshots := postgres.QueueSnapshots{Pool: pool, RefreshInterval: queueRefreshInterval}
	acquisition.Acquirer.Queues = queueSnapshots
	loops.Go(func(ctx context.Context) {
		delay := queueRefreshInterval
		failed := false
		for {
			refresh, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := queueSnapshots.Refresh(refresh)
			cancel()
			if err != nil && ctx.Err() == nil {
				if !failed {
					slog.Warn("queue backlog refresh unavailable")
				}
				failed = true
				delay = min(delay*2, max(queueRefreshInterval, 15*time.Second))
			} else {
				failed = false
				delay = queueRefreshInterval
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	})
	// The registry checks registered plugins with the Contract Runner (api)
	// and records activations with the spaces they register, refusing one
	// that breaks a startup rule of this engine.
	pluginRegistry := pluginregistry.Service{Store: postgres.PluginStore{Pool: pool}, Spaces: cfg.DeploymentSpaces, Wake: make(chan struct{}, 1),
		Validate: func(set *plugins.PinSet) error {
			if command == "api" && len(set.Retrievals()) == 0 {
				return errors.New("the active pipeline plan requires a retrieval plugin")
			}
			if _, err := set.RetrievalProfiles(cfg.Retrieval.Profiles); err != nil {
				return err
			}
			_, err := connectors.NewRegistry(kindsOf(set)...)
			return err
		}}
	planID, resolved, err := applyPluginConfiguration(ctx, pluginRegistry, pins)
	if err != nil {
		return err
	}
	if resolved.Ingestion() == nil {
		return errors.New("the active pipeline plan has no ingestion plugin: pin plugins/core-ingest (core.ingest) or another ingestion plugin in `plugins` (plugins/core-ingest/README.md)")
	}
	if command == "api" && len(resolved.Retrievals()) == 0 {
		return errors.New("the active pipeline plan has no retrieval plugin: pin plugins/core-retrieve (core.retrieve) or another retrieval plugin in `plugins` (plugins/core-retrieve/README.md)")
	}
	if _, err = resolved.RetrievalProfiles(cfg.Retrieval.Profiles); err != nil {
		return err
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
		warming = warmQueries(ctx, func(ctx context.Context) error {
			for _, pin := range resolved.Ingestions() {
				if err := (pluginhttp.Ingestor{Pin: pin}).Warm(ctx); err != nil {
					return err
				}
			}
			return nil
		}, warmBound, warmRetry)
	}
	// Subscription evaluators follow the plan too: its alert-rule versions
	// serve new Subscription Versions, earlier ones keep judging the
	// Subscription Versions that pin them (THE-805).
	evaluators := &monitoring.LiveEvaluators{}
	installed, err := cfg.planEvaluators(ctx, pluginRegistry.Store, resolved, monitoring.PlanEvaluators{})
	if err != nil {
		return fmt.Errorf("alert-rule versions of earlier plans: %w", err)
	}
	evaluators.Store(installed)
	follower := &planFollower{store: pluginRegistry.Store, live: live, apply: func(set *plugins.PinSet) error {
		if command == "api" && len(set.Retrievals()) == 0 {
			return errors.New("the active pipeline plan requires a retrieval plugin")
		}
		if _, err := set.RetrievalProfiles(cfg.Retrieval.Profiles); err != nil {
			return err
		}
		return registry.Replace(kindsOf(set)...)
	},
		followed: func(ctx context.Context, set *plugins.PinSet) {
			installed, err := cfg.planEvaluators(ctx, pluginRegistry.Store, set, evaluators.Load())
			if err != nil {
				slog.ErrorContext(ctx, "alert-rule versions of earlier plans stay as this process last read them", "error", err)
			}
			evaluators.Store(installed)
		}}
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
	err = spaces.RegisterSpaces(register, cfg.DeploymentSpaces(resolved))
	if err == nil {
		err = alignDefaultGeneration(register, baseline)
	}
	cancel()
	if err != nil {
		return fmt.Errorf("vector space registry: %w", err)
	}
	pluginhttp.Observe(func(c pluginhttp.Call) {
		recorder.PluginCall(observability.PluginCall{Organization: c.Organization, Plugin: c.PluginID, Version: c.Version, Operation: c.Operation, Duration: c.Duration, ErrorCode: c.ErrorCode})
	})
	loops.Go(func(ctx context.Context) {
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				pruneCtx, cancel := context.WithTimeout(lifecycle.WorkContext(ctx), 5*time.Second)
				if err := connectorStore.PrunePushAnswers(pruneCtx); err != nil && ctx.Err() == nil {
					logger.Warn("connector push replay cleanup failed")
				}
				cancel()
			}
		}
	})
	recorderCtx, stopRecorder := context.WithCancel(lifecycle.WorkContext(ctx))
	recorderDone := make(chan struct{})
	go func() { defer close(recorderDone); recorder.Run(recorderCtx) }()
	flush = func(deadline context.Context) {
		stopRecorder()
		select {
		case <-recorderDone:
		case <-deadline.Done():
		}
	}
	embedding := tei.Encoder{Endpoint: cfg.TEIURL}
	// Coverage counts refresh independently of search deadlines. The API and
	// retrieval share snapshots; one count at a time bounds background load.
	spaceSnapshots := retrieval.NewSpaceSnapshots(lifecycle.WorkContext(ctx), spaces, coverageRefresh)
	metadataProjection := retrieval.MetadataProjection{Projection: projection, Metadata: records}
	search := retrieval.Service{Embedder: embedding, Routing: baseline, Registry: spaceSnapshots, Projection: metadataProjection, Content: contents}
	// External normalization runs in the worker only, before publication.
	normalizer := normalization.Service{Content: contents, Store: normalizations, Signer: blobs, Pin: live, Plugin: pluginhttp.Normalizer{}}
	processor := processing.Service{Content: contents, Retrieval: search, Enrichment: search, Normalizer: normalizer, Routing: baseline, LegacySpace: tei.Space().ID}
	rebuilder := retrieval.Rebuilder{Concurrency: rebuildConcurrency, Store: rebuilds, Cancellation: operationStore, Content: contents, Projection: metadataProjection, Routing: baseline}
	// The plan's ingestion plugin segments and embeds every Version, encodes
	// the queries of its spaces and derives rebuild targets; each call
	// resolves the plugin the plan names at that moment.
	ingestor := pluginhttp.LiveIngestor{Live: live, Owners: planStore}
	deriver := &processing.PluginDeriver{Content: contents, Plugin: ingestor}
	search.Spaces = ingestor
	processor.Retrieval, processor.Enrichment = search, search
	processor.Plugin = deriver
	processor.Evaluation = &processing.Evaluator{Store: ingestionEvaluations, Serving: servingProjections, Content: contents, Plugin: deriver, Projection: metadataProjection}
	rebuilder.Plugin = deriver
	// Backfills fill spaces through the plan each one is pinned to, paced
	// below live ingestion on their own task queue.
	pinnedIngestion := planIngestion{store: planStore, live: live}
	backfiller := &backfill.Backfiller{Store: backfills, Cancellation: operationStore, Content: contents, Plugin: deriver, Projection: metadataProjection, Pinned: pinnedIngestion, Steps: recorder, Settings: backfillSettings}
	// Quarantine reprocesses rerun normalization and processing through the
	// plan each one is pinned to, paced like backfills on their queue. The
	// processor is copied before the worker gives it its observer: a
	// reprocessed Version must not count as days from acceptance to
	// searchable.
	reprocessor := &quarantine.Reprocessor{Store: quarantines, Cancellation: operationStore, Normalizer: normalizer, Publisher: contents, Processor: processor,
		Settings: quarantine.Settings{Rate: backfillSettings.Rate, Poll: backfillSettings.Poll}}
	// The retrieval plugin, normally core.retrieve, answers every search: it
	// requests candidates, which search serves after authorization and
	// hydration, and ranks them. The api refuses to start without one.
	if len(resolved.Retrievals()) > 0 {
		search.ProfilesRouter = pluginhttp.LiveRetriever{Live: live, Aliases: cfg.Retrieval.Profiles}
	}
	// The api that served an activation follows it at once; new Corpora
	// start on the spaces it registered.
	pluginRegistry.Activated = func(ctx context.Context) {
		follower.Refresh(ctx)
		if err := alignDefaultGeneration(ctx, baseline); err != nil {
			slog.ErrorContext(ctx, "new Corpora stay on the previous vector spaces until the next start", "error", err)
		}
	}
	loops.Go(func(ctx context.Context) { follower.Run(ctx, planPoll) })
	loadMetrics := telemetry.NewLoadMetrics()
	loadMetrics.RegisterRoutes("/healthz", "/readyz", "/metrics")
	poolMetrics := postgres.NewPoolMetrics(pool)
	probes := http.NewServeMux()
	probes.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	probes.Handle("GET /readyz", readinessProbe(loops, ready, indexes))

	deliveryStore := postgres.DeliveryStore{Pool: pool}
	deliveryMetrics := &monitoring.DeliveryMetrics{}
	commands := telemetry.NewCommands()
	pruneMetrics := &telemetry.ChangePrune{}
	evaluationMetrics := &monitoring.EvaluationMetrics{}
	purgeMetrics := retrieval.NewPurgeMetrics()
	if command == "worker" {
		// Delivery attempt outcomes and admissible backlog, processing outcomes and
		// acceptance-to-searchable durations, in Prometheus text format.
		processingMetrics := telemetry.NewProcessing()
		processor.Observer = processingObserver{metrics: processingMetrics, store: materialization, steps: recorder}
		deliveryMetrics.Extra = func(w io.Writer) {
			processingMetrics.Write(w)
			pruneMetrics.Write(w)
			purgeMetrics.Write(w)
			evaluationMetrics.Write(w)
			recorder.WriteMetrics(w)
		}
		probes.Handle("GET /metrics", processMetrics(queueMetrics(buildMetrics(deliveryMetrics.Handler(deliveryStore.DeliveryBacklog)), queueSnapshots), loadMetrics, poolMetrics))
		slog.Info("plugins pinned", "plan", planID, "plugins", resolved.Describe(), "evaluators", len(evaluators.Load().Served))
	} else {
		// Accepted durable commands and the ingestion backlog: what the API committed
		// and how much of it still waits for the worker.
		probes.Handle("GET /metrics", processMetrics(queueMetrics(buildMetrics(apiMetrics(commands, materialization.IngestionBacklog, recorder.WriteMetrics)), queueSnapshots), loadMetrics, poolMetrics))
	}
	servers = []*http.Server{{Addr: cfg.ProbeListen, Handler: httpapi.AccessLog(probes, loadMetrics), ReadHeaderTimeout: 5 * time.Second}}
	if command == "api" {
		// The api checks registered plugins with the Contract Runner in
		// process; a check a restart interrupted runs again after its lease.
		loops.Go(func(ctx context.Context) { pluginRegistry.RunChecks(ctx, 5*time.Second, 2*time.Minute) })
		// Subscription previews call the subscription plugins from the API.
		previews := postgres.EvaluationStore{Pool: pool}
		handler, err := httpapi.New(postgres.Store{Pool: pool}, contents, search, uploadService, cfg.Keys, []byte(cfg.CursorKey), httpapi.WithChanges(changes.Service{Journal: journal, Key: []byte(cfg.CursorKey), Retention: retention}, streamPoll), httpapi.WithMonitoring(monitoring.Service{QueryEncoder: savedQueryEncoder{search: search, evaluators: evaluators}, Store: monitor, Corpora: baseline, Destinations: cfg.Destinations, Profiles: search, MatchStore: matches, Evaluators: evaluators, Moves: monitor, Evaluations: monitor, Recent: previews, Versions: versionParts{content: contents, metadata: previews, vectors: baseline}}), httpapi.WithOperations(operations.Service{Store: operationStore}), httpapi.WithQueues(queueSnapshots), httpapi.WithLifecycle(loops), httpapi.WithAudit(auditStore),
			httpapi.WithConnectors(connectors.Service{Store: connectorStore, Tokens: connectorStore, Registry: registry, Sealer: sealer, MinInterval: minInterval, PublicURL: cfg.PublicURL}), httpapi.WithCommands(commands), httpapi.WithLoadMetrics(loadMetrics), httpapi.WithVectorSpaces(spaceSnapshots),
			// Operators register, check and activate plugins (plugins:admin).
			httpapi.WithPlugins(pluginRegistry),
			// Operators backfill past Versions and promote vector spaces (plugins:admin).
			httpapi.WithBackfills(backfill.Service{Store: backfills, Registry: spaces, Plans: pinnedIngestion, Throughput: backfillThroughput{reader: observability.Reader{Store: rollups}}, Settings: backfillSettings}, backfill.Promotions{Store: backfills}),
			// Operators list the Versions stuck in quarantine and reprocess them (plugins:admin).
			httpapi.WithQuarantine(quarantine.Service{Store: quarantines}),
			// Operators follow documents through their steps (observability:read).
			httpapi.WithActivity(content.Activities{Store: activity}),
			// Searches are counted, and the rollups read back (observability:read).
			httpapi.WithObservability(recorder, observability.Reader{Store: rollups, RecordQueryText: cfg.Observability.RecordQueryText}),
			// Push deliveries are relayed by the API, which the source reaches.
			httpapi.WithTrustedPushProxies(pushConfig.TrustedProxyCIDRs),
			httpapi.WithRelay(connectors.Relay{Store: connectorStore, Tokens: connectors.Service{Tokens: connectorStore}, Registry: registry, Sealer: sealer, Ingest: contents, Protection: connectorStore, PushConfig: pushConfig, Replays: connectorStore}))
		if err != nil {
			return fmt.Errorf("compile public request schema: %w", err)
		}
		servers = append(servers, &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384})
	} else {
		if workerSettings.Serves(workqueue.Live) {
			// Monitoring evaluation keeps its durable state in PostgreSQL and runs
			// independently of Temporal availability.
			loops.Go(func(ctx context.Context) {
				evaluation := postgres.EvaluationStore{Pool: pool}
				monitoring.Engine{Store: evaluation, Versions: versionParts{content: contents, metadata: evaluation, vectors: baseline}, Evaluators: evaluators, Workers: 4, Lease: time.Minute, Metrics: evaluationMetrics, Matched: recorder.Matched}.Run(workqueue.WithTracker(ctx, postgres.QueueTracker{Pool: pool}))
			})
			// Webhook delivery is a separate PostgreSQL-leased loop: admission and
			// outcome facts commit around, never inside, the network attempt.
			loops.Go(func(ctx context.Context) {
				monitoring.Deliverer{Store: deliveryStore, Destinations: cfg.Destinations, Workers: 2, Lease: time.Minute, Timeout: deliveryTimeout, Retry: retryPolicy, Metrics: deliveryMetrics, AllowPrivateAddresses: cfg.Delivery.AllowPrivateDestinations}.Run(ctx)
			})
		}
		// The change-journal prune is a bounded PostgreSQL loop beside
		// evaluation and delivery; its watermark keeps cursor expiry exact.
		loops.Go(func(ctx context.Context) {
			changes.Pruner{Audit: auditStore, AuditRetentionMonths: auditMonths, Store: journal, Retention: prune.Retention, Interval: prune.Interval, Organizations: prune.Organizations, Metrics: pruneMetrics}.Run(ctx)
		})
		// Projection purge (THE-698): a bounded PostgreSQL-leased sweep that
		// deletes objects no route or current Version can serve again.
		loops.Go(func(ctx context.Context) {
			retrieval.Purger{Store: purges, Projection: projection, Grace: purgeGrace, Interval: time.Second, Batch: 4, Metrics: purgeMetrics}.Run(ctx)
		})
		loops.Go(func(ctx context.Context) {
			for ctx.Err() == nil {
				rt, err := orchestration.Start(ctx, cfg.TemporalAddress, processor, rebuilder, struct {
					orchestration.ReceiptDispatchStore
					orchestration.OperationDispatchStore
				}{materialization, operationStore}, acquisition, backfiller, reprocessor, workPinner, cfg.IngestionEvaluationConcurrency, tlsSettings.temporal, orchestration.RuntimeOptions{ShutdownGrace: grace, Queues: workerSettings, Tracker: postgres.QueueTracker{Pool: pool}})
				if err == nil {
					runtime.Store(rt)
					<-ctx.Done()
					rt.Close(lifecycle.WorkContext(ctx))
					return
				}
				slog.Warn("worker dependencies unavailable; retrying")
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
			}
		})
	}
	failures := make(chan error, len(servers))
	for _, server := range servers {
		listener, err := net.Listen("tcp", server.Addr)
		if err != nil {
			return errors.New("listen failed")
		}
		go func(s *http.Server, l net.Listener) { failures <- s.Serve(l) }(server, listener)
	}
	loops.Go(func(ctx context.Context) {
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for ctx.Err() == nil {
			check, cancel := context.WithTimeout(ctx, 2*time.Second)
			err := ready(check)
			cancel()
			if err == nil {
				events.ready(ctx, "process ready")
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	})
	select {
	case <-ctx.Done():
	case err = <-failures:
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.New("HTTP server stopped")
		}
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
		return badConfig(configInvalid, "public_url", "public_url must be an absolute http(s) URL without credentials, query or fragment, such as https://quivr.example.com")
	}
	return nil
}
