// Package changes exposes the Organization commit-ordered journal as a
// resumable public change feed with opaque, scope-bound Change Cursors.
package changes

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

var (
	// ErrCursorInvalid reports a malformed, tampered or unknown Change Cursor.
	ErrCursorInvalid = errors.New("invalid_cursor")
	// ErrCursorScope reports a cursor issued for another Corpus or authorization scope.
	ErrCursorScope = errors.New("cursor_scope_changed")
	// ErrCursorExpired reports that unconsumed positions aged out of retention.
	ErrCursorExpired = errors.New("cursor_expired")
)

// DefaultRetention is the public event-retention window.
const DefaultRetention = 7 * 24 * time.Hour

// ResyncURL documents the resynchronization procedure until a catalog route exists.
const ResyncURL = "/docs/quivr-v2-ingestion-contracts.md#change-feed-and-resynchronization"

// Event is one committed journal fact visible to a Corpus.
type Event struct {
	Position     int64
	ID           string
	Type         string
	CorpusID     string
	ResourceKind string
	ResourceID   string
	OccurredAt   time.Time
}

// Window is one consistent scan of the journal.
type Window struct {
	// Events are the visible events in (after, Through], in commit order.
	Events []Event
	// Through is the last committed position that was fully scanned.
	Through int64
	// Head is the committed head at scan time.
	Head int64
	// Expired is true when the first position after the cursor is older than retention.
	Expired bool
}

// Journal reads committed positions in one snapshot. It returns at most
// limit visible events; when more exist, Through is the last returned one.
type Journal interface {
	ReadChanges(ctx context.Context, organization, corpusID string, after int64, limit int, retention time.Duration) (Window, error)
}

// Change is an Event paired with the cursor that resumes after it.
type Change struct {
	Event
	Cursor string
}

// Page is a batch of changes and the position to resume from.
type Page struct {
	Items    []Change
	Next     string
	Position int64
	HasMore  bool
}

// Service reads the journal for one authorized Corpus.
type Service struct {
	Journal   Journal
	Key       []byte
	Retention time.Duration
}

// Start resolves an optional cursor to a position. An empty cursor means start
// now at the committed head. Expiry of a supplied cursor is detected here, so
// transports can reject it before a stream begins.
func (s Service) Start(ctx context.Context, scope corpus.Scope, corpusID, token string) (int64, error) {
	if token == "" {
		w, err := s.Journal.ReadChanges(ctx, scope.Organization, corpusID, 0, 0, s.retention())
		return w.Head, err
	}
	position, err := s.decode(scope, corpusID, token)
	if err != nil {
		return 0, err
	}
	w, err := s.Journal.ReadChanges(ctx, scope.Organization, corpusID, position, 0, s.retention())
	if err != nil {
		return 0, err
	}
	if position > w.Head {
		return 0, ErrCursorInvalid
	}
	if w.Expired {
		return 0, ErrCursorExpired
	}
	return position, nil
}

// Read returns up to limit visible changes after position.
func (s Service) Read(ctx context.Context, scope corpus.Scope, corpusID string, position int64, limit int) (Page, error) {
	w, err := s.Journal.ReadChanges(ctx, scope.Organization, corpusID, position, limit, s.retention())
	if err != nil {
		return Page{}, err
	}
	if position > w.Head {
		return Page{}, ErrCursorInvalid
	}
	if w.Expired {
		return Page{}, ErrCursorExpired
	}
	page := Page{Items: make([]Change, 0, len(w.Events)), Position: w.Through, HasMore: w.Through < w.Head}
	for _, e := range w.Events {
		page.Items = append(page.Items, Change{Event: e, Cursor: s.Cursor(scope, corpusID, e.Position)})
	}
	page.Next = s.Cursor(scope, corpusID, w.Through)
	return page, nil
}

// Poll implements start-now polling: without a cursor it returns no events
// and the committed head as the next cursor.
func (s Service) Poll(ctx context.Context, scope corpus.Scope, corpusID, token string, limit int) (Page, error) {
	if token == "" {
		position, err := s.Start(ctx, scope, corpusID, "")
		if err != nil {
			return Page{}, err
		}
		return Page{Items: []Change{}, Next: s.Cursor(scope, corpusID, position), Position: position}, nil
	}
	position, err := s.decode(scope, corpusID, token)
	if err != nil {
		return Page{}, err
	}
	return s.Read(ctx, scope, corpusID, position, limit)
}

type cursorPayload struct {
	Version  int    `json:"v"`
	Corpus   string `json:"c"`
	Scope    string `json:"s"`
	Position int64  `json:"p"`
}

// Cursor encodes an opaque Change Cursor bound to scope and Corpus.
func (s Service) Cursor(scope corpus.Scope, corpusID string, position int64) string {
	b, _ := json.Marshal(cursorPayload{1, corpusID, digest(scope), position})
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(s.sign(b))
}

func (s Service) decode(scope corpus.Scope, corpusID, token string) (int64, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return 0, ErrCursorInvalid
	}
	b, e1 := base64.RawURLEncoding.DecodeString(parts[0])
	sig, e2 := base64.RawURLEncoding.DecodeString(parts[1])
	var c cursorPayload
	if e1 != nil || e2 != nil || !hmac.Equal(sig, s.sign(b)) || json.Unmarshal(b, &c) != nil || c.Version != 1 || c.Position < 0 {
		return 0, ErrCursorInvalid
	}
	if c.Corpus != corpusID || c.Scope != digest(scope) {
		return 0, ErrCursorScope
	}
	return c.Position, nil
}

func (s Service) sign(b []byte) []byte {
	h := hmac.New(sha256.New, s.Key)
	h.Write([]byte("change-cursor\x00"))
	h.Write(b)
	return h.Sum(nil)
}

func (s Service) retention() time.Duration {
	if s.Retention <= 0 {
		return DefaultRetention
	}
	return s.Retention
}

func digest(s corpus.Scope) string {
	s.Actions = append([]string{}, s.Actions...)
	s.Corpora = append([]string{}, s.Corpora...)
	sort.Strings(s.Actions)
	sort.Strings(s.Corpora)
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(h[:])
}
