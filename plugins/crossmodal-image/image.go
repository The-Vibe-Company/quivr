package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

const (
	maxImageDimension = 16384
	maxImagePixels    = 64_000_000
)

type decodedImage struct {
	img        image.Image
	width      int
	height     int
	format     string
	mediaType  string
	textChunks map[string]string
}

type plannedRegion struct {
	region quivrplugin.ImageRegion
	rect   image.Rectangle
}

func decodeImage(data []byte, mediaType string) (decodedImage, error) {
	if len(data) == 0 {
		return decodedImage{}, quivrplugin.TerminalIngestError("invalid_image", "image payload is empty")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return decodedImage{}, quivrplugin.TerminalIngestError("invalid_image", "cannot decode image header")
	}
	if cfg.Width < 1 || cfg.Height < 1 {
		return decodedImage{}, quivrplugin.TerminalIngestError("invalid_image", "image dimensions must be positive")
	}
	if cfg.Width > maxImageDimension || cfg.Height > maxImageDimension || int64(cfg.Width)*int64(cfg.Height) > maxImagePixels {
		return decodedImage{}, quivrplugin.TerminalIngestError("image_too_large", "image dimensions exceed the decoded pixel limit")
	}
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return decodedImage{}, quivrplugin.TerminalIngestError("invalid_image", "cannot decode image bytes")
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 1 || h < 1 {
		return decodedImage{}, quivrplugin.TerminalIngestError("invalid_image", "image dimensions must be positive")
	}
	out := decodedImage{
		img:       img,
		width:     w,
		height:    h,
		format:    format,
		mediaType: mediaType,
	}
	if format == "png" {
		out.textChunks = parsePNGTextChunks(data)
	}
	return out, nil
}

// parsePNGTextChunks extracts standard uncompressed PNG tEXt metadata chunks
// (for example Title, Description, Keywords).
func parsePNGTextChunks(data []byte) map[string]string {
	const sig = "\x89PNG\r\n\x1a\n"
	if len(data) < len(sig) || string(data[:len(sig)]) != sig {
		return nil
	}
	chunks := map[string]string{}
	pos := len(sig)
	for pos+12 <= len(data) && len(chunks) < 16 {
		length := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		kind := string(data[pos+4 : pos+8])
		pos += 8
		if length < 0 || pos+length+4 > len(data) {
			break
		}
		payload := data[pos : pos+length]
		pos += length + 4 // payload + CRC
		if kind == "tEXt" && length <= 4096 {
			if nul := bytes.IndexByte(payload, 0); nul > 0 && nul < len(payload)-1 {
				k := strings.TrimSpace(string(payload[:nul]))
				v := strings.TrimSpace(string(payload[nul+1:]))
				if k != "" && v != "" && utf8.ValidString(k) && utf8.ValidString(v) && !strings.ContainsRune(v, 0) {
					chunks[k] = v
				}
			}
		}
		if kind == "IEND" {
			break
		}
	}
	if len(chunks) == 0 {
		return nil
	}
	return chunks
}

func planRegions(width, height int, cfg pluginConfig) []plannedRegion {
	full := plannedRegion{
		region: quivrplugin.ImageRegion{
			CoordinateSystem: "normalized_bbox",
			X:                0,
			Y:                0,
			Width:            1,
			Height:           1,
			ImageWidth:       width,
			ImageHeight:      height,
			Label:            "full",
		},
		rect: image.Rect(0, 0, width, height),
	}
	if cfg.RegionMode != "grid" || (cfg.GridRows <= 1 && cfg.GridCols <= 1) {
		return []plannedRegion{full}
	}
	out := make([]plannedRegion, 0, 1+cfg.GridRows*cfg.GridCols)
	out = append(out, full)
	for r := 0; r < cfg.GridRows; r++ {
		y0 := (r * height) / cfg.GridRows
		y1 := ((r + 1) * height) / cfg.GridRows
		if y1 <= y0 {
			continue
		}
		ny := round6(float64(y0) / float64(height))
		nh := clampSpan(ny, round6(float64(y1-y0)/float64(height)))
		if nh <= 0 {
			continue
		}
		for c := 0; c < cfg.GridCols; c++ {
			x0 := (c * width) / cfg.GridCols
			x1 := ((c + 1) * width) / cfg.GridCols
			if x1 <= x0 {
				continue
			}
			nx := round6(float64(x0) / float64(width))
			nw := clampSpan(nx, round6(float64(x1-x0)/float64(width)))
			if nw <= 0 {
				continue
			}
			out = append(out, plannedRegion{
				region: quivrplugin.ImageRegion{
					CoordinateSystem: "normalized_bbox",
					X:                nx,
					Y:                ny,
					Width:            nw,
					Height:           nh,
					ImageWidth:       width,
					ImageHeight:      height,
					Label:            fmt.Sprintf("grid:%d,%d", r, c),
				},
				rect: image.Rect(x0, y0, x1, y1),
			})
		}
	}
	return out
}

func clampSpan(origin, span float64) float64 {
	if origin+span > 1 {
		span = round6(1 - origin)
		for origin+span > 1 && span > 0 {
			span = math.Nextafter(span, 0)
		}
	}
	return span
}

func cropToPNG(img image.Image, rect image.Rectangle) ([]byte, error) {
	if img == nil {
		return nil, nil
	}
	b := img.Bounds()
	shifted := rect.Add(b.Min).Intersect(b)
	if shifted.Empty() {
		shifted = b
	}
	dst := image.NewNRGBA(image.Rect(0, 0, shifted.Dx(), shifted.Dy()))
	draw.Draw(dst, dst.Bounds(), img, shifted.Min, draw.Src)
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func round6(v float64) float64 {
	return math.Round(v*1e6) / 1e6
}
