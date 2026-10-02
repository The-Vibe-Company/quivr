package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/changes"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

// Option configures optional API capabilities.
type Option func(*API)

// WithChanges enables the public change feed over the committed journal. An
// open stream reads the journal again every poll; zero is DefaultStreamPoll.
func WithChanges(feed changes.Service, poll time.Duration) Option {
	return func(a *API) { a.Changes, a.changePoll = feed, poll }
}

// DefaultStreamPoll is how often an open change stream reads the journal again.
const DefaultStreamPoll = 250 * time.Millisecond

const (
	changeSchemaVersion = "1"
	streamKeepalive     = 15 * time.Second
	streamWriteTimeout  = 10 * time.Second
	streamPageSize      = 100
)

func changeToTransport(c changes.Change) transport.ChangeEvent {
	corpusID := c.CorpusID
	event := transport.ChangeEvent{EventId: c.ID, Type: c.Type, SchemaVersion: changeSchemaVersion, OccurredAt: c.OccurredAt.UTC(), Resource: transport.ResourceReference{Kind: c.ResourceKind, Id: c.ResourceID, CorpusId: &corpusID}, Cursor: c.Cursor}
	if m := c.Monitoring; m != nil {
		refs := transport.MonitoringReferences{MatchId: m.MatchID, RecordId: m.RecordID, RecordVersionId: m.RecordVersionID, SubscriptionId: m.SubscriptionID, SubscriptionVersionId: m.SubscriptionVersionID, DeliveryId: m.DeliveryID}
		if m.PreviousMatchID != "" {
			refs.PreviousMatchId = &m.PreviousMatchID
		}
		refs.Owner = owner(m.Owner)
		event.Monitoring = &refs
	}
	return event
}

// changeRequest authorizes a change-feed request and returns its Corpus and cursor.
func (a *API) changeRequest(w http.ResponseWriter, r *http.Request, scope corpus.Scope, allowed map[string]bool) (string, string, bool) {
	if a.Changes.Journal == nil {
		writeError(w, publicerr.NotFound, nil)
		return "", "", false
	}
	q := r.URL.Query()
	for k, v := range q {
		if !allowed[k] || len(v) != 1 {
			writeError(w, publicerr.InvalidQuery, nil)
			return "", "", false
		}
	}
	if q.Has("cursor") && q.Get("cursor") == "" {
		writeError(w, publicerr.InvalidCursor, nil)
		return "", "", false
	}
	if !scope.Allows("changes:read") {
		writeError(w, publicerr.Forbidden, nil)
		return "", "", false
	}
	corpusID := q.Get("corpus_id")
	if corpusID == "" {
		writeError(w, publicerr.InvalidQuery, nil)
		return "", "", false
	}
	if !scope.Contains(corpusID) {
		writeError(w, publicerr.NotFound, nil)
		return "", "", false
	}
	if _, err := a.Service.Store.Read(r.Context(), scope.Organization, corpusID); err != nil {
		writeError(w, err, publicerr.ChangesUnavailable)
		return "", "", false
	}
	return corpusID, q.Get("cursor"), true
}

func (a *API) pollChanges(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	corpusID, cursor, ok := a.changeRequest(w, r, scope, map[string]bool{"corpus_id": true, "cursor": true, "limit": true})
	if !ok {
		return
	}
	limit, ok := pageLimit(w, r.URL.Query(), 100, 100)
	if !ok {
		return
	}
	page, err := a.Changes.Poll(r.Context(), scope, corpusID, cursor, limit)
	if err != nil {
		writeError(w, err, publicerr.ChangesUnavailable, corpusID)
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
		writeError(w, err, publicerr.ChangesUnavailable, corpusID)
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
	poll := a.changePoll
	if poll <= 0 {
		poll = DefaultStreamPoll
	}
	ticker := time.NewTicker(poll)
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
			_, body := errorResponse(err, publicerr.ChangesUnavailable, corpusID)
			b, _ := json.Marshal(body)
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
