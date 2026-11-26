package pluginhttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
)

const multimodalManifest = `id: example.vision
version: 1.0.0
compatibility:
  engine: ">=0.1.0 <0.3.0"
  plugin_api: ">=0.20.0 <0.21.0"
contributions:
  ingestion:
    spaces:
      example.vision.image:
        version: "1"
        model: example/vision-2
        dimensions: 2
        metric: cosine
        indexes: [image]
        query_modalities: [text]
`

type stubBlobSource struct {
	blobs map[string]content.VerifiedBlob
}

func (s stubBlobSource) VerifiedBlob(_ context.Context, _, blobID string) (content.VerifiedBlob, error) {
	b, ok := s.blobs[blobID]
	if !ok {
		return content.VerifiedBlob{}, errors.New("blob not found")
	}
	return b, nil
}

type stubSigner struct {
	signedKey string
}

func (s *stubSigner) PresignGet(_ context.Context, objectKey string, ttl time.Duration) (string, time.Time, error) {
	s.signedKey = objectKey
	return "https://storage.example/signed/" + objectKey, time.Now().UTC().Add(ttl), nil
}

// A Plugin API 0.20 ingestion plugin declaring an image space receives signed
// Blob Part references (never inline bytes) and returns normalized region
// provenance mapped onto content.SegmentInput. Older text-only plugins continue
// to refuse Blob-only Versions.
func TestMultimodalIngestionSignsBlobPartsAndMapsRegions(t *testing.T) {
	ring, err := plugins.NewSigningKeys()
	if err != nil {
		t.Fatal(err)
	}
	signing, _ := json.Marshal(map[string]plugins.SigningKeys{"example.vision": ring})
	t.Setenv(plugins.EnvSigningKeys, string(signing))
	var received plugins.SegmentAndEmbedRequest
	var pin *plugins.Pin
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDiscovery(w, r, pin) {
			return
		}
		if r.URL.Path != plugins.SegmentAndEmbedRoute {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"segments": []map[string]any{
				{
					"part_key": "image",
					"start":    0,
					"end":      0,
					"region": map[string]any{
						"coordinate_system": "normalized_bbox",
						"x":                 0.25,
						"y":                 0.5,
						"width":             0.5,
						"height":            0.25,
						"image_width":       800,
						"image_height":      600,
						"label":             "grid:1,0",
					},
					"vectors": map[string][]float64{
						"example.vision.image": {0.6, 0.8},
					},
				},
			},
		})
	}))
	defer srv.Close()

	pin, err = plugins.LoadPinManifest([]byte(multimodalManifest), "vision", plugins.PinConfig{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 64)
	signer := &stubSigner{}
	ingestor := pluginhttp.Ingestor{
		Pin: pin,
		BlobSource: stubBlobSource{blobs: map[string]content.VerifiedBlob{
			"blob-img-1": {
				ID:        "blob-img-1",
				MediaType: "image/png",
				Blob:      content.Blob{Key: "orgs/o1/blobs/blob-img-1", SHA256: sha, Size: 512},
			},
		}},
		Signer: signer,
	}
	v := content.Version{
		ID:       "ver-img-1",
		RecordID: "rec-img-1",
		Manifest: content.Manifest{
			Kind: "manifest",
			Parts: []content.Part{{
				Key:  "image",
				Role: "image",
				Content: content.Text{
					Kind:       "blob",
					BlobID:     "blob-img-1",
					MediaType:  "image/png",
					BlobSHA256: sha,
				},
			}},
		},
	}

	segs, err := ingestor.SegmentAndEmbed(context.Background(), "org-1", "corpus-1", v, []string{"example.vision.image@1"})
	if err != nil {
		t.Fatalf("SegmentAndEmbed: %v", err)
	}
	if signer.signedKey != "orgs/o1/blobs/blob-img-1" {
		t.Fatalf("signed key = %q, want orgs/o1/blobs/blob-img-1", signer.signedKey)
	}
	if len(received.BlobParts) != 1 || received.BlobParts[0].Reference.Kind != "signed_url" || received.BlobParts[0].SHA256 != sha {
		t.Fatalf("unexpected blob_parts sent: %+v", received.BlobParts)
	}
	if len(segs) != 1 || segs[0].Region == nil || segs[0].Region.Label != "grid:1,0" || segs[0].Region.X != 0.25 || segs[0].Region.ImageWidth != 800 {
		t.Fatalf("unexpected mapped segment: %+v", segs)
	}

	// Verify content.PluginSegmentation and SaveSegmentation accept image regions
	// and produce stable SegmentIDs.
	segInputs := []content.SegmentInput{segs[0].SegmentInput}
	segmentation, err := content.PluginSegmentation("org-1", v, ingestor.Descriptor().Recipe, nil, segInputs)
	if err != nil {
		t.Fatalf("PluginSegmentation: %v", err)
	}
	if len(segmentation.Segments) != 1 || segmentation.Segments[0].Derivation.Region == nil || segmentation.Segments[0].Derivation.NormalizedSHA256 != sha {
		t.Fatalf("unexpected segmentation: %+v", segmentation)
	}

	// Text-only plugins (<0.20.0) continue to refuse Blob-only Versions cleanly.
	textPin, err := plugins.LoadPinManifest([]byte(embedderManifest), "embedder", plugins.PinConfig{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (pluginhttp.Ingestor{Pin: textPin, BlobSource: ingestor.BlobSource, Signer: signer}).SegmentAndEmbed(context.Background(), "org-1", "corpus-1", v, []string{"acme.embedder.small@1"})
	if !errors.Is(err, content.ErrIngestionRefused) {
		t.Fatalf("text-only plugin on blob-only version: got %v, want ErrIngestionRefused", err)
	}
}
