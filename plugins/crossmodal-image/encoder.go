package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"image"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

const (
	visionSpaceID   = "crossmodal.image.vision"
	externalSpaceID = "crossmodal.image.external"
	builtinModelID  = "crossmodal/builtin-vision-512"
	httpModelID     = "crossmodal/http-vision-512"
	vectorDims      = 512
	colorSubspace   = 0  // 0..31: color & hue semantics
	toneSubspace    = 32 // 32..47: luminance, saturation, contrast
	layoutSubspace  = 48 // 48..63: aspect ratio & spatial region geometry
	hashSubspace    = 64 // 64..511: shared signed feature hashing (448 dims)
	hashDims        = vectorDims - hashSubspace
)

type crossModalEncoder interface {
	embedRegion(ctx context.Context, decoded decodedImage, rawBytes []byte, pr plannedRegion) ([]float32, error)
	embedQuery(ctx context.Context, text string) ([]float32, error)
}

func newEncoderForSpace(cfg pluginConfig, spaceID string) (crossModalEncoder, string, error) {
	switch spaceID {
	case visionSpaceID:
		if cfg.Backend != "builtin" {
			return nil, "", quivrplugin.TerminalIngestError("space_backend_mismatch", "crossmodal.image.vision requires backend=builtin")
		}
		if cfg.Model != "" && cfg.Model != builtinModelID {
			return nil, "", quivrplugin.TerminalIngestError("space_model_mismatch", "model does not match crossmodal.image.vision")
		}
		return builtinEncoder{}, builtinModelID, nil
	case externalSpaceID:
		if cfg.Backend != "http" {
			return nil, "", quivrplugin.TerminalIngestError("space_backend_mismatch", "crossmodal.image.external requires backend=http")
		}
		if cfg.Model != "" && cfg.Model != httpModelID {
			return nil, "", quivrplugin.TerminalIngestError("space_model_mismatch", "model does not match crossmodal.image.external")
		}
		if cfg.BaseURL == "" {
			return nil, "", quivrplugin.TerminalIngestError("invalid_configuration", "base_url is required when backend is http")
		}
		apiKey := os.Getenv("CROSSMODAL_API_KEY")
		if (cfg.Auth == "bearer" || cfg.Auth == "api-key") && apiKey == "" {
			return nil, "", quivrplugin.RetryableIngestError("missing_secret", "CROSSMODAL_API_KEY must be set when auth is bearer or api-key")
		}
		return &httpEncoder{
			baseURL: cfg.BaseURL,
			auth:    cfg.Auth,
			apiKey:  apiKey,
			model:   httpModelID,
			client: &http.Client{
				Timeout: time.Duration(cfg.RequestTimeoutMS) * time.Millisecond,
				CheckRedirect: func(*http.Request, []*http.Request) error {
					return http.ErrUseLastResponse
				},
			},
		}, httpModelID, nil
	default:
		return nil, "", quivrplugin.TerminalIngestError("unknown_space", fmt.Sprintf("unsupported cross-modal space %q", spaceID))
	}
}

// builtinEncoder projects visual region features and natural-language queries
// into a deterministic 512-dimensional L2-normalized Cross-modal Vector Space.
type builtinEncoder struct{}

func (builtinEncoder) embedRegion(_ context.Context, decoded decodedImage, _ []byte, pr plannedRegion) ([]float32, error) {
	vec := make([]float64, vectorDims)
	tokens := make([]string, 0, 32)
	tokens = append(tokens, "image", "picture", "photo", "visual")
	if decoded.format != "" {
		tokens = append(tokens, decoded.format)
	}

	// Geometry and spatial region semantics.
	aspect := float64(pr.rect.Dx()) / math.Max(1, float64(pr.rect.Dy()))
	switch {
	case aspect >= 1.25:
		vec[layoutSubspace+0] += 1.0
		tokens = append(tokens, "landscape", "wide", "horizontal")
	case aspect <= 0.8:
		vec[layoutSubspace+1] += 1.0
		tokens = append(tokens, "portrait", "tall", "vertical")
	default:
		vec[layoutSubspace+2] += 1.0
		tokens = append(tokens, "square")
	}

	cx := pr.region.X + pr.region.Width/2
	cy := pr.region.Y + pr.region.Height/2
	if pr.region.Width >= 0.99 && pr.region.Height >= 0.99 {
		vec[layoutSubspace+3] += 0.8
		tokens = append(tokens, "full", "whole", "scene")
	} else {
		vec[layoutSubspace+4] += 0.8
		tokens = append(tokens, "region", "crop", "detail")
		if cy < 0.4 {
			vec[layoutSubspace+5] += 1.0
			tokens = append(tokens, "top", "upper")
		} else if cy > 0.6 {
			vec[layoutSubspace+6] += 1.0
			tokens = append(tokens, "bottom", "lower")
		}
		if cx < 0.4 {
			vec[layoutSubspace+7] += 1.0
			tokens = append(tokens, "left")
		} else if cx > 0.6 {
			vec[layoutSubspace+8] += 1.0
			tokens = append(tokens, "right")
		}
		if cx >= 0.35 && cx <= 0.65 && cy >= 0.35 && cy <= 0.65 {
			vec[layoutSubspace+9] += 1.0
			tokens = append(tokens, "center", "middle")
		}
	}

	// Pixel-derived visual features (color distribution, luminance, saturation, contrast).
	if decoded.img != nil {
		stats := analyzePixels(decoded.img, pr.rect)
		for idx, w := range stats.colorWeights {
			vec[colorSubspace+idx] += w * 2.2
		}
		for _, tok := range stats.tokens {
			tokens = append(tokens, tok)
		}
		vec[toneSubspace+0] += stats.brightWeight * 1.6
		vec[toneSubspace+1] += stats.darkWeight * 1.6
		vec[toneSubspace+2] += stats.vividWeight * 1.4
		vec[toneSubspace+3] += stats.mutedWeight * 1.4
		vec[toneSubspace+4] += stats.contrastWeight * 1.4
		vec[toneSubspace+5] += stats.smoothWeight * 1.2
		vec[layoutSubspace+10] += stats.topBrightWeight * 1.0
		vec[layoutSubspace+11] += stats.bottomDarkWeight * 1.0
	}

	// Optional PNG tEXt metadata tokens (deterministic sorted order).
	if len(decoded.textChunks) > 0 {
		keys := make([]string, 0, len(decoded.textChunks))
		for k := range decoded.textChunks {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			for _, tok := range tokenize(decoded.textChunks[k]) {
				tokens = append(tokens, tok)
				addConceptWeights(vec, tok, 0.8)
			}
		}
	}

	addHashedTokens(vec, tokens, 0.65)
	return normalizeVector(vec), nil
}

func (builtinEncoder) embedQuery(_ context.Context, text string) ([]float32, error) {
	vec := make([]float64, vectorDims)
	rawTokens := tokenize(text)
	expanded := make([]string, 0, len(rawTokens)*2)
	for _, tok := range rawTokens {
		expanded = append(expanded, tok)
		if canonical := canonicalConcept(tok); canonical != "" {
			expanded = append(expanded, canonical)
			addConceptWeights(vec, canonical, 2.2)
		} else {
			addConceptWeights(vec, tok, 1.8)
		}
	}
	addHashedTokens(vec, expanded, 0.65)
	return normalizeVector(vec), nil
}

type pixelStats struct {
	colorWeights     [16]float64
	brightWeight     float64
	darkWeight       float64
	vividWeight      float64
	mutedWeight      float64
	contrastWeight   float64
	smoothWeight     float64
	topBrightWeight  float64
	bottomDarkWeight float64
	tokens           []string
}

func analyzePixels(img image.Image, rect image.Rectangle) pixelStats {
	b := img.Bounds()
	area := rect.Add(b.Min).Intersect(b)
	if area.Empty() {
		area = b
	}
	dx, dy := area.Dx(), area.Dy()
	stepX := max(1, dx/32)
	stepY := max(1, dy/32)

	var stats pixelStats
	var count, lumSum, lumSqSum, satSum, edgeSum float64
	var topLum, topCount, botLum, botCount float64
	midY := area.Min.Y + dy/2

	var prevLum float64
	for y := area.Min.Y; y < area.Max.Y; y += stepY {
		rowStart := true
		for x := area.Min.X; x < area.Max.X; x += stepX {
			r16, g16, b16, _ := img.At(x, y).RGBA()
			r := float64(r16) / 65535.0
			g := float64(g16) / 65535.0
			bl := float64(b16) / 65535.0
			h, s, l := rgbToHSL(r, g, bl)

			count++
			lumSum += l
			lumSqSum += l * l
			satSum += s
			if y < midY {
				topLum += l
				topCount++
			} else {
				botLum += l
				botCount++
			}
			if !rowStart {
				edgeSum += math.Abs(l - prevLum)
			}
			prevLum = l
			rowStart = false

			switch {
			case l >= 0.85 && s < 0.2:
				stats.colorWeights[9]++ // white
			case l <= 0.15:
				stats.colorWeights[11]++ // black
			case s < 0.15:
				stats.colorWeights[10]++ // gray
			case h < 18 || h >= 340:
				stats.colorWeights[0]++ // red
				stats.colorWeights[12]++ // warm
			case h < 45:
				if l < 0.45 {
					stats.colorWeights[8]++ // brown
				} else {
					stats.colorWeights[1]++ // orange
				}
				stats.colorWeights[12]++ // warm
			case h < 72:
				stats.colorWeights[2]++ // yellow
				stats.colorWeights[12]++ // warm
			case h < 165:
				stats.colorWeights[3]++ // green
				stats.colorWeights[13]++ // cool
			case h < 200:
				stats.colorWeights[4]++ // cyan
				stats.colorWeights[13]++ // cool
			case h < 255:
				stats.colorWeights[5]++ // blue
				stats.colorWeights[13]++ // cool
			case h < 295:
				stats.colorWeights[6]++ // purple
				stats.colorWeights[13]++ // cool
			default:
				stats.colorWeights[7]++ // pink
				stats.colorWeights[12]++ // warm
			}
		}
	}
	if count == 0 {
		return stats
	}
	colorNames := [14]string{
		"red", "orange", "yellow", "green", "cyan", "blue", "purple", "pink",
		"brown", "white", "gray", "black", "warm", "cool",
	}
	for i := range colorNames {
		stats.colorWeights[i] /= count
		if stats.colorWeights[i] >= 0.22 {
			stats.tokens = append(stats.tokens, colorNames[i])
		}
	}
	meanLum := lumSum / count
	meanSat := satSum / count
	variance := math.Max(0, lumSqSum/count-meanLum*meanLum)
	contrast := math.Sqrt(variance) + (edgeSum / count)

	if meanLum >= 0.62 {
		stats.brightWeight = meanLum
		stats.tokens = append(stats.tokens, "bright", "light")
	} else if meanLum <= 0.35 {
		stats.darkWeight = 1.0 - meanLum
		stats.tokens = append(stats.tokens, "dark", "night")
	}
	if meanSat >= 0.45 {
		stats.vividWeight = meanSat
		stats.tokens = append(stats.tokens, "vivid", "colorful")
	} else if meanSat <= 0.18 {
		stats.mutedWeight = 1.0 - meanSat
		stats.tokens = append(stats.tokens, "muted", "monochrome")
	}
	if contrast >= 0.22 {
		stats.contrastWeight = math.Min(1.0, contrast)
		stats.tokens = append(stats.tokens, "contrast", "detailed")
	} else {
		stats.smoothWeight = 1.0 - contrast
		stats.tokens = append(stats.tokens, "smooth", "solid")
	}
	if topCount > 0 && botCount > 0 {
		tl, bl := topLum/topCount, botLum/botCount
		if tl-bl > 0.2 {
			stats.topBrightWeight = math.Min(1.0, tl-bl)
			stats.bottomDarkWeight = math.Min(1.0, tl-bl)
		}
	}
	return stats
}

func rgbToHSL(r, g, b float64) (h, s, l float64) {
	maxC := math.Max(r, math.Max(g, b))
	minC := math.Min(r, math.Min(g, b))
	l = (maxC + minC) / 2
	d := maxC - minC
	if d == 0 {
		return 0, 0, l
	}
	if l > 0.5 {
		s = d / (2 - maxC - minC)
	} else {
		s = d / (maxC + minC)
	}
	switch maxC {
	case r:
		h = (g - b) / d
		if g < b {
			h += 6
		}
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	h *= 60
	return h, s, l
}

var conceptSynonyms = map[string]string{
	"crimson": "red", "scarlet": "red", "ruby": "red", "maroon": "red",
	"amber": "orange", "tangerine": "orange", "sunset": "orange",
	"gold": "yellow", "golden": "yellow", "lemon": "yellow",
	"emerald": "green", "forest": "green", "leaf": "green", "grass": "green", "lime": "green",
	"teal": "cyan", "aqua": "cyan", "turquoise": "cyan",
	"azure": "blue", "navy": "blue", "cobalt": "blue", "sky": "blue", "ocean": "blue", "sea": "blue",
	"violet": "purple", "lavender": "purple", "indigo": "purple",
	"magenta": "pink", "rose": "pink",
	"tan": "brown", "earth": "brown", "wood": "brown",
	"snow": "white", "ivory": "white",
	"grey": "gray", "silver": "gray", "slate": "gray",
	"charcoal": "black", "ebony": "black",
	"sunny": "bright", "luminous": "bright", "daylight": "bright",
	"dim": "dark", "shadow": "dark", "midnight": "dark", "evening": "dark",
	"vibrant": "vivid", "saturated": "vivid",
	"grayscale": "monochrome", "greyscale": "monochrome", "neutral": "monochrome",
	"sharp": "detailed", "textured": "detailed", "edge": "detailed",
	"uniform": "solid", "flat": "smooth",
	"horizontal": "landscape", "panoramic": "landscape", "widescreen": "landscape",
	"vertical": "portrait",
	"upper": "top", "lower": "bottom", "middle": "center", "closeup": "detail",
}

func canonicalConcept(tok string) string {
	if c, ok := conceptSynonyms[tok]; ok {
		return c
	}
	return ""
}

func addConceptWeights(vec []float64, tok string, weight float64) {
	switch tok {
	case "red":
		vec[colorSubspace+0] += weight
		vec[colorSubspace+12] += weight * 0.4
	case "orange":
		vec[colorSubspace+1] += weight
		vec[colorSubspace+12] += weight * 0.4
	case "yellow":
		vec[colorSubspace+2] += weight
		vec[colorSubspace+12] += weight * 0.4
	case "green":
		vec[colorSubspace+3] += weight
		vec[colorSubspace+13] += weight * 0.4
	case "cyan":
		vec[colorSubspace+4] += weight
		vec[colorSubspace+13] += weight * 0.4
	case "blue":
		vec[colorSubspace+5] += weight
		vec[colorSubspace+13] += weight * 0.4
	case "purple":
		vec[colorSubspace+6] += weight
		vec[colorSubspace+13] += weight * 0.4
	case "pink":
		vec[colorSubspace+7] += weight
		vec[colorSubspace+12] += weight * 0.4
	case "brown":
		vec[colorSubspace+8] += weight
		vec[colorSubspace+12] += weight * 0.3
	case "white":
		vec[colorSubspace+9] += weight
	case "gray":
		vec[colorSubspace+10] += weight
	case "black":
		vec[colorSubspace+11] += weight
	case "warm":
		vec[colorSubspace+12] += weight
	case "cool":
		vec[colorSubspace+13] += weight
	case "bright", "light":
		vec[toneSubspace+0] += weight
	case "dark", "night":
		vec[toneSubspace+1] += weight
	case "vivid", "colorful":
		vec[toneSubspace+2] += weight
	case "muted", "monochrome":
		vec[toneSubspace+3] += weight
	case "contrast", "detailed":
		vec[toneSubspace+4] += weight
	case "smooth", "solid":
		vec[toneSubspace+5] += weight
	case "landscape", "wide":
		vec[layoutSubspace+0] += weight
	case "portrait", "tall":
		vec[layoutSubspace+1] += weight
	case "square":
		vec[layoutSubspace+2] += weight
	case "full", "whole", "scene":
		vec[layoutSubspace+3] += weight
	case "region", "crop", "detail":
		vec[layoutSubspace+4] += weight
	case "top":
		vec[layoutSubspace+5] += weight
	case "bottom":
		vec[layoutSubspace+6] += weight
	case "left":
		vec[layoutSubspace+7] += weight
	case "right":
		vec[layoutSubspace+8] += weight
	case "center":
		vec[layoutSubspace+9] += weight
	}
}

func tokenize(text string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

func addHashedTokens(vec []float64, tokens []string, weight float64) {
	for _, tok := range tokens {
		h := fnv.New64a()
		_, _ = h.Write([]byte(tok))
		sum := h.Sum64()
		idx := hashSubspace + int(sum%hashDims)
		sign := 1.0
		if (sum>>32)&1 == 1 {
			sign = -1.0
		}
		vec[idx] += sign * weight
		// Character trigrams give partial-word recall across captions and queries.
		runes := []rune(tok)
		if len(runes) >= 4 {
			for i := 0; i <= len(runes)-3; i++ {
				g := fnv.New64a()
				_, _ = g.Write([]byte("3:" + string(runes[i:i+3])))
				gs := g.Sum64()
				gidx := hashSubspace + int(gs%hashDims)
				gsign := 1.0
				if (gs>>32)&1 == 1 {
					gsign = -1.0
				}
				vec[gidx] += gsign * (weight * 0.25)
			}
		}
	}
}

func normalizeVector(vec []float64) []float32 {
	var normSq float64
	for _, x := range vec {
		normSq += x * x
	}
	if normSq == 0 || math.IsNaN(normSq) || math.IsInf(normSq, 0) {
		vec[0] = 1.0
		normSq = 1.0
	}
	inv := 1.0 / math.Sqrt(normSq)
	out := make([]float32, len(vec))
	for i, x := range vec {
		out[i] = float32(x * inv)
	}
	return out
}

// httpEncoder delegates image-region and text-query embedding to an external
// cross-modal embedding HTTP service.
type httpEncoder struct {
	baseURL string
	auth    string
	apiKey  string
	model   string
	client  *http.Client
}

func (e *httpEncoder) embedRegion(ctx context.Context, decoded decodedImage, rawBytes []byte, pr plannedRegion) ([]float32, error) {
	payload := rawBytes
	mediaType := decoded.mediaType
	if decoded.img != nil {
		cropped, err := cropToPNG(decoded.img, pr.rect)
		if err != nil {
			return nil, quivrplugin.TerminalIngestError("crop_failed", "cannot encode image region crop")
		}
		payload = cropped
		mediaType = "image/png"
	}
	if mediaType == "" {
		mediaType = "image/png"
	}
	dataURI := "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(payload)
	return e.call(ctx, map[string]any{
		"model": e.model,
		"input": map[string]any{
			"modality": "image",
			"data_uri": dataURI,
			"region":   pr.region,
		},
	})
}

func (e *httpEncoder) embedQuery(ctx context.Context, text string) ([]float32, error) {
	return e.call(ctx, map[string]any{
		"model": e.model,
		"input": map[string]any{
			"modality": "text",
			"text":     text,
		},
	})
}

func (e *httpEncoder) call(ctx context.Context, body map[string]any) ([]float32, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, quivrplugin.TerminalIngestError("encode_request", "cannot serialize embedding request")
	}
	backoffs := [...]time.Duration{50 * time.Millisecond, 150 * time.Millisecond}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return nil, quivrplugin.RetryableIngestError("request_cancelled", "embedding backend request cancelled or timed out")
		}
		if attempt > 0 {
			timer := time.NewTimer(backoffs[attempt-1])
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, quivrplugin.RetryableIngestError("request_cancelled", "embedding backend request cancelled or timed out")
			case <-timer.C:
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL, bytes.NewReader(raw))
		if err != nil {
			return nil, quivrplugin.TerminalIngestError("invalid_configuration", "invalid embedding backend URL")
		}
		req.Header.Set("Content-Type", "application/json")
		switch e.auth {
		case "bearer":
			req.Header.Set("Authorization", "Bearer "+e.apiKey)
		case "api-key":
			req.Header.Set("X-API-Key", e.apiKey)
		}
		resp, err := e.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, quivrplugin.RetryableIngestError("request_cancelled", "embedding backend request cancelled or timed out")
			}
			lastErr = quivrplugin.RetryableIngestError("backend_unavailable", "embedding backend request failed")
			continue
		}
		respBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if readErr != nil {
			if ctx.Err() != nil {
				return nil, quivrplugin.RetryableIngestError("request_cancelled", "embedding backend request cancelled or timed out")
			}
			lastErr = quivrplugin.RetryableIngestError("backend_unavailable", "embedding backend response unreadable")
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = quivrplugin.RetryableIngestError("backend_unavailable", fmt.Sprintf("embedding backend answered HTTP %d", resp.StatusCode))
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, quivrplugin.TerminalIngestError("backend_refused", fmt.Sprintf("embedding backend refused request with HTTP %d", resp.StatusCode))
		}
		vec, err := parseEmbeddingResponse(respBytes)
		if err != nil {
			return nil, err
		}
		return vec, nil
	}
	return nil, lastErr
}

func parseEmbeddingResponse(raw []byte) ([]float32, error) {
	var direct struct {
		Vector []float64 `json:"vector"`
		Data   []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &direct); err != nil {
		return nil, quivrplugin.TerminalIngestError("invalid_backend_response", "embedding backend returned invalid JSON")
	}
	values := direct.Vector
	if len(values) == 0 && len(direct.Data) > 0 {
		values = direct.Data[0].Embedding
	}
	if len(values) != vectorDims {
		return nil, quivrplugin.TerminalIngestError("dimension_mismatch", fmt.Sprintf("embedding backend returned %d dimensions; expected %d", len(values), vectorDims))
	}
	out := make([]float32, vectorDims)
	nonZero := false
	for i, x := range values {
		if math.IsNaN(x) || math.IsInf(float64(float32(x)), 0) {
			return nil, quivrplugin.TerminalIngestError("invalid_vector", "embedding backend returned non-finite number")
		}
		out[i] = float32(x)
		if out[i] != 0 {
			nonZero = true
		}
	}
	if !nonZero {
		return nil, quivrplugin.TerminalIngestError("invalid_vector", "embedding backend returned all-zero vector")
	}
	return out, nil
}
