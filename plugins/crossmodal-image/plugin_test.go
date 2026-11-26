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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
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
		Spaces:        []string{visionSpaceID},
	})
	if err != nil {
		t.Fatalf("http SegmentAndEmbed: %v", err)
	}
	if len(segs) != 1 || gotAuth != "Bearer super-secret-token" {
		t.Fatalf("unexpected http result: len=%d auth=%q", len(segs), gotAuth)
	}
	qVec, err := pl.EmbedQuery(context.Background(), &quivrplugin.QueryRequest{
		Configuration: cfg,
		Space:         visionSpaceID,
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
}

func TestInvalidImageRejectedDeterministically(t *testing.T) {
	bp := writeTempBlob(t, "bad.png", []byte("not-a-valid-image"))
	_, err := (crossModalPlugin{}).SegmentAndEmbed(context.Background(), &quivrplugin.IngestRequest{
		Configuration: json.RawMessage(`{}`),
		BlobParts:     []quivrplugin.IngestBlobPart{bp},
		Spaces:        []string{visionSpaceID},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid_image") {
		t.Fatalf("expected invalid_image terminal error, got %v", err)
	}
}
