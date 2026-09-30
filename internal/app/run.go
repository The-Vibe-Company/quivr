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
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/tokenizer"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/weaviate"
	"github.com/The-Vibe-Company/quivr-v2/internal/changes"
	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/The-Vibe-Company/quivr-v2/internal/normalization"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	orchestration "github.com/The-Vibe-Company/quivr-v2/internal/orchestration/temporal"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/processing"
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
	TEIURL          string                  `json:"tei_url"`
	Tokenizer       tokenizer.Config        `json:"tokenizer"`
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
	// ProjectionPurgeGrace delays the physical purge of dead projection
	// objects (Go duration, default 1h; worker only).
	ProjectionPurgeGrace string `json:"projection_purge_grace"`
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
		return errors.New("m365 moved to the connector.m365_mail plugin's configuration; pin plugins/m365-mail with login_endpoint and graph_endpoint (docs/connectors/microsoft-365.md)")
	}
	// Validate the pins before logs move to files, so a refusal is reported on stderr.
	pins, err := cfg.loadPins(command)
	if err != nil {
		return err
	}
	// Pinned plugins own their declared extension namespaces beside the
	// built-in ones: clients cannot write them, retrieval mappings may read them.
	extensions, err := plugins.PinsExtensionRegistry(pins)
	if err != nil {
		return err
	}
	evaluators := cfg.evaluators(pins)
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
	var kinds []connectors.Connector
	if cfg.ConnectorFixtures {
		kinds = append(kinds, connectors.Fixture{})
	}
	// Connector kinds of pinned plugins resolve beside the enabled built-in
	// kinds; a kind with two providers refuses startup.
	kinds = append(kinds, pluginhttp.Connectors(pins)...)
	registry, err := connectors.NewRegistry(kinds...)
	if err != nil {
		return err
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
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:8080"
	}
	if cfg.ProbeListen == "" {
		cfg.ProbeListen = "127.0.0.1:8081"
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
	contents := content.Service{Repository: store, Catalog: store, Blobs: blobs, Baseline: store, Embeddings: store, BlobSource: store, Relations: store, Extensions: extensions, Normalizations: store, Supersession: store}
	if pins != nil {
		contents.Routes = pins
	}
	uploadService := uploads.Service{Store: store, Transfer: blobs}
	projection := weaviate.New(cfg.WeaviateURL)
	// One long-lived pinned tokenizer per process; a process per call cost ~850 ms per search (THE-675).
	encoder := &tokenizer.Server{Config: cfg.Tokenizer}
	defer encoder.Close()
	windows := processing.TokenWindows{Tokenizer: encoder}
	embedding := tei.Encoder{Endpoint: cfg.TEIURL}
	search := retrieval.Service{Embedder: embedding, Routing: store, Projection: projection, Content: contents, QueryNormalizer: windows}
	// External normalization runs in the worker only, before publication.
	normalizer := normalization.Service{Content: contents, Store: store, Signer: blobs, Pin: pins}
	processor := processing.Service{Content: contents, Processor: windows, Retrieval: search, Embedder: embedding, Enrichment: search, Normalizer: normalizer}
	// Rebuilds reuse stored vectors; the TEI encoder only names the pinned space and producer.
	rebuilder := retrieval.Rebuilder{Store: store, Content: contents, Projection: projection, Artifacts: embedding, Segment: func(ctx context.Context, org string, v content.Version) (content.Segmentation, error) {
		return windows.Process(ctx, processing.Input{Organization: org, Version: v})
	}}
	if command == "migrate" {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		// The PostgreSQL part runs first and alone needs no other dependency;
		// rerunning migrate completes the S3, Weaviate and tokenizer steps.
		if err = BootstrapDatabase(ctx, pool); err != nil {
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
		if _, err = encoder.Encode(ctx, []processing.TokenInput{{Text: "tokenizer readiness"}}); err != nil {
			return errors.New("tokenizer preparation required")
		}
		slog.Info("migrations complete")
		return nil
	}
	connectorStore := postgres.ConnectorStore{ContentStore: store}
	acquisition := &orchestration.Connectors{Scheduler: connectorStore, Acquirer: connectors.Acquirer{PublicURL: cfg.PublicURL, Store: connectorStore, Registry: registry, Sealer: sealer, Ingest: contents, Blobs: uploadService, Receipts: store}}
	var runtime atomic.Pointer[orchestration.Runtime]
	schemaReady := func(ctx context.Context) error { return postgres.SchemaReady(ctx, pool) }
	ready := func(ctx context.Context) error {
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
	startup, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = schemaReady(startup)
	cancel()
	if err != nil {
		slog.Error("schema readiness failed", "error", err)
		return errors.New("database/schema unavailable; run migrate")
	}
	// Load the tokenizer before serving so the first search does not pay for it.
	warm, cancel := context.WithTimeout(ctx, 15*time.Second)
	if _, err = encoder.Encode(warm, []processing.TokenInput{{Text: "tokenizer readiness"}}); err != nil {
		slog.Warn("tokenizer warm-up failed; it will be retried on use")
	}
	cancel()
	probes := http.NewServeMux()
	probes.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	probes.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if ready(ctx) != nil {
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
		processor.Observer = processingObserver{metrics: processingMetrics, store: store}
		deliveryMetrics.Extra = func(w io.Writer) {
			processingMetrics.Write(w)
			pruneMetrics.Write(w)
			purgeMetrics.Write(w)
			evaluationMetrics.Write(w)
		}
		probes.Handle("GET /metrics", deliveryMetrics.Handler(deliveryStore.DeliveryBacklog))
		slog.Info("plugins pinned", "plugins", pins.Describe(), "evaluators", len(evaluators))
	} else {
		// Accepted durable commands and the ingestion backlog: what the API committed
		// and how much of it still waits for the worker.
		probes.Handle("GET /metrics", apiMetrics(commands, store.IngestionBacklog))
	}
	servers := []*http.Server{{Addr: cfg.ProbeListen, Handler: probes, ReadHeaderTimeout: 5 * time.Second}}
	if command == "api" {
		// Subscription previews call the subscription plugins from the API.
		previews := postgres.EvaluationStore{ContentStore: store}
		handler, err := httpapi.New(postgres.Store{Pool: pool}, contents, search, uploadService, cfg.Keys, []byte(cfg.CursorKey), httpapi.WithChanges(changes.Service{Journal: store, Key: []byte(cfg.CursorKey), Retention: retention}), httpapi.WithMonitoring(monitoring.Service{Store: store, Corpora: store, Destinations: cfg.Destinations, MatchStore: store, Evaluators: evaluators, Recent: previews, Versions: versionParts{content: contents, metadata: previews}}), httpapi.WithOperations(operations.Service{Store: store}),
			httpapi.WithConnectors(connectors.Service{Store: connectorStore, Registry: registry, Sealer: sealer, MinInterval: minInterval, PublicURL: cfg.PublicURL}), httpapi.WithCommands(commands),
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
			monitoring.Engine{Store: evaluation, Versions: versionParts{content: contents, metadata: evaluation}, Evaluators: evaluators, Workers: 4, Lease: time.Minute, Metrics: evaluationMetrics}.Run(ctx)
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
				rt, err := orchestration.Start(ctx, cfg.TemporalAddress, processor, rebuilder, store, acquisition)
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
