// quivr-index-warmup is a deployment companion for Weaviate.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/indexwarmup"
)

func setting(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func main() {
	endpoint := flag.String("url", setting("WEAVIATE_URL", "http://weaviate:8080"), "private index URL")
	interval := flag.String("interval", setting("QUIVR_INDEX_WARMUP_INTERVAL", "60s"), "wait after each pass")
	request := flag.String("request-timeout", setting("QUIVR_INDEX_WARMUP_REQUEST_TIMEOUT", "30s"), "per-request budget")
	pass := flag.String("pass-timeout", setting("QUIVR_INDEX_WARMUP_PASS_TIMEOUT", "120s"), "whole-pass budget")
	once := flag.Bool("once", false, "run one pass and exit; errors return nonzero")
	flag.Parse()
	cadence, e1 := time.ParseDuration(*interval)
	requestTimeout, e2 := time.ParseDuration(*request)
	passTimeout, e3 := time.ParseDuration(*pass)
	if e1 != nil || e2 != nil || e3 != nil || cadence <= 0 || cadence > time.Hour {
		fmt.Fprintln(os.Stderr, "invalid index warm-up interval or budget")
		os.Exit(2)
	}
	warmer, err := indexwarmup.New(indexwarmup.Config{URL: *endpoint, APIKey: os.Getenv("WEAVIATE_API_KEY"), RequestTimeout: requestTimeout, PassTimeout: passTimeout})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	encoder := json.NewEncoder(os.Stdout)
	if *once {
		report := warmer.Pass(ctx)
		encoder.Encode(report)
		if report.Errors != 0 || report.PendingScopes != 0 {
			os.Exit(1)
		}
		return
	}
	warmer.Run(ctx, cadence, func(report indexwarmup.Report) { encoder.Encode(report) })
}
