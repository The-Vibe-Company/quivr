package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

// KeyPrefix reserves an idempotency-key family for connector-originated
// commands; the public API refuses it so a client cannot pre-claim a key.
const KeyPrefix = "connector:"

// IsConnectorKey reports whether a key belongs to the reserved family.
func IsConnectorKey(key string) bool { return strings.HasPrefix(key, KeyPrefix) }

// ConnectorVersion versions the built-in item mapping recorded in provenance:
// producer is the Connector Instance, producer_version is "<kind>/<version>".
const ConnectorVersion = "v1"

// DefaultMaxPages bounds the pages one run may fetch.
const DefaultMaxPages = 10

// Target is everything one acquisition run needs, loaded at run start.
type Target struct {
	Instance
	RunSequence int64
	Checkpoint  json.RawMessage
	Sealed      *Sealed
}

// RunStore persists acquisition progress.
type RunStore interface {
	LoadRun(ctx context.Context, org, id string) (Target, error)
	// CommitCheckpoint durably advances the Acquisition Checkpoint of the
	// given run; it reports false when the run is stale or the instance is
	// disabled, which ends the run.
	CommitCheckpoint(ctx context.Context, org, id string, run int64, checkpoint json.RawMessage, items bool) (bool, error)
	// FinishRun ends the run, schedules the next one and commits re-evaluated
	// Connector Health (with its event) in one transaction.
	FinishRun(ctx context.Context, org, id string, run int64, failure *RunError) error
}

// Ingestor is the internal ingestion command path shared with the public API.
type Ingestor interface {
	Accept(context.Context, corpus.Scope, content.Command) (content.Receipt, error)
	Withdraw(context.Context, corpus.Scope, content.Withdrawal) (content.Receipt, error)
}

// Acquirer executes one acquisition run of one instance.
type Acquirer struct {
	Store    RunStore
	Registry *Registry
	Sealer   Sealer
	Ingest   Ingestor
	MaxPages int
	Now      func() time.Time
}

func (a Acquirer) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Run fetches pages since the checkpoint, submits every item through the
// ingestion command path and only then commits the page's checkpoint. A crash
// between the two re-fetches the same items, whose deterministic idempotency
// keys replay the same Receipts. Source and ingestion failures end the run and
// are recorded on the instance; only store failures are returned for retry.
func (a Acquirer) Run(ctx context.Context, org, id string, run int64) error {
	target, err := a.Store.LoadRun(ctx, org, id)
	if err != nil {
		return err
	}
	if !target.Enabled || target.RunSequence != run {
		return nil
	}
	failure := func(class ErrorClass, code string) error {
		slog.Warn("connector acquisition failed", "connector_id", id, "class", string(class), "code", code)
		return a.Store.FinishRun(ctx, org, id, run, &RunError{Class: class, Code: code, At: a.now()})
	}
	connector, ok := a.Registry.Lookup(target.Kind)
	if !ok {
		return failure(ClassSource, "unsupported_connector_kind")
	}
	var credential json.RawMessage
	if target.Sealed != nil {
		if target.Sealed.ExpiresAt != nil && !target.Sealed.ExpiresAt.After(a.now()) {
			return failure(ClassAccess, "credential_expired")
		}
		plaintext, err := a.Sealer.Open(org, id, *target.Sealed)
		if err != nil {
			return failure(ClassAccess, "credential_unreadable")
		}
		credential = plaintext
	}
	scope := corpus.Scope{Organization: org, Actions: []string{"content:write", "content:read"}, Corpora: []string{target.CorpusID}}
	checkpoint := target.Checkpoint
	pages := a.MaxPages
	if pages <= 0 {
		pages = DefaultMaxPages
	}
	var rejected string
	for i := 0; i < pages; i++ {
		page, err := connector.Fetch(ctx, FetchRequest{Config: target.Config, Credential: credential, Checkpoint: checkpoint, Now: a.now()})
		if err != nil {
			var typed *Error
			if errors.As(err, &typed) {
				return failure(typed.Class, typed.Code)
			}
			return failure(ClassTransient, "source_unavailable")
		}
		accepted := false
		for _, item := range page.Items {
			if err = a.submit(ctx, scope, target.Instance, item); err != nil {
				switch {
				case errors.Is(err, corpus.ErrNotFound), errors.Is(err, corpus.ErrForbidden):
					return failure(ClassAccess, "corpus_unavailable")
				case errors.Is(err, content.ErrConflict), errors.Is(err, content.ErrInvalid), errors.Is(err, content.ErrUnsupported), errors.Is(err, content.ErrUnverifiedBlob):
					// The item can never be accepted as-is; record it and move on so
					// one bad item cannot stall the source.
					rejected = "item_rejected"
				default:
					return failure(ClassTransient, "ingestion_unavailable")
				}
				continue
			}
			accepted = true
		}
		ok, err := a.Store.CommitCheckpoint(ctx, org, id, run, page.Checkpoint, accepted)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		checkpoint = page.Checkpoint
		if !page.More {
			break
		}
	}
	if rejected != "" {
		slog.Warn("connector item rejected", "connector_id", id, "code", rejected)
		return a.Store.FinishRun(ctx, org, id, run, &RunError{Class: ClassSource, Code: rejected, At: a.now(), Completed: true})
	}
	return a.Store.FinishRun(ctx, org, id, run, nil)
}

func (a Acquirer) submit(ctx context.Context, scope corpus.Scope, inst Instance, item Item) error {
	source := content.Source{CorpusID: inst.CorpusID, Namespace: inst.Namespace, RecordKey: item.RecordKey}
	revision := item.Revision
	if revision == "" {
		b, _ := json.Marshal(struct {
			Content    content.Text       `json:"content"`
			Manifest   *content.Manifest  `json:"manifest,omitempty"`
			Extensions content.Extensions `json:"extensions,omitempty"`
		}{item.Content, item.Manifest, item.Extensions})
		revision = "sha256:" + content.Hash(b)
	}
	if item.Withdraw {
		_, err := a.Ingest.Withdraw(ctx, scope, content.Withdrawal{Key: KeyPrefix + content.StableID("withdraw", inst.ID, item.RecordKey, revision), Source: source, Reason: "source_withdrawn"})
		return err
	}
	c := content.Command{Key: KeyPrefix + content.StableID("item", inst.ID, item.RecordKey, revision), Source: source, Revision: revision, Position: item.Position, Content: item.Content, Manifest: item.Manifest, Extensions: item.Extensions, Provenance: map[string]any{"producer": inst.ID, "producer_version": inst.Kind + "/" + ConnectorVersion}}
	if c.Manifest != nil {
		c.Content = content.Text{Kind: "manifest"}
	}
	_, err := a.Ingest.Accept(ctx, scope, c)
	return err
}
