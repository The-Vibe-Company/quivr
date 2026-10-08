package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"maps"
	"time"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

func (i *ingester) SegmentAndEmbedPage(ctx context.Context, req *quivrplugin.IngestRequest) (quivrplugin.IngestPage, error) {
	if req.Page == nil || len(req.Parts) != 1 || req.Page.MaxSegments < 1 {
		return quivrplugin.IngestPage{}, quivrplugin.TerminalIngestError("invalid_page", "one source window and a page bound are required")
	}
	part := req.Parts[0]
	runes := []rune(part.Text)
	if req.Page.Start < 0 || req.Page.Start >= len(runes) {
		return quivrplugin.IngestPage{}, quivrplugin.TerminalIngestError("invalid_page", "source start is outside the window")
	}
	c := i.config
	// Every text Part, including title/context text, receives its own passages.
	// Repeating a large title in every model input would recreate a size refusal.
	c.TitleSource = "none"
	c.TitleContextParts = nil
	window := part
	window.Role = "body"
	window.Text = string(runes[req.Page.Start:])
	segments, inputs, err := c.packedSegments(ctx, []quivrplugin.IngestPart{window}, i.tokenizer)
	if err != nil {
		var refusal *quivrplugin.IngestError
		if !errors.As(err, &refusal) || (refusal.Code != "segmentation_limit" && refusal.Code != "no_indexable_text") {
			return quivrplugin.IngestPage{}, err
		}
		// A conservative token estimate or a whitespace-only window cannot
		// refuse source text. Let the provider negotiate finer cuts instead.
		segments = []quivrplugin.Segment{{PartKey: part.Key, Start: 0, End: len(runes) - req.Page.Start, Vectors: map[string][]float32{}, Provenance: map[string]any{"budget_negotiated": true}}}
		inputs = []string{c.documentInput("", window.Text)}
	}
	if len(req.Spaces) > 0 && (len(req.Spaces) != 1 || req.Spaces[0] != i.config.spaceID()) {
		return quivrplugin.IngestPage{}, quivrplugin.TerminalIngestError("unknown_space", "request must name the configured space")
	}
	type passage struct {
		segment quivrplugin.Segment
		input   string
	}
	pending := make([]passage, len(segments))
	for n, s := range segments {
		s.Start += req.Page.Start
		s.End += req.Page.Start
		for k := range s.SourceRanges {
			s.SourceRanges[k].Start += req.Page.Start
			s.SourceRanges[k].End += req.Page.Start
		}
		pending[n] = passage{s, inputs[n]}
		pending[n].segment.Provenance["context_mode"] = "separate_passages"
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.CallBudgetMS)*time.Millisecond)
	defer cancel()
	page := quivrplugin.IngestPage{Segments: []quivrplugin.Segment{}}
	limit := min(c.MaxChunks, req.Page.MaxSegments)
	for len(pending) > 0 && len(page.Segments) < limit {
		if err := ctx.Err(); err != nil {
			return quivrplugin.IngestPage{}, quivrplugin.RetryableIngestError("embedding_incomplete", "page interrupted; committed pages are retained")
		}
		p := pending[0]
		if len(req.Spaces) > 0 {
			key := sha256.Sum256([]byte(req.OrganizationID + "\x00" + p.input))
			vector := i.get(key)
			if vector == nil {
				cost, costErr := i.provider.inputCost(ctx, p.input)
				if costErr != nil {
					return quivrplugin.IngestPage{}, pageEmbeddingError(costErr)
				}
				var vectors [][]float32
				if cost > c.BatchTokens && p.segment.End-p.segment.Start > 1 {
					err = &inputRefusal{quivrplugin.TerminalIngestError("input_size", "provider work group needs smaller passages")}
				} else if c.BatchWaitMS == 0 {
					vectors, err = i.provider.embed(ctx, []string{p.input}, "document", req.InvocationID)
				} else {
					vectors, err = i.documents.embed(ctx, req.OrganizationID, []string{p.input}, req.InvocationID)
				}
				if err != nil {
					var refusal *inputRefusal
					if errors.As(err, &refusal) && p.segment.End-p.segment.Start > 1 {
						// Keep both halves as independently embedded source passages.
						// Monotone Unicode bisection also handles an unknown provider bound.
						mid := p.segment.Start + (p.segment.End-p.segment.Start)/2
						children := make([]passage, 2)
						for n, bounds := range [][2]int{{p.segment.Start, mid}, {mid, p.segment.End}} {
							s := p.segment
							s.Start = bounds[0]
							s.End = bounds[1]
							s.SourceRanges = []quivrplugin.SourceRange{{PartKey: part.Key, Start: s.Start, End: s.End}}
							s.SourceSeparator = sourceSeparator
							s.Vectors = map[string][]float32{}
							s.Provenance = maps.Clone(s.Provenance)
							s.Provenance["provider_split"] = true
							input := c.documentInput("", string(runes[s.Start:s.End]))
							e, err := encodeOne(ctx, i.tokenizer, input, true)
							if err != nil {
								return quivrplugin.IngestPage{}, err
							}
							s.Provenance["input_tokens"] = e.Tokens
							children[n] = passage{s, input}
						}
						pending = append(children, pending[1:]...)
						i.provider.log.Info("embedding_split", "event", "hosted_embedding_split", "invocation_id", req.InvocationID, "source_codepoints", p.segment.End-p.segment.Start)
						continue
					}
					return quivrplugin.IngestPage{}, pageEmbeddingError(err)
				}
				vector = vectors[0]
				i.put(key, vector)
			}
			p.segment.Vectors[i.config.spaceID()] = vector
		}
		page.Segments = append(page.Segments, p.segment)
		pending = pending[1:]
	}
	end := page.Segments[len(page.Segments)-1].End
	if end < len(runes) {
		page.NextStart = &end
	}
	return page, nil
}

var _ quivrplugin.PagedIngester = (*ingester)(nil)

func pageEmbeddingError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return quivrplugin.RetryableIngestError("embedding_incomplete", "page interrupted; committed pages are retained")
	}
	return err
}
