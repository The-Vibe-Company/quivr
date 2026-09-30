// Command hash-embedder is a sample ingestion plugin built with the Quivr Go
// plugin SDK. It cuts every Part except the title into paragraphs of at most
// 600 code points, embeds each paragraph with the title by hashing its words
// (feature hashing, L2-normalized) in every requested space, and returns the
// folded words as lexical text. It is deterministic and needs no model.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"strings"
	"unicode"

	"github.com/The-Vibe-Company/quivr-v2/sdks/go/quivrplugin"
)

// maxRunes bounds one segment.
const maxRunes = 600

type embedder struct {
	spaces map[string]quivrplugin.Space
}

func (e embedder) SegmentAndEmbed(_ context.Context, req *quivrplugin.IngestRequest) ([]quivrplugin.Segment, error) {
	title := ""
	for _, p := range req.Parts {
		if p.Role == "title" {
			title = strings.TrimSpace(p.Text)
			break
		}
	}
	var segments []quivrplugin.Segment
	add := func(part quivrplugin.IngestPart, start, end int) {
		text := string([]rune(part.Text)[start:end])
		input := strings.TrimSpace(title + "\n\n" + text)
		vectors := map[string][]float32{}
		for _, id := range req.Spaces {
			vectors[id] = hashWords(input, e.spaces[id].Dimensions)
		}
		segments = append(segments, quivrplugin.Segment{PartKey: part.Key, Start: start, End: end, Vectors: vectors,
			LexicalText: strings.Join(words(text), " "), Provenance: map[string]any{"words": len(words(input))}})
	}
	for _, p := range req.Parts {
		if p.Role == "title" {
			continue
		}
		for _, span := range paragraphs([]rune(p.Text)) {
			add(p, span[0], span[1])
		}
	}
	if len(segments) == 0 {
		// Nothing but a title: the title Part is the only segment.
		for _, p := range req.Parts {
			if p.Role == "title" && strings.TrimSpace(p.Text) != "" {
				add(p, 0, len([]rune(p.Text)))
				break
			}
		}
	}
	if len(segments) == 0 {
		return nil, quivrplugin.TerminalIngestError("no_text", "the Version has no text to segment")
	}
	return segments, nil
}

func (e embedder) EmbedQuery(_ context.Context, req *quivrplugin.QueryRequest) ([]float32, error) {
	return hashWords(req.Query.Text, e.spaces[req.Space].Dimensions), nil
}

// paragraphs returns the [start, end) code point spans of the paragraphs of
// text (separated by blank lines), trimmed, each cut at a space into pieces
// of at most maxRunes.
func paragraphs(text []rune) [][2]int {
	var spans [][2]int
	start := 0
	flush := func(end int) {
		for start < end && unicode.IsSpace(text[start]) {
			start++
		}
		for end > start && unicode.IsSpace(text[end-1]) {
			end--
		}
		for end-start > maxRunes {
			cut := start + maxRunes
			for cut > start+maxRunes/2 && !unicode.IsSpace(text[cut]) {
				cut--
			}
			spans = append(spans, [2]int{start, cut})
			start = cut
			for start < end && unicode.IsSpace(text[start]) {
				start++
			}
		}
		if end > start {
			spans = append(spans, [2]int{start, end})
		}
	}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' && i+1 < len(text) && text[i+1] == '\n' {
			flush(i)
			start = i
		}
	}
	flush(len(text))
	return spans
}

// fold maps common Latin accented letters to their base letter.
var fold = strings.NewReplacer("à", "a", "â", "a", "ä", "a", "ç", "c", "é", "e", "è", "e", "ê", "e", "ë", "e", "î", "i", "ï", "i", "ô", "o", "ö", "o", "ù", "u", "û", "u", "ü", "u", "œ", "oe")

// words returns the lower-cased, folded words of text.
func words(text string) []string {
	return strings.FieldsFunc(fold.Replace(strings.ToLower(text)), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// hashWords embeds text by feature hashing its words into dims buckets.
func hashWords(text string, dims int) []float32 {
	v := make([]float64, dims)
	for _, w := range words(text) {
		sum := sha256.Sum256([]byte(w))
		i := binary.BigEndian.Uint32(sum[:4]) % uint32(dims)
		if sum[4]&1 == 0 {
			v[i]++
		} else {
			v[i]--
		}
	}
	norm := 0.0
	for _, x := range v {
		norm += x * x
	}
	if norm == 0 {
		v[0], norm = 1, 1
	}
	out := make([]float32, dims)
	for i, x := range v {
		out[i] = float32(x / math.Sqrt(norm))
	}
	return out
}

func main() {
	plugin, err := quivrplugin.New("")
	if err == nil {
		err = plugin.Ingestion(embedder{spaces: plugin.Manifest().Ingestion.Spaces})
	}
	if err == nil {
		err = plugin.Serve()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hash-embedder:", err)
		os.Exit(1)
	}
}
