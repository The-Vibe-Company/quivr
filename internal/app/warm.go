package app

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// errWarming is the readiness answer while the query encoding warms up.
var errWarming = errors.New("search warming up")

// Warm-up of the api's query encoding (THE-813): the ingestion plugin loads
// what it needs on its first embed_query (core.ingest starts its tokenizer
// process, about half a second), which would otherwise fall within the first
// search's latency budget and fail it.
const (
	// warmBound is the longest the api waits for the warm-up before it
	// reports ready anyway: the first searches are then only slower.
	warmBound = 5 * time.Second
	// warmRetry spaces the attempts while the plugin does not answer yet: a
	// sidecar may start after the api.
	warmRetry = 100 * time.Millisecond
)

// warmQueries runs warm until the plugin answers or bound passes, in the
// background, and returns a channel closed when it is over. warm fails only
// when the plugin does not answer; an answer that reports an unavailable
// embedding service still warmed it, so readiness never waits for that
// service.
func warmQueries(ctx context.Context, warm func(context.Context) error, bound, retry time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(ctx, bound)
		defer cancel()
		started := time.Now()
		for {
			err := warm(ctx)
			if err == nil {
				slog.Info("query encoding warmed", "component", "search", "elapsed_ms", time.Since(started).Milliseconds())
				return
			}
			select {
			case <-ctx.Done():
				slog.Warn("query encoding not warmed; the first semantic searches may be slower", "component", "search", "error", err.Error())
				return
			case <-time.After(retry):
			}
		}
	}()
	return done
}

// warmed reports whether done is closed.
func warmed(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}
