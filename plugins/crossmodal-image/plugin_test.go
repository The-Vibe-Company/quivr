package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

func makePNG(t *testing.T, w, h int, fill func(x, y int) color.NRGBA, textChunks map[string]string) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, fill(x, y))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	if len(textChunks) == 0 {
		return raw
	}
	// Insert tEXt chunks right before the final 12-byte IEND chunk.
	iend := append([]byte(nil), raw[len(raw)-12:]...)
	var out bytes.Buffer
	out.Write(raw[:len(raw)-12])
	for k, v := range textChunks {
		payload := append([]byte(k), 0)
		payload = append(payload, []byte(v)...)
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(payload)))
		out.Write(lenBuf[:])
		out.WriteString("tEXt")
		out.Write(payload)
		crc := crc32.ChecksumIEEE(append([]byte("tEXt"), payload...))
		var crcBuf [4]byte
		binary.BigEndian.PutUint32(crcBuf[:], crc)
		out.Write(crcBuf[:])
	}
	out.Write(iend)
	return out.Bytes()
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func writeTempBlob(t *testing.T, name string, data []byte) quivrplugin.IngestBlobPart {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return quivrplugin.IngestBlobPart{
		Key:       "image",
		Role:      "image",
		BlobID:    "blob-" + name,
		MediaType: "image/png",
		SizeBytes: int64(len(data)),
		SHA256:    hex.EncodeToString(sum[:]),
		Reference: quivrplugin.BlobReference{Kind: "file", URL: "file://" + p},
	}
}

func TestNormalizerPreservesBlobAndExtractsMetadata(t *testing.T) {
	data := makePNG(t, 40, 20, func(x, y int) color.NRGBA {
		return color.NRGBA{R: 220, G: 30, B: 30, A: 255}
	}, map[string]string{"Description": "crimson beacon"})
	p := filepath.Join(t.TempDir(), "beacon.png")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	req := &quivrplugin.NormalizerRequest{
		Configuration: json.RawMessage(`{}`),
		Input: quivrplugin.NormalizerInput{
			BlobID:    "blob-1",
			MediaType: "image/png",
			SizeBytes: int64(len(data)),
			SHA256:    hex.EncodeToString(sum[:]),
		},
	}
	req.Input.Reference.Kind = "file"
	req.Input.Reference.URL = "file://" + p

	resp, err := (crossModalPlugin{}).Normalize(context.Background(), req)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(resp.Manifest.Parts) != 1 || resp.Manifest.Parts[0].Content.Kind != "blob" || resp.Manifest.Parts[0].Content.BlobID != "blob-1" {
		t.Fatalf("unexpected parts: %+v", resp.Manifest.Parts)
	}
	ext, ok := resp.Extensions["crossmodal.image.metadata"]
	if !ok || ext.SchemaVersion != "1" {
		t.Fatalf("missing metadata extension: %+v", resp.Extensions)
	}
	if ext.Data["width"] != 40 || ext.Data["height"] != 20 || ext.Data["format"] != "png" || ext.Data["aspect_ratio"] != float64(2) {
		t.Fatalf("unexpected metadata: %+v", ext.Data)
	}
	tm, _ := ext.Data["text_metadata"].(map[string]string)
	if tm["Description"] != "crimson beacon" {
		t.Fatalf("unexpected text_metadata: %+v", ext.Data["text_metadata"])
	}
}

func TestNaturalLanguageQueriesRankMatchingImagesAndGridRegions(t *testing.T) {
	redSquare := makePNG(t, 32, 32, func(x, y int) color.NRGBA {
		return color.NRGBA{R: 235, G: 25, B: 25, A: 255}
	}, map[string]string{"Title": "red signal marker"})
	blueLandscape := makePNG(t, 64, 24, func(x, y int) color.NRGBA {
		return color.NRGBA{R: 15, G: 35, B: 170, A: 255}
	}, map[string]string{"Title": "blue coastal horizon"})
	splitQuad := makePNG(t, 32, 32, func(x, y int) color.NRGBA {
		if x < 16 && y < 16 {
			return color.NRGBA{R: 240, G: 20, B: 20, A: 255} // top-left red
		}
		return color.NRGBA{R: 20, G: 180, B: 40, A: 255} // rest green
	}, nil)

	pl := crossModalPlugin{}
	embedOne := func(bp quivrplugin.IngestBlobPart, cfg string) []quivrplugin.Segment {
		req := &quivrplugin.IngestRequest{
			Configuration: json.RawMessage(cfg),
			BlobParts:     []quivrplugin.IngestBlobPart{bp},
			Spaces:        []string{visionSpaceID},
		}
		segs, err := pl.SegmentAndEmbed(context.Background(), req)
		if err != nil {
			t.Fatalf("SegmentAndEmbed: %v", err)
		}
		return segs
	}

	redSegs := embedOne(writeTempBlob(t, "red.png", redSquare), `{"region_mode":"full"}`)
	blueSegs := embedOne(writeTempBlob(t, "blue.png", blueLandscape), `{"region_mode":"full"}`)
	if len(redSegs) != 1 || redSegs[0].Region == nil || redSegs[0].Region.Width != 1 || redSegs[0].Region.Label != "full" {
		t.Fatalf("unexpected full region: %+v", redSegs)
	}

	qRed, err := pl.EmbedQuery(context.Background(), &quivrplugin.QueryRequest{
		Configuration: json.RawMessage(`{}`),
		Space:         visionSpaceID,
		Query: struct {
			Modality string `json:"modality"`
			Text     string `json:"text"`
		}{Modality: "text", Text: "red square signal marker"},
	})
	if err != nil {
		t.Fatalf("EmbedQuery red: %v", err)
	}
	qBlue, err := pl.EmbedQuery(context.Background(), &quivrplugin.QueryRequest{
		Configuration: json.RawMessage(`{}`),
		Space:         visionSpaceID,
		Query: struct {
			Modality string `json:"modality"`
			Text     string `json:"text"`
		}{Modality: "text", Text: "dark blue wide coastal landscape"},
	})
	if err != nil {
		t.Fatalf("EmbedQuery blue: %v", err)
	}

	simRedOnRed := cosine(qRed, redSegs[0].Vectors[visionSpaceID])
	simRedOnBlue := cosine(qRed, blueSegs[0].Vectors[visionSpaceID])
	if simRedOnRed <= simRedOnBlue+0.25 {
		t.Fatalf("red query should rank red image strictly higher: red=%.3f blue=%.3f", simRedOnRed, simRedOnBlue)
	}
	simBlueOnBlue := cosine(qBlue, blueSegs[0].Vectors[visionSpaceID])
	simBlueOnRed := cosine(qBlue, redSegs[0].Vectors[visionSpaceID])
	if simBlueOnBlue <= simBlueOnRed+0.25 {
		t.Fatalf("blue query should rank blue image strictly higher: blue=%.3f red=%.3f", simBlueOnBlue, simBlueOnRed)
	}

	// Grid mode emits full + 2x2 regions with exact bounding-box provenance;
	// querying "red top left" ranks grid:0,0 above the green bottom-right cell grid:1,1.
	gridSegs := embedOne(writeTempBlob(t, "quad.png", splitQuad), `{"region_mode":"grid","grid_rows":2,"grid_cols":2}`)
	if len(gridSegs) != 5 {
		t.Fatalf("expected 5 regions (full + 2x2), got %d", len(gridSegs))
	}
	qTopLeftRed, err := pl.EmbedQuery(context.Background(), &quivrplugin.QueryRequest{
		Configuration: json.RawMessage(`{}`),
		Space:         visionSpaceID,
		Query: struct {
			Modality string `json:"modality"`
			Text     string `json:"text"`
		}{Modality: "text", Text: "red top left region"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var simTL, simBR float64
	for _, s := range gridSegs {
		switch s.Region.Label {
		case "grid:0,0":
			simTL = cosine(qTopLeftRed, s.Vectors[visionSpaceID])
			if s.Region.X != 0 || s.Region.Y != 0 || s.Region.Width != 0.5 || s.Region.Height != 0.5 {
				t.Fatalf("grid:0,0 bad bbox: %+v", s.Region)
			}
		case "grid:1,1":
			simBR = cosine(qTopLeftRed, s.Vectors[visionSpaceID])
		}
	}
	if simTL <= simBR+0.2 {
		t.Fatalf("top-left red region should outscore bottom-right green region: tl=%.3f br=%.3f", simTL, simBR)
	}

	// segments_only (no spaces) returns exact same regions without vectors.
	segOnly, err := pl.SegmentAndEmbed(context.Background(), &quivrplugin.IngestRequest{
		Configuration: json.RawMessage(`{"region_mode":"grid","grid_rows":2,"grid_cols":2}`),
		BlobParts:     []quivrplugin.IngestBlobPart{writeTempBlob(t, "quad2.png", splitQuad)},
		Spaces:        []string{},
	})
	if err != nil {
		t.Fatalf("segments_only: %v", err)
	}
	if len(segOnly) != len(gridSegs) || len(segOnly[0].Vectors) != 0 {
		t.Fatalf("segments_only unexpected: count=%d vectors=%d", len(segOnly), len(segOnly[0].Vectors))
	}
}

func TestHTTPBackendAuthAndErrorScrubbing(t *testing.T) {
	t.Setenv("CROSSMODAL_API_KEY", "super-secret-token")
	var gotAuth string
	var modalities []string
	failStatus := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if failStatus != 0 {
			w.WriteHeader(failStatus)
			_, _ = w.Write([]byte("backend rejected super-secret-token at " + r.Host))
			return
		}
		var body struct {
			Input struct {
				Modality string `json:"modality"`
				DataURI  string `json:"data_uri"`
				Text     string `json:"text"`
			} `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		modalities = append(modalities, body.Input.Modality)
		vec := make([]float64, vectorDims)
		vec[0] = 1.0
		_ = json.NewEncoder(w).Encode(map[string]any{"vector": vec})
	}))
	defer srv.Close()

	cfg := json.RawMessage(`{"backend":"http","base_url":"` + srv.URL + `","auth":"bearer"}`)
	pl := crossModalPlugin{}
	pngData := makePNG(t, 8, 8, func(x, y int) color.NRGBA { return color.NRGBA{R: 100, G: 100, B: 200, A: 255} }, nil)
	segs, err := pl.SegmentAndEmbed(context.Background(), &quivrplugin.IngestRequest{
		Configuration: cfg,
		BlobParts:     []quivrplugin.IngestBlobPart{writeTempBlob(t, "http.png", pngData)},
		Spaces:        []string{externalSpaceID},
	})
	if err != nil {
		t.Fatalf("http SegmentAndEmbed: %v", err)
	}
	if len(segs) != 1 || gotAuth != "Bearer super-secret-token" {
		t.Fatalf("unexpected http result: len=%d auth=%q", len(segs), gotAuth)
	}
	qVec, err := pl.EmbedQuery(context.Background(), &quivrplugin.QueryRequest{
		Configuration: cfg,
		Space:         externalSpaceID,
		Query: struct {
			Modality string `json:"modality"`
			Text     string `json:"text"`
		}{Modality: "text", Text: "blue sky"},
	})
	if err != nil || len(qVec) != vectorDims {
		t.Fatalf("http EmbedQuery: err=%v len=%d", err, len(qVec))
	}
	if strings.Join(modalities, ",") != "image,text" {
		t.Fatalf("modalities = %v, want [image text]", modalities)
	}

	// Verify backend error responses never leak secret tokens or backend URLs.
	for _, status := range []int{http.StatusUnauthorized, http.StatusInternalServerError} {
		failStatus = status
		_, err = pl.EmbedQuery(context.Background(), &quivrplugin.QueryRequest{
			Configuration: cfg,
			Space:         externalSpaceID,
			Query: struct {
				Modality string `json:"modality"`
				Text     string `json:"text"`
			}{Modality: "text", Text: "blue sky"},
		})
		if err == nil {
			t.Fatalf("status %d: expected error", status)
		}
		if strings.Contains(err.Error(), "super-secret-token") || strings.Contains(err.Error(), srv.URL) {
			t.Fatalf("status %d leaked secret or URL: %v", status, err)
		}
	}

	// Verify EmbedQuery fails fast (1 attempt) on transient 5xx while SegmentAndEmbed retries and recovers.
	var queryAttempts, ingestAttempts int
	retrySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input struct {
				Modality string `json:"modality"`
			} `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Input.Modality == "text" {
			queryAttempts++
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		ingestAttempts++
		if ingestAttempts == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		vec := make([]float64, vectorDims)
		vec[0] = 1.0
		_ = json.NewEncoder(w).Encode(map[string]any{"vector": vec})
	}))
	defer retrySrv.Close()
	retryCfg := json.RawMessage(`{"backend":"http","base_url":"` + retrySrv.URL + `","auth":"none"}`)
	_, err = pl.EmbedQuery(context.Background(), &quivrplugin.QueryRequest{
		Configuration: retryCfg,
		Space:         externalSpaceID,
		Query: struct {
			Modality string `json:"modality"`
			Text     string `json:"text"`
		}{Modality: "text", Text: "blue sky"},
	})
	if err == nil || queryAttempts != 1 {
		t.Fatalf("EmbedQuery should fail fast on 1 attempt: err=%v attempts=%d", err, queryAttempts)
	}
	_, err = pl.SegmentAndEmbed(context.Background(), &quivrplugin.IngestRequest{
		Configuration: retryCfg,
		BlobParts:     []quivrplugin.IngestBlobPart{writeTempBlob(t, "retry.png", pngData)},
		Spaces:        []string{externalSpaceID},
	})
	if err != nil || ingestAttempts != 2 {
		t.Fatalf("SegmentAndEmbed should retry transient 503 and succeed on attempt 2: err=%v attempts=%d", err, ingestAttempts)
	}
}

func TestInvalidImageSpaceBindingDecompressionBombAndSegmentLimit(t *testing.T) {
	pl := crossModalPlugin{}

	t.Run("invalid image bytes rejected", func(t *testing.T) {
		bp := writeTempBlob(t, "bad.png", []byte("not-a-valid-image"))
		_, err := pl.SegmentAndEmbed(context.Background(), &quivrplugin.IngestRequest{
			Configuration: json.RawMessage(`{}`),
			BlobParts:     []quivrplugin.IngestBlobPart{bp},
			Spaces:        []string{visionSpaceID},
		})
		if err == nil || !strings.Contains(err.Error(), "invalid_image") {
			t.Fatalf("expected invalid_image terminal error, got %v", err)
		}
	})

	t.Run("decompression bomb rejected by header check before decode", func(t *testing.T) {
		// Minimal PNG header declaring 20000x20000 pixels (400MP > 64MP cap).
		var hdr bytes.Buffer
		hdr.WriteString("\x89PNG\r\n\x1a\n")
		var ihdr [13]byte
		binary.BigEndian.PutUint32(ihdr[0:4], 20000)
		binary.BigEndian.PutUint32(ihdr[4:8], 20000)
		ihdr[8] = 8 // bit depth
		ihdr[9] = 2 // truecolor
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(ihdr)))
		hdr.Write(lenBuf[:])
		hdr.WriteString("IHDR")
		hdr.Write(ihdr[:])
		var crcBuf [4]byte
		binary.BigEndian.PutUint32(crcBuf[:], crc32.ChecksumIEEE(append([]byte("IHDR"), ihdr[:]...)))
		hdr.Write(crcBuf[:])

		_, err := decodeImage(hdr.Bytes(), "image/png")
		if err == nil || !strings.Contains(err.Error(), "image_too_large") {
			t.Fatalf("expected image_too_large for oversized PNG header, got %v", err)
		}
	})

	t.Run("space and backend binding enforced", func(t *testing.T) {
		pngData := makePNG(t, 4, 4, func(x, y int) color.NRGBA { return color.NRGBA{R: 10, G: 20, B: 30, A: 255} }, nil)
		bp := writeTempBlob(t, "bind.png", pngData)
		_, err := pl.SegmentAndEmbed(context.Background(), &quivrplugin.IngestRequest{
			Configuration: json.RawMessage(`{"backend":"builtin"}`),
			BlobParts:     []quivrplugin.IngestBlobPart{bp},
			Spaces:        []string{externalSpaceID},
		})
		if err == nil || !strings.Contains(err.Error(), "space_backend_mismatch") {
			t.Fatalf("expected space_backend_mismatch, got %v", err)
		}
	})

	t.Run("aggregate max_segments enforced across blob parts", func(t *testing.T) {
		pngData := makePNG(t, 8, 8, func(x, y int) color.NRGBA { return color.NRGBA{R: 10, G: 20, B: 30, A: 255} }, nil)
		bp := writeTempBlob(t, "many.png", pngData)
		// 16 blob parts x (1 + 4*4 = 17 regions) = 272 > 256.
		parts := make([]quivrplugin.IngestBlobPart, 16)
		for i := range parts {
			parts[i] = bp
			parts[i].Key = "img-" + string(rune('a'+i))
		}
		_, err := pl.SegmentAndEmbed(context.Background(), &quivrplugin.IngestRequest{
			Configuration: json.RawMessage(`{"region_mode":"grid","grid_rows":4,"grid_cols":4}`),
			BlobParts:     parts,
			Spaces:        []string{},
		})
		if err == nil || !strings.Contains(err.Error(), "too_many_segments") {
			t.Fatalf("expected too_many_segments error, got %v", err)
		}
	})
}

func TestDecodePluginConfigBaseURL(t *testing.T) {
	for _, tc := range []struct {
		name   string
		raw    string
		accept bool
	}{
		{"valid http loopback", `{"backend":"http","base_url":"http://127.0.0.1:8080/embed"}`, true},
		{"valid https host", `{"backend":"http","base_url":"https://embed.example.test/v1"}`, true},
		{"unsupported scheme", `{"backend":"http","base_url":"ftp://embed.example.test"}`, false},
		{"userinfo rejected", `{"backend":"http","base_url":"http://user:pass@127.0.0.1:8080"}`, false},
		{"whitespace rejected", `{"backend":"http","base_url":"http://127.0.0.1:8080/ embed"}`, false},
		{"control char rejected", `{"backend":"http","base_url":"http://127.0.0.1:8080/\nembed"}`, false},
		{"missing host rejected", `{"backend":"http","base_url":"http:///embed"}`, false},
		{"port without hostname rejected", `{"backend":"http","base_url":"http://:8080"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodePluginConfig(json.RawMessage(tc.raw))
			if tc.accept && err != nil {
				t.Fatalf("expected %q accepted, got %v", tc.raw, err)
			}
			if !tc.accept && err == nil {
				t.Fatalf("expected %q rejected, got nil", tc.raw)
			}
		})
	}
}
