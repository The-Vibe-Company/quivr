// Package changes exposes the durable journal with stable per-Record order as a
// resumable public change feed with opaque, scope-bound Change Cursors.
package changes

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	"github.com/The-Vibe-Company/quivr/internal/visibility"
)

var (
	// ErrCursorInvalid reports a malformed, tampered or unknown Change Cursor.
	ErrCursorInvalid = publicerr.InvalidCursor
	// ErrCursorScope reports a cursor issued for another Corpus or authorization scope.
	ErrCursorScope = publicerr.CursorScopeChanged
	// ErrCursorExpired reports that unconsumed positions aged out of retention.
	ErrCursorExpired = publicerr.CursorExpired
)

// DefaultRetention is the public event-retention window.
const DefaultRetention = 7 * 24 * time.Hour

// ResyncURL is the API-relative reference from which a client restarts
// resynchronization of a Corpus: the first page of its authorized Record catalog.
func ResyncURL(corpusID string) string {
	return "/v0/records?" + url.Values{"corpus_id": {corpusID}}.Encode()
}

// Event is one committed journal fact visible to a Corpus.
type Event struct {
	Position     int64
	ID           string
	Type         string
	CorpusID     string
	ResourceKind string
	ResourceID   string
	OccurredAt   time.Time
	// Monitoring carries the references of a monitoring notice event; it is
	// nil for every other event.
	Monitoring *References
}

// References are the reference-only identifiers a monitoring notice shares
// with its webhook body. They confer no access by themselves.
type References struct {
	MatchID, RecordID, RecordVersionID, SubscriptionID, SubscriptionVersionID, DeliveryID, PreviousMatchID string
	// Owner is the Subscription Owner ("" for a global Subscription).
	Owner string
}

// Window is one consistent scan of the journal.
type Window struct {
	// Events are the visible events in (after, Through], in stable publication order.
	Events []Event
	// Through is the last committed position that was fully scanned.
	Through int64
	// Head is the committed head at scan time.
	Head int64
	// Expired is true when the cursor precedes the pruned watermark, or when
	// the first position after it is older than retention.
	Expired bool
	// Resume is the state to carry to the next read when the journal supports
	// snapshot-aware starts. Legacy journals leave ResumeSet false.
	Resume    Resume
	ResumeSet bool
	// StartCeiling is the sequence ceiling captured with Resume.StartID. It is
	// used only to omit the snapshot reference from event cursors after the
	// start has been fully crossed.
	StartCeiling int64
}

// Resume is the state needed to continue a change-feed traversal. Position
// remains the public numeric position used by v1 cursors; StartID identifies a
// short-lived, scope-bound snapshot exclusion state when present.
type Resume struct {
	Position int64
	StartID  string
}

// Journal reads committed positions in one snapshot. It returns at most
// limit visible events; when more exist, Through is the last returned one.
type Journal interface {
	ReadChanges(ctx context.Context, organization, corpusID string, after int64, limit int, retention time.Duration) (Window, error)
}

// VisibilityJournal rechecks the current policy for an event about to be emitted.
type VisibilityJournal interface {
	VisibleChange(ctx context.Context, organization, corpusID string, position int64) (bool, error)
}

func (s Service) VisibleChange(ctx context.Context, scope corpus.Scope, event Event) (bool, error) {
	if _, enforced := visibility.Principals(ctx); !enforced {
		return true, nil
	}
	if journal, ok := s.Journal.(VisibilityJournal); ok {
		return journal.VisibleChange(ctx, scope.Organization, event.CorpusID, event.Position)
	}
	return false, publicerr.ChangesUnavailable
}

// SnapshotJournal adds exact start-now and resume support without changing the
// legacy Journal contract. In-memory and other lightweight Journal fakes can
// continue to implement only ReadChanges.
type SnapshotJournal interface {
	Journal
	StartChanges(ctx context.Context, organization, corpusID string, retention time.Duration) (Resume, error)
	ReadChangesResume(ctx context.Context, organization, corpusID string, resume Resume, limit int, retention time.Duration) (Window, error)
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
	Resume   Resume
}

// Service reads the journal for one authorized Corpus.
type Service struct {
	Corpora   corpus.Store
	Journal   Journal
	Key       []byte
	Retention time.Duration
}

// Start resolves an optional cursor to a position. An empty cursor means start
// now at the committed head. Expiry of a supplied cursor is detected here, so
// transports can reject it before a stream begins.
func (s Service) Start(ctx context.Context, scope corpus.Scope, corpusID, token string) (Resume, error) {

	if err := scope.Require(corpus.ActionChangesStart); err != nil {
		return Resume{}, err
	}
	if err := s.authorize(ctx, scope, corpusID); err != nil {
		return Resume{}, err
	}
	return s.start(ctx, scope, corpusID, token)
}

func (s Service) start(ctx context.Context, scope corpus.Scope, corpusID, token string) (Resume, error) {
	if token == "" {
		if snapshot, ok := s.Journal.(SnapshotJournal); ok {
			return snapshot.StartChanges(ctx, scope.Organization, corpusID, s.retention())
		}
		w, err := s.Journal.ReadChanges(ctx, scope.Organization, corpusID, 0, 0, s.retention())
		return Resume{Position: w.Head}, err
	}
	resume, err := s.decode(scope, corpusID, token)
	if err != nil {
		return Resume{}, err
	}
	w, err := s.readWindow(ctx, scope, corpusID, resume, 0)
	if err != nil {
		return Resume{}, err
	}
	if w.Expired {
		return Resume{}, ErrCursorExpired
	}
	if resume.Position > w.Head {
		return Resume{}, ErrCursorInvalid
	}
	if w.ResumeSet {
		resume = w.Resume
		resume.Position = w.Through
	}
	return resume, nil
}

// Read returns up to limit visible changes after a resume state. An int64 is
// also accepted for callers that still hold a v1 numeric position.
func (s Service) Read(ctx context.Context, scope corpus.Scope, corpusID string, position any, limit int) (Page, error) {

	if err := scope.Require(corpus.ActionChangesRead); err != nil {
		return Page{}, err
	}
	if err := s.authorize(ctx, scope, corpusID); err != nil {
		return Page{}, err
	}
	resume, err := asResume(position)
	if err != nil {
		return Page{}, err
	}
	return s.read(ctx, scope, corpusID, resume, limit)
}

func (s Service) readWindow(ctx context.Context, scope corpus.Scope, corpusID string, resume Resume, limit int) (Window, error) {
	if _, enforced := visibility.Principals(ctx); enforced {
		if _, ok := s.Journal.(VisibilityJournal); !ok {
			return Window{}, publicerr.ChangesUnavailable
		}
	}
	if snapshot, ok := s.Journal.(SnapshotJournal); ok {
		return snapshot.ReadChangesResume(ctx, scope.Organization, corpusID, resume, limit, s.retention())
	}
	return s.Journal.ReadChanges(ctx, scope.Organization, corpusID, resume.Position, limit, s.retention())
}

func (s Service) read(ctx context.Context, scope corpus.Scope, corpusID string, resume Resume, limit int) (Page, error) {
	w, err := s.readWindow(ctx, scope, corpusID, resume, limit)
	if err != nil {
		return Page{}, err
	}
	if w.Expired {
		return Page{}, ErrCursorExpired
	}
	if resume.Position > w.Head {
		return Page{}, ErrCursorInvalid
	}
	next := Resume{Position: w.Through, StartID: resume.StartID}
	if w.ResumeSet {
		next = w.Resume
		next.Position = w.Through
	}
	page := Page{Items: make([]Change, 0, len(w.Events)), Position: w.Through, HasMore: w.Through < w.Head, Resume: next}
	for _, e := range w.Events {
		eventResume := Resume{Position: e.Position, StartID: resume.StartID}
		if w.ResumeSet {
			eventResume.StartID = w.Resume.StartID
			if resume.StartID != "" && w.StartCeiling > 0 && e.Position < w.StartCeiling {
				eventResume.StartID = resume.StartID
			}
		}
		page.Items = append(page.Items, Change{Event: e, Cursor: s.CursorResume(scope, corpusID, eventResume)})
	}
	page.Next = s.CursorResume(scope, corpusID, next)
	return page, nil
}

// Poll implements start-now polling: without a cursor it returns no events
// and the exact start-now boundary as the next cursor.
func (s Service) Poll(ctx context.Context, scope corpus.Scope, corpusID, token string, limit int, prepare ...func() (int, error)) (Page, error) {

	if err := scope.Require(corpus.ActionChangesPoll); err != nil {
		return Page{}, err
	}
	if err := s.authorize(ctx, scope, corpusID); err != nil {
		return Page{}, err
	}
	for _, load := range prepare {
		var err error
		limit, err = load()
		if err != nil {
			return Page{}, err
		}
	}
	return s.poll(ctx, scope, corpusID, token, limit)
}

func (s Service) poll(ctx context.Context, scope corpus.Scope, corpusID, token string, limit int) (Page, error) {
	if token == "" {
		resume, err := s.start(ctx, scope, corpusID, "")
		if err != nil {
			return Page{}, err
		}
		return Page{Items: []Change{}, Next: s.CursorResume(scope, corpusID, resume), Position: resume.Position, Resume: resume}, nil
	}
	resume, err := s.decode(scope, corpusID, token)
	if err != nil {
		return Page{}, err
	}
	return s.read(ctx, scope, corpusID, resume, limit)
}

type cursorPayload struct {
	Version  int    `json:"v"`
	Corpus   string `json:"c"`
	Scope    string `json:"s"`
	Position int64  `json:"p"`
	StartID  string `json:"r,omitempty"`
}

// Cursor encodes an opaque Change Cursor bound to scope and Corpus.
func (s Service) Cursor(scope corpus.Scope, corpusID string, position int64) string {
	return s.CursorResume(scope, corpusID, Resume{Position: position})
}

// CursorResume encodes a cursor with optional snapshot-aware start state. A
// resume without StartID keeps the original v1 payload shape and signature.
func (s Service) CursorResume(scope corpus.Scope, corpusID string, resume Resume) string {
	version := 1
	if resume.StartID != "" {
		version = 2
	}
	b, _ := json.Marshal(cursorPayload{Version: version, Corpus: corpusID, Scope: digest(scope), Position: resume.Position, StartID: resume.StartID})
	if len(scope.Principals) > 0 {
		// A cursor's encoded length must be independent of journal position
		// and whether a short-lived start snapshot is still pinned.
		size := 256 + 6*len(corpusID)
		b = append(b, bytes.Repeat([]byte{' '}, max(0, size-len(b)))...)
		sealed := s.visibilityCursorCipher().Seal(nil, nil, b, nil)
		return "v3." + base64.RawURLEncoding.EncodeToString(sealed)
	}
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(s.sign(b))
}

func (s Service) decode(scope corpus.Scope, corpusID, token string) (Resume, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return Resume{}, ErrCursorInvalid
	}
	var b []byte
	if parts[0] == "v3" {
		sealed, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return Resume{}, ErrCursorInvalid
		}
		b, err = s.visibilityCursorCipher().Open(nil, nil, sealed, nil)
		if err != nil {
			return Resume{}, ErrCursorInvalid
		}
	} else {
		var err error
		b, err = base64.RawURLEncoding.DecodeString(parts[0])
		sig, signErr := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil || signErr != nil || !hmac.Equal(sig, s.sign(b)) {
			return Resume{}, ErrCursorInvalid
		}
	}
	var c cursorPayload
	if json.Unmarshal(b, &c) != nil || (c.Version != 1 && c.Version != 2) || (c.Version == 1 && c.StartID != "") || c.Position < 0 {
		return Resume{}, ErrCursorInvalid
	}
	if c.Corpus != corpusID || c.Scope != digest(scope) {
		return Resume{}, ErrCursorScope
	}
	return Resume{Position: c.Position, StartID: c.StartID}, nil
}

func asResume(value any) (Resume, error) {
	switch v := value.(type) {
	case Resume:
		if v.Position < 0 {
			return Resume{}, ErrCursorInvalid
		}
		return v, nil
	case *Resume:
		if v == nil || v.Position < 0 {
			return Resume{}, ErrCursorInvalid
		}
		return *v, nil
	case int64:
		if v < 0 {
			return Resume{}, ErrCursorInvalid
		}
		return Resume{Position: v}, nil
	case int:
		if v < 0 {
			return Resume{}, ErrCursorInvalid
		}
		return Resume{Position: int64(v)}, nil
	default:
		return Resume{}, ErrCursorInvalid
	}
}

// A signed legacy cursor exposes its numeric position. Delegated callers
// receive an authenticated encrypted cursor so hidden events leave no gap.
func (s Service) visibilityCursorCipher() cipher.AEAD {
	key := hmac.New(sha256.New, s.Key)
	key.Write([]byte("quivr/visibility-change-cursor/v3"))
	block, _ := aes.NewCipher(key.Sum(nil))
	sealed, _ := cipher.NewGCMWithRandomNonce(block)
	return sealed
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
	s.ActorID = "" // Audit attribution does not change the cursor scope.
	s.VisibilityMode = ""
	s.Actions = append([]string{}, s.Actions...)
	s.Corpora = append([]string{}, s.Corpora...)
	sort.Strings(s.Actions)
	sort.Strings(s.Corpora)
	b, _ := json.Marshal(s)
	if len(s.Principals) > 0 {
		principals, _ := json.Marshal(s.Principals)
		b = append(b, principals...)
	}
	h := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// ErrInvalidCorpus refuses a missing change-feed collection after permission.
var ErrInvalidCorpus = publicerr.InvalidQuery

func (s Service) authorize(ctx context.Context, scope corpus.Scope, id string) error {
	if id == "" {
		return ErrInvalidCorpus
	}
	if !scope.Contains(id) {
		return corpus.ErrNotFound
	}
	if s.Corpora != nil {
		c, err := s.Corpora.Read(ctx, scope.Organization, id)
		if err == nil && c.Archived {
			return corpus.ErrArchived
		}
		return err
	}
	return nil
}
