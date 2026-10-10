package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
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
func objectServer(t *testing.T, data []byte, keys ...string) *httptest.Server {
	t.Helper()
	objectKey := "inbox/2026.tar.gz"
	if bytes.HasPrefix(data, []byte("PK")) {
		objectKey = "inbox/2026.zip"
	}
	if len(keys) > 0 {
		objectKey = keys[0]
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
	for _, format := range []string{"tar.gz", "zip", "tar.gz_ref_budget"} {
		t.Run(format, func(t *testing.T) {
			makeArchive := syntheticTar
			if format == "zip" {
				makeArchive = syntheticZip
			}
			data := makeArchive(t, "fólder/001.xml", "fólder/skip.txt", "fólder/002.xml", "fólder/003\n.xml")
			var keys []string
			if format == "tar.gz_ref_budget" {
				// Escape-heavy key below the source's 512-byte key bound.
				key := "inbox/" + strings.Repeat("\"", 480) + ".tar.gz"
				for {
					ref, _ := json.Marshal(memberRef{Archive: key, ETag: `"immutable"`, Offset: 0})
					if len(ref) <= 1024 {
						break
					}
					key = strings.Replace(key, "\"", "", 1)
				}
				keys = []string{key}
			}
			if format == "zip" {
				data = append(data, []byte("trailing padding")...)
			}
			srv := objectServer(t, data, keys...)
			var objectReads atomic.Int64
			handler := srv.Config.Handler
			srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "" {
					objectReads.Add(1)
				}
				handler.ServeHTTP(w, r)
			})
			source := NewArchiveConnector()
			t.Cleanup(source.Close)
			req := fetchRequest(t, srv.URL, nil, `,"member_pattern":"fólder/**/*.xml"`)
			page, err := source.Fetch(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if format == "tar.gz_ref_budget" {
				var ref memberRef
				_ = json.Unmarshal([]byte(page.Items[0].Attachments[0].Ref), &ref)
				if ref.BatchEnd != nil || len(page.Items[0].Attachments[0].Ref) > 1024 {
					t.Fatal("page metadata exceeded compact reference budget")
				}
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
			if string(got) != "<item>fólder/002.xml</item>" {
				t.Fatalf("lost-cache bytes: %q", got)
			}
			if format != "tar.gz_ref_budget" {
				// A cold last-member upload must recover earlier page uploads
				// without another ranged GET or gzip prefix download.
				before := objectReads.Load()
				earlier, err := fresh.OpenAttachment(context.Background(), &quivrplugin.AttachmentRequest{OrganizationID: req.OrganizationID, Connector: req.Connector, Credential: req.Credential, Attachment: page.Items[0].Attachments[0]})
				if err != nil {
					t.Fatal(err)
				}
				firstBytes, err := io.ReadAll(earlier)
				earlier.Close()
				if err != nil || string(firstBytes) != "<item>fólder/001.xml</item>" {
					t.Fatal("earlier page recovery", err)
				}
				if objectReads.Load() != before {
					t.Fatal("earlier page upload re-downloaded the archive")
				}

			}

			// Literal legacy ref keys must remain usable after a sidecar upgrade.
			var oldRef map[string]any
			_ = json.Unmarshal([]byte(at.Ref), &oldRef)
			delete(oldRef, "batch_start")
			delete(oldRef, "batch_end")
			legacyRef, _ := json.Marshal(oldRef)
			legacy := at
			legacy.Ref = string(legacyRef)
			legacySource := NewArchiveConnector()
			defer legacySource.Close()
			oldBody, err := legacySource.OpenAttachment(context.Background(), &quivrplugin.AttachmentRequest{OrganizationID: req.OrganizationID, Connector: req.Connector, Credential: req.Credential, Attachment: legacy})
			if err != nil {
				t.Fatal("legacy ref refused", err)
			}
			oldBytes, err := io.ReadAll(oldBody)
			oldBody.Close()
			if err != nil || string(oldBytes) != "<item>fólder/002.xml</item>" {
				t.Fatal("legacy bytes", err)
			}
			req = fetchRequest(t, srv.URL, page.Checkpoint, `,"member_pattern":"fólder/**/*.xml"`)
			resumed, err := fresh.Fetch(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if len(resumed.Items) != 1 || resumed.Items[0].RecordKey != "003\n.xml" || resumed.Items[0].SourcePosition != "3" || resumed.Diagnostics["members_done"] != int64(3) {
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
// become a different Record or be listed before its newer revision.
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
			if len(first.Items) != 2 || first.Items[0].RecordKey != "ITEM7" || first.Items[0].SourcePosition != "12" || first.SubmissionConcurrency != 4 || !first.AllowRepeatedRecordKeys || first.Items[1].RecordKey != "ITEM7" || first.Items[1].SourcePosition != "3" {
				t.Fatalf("revision identities or source order changed: %+v", first)
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

// Owner boundary: reject directory allocations before parsing and prevent
// tiny deflate ReadAt calls from amplifying into thousands of S3 requests.
func TestZipDirectoryBoundsAndBufferedDeflate(t *testing.T) {
	t.Run("undersized_object", func(t *testing.T) {
		for _, size := range []int64{-1, 0, 21} {
			err := checkZipDirectory(bytes.NewReader(nil), size)
			classified, ok := err.(*quivrplugin.Error)
			if !ok || classified.Code != "malformed_archive" {
				t.Fatalf("size %d: %v", size, err)
			}
		}
	})
	t.Run("directory_limit", func(t *testing.T) {
		data := syntheticZip(t, "item.xml")
		end := bytes.LastIndex(data, []byte{'P', 'K', 5, 6})
		binary.LittleEndian.PutUint32(data[end+12:end+16], maxZipDirectoryBytes+1)
		source := NewArchiveConnector()
		defer source.Close()
		srv := objectServer(t, data)
		page, err := source.Fetch(context.Background(), fetchRequest(t, srv.URL, nil, ""))
		classified, ok := err.(*quivrplugin.Error)
		if page != nil || !ok || classified.Code != "zip_directory_too_large" {
			t.Fatalf("directory limit: page=%+v err=%v", page, err)
		}
	})
	t.Run("deflate_window", func(t *testing.T) {
		payload := make([]byte, 2<<20)
		_, _ = rand.New(rand.NewSource(1)).Read(payload)
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		out, err := zw.Create("item.xml")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = out.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err = zw.Close(); err != nil {
			t.Fatal(err)
		}
		srv := objectServer(t, buf.Bytes())
		handler := srv.Config.Handler
		var ranges atomic.Int64
		srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Range") != "" {
				ranges.Add(1)
			}
			handler.ServeHTTP(w, r)
		})
		source := NewArchiveConnector()
		defer source.Close()
		req := fetchRequest(t, srv.URL, nil, "")
		page, err := source.Fetch(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("page: %+v", page)
		}
		body, err := source.OpenAttachment(context.Background(), &quivrplugin.AttachmentRequest{OrganizationID: req.OrganizationID, Connector: req.Connector, Credential: req.Credential, Attachment: page.Items[0].Attachments[0]})
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(body)
		body.Close()
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatal("deflate bytes corrupted", err)
		}
		if count := ranges.Load(); count > 10 {
			t.Fatalf("2 MiB deflate required %d ranged GETs", count)
		}
	})
}

// Page cut ownership: a repeated source identity ends a page even when the
// byte budget and requested batch size have room. Distinct 30 KiB members
// cross 1 MiB on one page; cached source bytes do not enter the JSON response.
func TestArchiveReportsPageCuts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		repeat bool
		want   int
		cut    string
	}{
		{"distinct_members_cross_one_mebibyte", false, 40, "archive_end"},
		{"corrections_share_ordered_page", true, 40, "archive_end"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gz)
			for i := 0; i < 40; i++ {
				key := i
				position := 12
				if tc.repeat && i == 31 {
					key = 0
					position = 3
				}
				name := fmt.Sprintf("urn:example:ITEM%d-%d.xml", key, position)
				body := bytes.Repeat([]byte{byte(65 + i)}, 30<<10)
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
			srv := objectServer(t, buf.Bytes())
			source := NewArchiveConnector()
			defer source.Close()
			req := fetchRequest(t, srv.URL, nil, `,"record_key_pattern":"urn:example:(ITEM[0-9]+)-","source_position_pattern":"-([0-9]+)\\.xml$"`)
			var config map[string]any
			if err := json.Unmarshal(req.Connector.Config, &config); err != nil {
				t.Fatal(err)
			}
			config["batch_size"] = 500
			wantConcurrency := 1
			if tc.repeat {
				config["concurrency"], wantConcurrency = 8, 8
			}
			req.Connector.Config, _ = json.Marshal(config)
			page, err := source.Fetch(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			diagnostic := page.Diagnostics
			if page.SubmissionConcurrency != wantConcurrency || !page.AllowRepeatedRecordKeys || len(page.Items) != tc.want || diagnostic["page_cut"] != tc.cut || diagnostic["page_bytes"] != int64(tc.want*(30<<10)) {
				t.Fatalf("page items=%d diagnostic=%v, want %d/%s", len(page.Items), diagnostic, tc.want, tc.cut)
			}
			if tc.repeat && (page.Items[31].RecordKey != "ITEM0" || page.Items[31].SourcePosition != "3") {
				t.Fatalf("correction source order lost: %+v", page.Items[31])
			}
			if tc.repeat {
				refs := map[string]bool{}
				for _, item := range page.Items {
					ref := item.Attachments[0].Ref
					if refs[ref] {
						t.Fatal("revisions share an attachment reference")
					}
					refs[ref] = true
				}
				// A cold correction upload must rebuild both revisions' distinct
				// bytes even when uploads arrive in reverse source order.
				source.Close()
				cold := NewArchiveConnector()
				defer cold.Close()
				for _, index := range []int{31, 0} {
					body, err := cold.OpenAttachment(context.Background(), &quivrplugin.AttachmentRequest{OrganizationID: req.OrganizationID, Connector: req.Connector, Credential: req.Credential, Attachment: page.Items[index].Attachments[0]})
					if err != nil {
						t.Fatal(err)
					}
					data, err := io.ReadAll(body)
					body.Close()
					if err != nil || !bytes.Equal(data, bytes.Repeat([]byte{byte(65 + index)}, 30<<10)) {
						t.Fatalf("cold revision %d bytes differ: %v", index, err)
					}
				}
			}
		})
	}
}
