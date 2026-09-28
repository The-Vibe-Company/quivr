package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/changes"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

// Option configures optional API capabilities.
type Option func(*API)

// WithChanges enables the public change feed over the committed journal.
func WithChanges(feed changes.Service) Option {
	return func(a *API) { a.Changes = feed }
}

const (
	changeSchemaVersion = "1"
	streamPollInterval  = 250 * time.Millisecond
	streamKeepalive     = 15 * time.Second
	streamWriteTimeout  = 10 * time.Second
	streamPageSize      = 100
)

func changeFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, changes.ErrCursorExpired):
		send(w, 410, changeError(err))
	case errors.Is(err, changes.ErrCursorScope):
		failure(w, 409, "cursor_scope_changed")
	case errors.Is(err, changes.ErrCursorInvalid):
		failure(w, 422, "invalid_cursor")
	default:
		failure(w, 503, "changes_unavailable")
	}
}

func changeError(err error) transport.Error {
	if errors.Is(err, changes.ErrCursorExpired) {
		resync := changes.ResyncURL
		return transport.Error{Code: "cursor_expired", Message: "cursor expired; resynchronize", ResyncUrl: &resync}
	}
	return transport.Error{Code: "changes_unavailable", Message: "changes unavailable", Retryable: true}
}

func changeToTransport(c changes.Change) transport.ChangeEvent {
	corpusID := c.CorpusID
	return transport.ChangeEvent{EventId: c.ID, Type: c.Type, SchemaVersion: changeSchemaVersion, OccurredAt: c.OccurredAt.UTC(), Resource: transport.ResourceReference{Kind: c.ResourceKind, Id: c.ResourceID, CorpusId: &corpusID}, Cursor: c.Cursor}
}

// changeRequest authorizes a change-feed request and returns its Corpus and cursor.
func (a *API) changeRequest(w http.ResponseWriter, r *http.Request, scope corpus.Scope, allowed map[string]bool) (string, string, bool) {
	if a.Changes.Journal == nil {
		failure(w, 404, "not_found")
		return "", "", false
	}
	q := r.URL.Query()
	for k, v := range q {
		if !allowed[k] || len(v) != 1 {
			failure(w, 422, "invalid_query")
			return "", "", false
		}
	}
	if q.Has("cursor") && q.Get("cursor") == "" {
		failure(w, 422, "invalid_cursor")
		return "", "", false
	}
	if !scope.Allows("changes:read") {
		failure(w, 403, "forbidden")
		return "", "", false
	}
	corpusID := q.Get("corpus_id")
	if corpusID == "" {
		failure(w, 422, "invalid_query")
		return "", "", false
	}
	if !scope.Contains(corpusID) {
		failure(w, 404, "not_found")
		return "", "", false
	}
	if _, err := a.Service.Store.Read(r.Context(), scope.Organization, corpusID); errors.Is(err, corpus.ErrNotFound) {
		failure(w, 404, "not_found")
		return "", "", false
	} else if err != nil {
		failure(w, 503, "changes_unavailable")
		return "", "", false
	}
	return corpusID, q.Get("cursor"), true
}

func (a *API) pollChanges(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	corpusID, cursor, ok := a.changeRequest(w, r, scope, map[string]bool{"corpus_id": true, "cursor": true, "limit": true})
	if !ok {
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			failure(w, 422, "invalid_limit")
			return
		}
		limit = n
	}
	page, err := a.Changes.Poll(r.Context(), scope, corpusID, cursor, limit)
	if err != nil {
		changeFailure(w, err)
		return
	}
	items := make([]transport.ChangeEvent, 0, len(page.Items))
	for _, c := range page.Items {
		items = append(items, changeToTransport(c))
	}
	send(w, 200, transport.ChangePage{Items: items, NextCursor: page.Next, HasMore: page.HasMore})
}

// streamChanges serves SSE over the same journal. It runs without the request
// timeout; the client connection bounds its lifetime.
func (a *API) streamChanges(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	setup, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	corpusID, cursor, ok := a.changeRequest(w, r.WithContext(setup), scope, map[string]bool{"corpus_id": true, "cursor": true})
	if !ok {
		return
	}
	if last := r.Header.Get("Last-Event-ID"); last != "" {
		cursor = last
	}
	position, err := a.Changes.Start(setup, scope, corpusID, cursor)
	if err != nil {
		changeFailure(w, err)
		return
	}
	cancel()
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	write := func(format string, args ...any) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if cursor == "" {
		c := a.Changes.Cursor(scope, corpusID, position)
		if !write("id: %s\nevent: checkpoint\ndata: {\"cursor\":%q}\n\n", c, c) {
			return
		}
	} else if !write(": resumed\n\n") {
		return
	}
	slog.Info("change stream opened", "organization", scope.Organization, "corpus_id", corpusID)
	defer slog.Info("change stream closed", "organization", scope.Organization, "corpus_id", corpusID)
	ticker := time.NewTicker(streamPollInterval)
	defer ticker.Stop()
	lastWrite := time.Now()
	for {
		read, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		page, err := a.Changes.Read(read, scope, corpusID, position, streamPageSize)
		cancel()
		if r.Context().Err() != nil {
			return
		}
		if err != nil {
			b, _ := json.Marshal(changeError(err))
			write("event: stream_error\ndata: %s\n\n", b)
			return
		}
		for _, c := range page.Items {
			b, _ := json.Marshal(changeToTransport(c))
			if !write("id: %s\nevent: change\ndata: %s\n\n", c.Cursor, b) {
				return
			}
			lastWrite = time.Now()
		}
		if len(page.Items) == 0 && page.Position != position {
			if !write("id: %s\nevent: checkpoint\ndata: {\"cursor\":%q}\n\n", page.Next, page.Next) {
				return
			}
			lastWrite = time.Now()
		}
		position = page.Position
		if page.HasMore {
			continue
		}
		if time.Since(lastWrite) >= streamKeepalive {
			if !write(": keepalive\n\n") {
				return
			}
			lastWrite = time.Now()
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
