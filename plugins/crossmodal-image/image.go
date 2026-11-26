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
	if w, h, ok := parseWebPDimensions(data); ok {
		return decodedImage{
			width:     w,
			height:    h,
			format:    "webp",
			mediaType: mediaType,
		}, nil
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

// parseWebPDimensions extracts image width and height from RIFF WEBP headers
// (VP8, VP8L, VP8X) without external codecs.
func parseWebPDimensions(data []byte) (int, int, bool) {
	if len(data) < 30 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0, false
	}
	chunk := string(data[12:16])
	switch chunk {
	case "VP8X":
		if len(data) < 30 {
			return 0, 0, false
		}
		w := 1 + int(data[24]) | (int(data[25]) << 8) | (int(data[26]) << 16)
		h := 1 + int(data[27]) | (int(data[28]) << 8) | (int(data[29]) << 16)
		return w, h, w > 0 && h > 0
	case "VP8L":
		if len(data) < 25 || data[20] != 0x2f {
			return 0, 0, false
		}
		bits := binary.LittleEndian.Uint32(data[21:25])
		w := int(bits&0x3fff) + 1
		h := int((bits>>14)&0x3fff) + 1
		return w, h, w > 0 && h > 0
	case "VP8 ":
		if len(data) < 30 || data[23] != 0x9d || data[24] != 0x01 || data[25] != 0x2a {
			return 0, 0, false
		}
		w := int(binary.LittleEndian.Uint16(data[26:28]) & 0x3fff)
		h := int(binary.LittleEndian.Uint16(data[28:30]) & 0x3fff)
		return w, h, w > 0 && h > 0
	}
	return 0, 0, false
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
		nh := round6(float64(y1-y0) / float64(height))
		if ny+nh > 1 {
			nh = round6(1 - ny)
		}
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
			nw := round6(float64(x1-x0) / float64(width))
			if nx+nw > 1 {
				nw = round6(1 - nx)
			}
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
