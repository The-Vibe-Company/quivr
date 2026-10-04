package runner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
)

// CheckAttachments exchanges the attachments a fixture's pages returned
// (Plugin API 0.4) the way the core does.
const CheckAttachments = "attachments"

// Attachment issue codes.
const (
	// CodeAttachmentMismatch: the bytes uploaded to a grant differ from the
	// size and SHA-256 the plugin described.
	CodeAttachmentMismatch = "attachment_mismatch"
	// CodeAttachmentTooLarge: an attachment above attachments.max_bytes.
	CodeAttachmentTooLarge = plugins.CodeAttachmentTooLarge
)

// maxCheckedAttachments bounds the attachments one fixture exchanges.
const maxCheckedAttachments = 8

// ChecksumHeader carries the base64 SHA-256 a runner grant requires, as
// storage checksum headers do.
const ChecksumHeader = "X-Quivr-Checksum-Sha256"

type fetchedAttachment struct {
	item devhost.AttachmentItem
	at   plugins.ConnectorAttachment
}

// grantStore plays storage behind the runner's grants: one PUT per object,
// refused unless its length, checksum header and media type are the granted
// ones, then kept for the runner to hash again.
type grantStore struct {
	mu      sync.Mutex
	granted map[string]devhost.AttachmentGrant
	stored  map[string][]byte
}

func (s *grantStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	g, ok := s.granted[r.URL.Path]
	s.mu.Unlock()
	if !ok || r.Method != http.MethodPut || r.URL.Query().Get("signature") == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, g.SizeBytes+1))
	if err != nil || r.ContentLength != g.SizeBytes || r.Header.Get(ChecksumHeader) != g.Headers[ChecksumHeader] || r.Header.Get("Content-Type") != g.MediaType {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.stored[r.URL.Path] = body
	delete(s.granted, r.URL.Path)
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// attachmentsCheck describes each attachment (unless its descriptor carries
// the exact bytes), grants an upload to the runner's loopback storage, has
// the plugin upload it and hashes what arrived.
func (r *run) attachmentsCheck(ctx context.Context, cr connectorRun, fetched []fetchedAttachment) {
	if len(fetched) == 0 || plugins.AttachmentMaxBytes(r.m) == 0 {
		return
	}
	started := time.Now()
	check := Check{ID: CheckAttachments, Contribution: ContributionConnector, Fixture: cr.label,
		Title: "attachments are described and uploaded to a core grant with exactly the described bytes"}
	store := &grantStore{granted: map[string]devhost.AttachmentGrant{}, stored: map[string][]byte{}}
	server := httptest.NewServer(store)
	defer server.Close()
	timeout := time.Duration(plugins.AttachmentTimeoutMS(r.m)) * time.Millisecond
	uploaded, skipped := 0, 0
	for i, f := range fetched {
		if i == maxCheckedAttachments || len(check.Issues) > 0 {
			break
		}
		size, digest := int64(0), f.at.SHA256
		if f.at.SHA256 != "" && f.at.SizeBytes != nil {
			size = *f.at.SizeBytes
		} else {
			callCtx, cancel := context.WithTimeout(ctx, timeout)
			result, err := devhost.InvokeDescribeAttachment(callCtx, r.baseURL, cr.run.AttachmentRequest(f.item, f.at, nil, fmt.Sprintf("describe-%d", i)), func(body []byte) []plugins.Issue {
				return plugins.CheckAttachmentAnswer(ctx, body, r.m)
			})
			result, problem := r.connectorResult(ctx, callCtx, result, err)
			cancel()
			if issues := judgeSuccess(result, problem); len(issues) > 0 {
				check.Issues = prefix(issues, f.at.Key)
				break
			}
			var answer plugins.AttachmentAnswer
			_ = json.Unmarshal(result.Body, &answer)
			if answer.Skip != "" {
				skipped++
				continue
			}
			size, digest = answer.SizeBytes, answer.SHA256
		}
		sum, _ := hex.DecodeString(digest)
		signature := make([]byte, 16)
		_, _ = rand.Read(signature)
		path := fmt.Sprintf("/objects/%d", i)
		grant := devhost.AttachmentGrant{URL: server.URL + path + "?signature=" + hex.EncodeToString(signature), Method: "PUT",
			Headers:   map[string]string{ChecksumHeader: base64.StdEncoding.EncodeToString(sum), "Content-Type": f.at.MediaType},
			SizeBytes: size, SHA256: digest, MediaType: f.at.MediaType, ExpiresAt: time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339)}
		store.mu.Lock()
		store.granted[path] = grant
		store.mu.Unlock()
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		result, err := devhost.InvokeUploadAttachment(callCtx, r.baseURL, cr.run.AttachmentRequest(f.item, f.at, &grant, fmt.Sprintf("upload-%d", i)))
		result, problem := r.connectorResult(ctx, callCtx, result, err)
		cancel()
		if issues := judgeSuccess(result, problem); len(issues) > 0 {
			check.Issues = prefix(issues, f.at.Key)
			break
		}
		if strings.Contains(string(result.Body), grant.URL) {
			check.Issues = []plugins.Issue{{Code: CodeCredentialLeak, Message: fmt.Sprintf("attachment %q: the upload answer echoes the grant URL; a grant is a capability", f.at.Key)}}
			break
		}
		store.mu.Lock()
		body, ok := store.stored[path]
		store.mu.Unlock()
		got := sha256.Sum256(body)
		switch {
		case !ok:
			check.Issues = []plugins.Issue{{Code: CodeAttachmentMismatch, Message: fmt.Sprintf("attachment %q: the plugin answered uploaded, but the grant received no PUT with the described length, checksum header and media type", f.at.Key)}}
		case int64(len(body)) != size || hex.EncodeToString(got[:]) != digest:
			check.Issues = []plugins.Issue{{Code: CodeAttachmentMismatch, Message: fmt.Sprintf("attachment %q: %d bytes arrived with SHA-256 %x; the plugin described %d bytes with SHA-256 %s", f.at.Key, len(body), got, size, digest)}}
		}
		uploaded++
	}
	if len(check.Issues) == 0 {
		check.Note = fmt.Sprintf("%d uploaded, %d skipped", uploaded, skipped)
	}
	r.add(check, started)
}

func prefix(issues []plugins.Issue, key string) []plugins.Issue {
	for i := range issues {
		issues[i].Message = fmt.Sprintf("attachment %q: %s", key, issues[i].Message)
	}
	return issues
}
