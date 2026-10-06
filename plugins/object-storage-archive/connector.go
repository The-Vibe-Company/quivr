package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type checkpoint struct {
	Archive            string `json:"archive"`
	ETag               string `json:"etag"`
	ObjectSize         int64  `json:"object_size"`
	MemberOffset       int64  `json:"member_offset"`
	MembersDone        int64  `json:"members_done"`
	ArchiveMembersDone int64  `json:"archive_members_done"`
	Complete           bool   `json:"complete"`
}
type memberRef struct {
	Archive    string `json:"archive"`
	ETag       string `json:"etag"`
	Offset     int64  `json:"offset"`
	BatchStart int64  `json:"batch_start,omitempty"`
	BatchEnd   *int64 `json:"batch_end,omitempty"`
}
type bufferedMember struct {
	member
	offset int64
	data   []byte
}
type archiveSession struct {
	mu      sync.Mutex
	reader  *archiveReader
	cursor  checkpoint
	pending *bufferedMember
	cache   map[string][]byte
	input   string
	page    *quivrplugin.Page
	expires time.Time
	timer   *time.Timer
}
type ArchiveConnector struct {
	mu       sync.Mutex
	sessions map[string]*archiveSession
}

func NewArchiveConnector() *ArchiveConnector {
	return &ArchiveConnector{sessions: map[string]*archiveSession{}}
}
func sessionKey(org, id string, raw json.RawMessage, credential quivrplugin.Credential) string {
	var secret any
	_ = credential.Decode(&secret)
	b, _ := json.Marshal([]any{org, id, json.RawMessage(raw), secret})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (a *ArchiveConnector) session(key string) (*archiveSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s := a.sessions[key]; s != nil {
		s.mu.Lock()
		return s, nil
	}
	for k, s := range a.sessions {
		if s.mu.TryLock() {
			if !s.expires.After(time.Now()) || len(a.sessions) >= maxStreams {
				s.reset()
				delete(a.sessions, k)
			}
			s.mu.Unlock()
		}
	}
	if len(a.sessions) >= maxStreams {
		return nil, quivrplugin.TransientError("archive_capacity", "all archive stream slots are in use")
	}
	s := &archiveSession{}
	a.sessions[key] = s
	s.mu.Lock()
	return s, nil
}
func (s *archiveSession) reset() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if s.reader != nil {
		_ = s.reader.close()
		s.reader = nil
	}
	s.pending = nil
	s.page = nil
	s.cache = nil
	s.input = ""
}
func (s *archiveSession) retain() {
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
func (a *ArchiveConnector) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, s := range a.sessions {
		s.mu.Lock()
		s.reset()
		s.mu.Unlock()
		delete(a.sessions, key)
	}
}
func cpJSON(cp checkpoint) string { b, _ := json.Marshal(cp); return string(b) }
func decodeCheckpoint(req *quivrplugin.FetchRequest, c archiveConfig) (checkpoint, error) {
	var cp checkpoint
	if req.DecodeCheckpoint(&cp) != nil || cp.MemberOffset < 0 || cp.MembersDone < 0 || cp.ArchiveMembersDone < 0 || cp.ObjectSize < 0 || (cp.Archive != "" && (!strings.HasPrefix(cp.Archive, c.Prefix) || !c.acceptsArchive(cp.Archive) || cp.ETag == "")) {
		return cp, quivrplugin.SourceError("invalid_checkpoint", "archive checkpoint is invalid")
	}
	return cp, nil
}
func (a *ArchiveConnector) Fetch(ctx context.Context, req *quivrplugin.FetchRequest) (*quivrplugin.Page, error) {
	c, err := decodeConfig(req.Connector.Config)
	if err != nil {
		return nil, err
	}
	store, err := newStorage(c, req.Credential)
	if err != nil {
		return nil, err
	}
	defer store.close()
	cp, err := decodeCheckpoint(req, c)
	if err != nil {
		return nil, err
	}
	s, err := a.session(sessionKey(req.OrganizationID, req.Connector.ID, req.Connector.Config, req.Credential))
	if err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	input := cpJSON(cp)
	if s.page != nil && s.input == input {
		if _, err := store.head(ctx, s.cursor.Archive, s.cursor.ETag); err != nil {
			return nil, err
		}
		s.retain()
		return s.page, nil
	}
	if cp.Archive == "" || cp.Complete {
		obj, ok, err := store.next(ctx, cp.Archive)
		if err != nil {
			return nil, err
		}
		if !ok {
			return &quivrplugin.Page{Checkpoint: cp, Diagnostics: diagnostics(cp, nil, c), SubmissionConcurrency: c.Concurrency}, nil
		}
		cp = checkpoint{Archive: obj.Key, ETag: obj.ETag, ObjectSize: obj.Size, MembersDone: cp.MembersDone}
	}
	if s.reader == nil || cpJSON(s.cursor) != cpJSON(cp) {
		s.reset()
		obj, err := store.head(ctx, cp.Archive, cp.ETag)
		if err != nil {
			return nil, err
		}
		if obj.Size != cp.ObjectSize {
			return nil, quivrplugin.SourceError("archive_changed", "archive size changed")
		}
		r, err := openArchive(ctx, store, obj)
		if err != nil {
			return nil, err
		}
		s.reader = r
		s.cursor = cp
		stop := context.AfterFunc(ctx, r.cancel)
		for i := int64(0); i < cp.MemberOffset; i++ {
			m, err := r.next()
			if err == nil {
				_, err = io.Copy(io.Discard, m.body)
				_ = m.close()
			}
			if err != nil {
				stop()
				s.reset()
				return nil, archiveError(err)
			}
		}
		stop()
	} else if _, err := store.head(ctx, cp.Archive, cp.ETag); err != nil {
		return nil, err
	}
	s.cache = map[string][]byte{}
	s.page = nil
	r := s.reader
	stop := context.AfterFunc(ctx, r.cancel)
	defer stop()
	batchStart := cp.MemberOffset
	page := &quivrplugin.Page{SubmissionConcurrency: c.Concurrency}
	var cached int64
	seen := map[string]bool{}
	fail := func(err error) (*quivrplugin.Page, error) { s.reset(); return nil, archiveError(err) }
	for len(page.Items) < c.BatchSize {
		if err := ctx.Err(); err != nil {
			return fail(quivrplugin.TransientError("archive_cancelled", "archive read was interrupted"))
		}
		pending := s.pending
		s.pending = nil
		if pending == nil {
			m, err := r.next()
			if err == io.EOF {
				cp.Complete = true
				_ = r.close()
				s.reader = nil
				break
			}
			if err != nil {
				return fail(err)
			}
			offset := cp.MemberOffset
			if !m.regular || !c.members.MatchString(m.name) {
				_, err = io.Copy(io.Discard, m.body)
				_ = m.close()
				if err != nil {
					return fail(err)
				}
				cp.MemberOffset++
				continue
			}
			if m.size == 0 {
				_ = m.close()
				return fail(quivrplugin.SourceError("empty_member", "matched member is empty; exclude it with member_pattern"))
			}
			if m.size < 0 || m.size > maxMemberBytes {
				_ = m.close()
				return fail(quivrplugin.SourceError("member_too_large", "matched members must contain 1 to 25 MiB"))
			}
			data, err := io.ReadAll(io.LimitReader(m.body, maxMemberBytes+1))
			_ = m.close()
			if err != nil {
				return fail(err)
			}
			if int64(len(data)) != m.size {
				return fail(quivrplugin.SourceError("member_size_mismatch", "member bytes do not match the archive header"))
			}
			pending = &bufferedMember{member: m, offset: offset, data: data}
		}
		key, pos, err := c.identity(pending.name, cp.MembersDone+1)
		if err != nil {
			return fail(err)
		}
		// Versions of one record must cross a checkpoint boundary: the protocol
		// forbids repeated keys in a page, and concurrent submission must not reorder them.
		if seen[key] || cached+int64(len(pending.data)) > maxPageBytes {
			s.pending = pending
			break
		}
		refBytes, _ := json.Marshal(memberRef{Archive: cp.Archive, ETag: cp.ETag, Offset: pending.offset, BatchStart: batchStart})
		ref := string(refBytes)
		if len(ref) > 1024 {
			return fail(quivrplugin.SourceError("unsupported_archive_identity", "archive reference exceeds the protocol bound"))
		}
		h := sha256.Sum256(pending.data)
		digest := hex.EncodeToString(h[:])
		size := int64(len(pending.data))
		s.cache[ref] = pending.data
		cached += size
		seen[key] = true
		// Revision binds the numeric source position and bytes, so a correction
		// back to earlier bytes still creates a new version while replay converges.
		rev := sha256.Sum256([]byte(pos + ":" + digest))
		page.Items = append(page.Items, quivrplugin.Item{RecordKey: key, SourcePosition: pos, Revision: hex.EncodeToString(rev[:]), Content: quivrplugin.NewManifest(), Attachments: []quivrplugin.Attachment{{Key: "source", Role: "source", MediaType: c.MediaType, SizeBytes: &size, SHA256: digest, Ref: ref}}})
		cp.MemberOffset++
		cp.MembersDone++
		cp.ArchiveMembersDone++
	}
	// Every member carries the same page bounds. A cold attachment request for
	// the last member must rebuild the entire page, not start a later cache batch.
	for i := range page.Items {
		at := &page.Items[i].Attachments[0]
		var ref memberRef
		_ = json.Unmarshal([]byte(at.Ref), &ref)
		data := s.cache[at.Ref]
		delete(s.cache, at.Ref)
		batchEnd := cp.MemberOffset
		ref.BatchEnd = &batchEnd
		raw, _ := json.Marshal(ref)
		at.Ref = string(raw)
		if len(at.Ref) > 1024 {
			return fail(quivrplugin.SourceError("unsupported_archive_identity", "archive reference exceeds the protocol bound"))
		}
		s.cache[at.Ref] = data
	}
	page.Checkpoint = cp
	page.More = true
	page.Reads = int64(len(page.Items))
	page.Diagnostics = diagnostics(cp, r, c)
	s.cursor = cp
	s.input = input
	s.page = page
	s.retain()
	return page, nil
}
func diagnostics(cp checkpoint, r *archiveReader, c archiveConfig) map[string]any {
	read := int64(0)
	var left any
	if r != nil {
		read = r.bytes.Load()
		if r.total != nil {
			left = max(int64(0), *r.total-cp.ArchiveMembersDone)
		}
	}
	if cp.Complete {
		read = cp.ObjectSize
		left = int64(0)
	}
	if c.MembersTotal != nil {
		left = max(int64(0), *c.MembersTotal-cp.MembersDone)
	}
	read = min(read, cp.ObjectSize)
	percent := float64(0)
	if cp.ObjectSize > 0 {
		percent = 100 * float64(read) / float64(cp.ObjectSize)
	}
	return map[string]any{"archive": cp.Archive, "member_offset": cp.MemberOffset, "members_done": cp.MembersDone, "archive_members_done": cp.ArchiveMembersDone, "members_left": left, "compressed_bytes_read": read, "object_size_bytes": cp.ObjectSize, "progress_percent": percent, "archive_complete": cp.Complete}
}
func (a *ArchiveConnector) CheckCredential(ctx context.Context, req *quivrplugin.CredentialRequest) (*quivrplugin.CredentialStatus, error) {
	c, err := decodeConfig(req.Connector.Config)
	if err != nil {
		return nil, err
	}
	s, err := newStorage(c, req.Credential)
	if err != nil {
		return nil, err
	}
	defer s.close()
	_, err = s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(c.Bucket), Prefix: aws.String(c.Prefix), MaxKeys: aws.Int32(1)})
	if err != nil {
		return nil, storageError(err)
	}
	return &quivrplugin.CredentialStatus{}, nil
}
func (a *ArchiveConnector) OpenAttachment(ctx context.Context, req *quivrplugin.AttachmentRequest) (io.ReadCloser, error) {
	c, err := decodeConfig(req.Connector.Config)
	if err != nil {
		return nil, err
	}
	var ref memberRef
	if json.Unmarshal([]byte(req.Attachment.Ref), &ref) != nil || ref.Offset < 0 || ref.BatchStart < 0 || ref.BatchStart > ref.Offset || (ref.BatchEnd != nil && *ref.BatchEnd <= ref.Offset) || (ref.BatchEnd == nil && ref.BatchStart != 0) || ref.ETag == "" || !strings.HasPrefix(ref.Archive, c.Prefix) || !c.acceptsArchive(ref.Archive) {
		return nil, quivrplugin.SourceError("invalid_attachment_ref", "archive member reference is invalid")
	}
	key := sessionKey(req.OrganizationID, req.Connector.ID, req.Connector.Config, req.Credential)
	session, err := a.session(key)
	if err != nil {
		return nil, err
	}
	defer session.mu.Unlock()
	if data, ok := session.cache[req.Attachment.Ref]; ok {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	store, err := newStorage(c, req.Credential)
	if err != nil {
		return nil, err
	}
	defer store.close()
	obj, err := store.head(ctx, ref.Archive, ref.ETag)
	if err != nil {
		return nil, err
	}
	r, err := openArchive(ctx, store, obj)
	if err != nil {
		return nil, err
	}
	defer r.close()
	stop := context.AfterFunc(ctx, r.cancel)
	defer stop()
	// Losing the cache between fetch and upload rebuilds a bounded member
	// batch once, so concurrent grants do not each rescan the whole gzip stream.
	session.cache = map[string][]byte{}
	var cached int64
	// Older opaque refs have only archive/etag/offset. Retain their original
	// bounded-batch recovery, while new refs recover exact whole-page bounds.
	batchStart, batchEnd := ref.Offset, int64(1<<63-1)
	if ref.BatchEnd != nil {
		batchStart, batchEnd = ref.BatchStart, *ref.BatchEnd
	}
	for i := int64(0); i < batchEnd && len(session.cache) < c.BatchSize; i++ {
		m, err := r.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, archiveError(err)
		}
		if i < batchStart || !m.regular || !c.members.MatchString(m.name) {
			_, err = io.Copy(io.Discard, m.body)
			m.close()
			if err != nil {
				return nil, archiveError(err)
			}
			continue
		}
		if m.size < 1 || m.size > maxMemberBytes {
			m.close()
			return nil, quivrplugin.SourceError("invalid_attachment_ref", "referenced member cannot be uploaded")
		}
		if cached+m.size > maxPageBytes {
			m.close()
			break
		}
		data, err := io.ReadAll(io.LimitReader(m.body, maxMemberBytes+1))
		m.close()
		if err != nil {
			return nil, archiveError(err)
		}
		if int64(len(data)) != m.size {
			return nil, quivrplugin.SourceError("member_size_mismatch", "member bytes do not match the archive header")
		}
		raw, _ := json.Marshal(memberRef{Archive: ref.Archive, ETag: ref.ETag, Offset: i, BatchStart: ref.BatchStart, BatchEnd: ref.BatchEnd})
		session.cache[string(raw)] = data
		cached += m.size
	}
	data, ok := session.cache[req.Attachment.Ref]
	if !ok {
		return nil, quivrplugin.SourceError("invalid_attachment_ref", "member not found")
	}
	session.retain()
	return io.NopCloser(bytes.NewReader(data)), nil
}
