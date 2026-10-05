// load-plugin implements the public SDK contracts without a provider client.
// Its only networking is the SDK's inbound HTTP server. Synthetic vectors,
// reranking scores and decisions measure engine load, never model quality.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	q "github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

type fake struct{}
type configuration struct {
	Embedding int `json:"embedding_ms"`
	Reranking int `json:"reranking_ms"`
	Judge     int `json:"judge_ms"`
}

func latency(ctx context.Context, raw json.RawMessage, kind string) error {
	var c configuration
	if err := json.Unmarshal(raw, &c); err != nil {
		return err
	}
	delay := map[string]int{"embedding": c.Embedding, "reranking": c.Reranking, "judge": c.Judge}[kind]
	if delay == 0 {
		return nil
	}
	timer := time.NewTimer(time.Duration(delay) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func words(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func vector(text string) []float32 {
	out := make([]float32, 64)
	for _, word := range words(text) {
		sum := sha256.Sum256([]byte(word))
		out[int(sum[0])%len(out)]++
	}
	var norm float64
	for _, v := range out {
		norm += float64(v * v)
	}
	if norm == 0 {
		out[0], norm = 1, 1
	}
	for i := range out {
		out[i] /= float32(math.Sqrt(norm))
	}
	return out
}

func (fake) SegmentAndEmbed(ctx context.Context, r *q.IngestRequest) ([]q.Segment, error) {
	if err := latency(ctx, r.Configuration, "embedding"); err != nil {
		return nil, err
	}
	segments := []q.Segment{}
	for _, part := range r.Parts {
		if strings.TrimSpace(part.Text) == "" {
			continue
		}
		vectors := map[string][]float32{}
		for _, space := range r.Spaces {
			vectors[space] = vector(part.Text)
		}
		segments = append(segments, q.Segment{PartKey: part.Key, Start: 0, End: len([]rune(part.Text)),
			Vectors: vectors, LexicalText: strings.Join(words(part.Text), " ")})
	}
	if len(segments) == 0 {
		return nil, q.TerminalIngestError("empty_content", "no nonblank text parts")
	}
	return segments, nil
}

func (fake) EmbedQuery(ctx context.Context, r *q.QueryRequest) ([]float32, error) {
	if err := latency(ctx, r.Configuration, "embedding"); err != nil {
		return nil, err
	}
	return vector(r.Query.Text), nil
}

func (fake) Search(ctx context.Context, r *q.SearchRequest) (*q.SearchAnswer, error) {
	if r.Round == 1 {
		c := q.CandidateRequest{QueryText: r.Query.Text, K: 40}
		switch r.Query.Mode {
		case "lexical":
			c.Primitive, c.Field = q.PrimitiveBM25, q.FieldSource
		case "semantic":
			c.Primitive, c.Space = q.PrimitiveNearVector, r.ServedSpace().ID
		default:
			alpha := .5
			c.Primitive, c.Field, c.Space = q.PrimitiveHybrid, q.FieldSource, r.ServedSpace().ID
			c.Alpha, c.Fusion = &alpha, "relative_score"
		}
		return q.Ask(c), nil
	}
	if r.Profile == "deep" {
		if err := latency(ctx, r.Configuration, "reranking"); err != nil {
			return nil, err
		}
	}
	hits := []q.RankedHit{}
	for _, served := range r.Served {
		for _, candidate := range served.Candidates {
			score := candidate.Score
			if r.Profile == "deep" {
				score = 0
				text := " " + strings.Join(words(candidate.Text), " ") + " "
				for _, word := range words(r.Query.Text) {
					if strings.Contains(text, " "+word+" ") {
						score++
					}
				}
			}
			hits = append(hits, q.RankedHit{SegmentID: candidate.SegmentID, Score: score,
				Explanation: "deterministic local load score"})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].SegmentID < hits[j].SegmentID
	})
	return q.Rank(hits[:min(len(hits), r.Limit)]...), nil
}

func (fake) Evaluate(ctx context.Context, r *q.SubscriptionRequest) (*q.SubscriptionResponse, error) {
	if err := latency(ctx, r.Configuration, "judge"); err != nil {
		return nil, err
	}
	out := &q.SubscriptionResponse{Decisions: []q.Decision{}}
	for _, e := range r.Evaluations {
		var expression struct {
			Term string `json:"term"`
		}
		if err := json.Unmarshal(e.Expression, &expression); err != nil {
			return nil, err
		}
		decision := q.NoMatch(e.ID)
		for _, part := range r.Record.Parts {
			if strings.Contains(strings.ToLower(part.Text), strings.ToLower(expression.Term)) {
				decision = q.Match(e.ID, "deterministic local load decision", part.Key)
				break
			}
		}
		out.Decisions = append(out.Decisions, decision)
	}
	return out, nil
}

func main() {
	plugin, err := q.New("")
	if err == nil {
		err = plugin.Ingestion(fake{})
	}
	if err == nil {
		err = plugin.Retrieval(fake{})
	}
	if err == nil {
		err = plugin.Subscription(fake{})
	}
	if err == nil {
		err = plugin.Serve()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "load-plugin:", err)
		os.Exit(1)
	}
}
