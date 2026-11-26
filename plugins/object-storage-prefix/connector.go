package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const tsSnapshotTTL = 60 * time.Second

type checkpoint struct {
	Mode           string `json:"mode"`
	LastKey        string `json:"last_key,omitempty"`
	LastModified   string `json:"last_modified,omitempty"`
	ObjectsDone    int64  `json:"objects_done"`
	ObjectsSkipped int64  `json:"objects_skipped"`
}

type objectRef struct {
	Key  string `json:"key"`
	ETag string `json:"etag"`
}

type prefixSession struct {
	mu             sync.Mutex
	cache          map[string][]byte
	bytes          int64
	expires        time.Time
	timer          *time.Timer
	tsSnapshot     []objectMeta
	tsSnapshotMore bool
	tsSnapshotAt   time.Time
	tsCursorKey    string
	tsCursorTime   string
}

// PrefixConnector streams individual objects under an S3-compatible prefix
// through upload grants with resumable key or timestamp checkpoints.
type PrefixConnector struct {
	mu       sync.Mutex
	sessions map[string]*prefixSession
}

func NewPrefixConnector() *PrefixConnector {
	return &PrefixConnector{sessions: map[string]*prefixSession{}}
}

func sessionKey(org, id string, raw json.RawMessage, credential quivrplugin.Credential) string {
	var secret any
	_ = credential.Decode(&secret)
	b, _ := json.Marshal([]any{org, id, json.RawMessage(raw), secret})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (p *PrefixConnector) lookupSession(key string) *prefixSession {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions[key]
}

// session returns an existing or newly allocated session without holding p.mu
// across any per-session lock wait or network I/O. If maxSessions is reached
// and no existing session can be evicted, it returns a transient capacity error.
func (p *PrefixConnector) session(key string) (*prefixSession, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s := p.sessions[key]; s != nil {
		return s, nil
	}
	now := time.Now()
	if len(p.sessions) >= maxSessions {
		for k, s := range p.sessions {
			if s.mu.TryLock() {
				if !s.expires.After(now) {
					s.reset()
					delete(p.sessions, k)
				}
				s.mu.Unlock()
			}
		}
	}
	if len(p.sessions) >= maxSessions {
		var oldestKey string
		var oldest *prefixSession
		var oldestExp time.Time
		for k, s := range p.sessions {
			if !s.mu.TryLock() {
				continue
			}
			exp := s.expires
			s.mu.Unlock()
			if oldest == nil || exp.Before(oldestExp) {
				oldestKey = k
				oldest = s
				oldestExp = exp
			}
		}
		if oldest != nil && oldest.mu.TryLock() {
			oldest.reset()
			oldest.mu.Unlock()
			delete(p.sessions, oldestKey)
		}
	}
	if len(p.sessions) >= maxSessions {
		return nil, quivrplugin.TransientError("prefix_capacity", "too many concurrent prefix connector sessions")
	}
	s := &prefixSession{cache: map[string][]byte{}}
	p.sessions[key] = s
	return s, nil
}

func (s *prefixSession) reset() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.cache = map[string][]byte{}
	s.bytes = 0
	s.tsSnapshot = nil
	s.tsSnapshotMore = false
	s.tsSnapshotAt = time.Time{}
	s.tsCursorKey = ""
	s.tsCursorTime = ""
}

func (s *prefixSession) retain() {
	s.expires = time.Now().Add(15 * time.Minute)
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(15*time.Minute, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.expires.After(time.Now()) {
			s.reset()
		}
	})
}

func (p *PrefixConnector) Close() {
	p.mu.Lock()
	sessions := make([]*prefixSession, 0, len(p.sessions))
	for k, s := range p.sessions {
		sessions = append(sessions, s)
		delete(p.sessions, k)
	}
	p.mu.Unlock()
	for _, s := range sessions {
		s.mu.Lock()
		s.reset()
		s.mu.Unlock()
	}
}

func decodeCheckpoint(req *quivrplugin.FetchRequest, c prefixConfig) (checkpoint, time.Time, error) {
	if len(req.Checkpoint) == 0 || string(req.Checkpoint) == "null" {
		return checkpoint{Mode: c.CheckpointMode}, time.Time{}, nil
	}
	var cp checkpoint
	if req.DecodeCheckpoint(&cp) != nil || cp.ObjectsDone < 0 || cp.ObjectsSkipped < 0 {
		return cp, time.Time{}, quivrplugin.SourceError("invalid_checkpoint", "prefix checkpoint is invalid")
	}
	if cp.Mode == "" {
		cp.Mode = c.CheckpointMode
	}
	if cp.Mode != c.CheckpointMode {
		return cp, time.Time{}, quivrplugin.SourceError("invalid_checkpoint", "checkpoint mode does not match connector configuration")
	}
	if cp.LastKey != "" && !strings.HasPrefix(cp.LastKey, c.Prefix) {
		return cp, time.Time{}, quivrplugin.SourceError("invalid_checkpoint", "checkpoint last_key is outside the configured prefix")
	}
	var afterTime time.Time
	if cp.LastModified != "" {
		t, err := time.Parse(time.RFC3339Nano, cp.LastModified)
		if err != nil {
			return cp, time.Time{}, quivrplugin.SourceError("invalid_checkpoint", "checkpoint last_modified is not a valid timestamp")
		}
		afterTime = t.UTC()
	} else if c.CheckpointMode == checkpointTime && cp.LastKey != "" {
		return cp, time.Time{}, quivrplugin.SourceError("invalid_checkpoint", "timestamp checkpoint requires last_modified when last_key is set")
	}
	return cp, afterTime, nil
}

func (p *PrefixConnector) Fetch(ctx context.Context, req *quivrplugin.FetchRequest) (*quivrplugin.Page, error) {
	c, err := decodeConfig(req.Connector.Config)
	if err != nil {
		return nil, err
	}
	store, err := newStorage(c, req.Credential)
	if err != nil {
		return nil, err
	}
	defer store.close()
	cp, afterTime, err := decodeCheckpoint(req, c)
	if err != nil {
		return nil, err
	}

	session, err := p.session(sessionKey(req.OrganizationID, req.Connector.ID, req.Connector.Config, req.Credential))
	if err != nil {
		return nil, err
	}

	var candidates []objectMeta
	moreFromListing := false
	reusedSnapshot := false
	if c.CheckpointMode == checkpointTime {
		if req.PageInRun > 0 {
			session.mu.Lock()
			if len(session.tsSnapshot) > 0 &&
				session.tsCursorKey == cp.LastKey &&
				session.tsCursorTime == cp.LastModified &&
				time.Since(session.tsSnapshotAt) < tsSnapshotTTL {
				candidates = append([]objectMeta(nil), session.tsSnapshot...)
				moreFromListing = session.tsSnapshotMore
				reusedSnapshot = true
			}
			session.mu.Unlock()
		}
		if !reusedSnapshot {
			fetchLimit := min(c.BatchSize*8, 4000)
			candidates, moreFromListing, err = store.listAfterTimestamp(ctx, afterTime, cp.LastKey, fetchLimit)
			if err != nil {
				return nil, err
			}
		}
	} else {
		// Fetch extra metadata beyond batch_size so skipped entries still let
		// us fill a full page in one sweep.
		fetchLimit := min(c.BatchSize*4, 2000)
		candidates, moreFromListing, err = store.listAfterKey(ctx, cp.LastKey, fetchLimit)
		if err != nil {
			return nil, err
		}
	}

	// Build the page cache locally so session.mu is never held across network
	// I/O and failed fetches never leave unexpired partial data in session.cache.
	newCache := make(map[string][]byte, min(len(candidates), c.BatchSize))
	page := &quivrplugin.Page{
		SubmissionConcurrency:   c.Concurrency,
		AllowRepeatedRecordKeys: true,
	}
	var cached int64
	cut := "prefix_end"
	processed := 0
	for i, obj := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, quivrplugin.TransientError("prefix_cancelled", "prefix listing was interrupted")
		}
		if len(page.Items) >= c.BatchSize {
			cut = "batch_size"
			break
		}
		if obj.Size <= 0 || obj.Size > c.MaxObjectBytes {
			if !c.skipOversized() {
				if obj.Size <= 0 {
					return nil, quivrplugin.SourceError("empty_object", "matched object is empty; exclude it or set skip_oversized")
				}
				return nil, quivrplugin.SourceError("object_too_large", "matched object exceeds max_object_bytes")
			}
			cp.ObjectsSkipped++
			advanceCheckpoint(&cp, c.CheckpointMode, obj)
			processed = i + 1
			continue
		}
		if len(page.Items) > 0 && cached+obj.Size > maxPageBytes {
			cut = "page_bytes"
			break
		}
		data, contentType, err := store.get(ctx, obj.Key, obj.ETag, c.MaxObjectBytes)
		if err != nil {
			if errors.Is(err, errObjectMissing) {
				// Benign churn: object deleted between ListObjectsV2 and GetObject.
				cp.ObjectsSkipped++
				advanceCheckpoint(&cp, c.CheckpointMode, obj)
				processed = i + 1
				continue
			}
			if errors.Is(err, errObjectTooLarge) {
				if !c.skipOversized() {
					return nil, quivrplugin.SourceError("object_too_large", "object exceeds max_object_bytes")
				}
				cp.ObjectsSkipped++
				advanceCheckpoint(&cp, c.CheckpointMode, obj)
				processed = i + 1
				continue
			}
			if errors.Is(err, errObjectChanged) {
				// Benign churn: object overwritten after listing; invalidate any
				// reused timestamp snapshot so the immediate retry re-lists.
				if reusedSnapshot {
					session.mu.Lock()
					session.tsSnapshot = nil
					session.tsSnapshotMore = false
					session.tsCursorKey = ""
					session.tsCursorTime = ""
					session.tsSnapshotAt = time.Time{}
					session.mu.Unlock()
				}
				return nil, quivrplugin.TransientError("object_changed", "the object changed after it was listed")
			}
			return nil, err
		}
		if len(data) == 0 {
			if !c.skipOversized() {
				return nil, quivrplugin.SourceError("empty_object", "matched object is empty; exclude it or set skip_oversized")
			}
			cp.ObjectsSkipped++
			advanceCheckpoint(&cp, c.CheckpointMode, obj)
			processed = i + 1
			continue
		}
		mt := c.resolveMediaType(obj.Key, contentType)
		if !c.allowsMediaType(mt) {
			cp.ObjectsSkipped++
			advanceCheckpoint(&cp, c.CheckpointMode, obj)
			processed = i + 1
			continue
		}
		recordKey, pos, err := c.identity(obj.Key, cp.ObjectsDone+1)
		if err != nil {
			return nil, err
		}
		refBytes, _ := json.Marshal(objectRef{Key: obj.Key, ETag: obj.ETag})
		ref := string(refBytes)
		if len(ref) > 1024 {
			return nil, quivrplugin.SourceError("unsupported_object_identity", "object reference exceeds the protocol bound")
		}
		h := sha256.Sum256(data)
		digest := hex.EncodeToString(h[:])
		size := int64(len(data))
		newCache[ref] = data
		cached += size

		rev := sha256.Sum256([]byte(pos + ":" + obj.ETag + ":" + digest))
		page.Items = append(page.Items, quivrplugin.Item{
			RecordKey:      recordKey,
			SourcePosition: pos,
			Revision:       hex.EncodeToString(rev[:]),
			Content:        quivrplugin.NewManifest(),
			Attachments: []quivrplugin.Attachment{{
				Key:       "source",
				Role:      "source",
				MediaType: mt,
				SizeBytes: &size,
				SHA256:    digest,
				Ref:       ref,
			}},
		})
		cp.ObjectsDone++
		advanceCheckpoint(&cp, c.CheckpointMode, obj)
		processed = i + 1
	}

	page.Checkpoint = cp
	page.More = moreFromListing || processed < len(candidates)
	page.Reads = int64(len(page.Items))
	page.Diagnostics = map[string]any{
		"checkpoint_mode": c.CheckpointMode,
		"last_key":        cp.LastKey,
		"last_modified":   cp.LastModified,
		"objects_done":    cp.ObjectsDone,
		"objects_skipped": cp.ObjectsSkipped,
		"page_items":      len(page.Items),
		"page_bytes":      cached,
		"page_cut":        cut,
	}

	session.mu.Lock()
	session.cache = newCache
	session.bytes = cached
	if c.CheckpointMode == checkpointTime && processed < len(candidates) {
		session.tsSnapshot = append([]objectMeta(nil), candidates[processed:]...)
		session.tsSnapshotMore = moreFromListing
		if !reusedSnapshot {
			session.tsSnapshotAt = time.Now()
		}
		session.tsCursorKey = cp.LastKey
		session.tsCursorTime = cp.LastModified
	} else {
		session.tsSnapshot = nil
		session.tsSnapshotMore = false
		session.tsSnapshotAt = time.Time{}
		session.tsCursorKey = ""
		session.tsCursorTime = ""
	}
	session.retain()
	session.mu.Unlock()
	return page, nil
}

func advanceCheckpoint(cp *checkpoint, mode string, obj objectMeta) {
	cp.LastKey = obj.Key
	if mode == checkpointTime && !obj.LastModified.IsZero() {
		cp.LastModified = obj.LastModified.UTC().Format(time.RFC3339Nano)
	}
}

func (p *PrefixConnector) CheckCredential(ctx context.Context, req *quivrplugin.CredentialRequest) (*quivrplugin.CredentialStatus, error) {
	c, err := decodeConfig(req.Connector.Config)
	if err != nil {
		return nil, err
	}
	s, err := newStorage(c, req.Credential)
	if err != nil {
		return nil, err
	}
	defer s.close()
	_, err = s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(c.Bucket),
		Prefix:  aws.String(c.Prefix),
		MaxKeys: aws.Int32(1),
	})
	if err != nil {
		return nil, storageError(err)
	}
	return &quivrplugin.CredentialStatus{}, nil
}

func (p *PrefixConnector) OpenAttachment(ctx context.Context, req *quivrplugin.AttachmentRequest) (io.ReadCloser, error) {
	c, err := decodeConfig(req.Connector.Config)
	if err != nil {
		return nil, err
	}
	var ref objectRef
	if json.Unmarshal([]byte(req.Attachment.Ref), &ref) != nil || ref.Key == "" || ref.ETag == "" || !c.acceptsKey(ref.Key) {
		return nil, quivrplugin.SourceError("invalid_attachment_ref", "object reference is invalid")
	}
	key := sessionKey(req.OrganizationID, req.Connector.ID, req.Connector.Config, req.Credential)
	if session := p.lookupSession(key); session != nil {
		session.mu.Lock()
		data, ok := session.cache[req.Attachment.Ref]
		session.mu.Unlock()
		if ok {
			return io.NopCloser(bytes.NewReader(data)), nil
		}
	}

	store, err := newStorage(c, req.Credential)
	if err != nil {
		return nil, err
	}
	defer store.close()
	data, _, err := store.get(ctx, ref.Key, ref.ETag, c.MaxObjectBytes)
	if err != nil {
		if errors.Is(err, errObjectMissing) {
			return nil, quivrplugin.SourceError("object_missing", "the object is no longer available")
		}
		if errors.Is(err, errObjectTooLarge) {
			return nil, quivrplugin.SourceError("object_too_large", "object exceeds max_object_bytes")
		}
		if errors.Is(err, errObjectChanged) {
			return nil, quivrplugin.TransientError("object_changed", "the object changed before download completed")
		}
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
