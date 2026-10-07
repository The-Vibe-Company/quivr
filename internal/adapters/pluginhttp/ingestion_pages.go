package pluginhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/call"
	"github.com/The-Vibe-Company/quivr/internal/processing"
)

// Byte and code-point positions keep window selection linear in source size.
// The cursor is host-owned; plugins only advance inside the bounded window.
type sourceCursor struct {
	Part      int `json:"part"`
	ByteStart int `json:"byte_start"`
	RuneStart int `json:"rune_start"`
	PageStart int `json:"page_start"`
}

func (i Ingestor) SegmentAndEmbedPage(ctx context.Context, org, corpusID string, v content.Version, keys []string, cursor json.RawMessage) (processing.PluginPage, error) {
	var pos sourceCursor
	if len(cursor) > 0 {
		if err := json.Unmarshal(cursor, &pos); err != nil {
			return processing.PluginPage{}, i.refused("invalid ingestion continuation")
		}
	}
	if pos.Part < 0 || pos.Part > len(v.Manifest.Parts) {
		return processing.PluginPage{}, i.refused("invalid ingestion source position")
	}
	if !i.Descriptor().Paged {
		return processing.PluginPage{}, i.refused("the ingestion owner does not support pages")
	}
	for pos.Part < len(v.Manifest.Parts) {
		part := v.Manifest.Parts[pos.Part]
		if pos.Part < 0 || pos.ByteStart < 0 || pos.ByteStart > len(part.Content.Text) || pos.RuneStart < 0 || pos.PageStart < 0 {
			return processing.PluginPage{}, i.refused("invalid ingestion source position")
		}
		if part.Content.Kind != "text" || pos.ByteStart == len(part.Content.Text) {
			pos = sourceCursor{Part: pos.Part + 1}
			continue
		}
		if !utf8.RuneStart(part.Content.Text[pos.ByteStart]) {
			return processing.PluginPage{}, i.refused("source continuation is not a Unicode boundary")
		}
		window := sourceWindow(part.Content.Text[pos.ByteStart:])
		if pos.PageStart >= utf8.RuneCountInString(window) {
			return processing.PluginPage{}, i.refused("ingestion page position is outside its source window")
		}
		ids := []string{}
		byID := map[string]string{}
		for _, key := range keys {
			id, _, ok := i.declared(key)
			if !ok {
				return processing.PluginPage{}, i.refused("undeclared ingestion space")
			}
			ids = append(ids, id)
			byID[id] = key
		}
		// Source titles and context Parts are processed independently as body
		// passages too; a long headline can never crowd its own text out.

		var answer plugins.IngestionAnswer
		for {
			parts := []plugins.IngestionPart{{Key: part.Key, Role: "body", Text: window}}
			request := plugins.SegmentAndEmbedRequest{InvocationID: plugins.InvocationID(), IdempotencyKey: plugins.IngestionKey(i.Pin.Generation(), org, v.ID, ids) + "-" + content.Hash(cursor), OrganizationID: org, Configuration: i.Pin.Configuration, Version: plugins.IngestionVersion{CorpusID: corpusID, RecordID: v.RecordID, RecordVersionID: v.ID}, Parts: parts, Spaces: ids, Page: &plugins.IngestionPageRequest{Start: pos.PageStart, MaxSegments: i.pageSegmentBound(ids)}}
			body, err := plugins.BuildSegmentAndEmbedRequest(request)
			if err != nil {
				return processing.PluginPage{}, err
			}
			view := plugins.IngestionRequestView{Parts: parts, Spaces: ids, Page: request.Page}
			started := time.Now()
			result, err := call.Invoke(ctx, i.Pin, call.SegmentAndEmbed, call.Bytes(body), func(_ context.Context, raw []byte) []plugins.Issue {
				return plugins.CheckSegmentAndEmbedOutput(raw, view, &i.Pin.Manifest)
			}, nil)
			observe(i.Pin, org, OpSegmentAndEmbed, started, result, err)
			if err == nil {
				answer, err = plugins.DecodeSegmentAndEmbed(result.Body)
				if err != nil {
					return processing.PluginPage{}, i.refused("invalid ingestion page")
				}
				break
			}
			if pageResponseTooLarge(result) {
				// A byte budget partitions work too. Previously accepted prefixes keep
				// their canonical offsets; only the unprocessed suffix is made smaller.
				runes := []rune(window)
				if pos.PageStart > 0 {
					pos.ByteStart += len(string(runes[:pos.PageStart]))
					pos.RuneStart += pos.PageStart
					runes = runes[pos.PageStart:]
					pos.PageStart = 0
				}
				if len(runes) <= 1 {
					return processing.PluginPage{}, fmt.Errorf("%w: ingestion response budget cannot hold one source code point and its vectors", plugins.ErrUnavailable)
				}
				window = string(runes[:len(runes)/2])
				continue
			}
			if errors.Is(err, plugins.ErrCallDeadline) {
				// An interrupted bounded page retries without consuming the legacy
				// per-item deadline budget. Completed pages remain committed.
				return processing.PluginPage{}, fmt.Errorf("%w: ingestion page interrupted", plugins.ErrUnavailable)
			}
			return processing.PluginPage{}, err
		}
		page := processing.PluginPage{Segments: make([]processing.PluginSegment, len(answer.Segments))}
		for n, s := range answer.Segments {
			vectors := map[string][]float32{}
			for id, vector := range s.Vectors {
				vectors[byID[id]] = plugins.Float32s(vector)
			}
			ranges := make([]content.SourceRange, len(s.SourceRanges))
			for k, r := range s.SourceRanges {
				ranges[k] = content.SourceRange{PartKey: r.PartKey, Start: pos.RuneStart + r.Start, End: pos.RuneStart + r.End}
			}
			page.Segments[n] = processing.PluginSegment{SegmentInput: content.SegmentInput{PartKey: s.PartKey, Start: pos.RuneStart + s.Start, End: pos.RuneStart + s.End, SourceRanges: ranges, SourceSeparator: s.SourceSeparator, LexicalText: s.LexicalText, Provenance: s.Provenance}, Vectors: vectors}
		}
		if answer.NextStart != nil {
			pos.PageStart = *answer.NextStart
		} else {
			pos.ByteStart += len(window)
			pos.RuneStart += utf8.RuneCountInString(window)
			pos.PageStart = 0
			if pos.ByteStart == len(part.Content.Text) {
				pos = sourceCursor{Part: pos.Part + 1}
			}
		}
		if pos.Part < len(v.Manifest.Parts) {
			page.Next, _ = json.Marshal(pos)
		}
		return page, nil
	}
	// An exhausted cursor may follow trailing empty/blob Parts.
	return processing.PluginPage{}, nil
}

func sourceWindow(text string) string {
	count := 0
	for offset := range text {
		if count == 4096 {
			return text[:offset]
		}
		count++
	}
	return text
}

var _ processing.PagedIngestionPlugin = Ingestor{}

// Keep vector and bounded lexical/provenance fields within each response budget.
// A plugin may return fewer passages when its own serialization needs more room.
func (i Ingestor) pageSegmentBound(ids []string) int {
	perSegment := plugins.MaxProvenanceBytes + plugins.MaxLexicalTextRunes*6 + 256*1024 + 4096
	for _, id := range ids {
		perSegment += i.contribution().Spaces[id].Dimensions * 32
	}
	return max(1, min(16, plugins.IngestionMaxSegments(&i.Pin.Manifest), (plugins.IngestionMaxResponseBytes(&i.Pin.Manifest)-1024)/perSegment))
}

func pageResponseTooLarge(result *devhost.Result) bool {
	if result == nil {
		return false
	}
	for _, issue := range result.Issues {
		if issue.Code == devhost.CodeResponseTooLarge {
			return true
		}
	}
	if e := result.Error; e != nil {
		if e.Code == "response_too_large" {
			return true
		}
		if e.Code == "invalid_response" {
			return strings.Contains(e.Message, "max_response_bytes")
		}
	}
	return false
}
