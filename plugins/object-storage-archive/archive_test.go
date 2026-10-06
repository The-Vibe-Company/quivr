package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

func syntheticTar(t *testing.T, paths ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range paths {
		body := []byte("<item>" + name + "</item>")
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The fake owns only S3 HTTP responses. The connector owns parsing, page
// boundaries, checkpoints and recovery; no existing connector reads archives.
func objectServer(t *testing.T, data []byte) *httptest.Server {
	t.Helper()
	objectKey := "inbox/2026.tar.gz"
	if bytes.HasPrefix(data, []byte("PK")) {
		objectKey = "inbox/2026.zip"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list-type") == "2" {
			w.Header().Set("Content-Type", "application/xml")
			objects := ""
			if r.URL.Query().Get("start-after") == "" {
				objects = fmt.Sprintf(`<Contents><Key>%s</Key><ETag>&quot;immutable&quot;</ETag><Size>%d</Size></Contents>`, objectKey, len(data))
			}
			fmt.Fprintf(w, `<ListBucketResult><IsTruncated>false</IsTruncated>%s</ListBucketResult>`, objects)
			return
		}
		if r.Header.Get("If-Match") != `"immutable"` {
			w.WriteHeader(412)
			return
		}
		w.Header().Set("ETag", `"immutable"`)
		payload := data
		if raw := r.Header.Get("Range"); raw != "" {
			lo, hi, ok := strings.Cut(strings.TrimPrefix(raw, "bytes="), "-")
			start, e1 := strconv.Atoi(lo)
			end, e2 := strconv.Atoi(hi)
			if !ok || e1 != nil || e2 != nil || start < 0 || end >= len(data) || end < start {
				w.WriteHeader(416)
				return
			}
			payload = data[start : end+1]
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			w.WriteHeader(206)
		} else {
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		}
		if r.Method != http.MethodHead {
			_, _ = w.Write(payload)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}
func fetchRequest(t *testing.T, endpoint string, cp any, extra string) *quivrplugin.FetchRequest {
	t.Helper()
	checkpoint, err := json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`{"organization_id":"org","connector":{"instance_id":"instance","kind":"object_storage_archive","config":{"bucket":"archive","prefix":"inbox/","region":"us-east-1","endpoint":%q,"media_type":"application/xml","batch_size":2%s}},"credential":{"access_key_id":"synthetic-reader","secret_access_key":"synthetic-secret"},"checkpoint":%s,"now":"2026-10-06T12:00:00Z"}`, endpoint, extra, checkpoint)
	var req quivrplugin.FetchRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatal(err)
	}
	return &req
}
func TestArchivePagesReplayAndResumeAfterLostCache(t *testing.T) {
	for _, format := range []string{"tar.gz", "zip"} {
		t.Run(format, func(t *testing.T) {
			makeArchive := syntheticTar
			if format == "zip" {
				makeArchive = syntheticZip
			}
			data := makeArchive(t, "folder/001.xml", "folder/skip.txt", "folder/002.xml", "folder/003.xml")
			srv := objectServer(t, data)
			source := NewArchiveConnector()
			t.Cleanup(source.Close)
			req := fetchRequest(t, srv.URL, nil, `,"member_pattern":"**/*.xml"`)
			page, err := source.Fetch(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != 2 || page.Items[0].RecordKey != "001.xml" || page.Items[1].SourcePosition != "2" || !page.More {
				t.Fatalf("first page: %+v", page)
			}
			replay, err := source.Fetch(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			left, _ := json.Marshal(page)
			right, _ := json.Marshal(replay)
			if !bytes.Equal(left, right) {
				t.Fatalf("replay differs: %s / %s", left, right)
			}
			source.Close()
			fresh := NewArchiveConnector()
			t.Cleanup(fresh.Close)
			// The source ref is sufficient after losing the entire process cache.
			at := page.Items[1].Attachments[0]
			body, err := fresh.OpenAttachment(context.Background(), &quivrplugin.AttachmentRequest{OrganizationID: req.OrganizationID, Connector: req.Connector, Credential: req.Credential, Attachment: at})
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(body)
			body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "<item>folder/002.xml</item>" {
				t.Fatalf("lost-cache bytes: %q", got)
			}
			req = fetchRequest(t, srv.URL, page.Checkpoint, `,"member_pattern":"**/*.xml"`)
			resumed, err := fresh.Fetch(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if len(resumed.Items) != 1 || resumed.Items[0].RecordKey != "003.xml" || resumed.Items[0].SourcePosition != "3" || resumed.Diagnostics["members_done"] != int64(3) {
				t.Fatalf("resumed page: %+v", resumed)
			}
		})
	}
}

func syntheticZip(t *testing.T, paths ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range paths {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = fmt.Fprintf(w, "<item>%s</item>", name); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// This fixture protects URL-decoding, independent identity captures and the
// ordering boundary: a later path carrying an older source revision must not
// become a different Record or share a concurrent page with its newer revision.
func TestArchiveIdentityCapturesAndInvalidMembers(t *testing.T) {
	for _, tc := range []struct {
		name, positionPattern string
		paths                 []string
		wantError             string
	}{
		{"revisions", `-([0-9]+)\.xml$`, []string{"2026/01/01/10/urn%3Aexample%3AITEM7-12.xml", "2026/01/02/11/urn%3Aexample%3AITEM7-3.xml"}, ""},
		{"non_numeric_capture", `-([^/]+)\.xml$`, []string{"2026/01/01/10/urn%3Aexample%3AITEM7-old.xml"}, "invalid_member_identity"},
		{"missing_capture", `-([0-9]+)\.xml$`, []string{"2026/01/01/10/other.xml"}, "member_identity_mismatch"},
		{"bad_encoding", `-([0-9]+)\.xml$`, []string{"2026/01/01/10/urn%ZZITEM7-12.xml"}, "invalid_member_path"},
		{"invalid_decoded_utf8", `-([0-9]+)\.xml$`, []string{"2026/01/01/10/urn%3Aexample%3AITEM7%FF-12.xml"}, "invalid_member_path"},
		{"decoded_nul", `-([0-9]+)\.xml$`, []string{"2026/01/01/10/urn%3Aexample%3AITEM7%00-12.xml"}, "invalid_member_path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := objectServer(t, syntheticTar(t, tc.paths...))
			source := NewArchiveConnector()
			t.Cleanup(source.Close)
			extra, _ := json.Marshal(map[string]any{"record_key_pattern": `urn:example:(ITEM[0-9]+)-`, "source_position_pattern": tc.positionPattern, "concurrency": 4})
			req := fetchRequest(t, srv.URL, nil, ","+strings.Trim(string(extra), "{}"))
			first, err := source.Fetch(context.Background(), req)
			if tc.wantError != "" {
				classified, ok := err.(*quivrplugin.Error)
				if !ok || classified.Code != tc.wantError {
					t.Fatalf("want %s before any page, got page=%+v err=%v", tc.wantError, first, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(first.Items) != 1 || first.Items[0].RecordKey != "ITEM7" || first.Items[0].SourcePosition != "12" || first.SubmissionConcurrency != 4 {
				t.Fatalf("newer revision page: %+v", first)
			}
			req.Checkpoint, _ = json.Marshal(first.Checkpoint)
			second, err := source.Fetch(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if len(second.Items) != 1 || second.Items[0].RecordKey != "ITEM7" || second.Items[0].SourcePosition != "3" {
				t.Fatalf("older revision page: %+v", second)
			}
		})
	}
}

// A readable tar payload with a damaged gzip trailer must not yield a page
// whose checkpoint would permanently skip an unverified source archive.
func TestArchiveRejectsDamagedGzipBeforeReturningPage(t *testing.T) {
	data := syntheticTar(t, "folder/001.xml")
	data[len(data)-8] ^= 0xff
	srv := objectServer(t, data)
	source := NewArchiveConnector()
	t.Cleanup(source.Close)
	page, err := source.Fetch(context.Background(), fetchRequest(t, srv.URL, nil, ""))
	classified, ok := err.(*quivrplugin.Error)
	if page != nil || !ok || classified.Code != "malformed_archive" {
		t.Fatalf("damaged source returned a checkpoint: page=%+v error=%v", page, err)
	}
}
