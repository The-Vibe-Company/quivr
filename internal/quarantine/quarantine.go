// Package quarantine lists the Record Versions stuck in quarantine and
// reprocesses them (Spec 5). A reprocess reruns the step a Version failed at,
// its normalization or its ingestion, with the Pipeline Plan active when the
// reprocess is accepted. A Version that succeeds then goes through the normal
// publication path, as for a first success: it becomes current and
// searchable, its alerts are evaluated and the change feed announces it. One
// that fails again stays quarantined with its new reason.
//
// Only Versions that can still become current are stuck: a Version its
// Record no longer desires (a newer revision was accepted) or whose Record
// was withdrawn is neither listed nor reprocessed.
package quarantine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
)

var (
	// ErrDryRunRequired refuses a reprocess no dry run with the same key and
	// scope was recorded for.
	ErrDryRunRequired = publicerr.DryRunRequired
	// ErrInProgress refuses a reprocess of a Corpus another reprocess has
	// not finished: both would rerun the same Versions.
	ErrInProgress = publicerr.ReprocessInProgress
	// ErrInvalid refuses a scope the reprocess cannot use; the error says why.
	ErrInvalid = publicerr.InvalidReprocess
)

// Filter selects quarantined Versions. Every set field narrows the selection:
// the Corpus, the plugin the quarantine reason names, the reason code, and a
// window on when the Version was quarantined (after inclusive, before
// exclusive).
type Filter struct {
	CorpusID      string
	Plugin, Code  string
	After, Before *time.Time
}

// Entry is one quarantined Version with the step it failed at and its reason.
type Entry struct {
	VersionID, RecordID, CorpusID, ReceiptID string
	// Stage is content.QuarantineNormalization or content.QuarantineIngestion.
	Stage string
	// Reason is the recorded reason; a quarantine that predates structured
	// reasons has only its code, and names no plugin.
	Reason content.Diagnostic
	// QuarantinedAt is when it was quarantined, or else when its revision
	// was accepted.
	QuarantinedAt time.Time
}

// Store reads quarantined Versions and keeps reprocess requests.
type Store interface {
	// Quarantined lists, in Version id order after the given one, the stuck
	// Versions of an Organization the filter keeps, in corpora (all when
	// nil).
	Quarantined(ctx context.Context, org string, corpora []string, f Filter, after string, limit int) ([]Entry, error)
	// ReprocessSize counts the Versions a reprocess of f would take now.
	ReprocessSize(ctx context.Context, org string, f Filter) (operations.ReprocessEstimate, error)
	// RecordReprocessEstimate keeps a dry run under the request key,
	// replacing an earlier dry run of the same canonical request; another
	// canonical request under the key is operations.ErrConflict.
	RecordReprocessEstimate(ctx context.Context, org, corpusID, key string, canonical []byte, e operations.ReprocessEstimate) error
	// ReprocessEstimate returns the dry run recorded under the key, or
	// corpus.ErrNotFound.
	ReprocessEstimate(ctx context.Context, org, corpusID, key string) ([]byte, operations.ReprocessEstimate, error)
	// AcceptReprocess commits a queued reprocess Operation that takes the
	// Versions f keeps at this moment and is pinned to the active plan, or
	// returns the one already accepted for the same key and canonical
	// request; another canonical request under the key is
	// operations.ErrConflict, and a Corpus another reprocess has not
	// finished is ErrInProgress.
	AcceptReprocess(ctx context.Context, org, key string, canonical []byte, f Filter, e operations.ReprocessEstimate) (operations.Operation, error)
}

// Service lists quarantined Versions and accepts reprocess requests. Both
// need plugins:admin, on a key of the Organization that grants the Corpora.
type Service struct {
	Store Store
}

// Request is one reprocess command: a Corpus and optional filters.
type Request struct {
	Key    string
	Filter Filter
	DryRun bool
}

// MaxPage bounds one page of the listing.
const MaxPage = 100

// List reads one page of the stuck Versions the filter keeps within the
// caller's Corpora, after the Version id of the previous page.
func (s Service) List(ctx context.Context, scope corpus.Scope, f Filter, after string, limit int, prepare ...func() (Filter, string, int, error)) ([]Entry, error) {
	if err := scope.Require(corpus.ActionQuarantineList); err != nil {
		return nil, err
	}
	for _, load := range prepare {
		var err error
		f, after, limit, err = load()
		if err != nil {
			return nil, err
		}
	}
	if err := checkWindow(f); err != nil {
		return nil, fmt.Errorf("%w: %w", publicerr.InvalidQuery, err)
	}
	var corpora []string
	switch {
	case f.CorpusID != "" && !scope.Contains(f.CorpusID):
		return nil, corpus.ErrNotFound
	case !scope.AllCorpora():
		corpora = append([]string{}, scope.Corpora...)
	}
	if limit <= 0 || limit > MaxPage {
		limit = MaxPage
	}
	entries, err := s.Store.Quarantined(ctx, scope.Organization, corpora, f, after, limit)
	if errors.Is(err, ErrInvalid) {
		err = fmt.Errorf("%w: %w", publicerr.InvalidQuery, err)
	}
	return entries, err
}

// Request answers a dry run with its count, which it records, or accepts a
// reprocess a dry run with the same key and scope preceded.
func (s Service) Request(ctx context.Context, scope corpus.Scope, r Request, prepare ...func() (Request, error)) (operations.ReprocessEstimate, operations.Operation, error) {
	var none operations.ReprocessEstimate
	if err := scope.Require(corpus.ActionQuarantineRequest); err != nil {
		return none, operations.Operation{}, err
	}
	for _, load := range prepare {
		var err error
		r, err = load()
		if err != nil {
			return none, operations.Operation{}, err
		}
	}
	f := r.Filter
	if f.CorpusID == "" || !scope.Contains(f.CorpusID) {
		return none, operations.Operation{}, corpus.ErrNotFound
	}
	if err := checkWindow(f); err != nil {
		return none, operations.Operation{}, err
	}
	f.After, f.Before = utc(f.After), utc(f.Before)
	canonical, err := canonicalRequest(r.Key, f)
	if err != nil {
		return none, operations.Operation{}, err
	}
	if r.DryRun {
		e, err := s.Store.ReprocessSize(ctx, scope.Organization, f)
		if err != nil {
			return none, operations.Operation{}, err
		}
		if err = s.Store.RecordReprocessEstimate(ctx, scope.Organization, f.CorpusID, r.Key, canonical, e); err != nil {
			return none, operations.Operation{}, err
		}
		return e, operations.Operation{}, nil
	}
	// Every accepted reprocess had its dry run recorded under its key, so
	// the recorded dry run decides; an accepted key replays its Operation.
	recorded, e, err := s.Store.ReprocessEstimate(ctx, scope.Organization, f.CorpusID, r.Key)
	if errors.Is(err, corpus.ErrNotFound) {
		return none, operations.Operation{}, ErrDryRunRequired
	}
	if err != nil {
		return none, operations.Operation{}, err
	}
	if string(recorded) != string(canonical) {
		return none, operations.Operation{}, operations.ErrConflict
	}
	op, err := s.Store.AcceptReprocess(ctx, scope.Organization, r.Key, canonical, f, e)
	if err == nil && op.Reprocess != nil {
		e = op.Reprocess.Estimate
	}
	return e, op, err
}

func checkWindow(f Filter) error {
	if f.After != nil && f.Before != nil && !f.After.Before(*f.Before) {
		return fmt.Errorf("%w: quarantined_after must be before quarantined_before", ErrInvalid)
	}
	return nil
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// canonicalRequest is what idempotent replay compares: the key and the
// scope, never dry_run.
func canonicalRequest(key string, f Filter) ([]byte, error) {
	format := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.Format(time.RFC3339Nano)
	}
	return json.Marshal(struct {
		Key    string `json:"idempotency_key"`
		Corpus string `json:"corpus_id"`
		Plugin string `json:"plugin"`
		Code   string `json:"code"`
		After  string `json:"quarantined_after"`
		Before string `json:"quarantined_before"`
	}{key, f.CorpusID, f.Plugin, f.Code, format(f.After), format(f.Before)})
}
