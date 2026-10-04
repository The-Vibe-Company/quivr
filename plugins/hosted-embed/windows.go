package main

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

const specialTokens = 8

func (c configuration) segments(parts []quivrplugin.IngestPart) ([]quivrplugin.Segment, []string, error) {
	if len(parts) > 64 {
		return nil, nil, quivrplugin.TerminalIngestError("segmentation_limit", "more than 64 Parts")
	}
	total, titles := 0, 0
	hasBody := false
	for _, p := range parts {
		total += len(p.Text)
		if p.Role == "title" {
			titles++
		}
		hasBody = hasBody || (p.Role == "body" && strings.TrimSpace(p.Text) != "")
		if !utf8.ValidString(p.Text) || strings.ContainsRune(p.Text, 0) {
			return nil, nil, quivrplugin.TerminalIngestError("invalid_text", "text must be UTF-8 without NUL")
		}
	}
	if total > 256<<10 || titles > 1 {
		return nil, nil, quivrplugin.TerminalIngestError("segmentation_limit", "more than 256 KiB of text or one title")
	}
	var segments []quivrplugin.Segment
	var inputs []string
	budget := c.MaxTokens - len(c.DocumentPrefix) - specialTokens
	for _, p := range parts {
		if (hasBody && p.Role != "body") || (!hasBody && p.Role != "title") || strings.TrimSpace(p.Text) == "" {
			continue
		}
		runes := []rune(p.Text)
		for start := 0; start < len(runes); {
			end, n := start, 0
			for end < len(runes) && n+utf8.RuneLen(runes[end]) <= budget {
				n += utf8.RuneLen(runes[end])
				end++
			}
			if end == start {
				return nil, nil, quivrplugin.TerminalIngestError("segmentation_limit", "prefix leaves no room for a code point")
			}
			hard := end < len(runes)
			if hard {
				end = snap(runes, start, end)
			}
			input := c.DocumentPrefix + string(runes[start:end])
			segments = append(segments, quivrplugin.Segment{PartKey: p.Key, Start: start, End: end, Vectors: map[string][]float32{}, Provenance: map[string]any{"token_estimate": len(input) + specialTokens, "hard_cut": hard, "model": c.Model}})
			inputs = append(inputs, input)
			if len(segments) > 256 {
				return nil, nil, quivrplugin.TerminalIngestError("segmentation_limit", "more than 256 windows")
			}
			if end == len(runes) {
				break
			}
			next, overlap := end, 0
			for next > start+1 && overlap+utf8.RuneLen(runes[next-1]) <= c.Overlap {
				next--
				overlap += utf8.RuneLen(runes[next])
			}
			start = next
		}
	}
	if len(segments) == 0 {
		return nil, nil, quivrplugin.TerminalIngestError("no_indexable_text", "no title or body text")
	}
	return segments, inputs, nil
}

// Prefer the latest paragraph, then line, then sentence end in the latter
// half of a window. Hard cuts still preserve every source code point.
func snap(runes []rune, start, end int) int {
	floor := start + (end-start)/2
	for k := end; k > floor; k-- {
		if k > start+1 && runes[k-1] == '\n' && runes[k-2] == '\n' {
			return k
		}
	}
	for k := end; k > floor; k-- {
		if runes[k-1] == '\n' {
			return k
		}
	}
	for k := end; k > floor; k-- {
		if k > start+1 && unicode.IsSpace(runes[k-1]) && strings.ContainsRune(".!?。！？", runes[k-2]) {
			return k
		}
	}
	return end
}
