package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// MaxAttachmentBytes bounds one attachment stored as a Blob Part.
const MaxAttachmentBytes int64 = 25 << 20

// DefaultAttachmentBudget bounds the attachment bytes one run stores; the run
// stops after the page that crosses it and the next run continues.
const DefaultAttachmentBudget int64 = 200 << 20

// BlobDepositor streams bytes into a verified Blob of the Organization. It
// must not buffer the whole stream in memory and must refuse more than max
// bytes with an error wrapping content.ErrInvalid.
type BlobDepositor interface {
	Deposit(ctx context.Context, org string, r io.Reader, mediaType string, max int64) (blobID string, size int64, err error)
}

// ReceiptChecker reports whether an ingestion idempotency key already has a
// Receipt, so an unchanged item revision is skipped before any download.
type ReceiptChecker interface {
	HasReceipt(ctx context.Context, org, key string) (bool, error)
}

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
	// Blobs stores item attachments; Receipts (optional) skips revisions
	// already accepted before their attachments are downloaded.
	Blobs            BlobDepositor
	Receipts         ReceiptChecker
	MaxPages         int
	AttachmentBudget int64
	Now              func() time.Time
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
	budget := a.AttachmentBudget
	if budget <= 0 {
		budget = DefaultAttachmentBudget
	}
	var stored int64
	var rejected string
	for i := 0; i < pages; i++ {
		page, err := connector.Fetch(ctx, FetchRequest{Config: target.Config, Credential: credential, Checkpoint: checkpoint, Now: a.now()})
		if errors.Is(err, ErrNotDue) && i == 0 {
			slog.Info("connector run skipped", "connector_id", id, "reason", "not_due")
			return a.Store.FinishRun(ctx, org, id, run, &RunError{Skipped: true, At: a.now()})
		}
		if errors.Is(err, ErrNotDue) {
			break
		}
		if err != nil {
			var typed *Error
			if errors.As(err, &typed) {
				return failure(typed.Class, typed.Code)
			}
			return failure(ClassTransient, "source_unavailable")
		}
		// Only an item that reserves a new Version counts as source activity;
		// a replayed Receipt (re-fetched unchanged item) does not.
		fresh := false
		for _, item := range page.Items {
			size, created, err := a.submit(ctx, scope, target.Instance, item)
			if err != nil {
				var typed *Error
				switch {
				case errors.As(err, &typed):
					return failure(typed.Class, typed.Code)
				case errors.Is(err, errSourceRead):
					return failure(ClassTransient, "source_unavailable")
				case errors.Is(err, errSkipped):
					continue
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
			stored += size
			fresh = fresh || created
		}
		ok, err := a.Store.CommitCheckpoint(ctx, org, id, run, page.Checkpoint, fresh)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		checkpoint = page.Checkpoint
		if !page.More || stored >= budget {
			break
		}
	}
	if rejected != "" {
		slog.Warn("connector item rejected", "connector_id", id, "code", rejected)
		return a.Store.FinishRun(ctx, org, id, run, &RunError{Class: ClassSource, Code: rejected, At: a.now(), Completed: true})
	}
	return a.Store.FinishRun(ctx, org, id, run, nil)
}

var (
	// errSkipped marks an item whose revision was already accepted.
	errSkipped = errors.New("already accepted")
	// errSourceRead marks an untyped failure while streaming from the source.
	errSourceRead = errors.New("source read failed")
)

// submit sends one item through the ingestion command path. It returns the
// attachment bytes it stored and whether the item reserved a new Record
// Version (replays and withdrawals do not).
func (a Acquirer) submit(ctx context.Context, scope corpus.Scope, inst Instance, item Item) (int64, bool, error) {
	source := content.Source{CorpusID: inst.CorpusID, Namespace: inst.Namespace, RecordKey: item.RecordKey}
	revision := item.Revision
	if revision == "" {
		type attachmentShape struct{ Key, Role, MediaType string }
		shapes := make([]attachmentShape, len(item.Attachments))
		for i, at := range item.Attachments {
			shapes[i] = attachmentShape{at.Key, at.Role, at.MediaType}
		}
		b, _ := json.Marshal(struct {
			Content     content.Text       `json:"content"`
			Manifest    *content.Manifest  `json:"manifest,omitempty"`
			Extensions  content.Extensions `json:"extensions,omitempty"`
			Attachments []attachmentShape  `json:"attachments,omitempty"`
		}{item.Content, item.Manifest, item.Extensions, shapes})
		revision = "sha256:" + content.Hash(b)
	}
	if item.Withdraw {
		_, err := a.Ingest.Withdraw(ctx, scope, content.Withdrawal{Key: KeyPrefix + content.StableID("withdraw", inst.ID, item.RecordKey, revision), Source: source, Reason: "source_withdrawn"})
		return 0, false, err
	}
	key := KeyPrefix + content.StableID("item", inst.ID, item.RecordKey, revision)
	manifest := item.Manifest
	var stored int64
	if len(item.Attachments) > 0 {
		// The key embeds the revision, so only this exact revision is skipped;
		// a changed item still becomes a correction.
		if a.Receipts != nil {
			known, err := a.Receipts.HasReceipt(ctx, scope.Organization, key)
			if err != nil {
				return 0, false, err
			}
			if known {
				return 0, false, errSkipped
			}
		}
		if a.Blobs == nil {
			return 0, false, content.ErrUnsupported
		}
		m := content.Manifest{Kind: "manifest"}
		if manifest != nil {
			m = *manifest
			m.Parts = append([]content.Part(nil), manifest.Parts...)
		}
		for _, at := range item.Attachments {
			id, size, err := a.deposit(ctx, scope.Organization, at)
			if errors.Is(err, errTooLarge) && at.Skip != nil {
				at.Skip("too_large")
				continue
			}
			if err != nil {
				return 0, false, err
			}
			stored += size
			m.Parts = append(m.Parts, content.Part{Key: at.Key, ParentKey: at.ParentKey, Role: at.Role, Content: content.Text{Kind: "blob", BlobID: id, MediaType: at.MediaType}, Extensions: at.Extensions})
		}
		manifest = &m
	}
	c := content.Command{Key: key, Source: source, Revision: revision, Position: item.Position, Content: item.Content, Manifest: manifest, Extensions: item.Extensions, Provenance: map[string]any{"producer": inst.ID, "producer_version": inst.Kind + "/" + ConnectorVersion}}
	if c.Manifest != nil {
		c.Content = content.Text{Kind: "manifest"}
	}
	receipt, err := a.Ingest.Accept(ctx, scope, c)
	return stored, err == nil && receipt.NewRevision, err
}

// errTooLarge marks an attachment whose bytes exceed MaxAttachmentBytes.
var errTooLarge = fmt.Errorf("%w: attachment exceeds %d bytes", content.ErrInvalid, MaxAttachmentBytes)

// meter counts streamed bytes and remembers a source read failure, so a
// failure can be attributed to the source or to storage.
type meter struct {
	r   io.Reader
	n   int64
	err error
}

func (m *meter) Read(p []byte) (int, error) {
	n, err := m.r.Read(p)
	m.n += int64(n)
	if err != nil && err != io.EOF {
		m.err = err
	}
	return n, err
}

// deposit streams one attachment from the source into a verified Blob.
func (a Acquirer) deposit(ctx context.Context, org string, at Attachment) (string, int64, error) {
	if at.Open == nil {
		return "", 0, content.ErrInvalid
	}
	body, err := at.Open(ctx)
	if err != nil {
		var typed *Error
		if errors.As(err, &typed) {
			return "", 0, err
		}
		return "", 0, errSourceRead
	}
	defer body.Close()
	m := &meter{r: body}
	id, size, err := a.Blobs.Deposit(ctx, org, m, at.MediaType, MaxAttachmentBytes)
	switch {
	case err == nil:
		return id, size, nil
	case m.err != nil:
		var typed *Error
		if errors.As(m.err, &typed) {
			return "", 0, m.err
		}
		return "", 0, errSourceRead
	case m.n > MaxAttachmentBytes:
		return "", 0, errTooLarge
	}
	return "", 0, err
}
