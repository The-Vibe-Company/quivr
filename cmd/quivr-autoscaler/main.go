// quivr-autoscaler reconciles bulk-worker replicas from Quivr's queue endpoint.
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

	railway "github.com/The-Vibe-Company/quivr/deploy/railway/autoscaler"
	"github.com/The-Vibe-Company/quivr/internal/autoscaling"
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

func run(logger *slog.Logger) error {
	enabled := os.Getenv("QUIVR_AUTOSCALER_ENABLED")
	if enabled == "false" {
		logger.Info("autoscaler disabled")
		return nil
	}
	if enabled != "" && enabled != "true" {
		return errors.New("QUIVR_AUTOSCALER_ENABLED must be true or false")
	}
	required := []string{"QUIVR_QUEUE_URL", "QUIVR_QUEUE_KEY", "RAILWAY_TOKEN", "RAILWAY_BULK_SERVICE_ID", "RAILWAY_ENVIRONMENT_ID"}
	for _, name := range required {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			return errors.New(name + " is required")
		}
	}
	endpoint, err := url.Parse(os.Getenv("QUIVR_QUEUE_URL"))
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("QUIVR_QUEUE_URL must be an HTTP(S) queue URL without credentials, query or fragment")
	}
	minimum, err := integer("QUIVR_AUTOSCALER_MIN", 1)
	if err != nil {
		return err
	}
	maximum, err := integer("QUIVR_AUTOSCALER_MAX", 8)
	if err != nil {
		return err
	}
	// Replica counts are GraphQL Int (signed 32-bit).
	if minimum < 1 || maximum < minimum || maximum > 1<<31-1 {
		return errors.New("autoscaler replica bounds must be positive ordered GraphQL integers")
	}
	documents, err := integer("QUIVR_AUTOSCALER_DOCUMENTS_PER_REPLICA", 20000)
	if err != nil {
		return err
	}
	window, err := duration("QUIVR_AUTOSCALER_DOWNSCALE_WINDOW", 5*time.Minute, true)
	if err != nil {
		return err
	}
	interval, err := duration("QUIVR_AUTOSCALER_INTERVAL", 30*time.Second, false)
	if err != nil {
		return err
	}
	minScaleInterval, err := duration("QUIVR_AUTOSCALER_MIN_SCALE_INTERVAL", 2*time.Minute, true)
	if err != nil {
		return err
	}
	timeout, err := duration("QUIVR_AUTOSCALER_REQUEST_TIMEOUT", 10*time.Second, false)
	if err != nil {
		return err
	}
	policy, err := autoscaling.NewPolicy(autoscaling.Config{Min: int(minimum), Max: int(maximum), DocumentsPerReplica: documents, DownscaleWindow: window})
	if err != nil {
		return err
	}
	client := autoscaling.NewHTTPClient(timeout)
	source := autoscaling.QueueSource{URL: endpoint.String(), Key: os.Getenv("QUIVR_QUEUE_KEY"), Client: client}
	backend := railway.Backend{Token: os.Getenv("RAILWAY_TOKEN"), ServiceID: os.Getenv("RAILWAY_BULK_SERVICE_ID"), EnvironmentID: os.Getenv("RAILWAY_ENVIRONMENT_ID"), Client: client}
	controller := autoscaling.NewController(policy, source, backend)
	controller.MinScaleInterval = minScaleInterval
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("autoscaler started", "min", minimum, "max", maximum, "documents_per_replica", documents, "downscale_window", window.String(), "interval", interval.String(), "min_scale_interval", minScaleInterval.String())
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
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
