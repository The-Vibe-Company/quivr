// quivr-autoscaler reconciles one worker service's replicas from Quivr's queue
// endpoint, through a backend selected by QUIVR_AUTOSCALER_BACKEND.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/autoscaling"
	"github.com/The-Vibe-Company/quivr/internal/autoscaling/kubernetes"
	"github.com/The-Vibe-Company/quivr/internal/autoscaling/railway"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("autoscaler stopped", "error", err)
		os.Exit(1)
	}
}

func integer(name string, fallback int64) (int64, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, errors.New(name + " must be an integer")
	}
	return parsed, nil
}

func duration(name string, fallback time.Duration, zero bool) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < 0 || (!zero && parsed < time.Second) {
		if zero {
			return 0, errors.New(name + " must be a non-negative duration")
		}
		return 0, errors.New(name + " must be a duration of at least one second")
	}
	return parsed, nil
}

// settings is the autoscaler configuration read from the environment.
type settings struct {
	backend, queue, endpoint, key                    string
	deployment                                       string
	railwayToken, railwayService, railwayEnvironment string
	policy                                           autoscaling.Config
	interval, minScaleInterval, timeout              time.Duration
}

// load reads the backend first, so only the selected backend's variables are required.
func load() (settings, error) {
	s := settings{backend: os.Getenv("QUIVR_AUTOSCALER_BACKEND"), queue: os.Getenv("QUIVR_AUTOSCALER_QUEUE")}
	required := []string{"QUIVR_QUEUE_URL", "QUIVR_QUEUE_KEY"}
	switch s.backend {
	case "kubernetes":
		required = append(required, "QUIVR_AUTOSCALER_KUBERNETES_DEPLOYMENT")
	case "railway":
		required = append(required, "RAILWAY_TOKEN", "QUIVR_AUTOSCALER_RAILWAY_SERVICE_ID", "RAILWAY_ENVIRONMENT_ID")
	default:
		return s, errors.New("QUIVR_AUTOSCALER_BACKEND must be kubernetes or railway")
	}
	for _, name := range required {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			return s, errors.New(name + " is required")
		}
	}
	s.deployment = os.Getenv("QUIVR_AUTOSCALER_KUBERNETES_DEPLOYMENT")
	s.railwayToken, s.railwayService, s.railwayEnvironment = os.Getenv("RAILWAY_TOKEN"), os.Getenv("QUIVR_AUTOSCALER_RAILWAY_SERVICE_ID"), os.Getenv("RAILWAY_ENVIRONMENT_ID")
	if s.queue == "" {
		s.queue = "bulk"
	}
	if s.queue != "bulk" && s.queue != "live" {
		return s, errors.New("QUIVR_AUTOSCALER_QUEUE must be bulk or live")
	}
	endpoint, err := url.Parse(os.Getenv("QUIVR_QUEUE_URL"))
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return s, errors.New("QUIVR_QUEUE_URL must be an HTTP(S) queue URL without credentials, query or fragment")
	}
	s.endpoint, s.key = endpoint.String(), os.Getenv("QUIVR_QUEUE_KEY")
	minimum, err := integer("QUIVR_AUTOSCALER_MIN", 1)
	if err != nil {
		return s, err
	}
	maximum, err := integer("QUIVR_AUTOSCALER_MAX", 8)
	if err != nil {
		return s, err
	}
	// Both backends store replica counts as signed 32-bit integers.
	if minimum < 1 || maximum < minimum || maximum > 1<<31-1 {
		return s, errors.New("autoscaler replica bounds must be positive ordered 32-bit integers")
	}
	documents, err := integer("QUIVR_AUTOSCALER_DOCUMENTS_PER_REPLICA", 20000)
	if err != nil {
		return s, err
	}
	window, err := duration("QUIVR_AUTOSCALER_DOWNSCALE_WINDOW", 5*time.Minute, true)
	if err != nil {
		return s, err
	}
	s.policy = autoscaling.Config{Min: int(minimum), Max: int(maximum), DocumentsPerReplica: documents, DownscaleWindow: window}
	if s.interval, err = duration("QUIVR_AUTOSCALER_INTERVAL", 30*time.Second, false); err != nil {
		return s, err
	}
	if s.minScaleInterval, err = duration("QUIVR_AUTOSCALER_MIN_SCALE_INTERVAL", 2*time.Minute, true); err != nil {
		return s, err
	}
	if s.timeout, err = duration("QUIVR_AUTOSCALER_REQUEST_TIMEOUT", 10*time.Second, false); err != nil {
		return s, err
	}
	return s, nil
}

func run(logger *slog.Logger) error {
	enabled := os.Getenv("QUIVR_AUTOSCALER_ENABLED")
	if enabled == "false" {
		logger.Info("autoscaler disabled")
		return nil
	}
	if enabled != "" && enabled != "true" {
		return errors.New("QUIVR_AUTOSCALER_ENABLED must be true or false")
	}
	s, err := load()
	if err != nil {
		return err
	}
	policy, err := autoscaling.NewPolicy(s.policy)
	if err != nil {
		return err
	}
	client := autoscaling.NewHTTPClient(s.timeout)
	source := autoscaling.QueueSource{URL: s.endpoint, Key: s.key, Queue: s.queue, Client: client}
	var backend autoscaling.Backend
	switch s.backend {
	case "kubernetes":
		if backend, err = kubernetes.InCluster(s.deployment, s.timeout); err != nil {
			return err
		}
	case "railway":
		backend = railway.Backend{Token: s.railwayToken, ServiceID: s.railwayService, EnvironmentID: s.railwayEnvironment, Client: client}
	}
	controller := autoscaling.NewController(policy, source, backend)
	controller.MinScaleInterval = s.minScaleInterval
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("autoscaler started", "backend", s.backend, "queue", s.queue, "min", s.policy.Min, "max", s.policy.Max, "documents_per_replica", s.policy.DocumentsPerReplica, "downscale_window", s.policy.DownscaleWindow.String(), "interval", s.interval.String(), "min_scale_interval", s.minScaleInterval.String())
	for {
		decision, err := controller.Step(ctx, time.Now())
		if ctx.Err() != nil {
			return nil
		}
		attrs := []any{"outcome", decision.Outcome, "waiting", decision.Waiting, "current", decision.Current, "target", decision.Target, "applied", decision.Applied}
		if err != nil {
			logger.Warn("autoscaling decision", append(attrs, "error", err)...)
		} else {
			logger.Info("autoscaling decision", attrs...)
		}
		// Wait after each reconciliation; slow APIs cannot cause a catch-up burst.
		timer := time.NewTimer(s.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
