package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
)

// KeyPrefix reserves an idempotency-key family for connector-originated
// commands; the public API refuses it so a client cannot pre-claim a key.
const KeyPrefix = "connector:"

// IsConnectorKey reports whether a key belongs to the reserved family.
func IsConnectorKey(key string) bool { return strings.HasPrefix(key, KeyPrefix) }

// ConnectorVersion versions the built-in item mapping recorded in provenance:
// producer is the Connector Instance, producer_version is "<kind>/<version>".
const ConnectorVersion = "v1"

// DefaultMaxBulkIngestionWaiting bounds prefetched ingestion work, not usage.
const DefaultMaxBulkIngestionWaiting int64 = 1000

// DefaultMaxPages bounds the pages one run may fetch.
const DefaultMaxPages = 10

// MaxSubmissionConcurrency bounds concurrent item submissions per page.
const MaxSubmissionConcurrency = 32

// MaxAttachmentBytes bounds one attachment stored as a Blob Part.
const MaxAttachmentBytes int64 = 25 << 20

// DefaultAttachmentBudget bounds the attachment bytes one run grants; the run
// stops after the page that crosses it and the next run continues.
const DefaultAttachmentBudget int64 = 200 << 20

// DefaultSoftRunLimit bounds how long one run keeps starting pages: after
// the page that crosses it is committed, the run ends and the next run
// continues, so a run with slow attachment uploads still ends well within
// its activity deadline.
const DefaultSoftRunLimit = 2 * time.Minute

// BlobGrants issues upload grants for attachment bytes and verifies what was
// stored (uploads.Service). Grant answers a verified session carrying the
// Blob when one with the same identity exists, and uploads nothing.
type BlobGrants interface {
	Grant(ctx context.Context, org string, req uploads.Request) (uploads.Session, error)
	Confirm(ctx context.Context, org, id string) (uploads.Session, error)
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
	// ReadsToday is the usage counter of the current UTC day at run start.
	ReadsToday int64
}

// Progress is what one accepted page advances: the checkpoint, whether it
// carried items, the source resources it read and the kind's diagnostics.
type Progress struct {
	Checkpoint  json.RawMessage
	Items       bool
	Reads       int64
	Diagnostics json.RawMessage
	// Push is the page's push report, nil for none.
	Push *PushStatus
	// Missed: push was active since before the run, yet the page created
	// new Versions the source never delivered.
	Missed bool
}

// ConnectorRun identifies one scheduled acquisition run. Its identity stays
// stable across dispatcher leases and worker restarts.
type ConnectorRun struct {
	WorkQueue    string
	Organization string
	ConnectorID  string
	Run          int64
}

// RunStore persists acquisition progress.
type RunStore interface {
	LoadRun(ctx context.Context, org, id string) (Target, error)
	// CommitCheckpoint durably advances the Acquisition Checkpoint of the
	// given run; it reports false when the run is stale or the instance is
	// disabled, which ends the run. Reads are added to the current UTC day's
	// usage in the same transaction.
	CommitCheckpoint(ctx context.Context, org, id string, run int64, progress Progress) (bool, error)
	// FinishRun ends the run, schedules the next one and commits re-evaluated
	// Connector Health (with its event) in one transaction.
	FinishRun(ctx context.Context, org, id string, run int64, failure *RunError) error
}

// RunContinuationStore optionally makes the next bounded acquisition run due
// immediately. It must retain FinishRun's lease, sequence and lifecycle fences.
// Stores without it keep the configured interval.
type RunContinuationStore interface {
	ContinueRun(ctx context.Context, org, id string, run int64) error
}

// JSON checkpoints can come back from persistence with different whitespace
// and key order. Preserve integer precision when comparing opaque cursors.
func checkpointChanged(previous, next json.RawMessage) bool {
	canonical := func(raw json.RawMessage) ([]byte, error) {
		if len(raw) == 0 {
			return []byte("null"), nil
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		return json.Marshal(value)
	}
	before, errBefore := canonical(previous)
	after, errAfter := canonical(next)
	return errBefore == nil && errAfter == nil && !bytes.Equal(before, after)
}

// PollingStore coordinates source attempts with reversible corpus visibility.
// Release must run before ingestion or progress transactions begin.
type PollingStore interface {
	BeginPoll(context.Context, string, string, int64) (release func(), active bool, err error)
}

func (a Acquirer) beginPoll(ctx context.Context, org, id string, run int64) (func(), bool, error) {
	if store, ok := a.Store.(PollingStore); ok {
		return store.BeginPoll(ctx, org, id, run)
	}
	return func() {}, true, nil
}

// Ingestor is the internal ingestion command path shared with the public API.
type Ingestor interface {
	TrustedAccept(context.Context, string, string, content.Command) (content.Receipt, error)
	TrustedWithdraw(context.Context, string, string, content.Withdrawal) (content.Receipt, error)
}

// Acquirer executes one acquisition run of one instance.
type Acquirer struct {
	Store RunStore
	// Queues supplies the ingestion-only prefetch backpressure observation.
	Queues   workqueue.Reader
	Registry *Registry
	// Kinds, when set, resolves the kinds of the Pipeline Plan the run is
	// pinned to (Spec 5), so a run never changes provider midway; nil uses
	// Registry.
	Kinds  func(context.Context) *Registry
	Sealer Sealer
	Ingest Ingestor
	// Blobs grants and verifies attachment uploads; Receipts (optional)
	// skips revisions already accepted before their attachments are read.
	Blobs            BlobGrants
	Receipts         ReceiptChecker
	MaxPages         int
	AttachmentBudget int64
	// SoftRunLimit overrides DefaultSoftRunLimit.
	SoftRunLimit time.Duration
	// PublicURL builds the webhook address push kinds register with the
	// source; "" leaves push kinds without one.
	PublicURL string
	Now       func() time.Time
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
	if !target.Enabled || target.PausedAt != nil || target.RunSequence != run {
		return nil
	}
	failure := func(class ErrorClass, code string) error {
		slog.Warn("connector acquisition failed", "connector_id", id, "class", string(class), "code", code)
		return a.Store.FinishRun(ctx, org, id, run, &RunError{Class: class, Code: code, At: a.now()})
	}
	deferred := func(typed *Error) error {
		slog.Warn("connector acquisition failed", "connector_id", id, "class", string(typed.Class), "code", typed.Code, "retry_after", typed.RetryAfter.String())
		return a.Store.FinishRun(ctx, org, id, run, &RunError{Class: typed.Class, Code: typed.Code, At: a.now(), RetryAfter: typed.RetryAfter})
	}
	kinds := a.Registry
	if a.Kinds != nil {
		kinds = a.Kinds(ctx)
	}
	connector, ok := kinds.Lookup(target.Kind)
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
	descriptor := connector.Descriptor()
	if checker := descriptor.Credentials; checker != nil && credential != nil && target.Credential != nil &&
		(target.Health.LastSuccessAt == nil || target.Credential.DepositedAt.After(*target.Health.LastSuccessAt)) {
		// A new or rotated credential, or one that has not worked since its
		// deposit: ask the source before fetching.
		release, active, err := a.beginPoll(ctx, org, id, run)
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		err = func() error {
			defer release()
			return checker.CheckCredential(ctx, CredentialRequest{Organization: org, InstanceID: id, Config: target.Config, Credential: credential, Now: a.now()})
		}()
		if err != nil {
			var typed *Error
			if errors.As(err, &typed) {
				return deferred(typed)
			}
			return failure(ClassTransient, "source_unavailable")
		}
	}
	if owner := descriptor.ExtensionOwner; owner != "" {
		ctx = content.WithExtensionWriter(ctx, owner)
	}
	checkpoint := target.Checkpoint
	pages := a.MaxPages
	if pages <= 0 {
		pages = DefaultMaxPages
	}
	budget := a.AttachmentBudget
	if budget <= 0 {
		budget = DefaultAttachmentBudget
	}
	soft := a.SoftRunLimit
	if soft <= 0 {
		soft = DefaultSoftRunLimit
	}
	started := a.now()
	rc := runContext{descriptor: descriptor, target: target, credential: credential}
	var stored int64
	var rejected string
	var notice string
	continuationStore, canContinue := a.Store.(RunContinuationStore)
	continueNext := false
	reads := target.ReadsToday
	var activeTiming *pageTimings
	var activePage int
	var activeMore bool
	defer func() {
		if activeTiming != nil {
			logPageTiming(id, run, activePage, "incomplete", activeTiming.diagnostic(a.now(), started, activeMore, "incomplete", target.Interval))
		}
	}()
	for i := 0; i < pages; i++ {
		if reason := a.bulkDeferral(ctx, target.WorkQueue); reason != "" {
			continueNext = false
			slog.Info("connector run deferred", "connector_id", id, "run", run, "continuation_reason", reason)
			if i == 0 {
				return a.Store.FinishRun(ctx, org, id, run, &RunError{Skipped: true, At: a.now()})
			}
			break
		}
		pageStarted := a.now()
		release, active, err := a.beginPoll(ctx, org, id, run)
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		activeTiming = &pageTimings{started: pageStarted}
		activePage, activeMore = i, false
		rc.timing = activeTiming
		page, err := func() (Page, error) {
			defer release()
			return connector.Fetch(ctx, FetchRequest{Organization: org, InstanceID: id, CorpusID: target.CorpusID, Namespace: target.Namespace, WebhookURL: receiverWebhookURL(connector, a.PublicURL, id), Config: target.Config, Credential: credential, Checkpoint: checkpoint, Now: a.now(), PageInRun: i, ReadsToday: reads})
		}()
		activeTiming.fetch = a.now().Sub(activeTiming.started)
		activeTiming.items, activeMore = len(page.Items), page.More
		if errors.Is(err, ErrNotDue) && i == 0 {
			activeTiming = nil
			slog.Info("connector run skipped", "connector_id", id, "reason", "not_due")
			return a.Store.FinishRun(ctx, org, id, run, &RunError{Skipped: true, At: a.now()})
		}
		if errors.Is(err, ErrNotDue) {
			activeTiming = nil
			break
		}
		if err != nil {
			var typed *Error
			if errors.As(err, &typed) {
				return deferred(typed)
			}
			return failure(ClassTransient, "source_unavailable")
		}
		// Only an item that reserves a new Version counts as source activity;
		// a replayed Receipt (re-fetched unchanged item) does not.
		fresh := false
		concurrent := a.submitConcurrentPage(ctx, org, rc, page)
		for index, item := range page.Items {
			var size int64
			var receipt content.Receipt
			var err error
			if concurrent == nil {
				size, receipt, err = a.submit(ctx, org, rc, item)
			} else {
				size, receipt, err = concurrent[index].size, concurrent[index].receipt, concurrent[index].err
			}
			if err != nil {
				var typed *Error
				switch {
				case errors.Is(err, corpus.ErrArchived):
					return nil
				case errors.As(err, &typed):
					return deferred(typed)
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
			fresh = fresh || receipt.NewRevision
		}
		// Push active since before this run should have delivered anything
		// new; a push kind holds back what is too recent to have arrived.
		missed := fresh && target.Health.Push != nil && target.Health.Push.Healthy()
		now := a.now()
		reason := ""
		switch {
		case !page.More:
			reason = "source_drained"
		case stored >= budget:
			reason = "attachment_budget"
		case now.Sub(started) >= soft:
			reason = "soft_limit"
		case i+1 == pages:
			reason = "page_limit"
		}
		timing := activeTiming.diagnostic(now, started, page.More, reason, target.Interval)
		continuation := func() string {
			switch {
			case !page.More:
				return "source_drained"
			case notice != "" || page.Notice != "":
				return "source_notice"
			case rejected != "":
				return "item_rejected"
			case reason == "":
				return "run_in_progress"
			case !canContinue:
				return "store_unsupported"
			case !checkpointChanged(checkpoint, page.Checkpoint):
				return "checkpoint_stalled"
			case !checkpointChanged(target.Checkpoint, page.Checkpoint):
				return "checkpoint_cycled"
			default:
				return ""
			}
		}
		timing["continuation_reason"] = continuation()
		timing["continuation"] = continuation() == ""
		ok, err := a.Store.CommitCheckpoint(ctx, org, id, run, Progress{Checkpoint: page.Checkpoint, Items: fresh, Reads: page.Reads, Diagnostics: acquisitionDiagnostics(page.Diagnostics, timing), Push: page.Push, Missed: missed})
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		// Checkpoint persistence also counts toward the soft run limit. The
		// committed diagnostic is a pre-commit snapshot; logs include this wait.
		now = a.now()
		if reason == "" && now.Sub(started) >= soft {
			reason = "soft_limit"
		}
		timing = activeTiming.diagnostic(now, started, page.More, reason, target.Interval)
		continueNext = continuation() == ""
		timing["continuation_reason"] = continuation()
		timing["continuation"] = continueNext
		logPageTiming(id, run, i, "committed", timing)
		activeTiming = nil
		checkpoint = page.Checkpoint
		reads += page.Reads
		if page.Notice != "" {
			notice = page.Notice
		}
		if reason != "" {
			break
		}
	}
	if notice != "" && rejected == "" {
		slog.Info("connector run notice", "connector_id", id, "code", notice)
		return a.Store.FinishRun(ctx, org, id, run, &RunError{Class: ClassSource, Code: notice, At: a.now(), Completed: true})
	}
	if rejected != "" {
		slog.Warn("connector item rejected", "connector_id", id, "code", rejected)
		return a.Store.FinishRun(ctx, org, id, run, &RunError{Class: ClassSource, Code: rejected, At: a.now(), Completed: true})
	}
	if continueNext {
		return continuationStore.ContinueRun(ctx, org, id, run)
	}
	return a.Store.FinishRun(ctx, org, id, run, nil)
}

// The snapshot is installation-wide and bounded to two rows. A missing split
// is unavailable, not an empty queue (including during a rolling upgrade).
func (a Acquirer) bulkDeferral(ctx context.Context, queue string) string {
	if queue != workqueue.Bulk {
		return ""
	}
	if a.Queues == nil {
		return "queue_unavailable"
	}
	read, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := a.Queues.QueueBacklog(read)
	if err != nil {
		return "queue_unavailable"
	}
	for _, row := range rows {
		if row.Queue == workqueue.Bulk && row.IngestionWaiting != nil {
			if *row.IngestionWaiting >= DefaultMaxBulkIngestionWaiting {
				return "ingestion_backpressure"
			}
			return ""
		}
	}
	return "queue_unavailable"
}

type submissionResult struct {
	size    int64
	receipt content.Receipt
	err     error
}

// submitConcurrentPage drains in-flight submissions before Run records a
// failure or commits a checkpoint. Opted-in pages submit each record's items
// in source order; only independent records may run concurrently. Legacy
// internal pages with repeated keys retain whole-page serial submission.
func (a Acquirer) submitConcurrentPage(ctx context.Context, org string, rc runContext, page Page) []submissionResult {
	workers := min(page.SubmissionConcurrency, MaxSubmissionConcurrency, len(page.Items))
	if workers <= 1 {
		return nil
	}
	chains := make([][]int, 0, len(page.Items))
	byKey := make(map[string]int, len(page.Items))
	for index, item := range page.Items {
		chain, repeated := byKey[item.RecordKey]
		if repeated && !page.AllowRepeatedRecordKeys {
			return nil
		}
		if !repeated {
			chain = len(chains)
			byKey[item.RecordKey] = chain
			chains = append(chains, nil)
		}
		chains[chain] = append(chains[chain], index)
	}
	results := make([]submissionResult, len(page.Items))
	var mu sync.Mutex
	var wg sync.WaitGroup
	next, stopped := 0, false
	for range min(workers, len(chains)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if stopped || next == len(chains) {
					mu.Unlock()
					return
				}
				chain := chains[next]
				next++
				mu.Unlock()
				for _, index := range chain {
					mu.Lock()
					stop := stopped
					mu.Unlock()
					if stop {
						return
					}
					size, receipt, err := a.submit(ctx, org, rc, page.Items[index])
					results[index] = submissionResult{size: size, receipt: receipt, err: err}
					if submissionStopsPage(err) {
						mu.Lock()
						stopped = true
						mu.Unlock()
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	return results
}

func submissionStopsPage(err error) bool {
	if err == nil || errors.Is(err, errSkipped) {
		return false
	}
	var typed *Error
	if errors.As(err, &typed) {
		return true
	}
	return !errors.Is(err, content.ErrConflict) && !errors.Is(err, content.ErrInvalid) && !errors.Is(err, content.ErrUnsupported) && !errors.Is(err, content.ErrUnverifiedBlob)
}

// errSkipped marks an item whose revision was already accepted.
var errSkipped = errors.New("already accepted")

// runContext is what submitting an item needs from its run.
type runContext struct {
	descriptor Descriptor
	target     Target
	credential json.RawMessage
	timing     *pageTimings
}

// submit sends one item through the ingestion command path. It returns the
// attachment bytes it granted and the ingestion Receipt.
func (a Acquirer) submit(ctx context.Context, org string, rc runContext, item Item) (int64, content.Receipt, error) {
	inst := rc.target.Instance
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
		done := a.measure(rc.timing, stageAccept)
		defer done()
		receipt, err := a.Ingest.TrustedWithdraw(ctx, org, inst.CorpusID, content.Withdrawal{Key: KeyPrefix + content.StableID("withdraw", inst.ID, item.RecordKey, revision), Source: source, Reason: "source_withdrawn"})
		receipt.NewRevision = false
		return 0, receipt, err
	}
	// An attachment-only item is raw input for the normalizer, rather than
	// a pre-normalized Manifest. Its canonical source descriptor carries no
	// Part metadata that would be lost when converted to a Blob command.
	rawInput := len(item.Attachments) > 0 && (item.Manifest == nil || len(item.Manifest.Parts) == 0)
	if rawInput {
		at := item.Attachments[0]
		if len(item.Attachments) != 1 || item.Manifest != nil && len(item.Manifest.Relations) != 0 || at.Key != "source" || at.Role != "source" || at.ParentKey != "" || len(at.Extensions) != 0 {
			return 0, content.Receipt{}, fmt.Errorf("%w: attachment-only input requires one source attachment without relations, a parent or Part extensions", content.ErrUnsupported)
		}
	}
	if item.Manifest != nil && len(item.Manifest.Relations) > 0 {
		// Unbound relation targets refer to Records of the same Source Namespace.
		m := *item.Manifest
		m.Relations = append([]content.Relation(nil), m.Relations...)
		for i, r := range m.Relations {
			if r.Target.CorpusID == "" && r.Target.Namespace == "" {
				m.Relations[i].Target.CorpusID, m.Relations[i].Target.Namespace = inst.CorpusID, inst.Namespace
			}
		}
		item.Manifest = &m
	}
	key := KeyPrefix + content.StableID("item", inst.ID, item.RecordKey, revision)
	manifest := item.Manifest
	var stored int64
	if len(item.Attachments) > 0 {
		// The key embeds the revision, so only this exact revision is skipped;
		// a changed item still becomes a correction.
		if a.Receipts != nil {
			done := a.measure(rc.timing, stageReceipt)
			known, err := a.Receipts.HasReceipt(ctx, org, key)
			done()
			if err != nil {
				return 0, content.Receipt{}, err
			}
			if known {
				return 0, content.Receipt{}, errSkipped
			}
		}
		exchanger := rc.descriptor.Attachments
		if exchanger == nil || a.Blobs == nil {
			return 0, content.Receipt{}, content.ErrUnsupported
		}
		m := content.Manifest{Kind: "manifest"}
		if manifest != nil {
			m = *manifest
			m.Parts = append([]content.Part(nil), manifest.Parts...)
		}
		for _, at := range item.Attachments {
			req := AttachmentRequest{Organization: org, InstanceID: inst.ID, Config: inst.Config, Credential: rc.credential, Now: a.now(),
				RecordKey: item.RecordKey, Revision: revision, Extensions: item.Extensions, Attachment: at}
			blobID, size, skip, err := a.transfer(ctx, exchanger, rc.descriptor.MaxAttachmentBytes, rc.target.RunSequence, key, req, rc.timing)
			if err != nil {
				return 0, content.Receipt{}, err
			}
			if skip != nil {
				if rawInput {
					return 0, content.Receipt{}, errAttachmentInvalid
				}
				if skip.ItemExtensions != nil {
					item.Extensions = skip.ItemExtensions
				}
				continue
			}
			stored += size
			m.Parts = append(m.Parts, content.Part{Key: at.Key, ParentKey: at.ParentKey, Role: at.Role, Content: content.Text{Kind: "blob", BlobID: blobID, MediaType: at.MediaType}, Extensions: at.Extensions})
		}
		if rawInput {
			// Submit the verified source Blob through the existing normalizer
			// path; accepting it as a Manifest would bypass normalization.
			item.Content, manifest = m.Parts[0].Content, nil
		} else {
			manifest = &m
		}
	}
	c := content.Command{ConnectorInstanceID: inst.ID, Key: key, Source: source, Revision: revision, Position: item.Position, Content: item.Content, Manifest: manifest, Extensions: item.Extensions, Provenance: map[string]any{"producer": inst.ID, "producer_version": inst.Kind + "/" + ConnectorVersion}}
	if c.Manifest != nil {
		c.Content = content.Text{Kind: "manifest"}
	}
	done := a.measure(rc.timing, stageAccept)
	defer done()
	receipt, err := a.Ingest.TrustedAccept(ctx, org, inst.CorpusID, c)
	return stored, receipt, err
}

// errAttachmentInvalid rejects only the item: its attachment can never be
// stored as described (too large, or bytes that keep changing).
var errAttachmentInvalid = fmt.Errorf("%w: attachment cannot be stored", content.ErrInvalid)

// transfer stores one attachment as a verified Blob through a core-issued
// grant: describe (unless the descriptor already carries the exact bytes),
// grant (or reuse a Blob with the same identity), upload, then verify what
// storage holds. Bytes that changed between describe and upload are
// described again once. It returns the Blob and its size, or the skip the
// source asked for.
func (a Acquirer) transfer(ctx context.Context, ex AttachmentExchanger, maxBytes int64, run int64, itemKey string, req AttachmentRequest, timing *pageTimings) (string, int64, *AttachmentDescription, error) {
	at := req.Attachment
	limit := MaxAttachmentBytes
	if own := maxBytes; own > 0 && own < limit {
		limit = own
	}
	for attempt := 0; attempt < 2; attempt++ {
		var d AttachmentDescription
		if attempt == 0 && at.SHA256 != "" && at.SizeBytes != nil {
			d = AttachmentDescription{SizeBytes: *at.SizeBytes, SHA256: at.SHA256}
		} else {
			release, active, err := a.beginPoll(ctx, req.Organization, req.InstanceID, run)
			if err != nil {
				return "", 0, nil, err
			}
			if !active {
				return "", 0, nil, corpus.ErrArchived
			}
			d, err = func() (AttachmentDescription, error) {
				defer release()
				done := a.measure(timing, stageDescribe)
				defer done()
				return ex.DescribeAttachment(ctx, req)
			}()
			if err != nil {
				return "", 0, nil, err
			}
		}
		if d.Skip != "" {
			return "", 0, &d, nil
		}
		if d.SizeBytes < 1 || d.SizeBytes > limit {
			return "", 0, nil, errAttachmentInvalid
		}
		done := a.measure(timing, stageGrant)
		session, err := a.Blobs.Grant(ctx, req.Organization, uploads.Request{
			Key:       KeyPrefix + content.StableID("attachment", itemKey, at.Key, d.SHA256, strconv.FormatInt(run, 10), strconv.Itoa(attempt)),
			SizeBytes: d.SizeBytes, SHA256: d.SHA256, MediaType: at.MediaType})
		done()
		if errors.Is(err, uploads.ErrInvalid) {
			return "", 0, nil, errAttachmentInvalid
		}
		if err != nil {
			return "", 0, nil, err
		}
		if session.State == "verified" {
			return session.BlobID, d.SizeBytes, nil, nil
		}
		// Only a session awaiting its bytes carries an upload URL. A replayed
		// one whose verification was interrupted (a worker stopped mid-Confirm)
		// already holds them: confirm it again instead of uploading.
		if session.State == "awaiting_upload" {
			release, active, err := a.beginPoll(ctx, req.Organization, req.InstanceID, run)
			if err != nil {
				return "", 0, nil, err
			}
			if !active {
				return "", 0, nil, corpus.ErrArchived
			}
			err = func() error {
				defer release()
				done := a.measure(timing, stageUpload)
				defer done()
				return ex.UploadAttachment(ctx, req, UploadGrant{URL: session.UploadURL, Headers: session.UploadHeaders, SizeBytes: d.SizeBytes, SHA256: d.SHA256, MediaType: at.MediaType, ExpiresAt: session.ExpiresAt})
			}()
			var typed *Error
			if errors.As(err, &typed) && typed.Class == ClassSource && typed.Code == CodeAttachmentChanged {
				continue
			}
			if err != nil {
				return "", 0, nil, err
			}
		}
		done = a.measure(timing, stageVerify)
		verified, err := a.Blobs.Confirm(ctx, req.Organization, session.ID)
		done()
		switch {
		case err != nil:
			return "", 0, nil, err
		case verified.State == "verified":
			return verified.BlobID, d.SizeBytes, nil, nil
		case verified.State == "rejected":
			// Storage holds other bytes than the grant allowed: nothing from
			// this upload is used, and the checkpoint does not move.
			return "", 0, nil, SourceError("attachment_unverified")
		default:
			return "", 0, nil, TransientError("attachment_unverified")
		}
	}
	return "", 0, nil, errAttachmentInvalid
}
