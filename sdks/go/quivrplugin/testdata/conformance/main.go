// Command conformance is an offline certification peer for the missing Go
// Contributions. It processes normative input through the public SDK.
package main

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	sdk "github.com/The-Vibe-Company/quivr-v2/sdks/go/quivrplugin"
)

type peer struct{}

func (peer) Normalize(ctx context.Context, r *sdk.NormalizerRequest) (*sdk.NormalizerResponse, error) {
	data, err := r.ReadInput(ctx)
	if err != nil {
		return nil, err
	}
	return &sdk.NormalizerResponse{Manifest: sdk.NormalizedManifest{Kind: "manifest", Parts: []sdk.NormalizedPart{{Key: "body", Role: "body", Content: sdk.NormalizedContent{Kind: "text", Text: string(data)}}}}}, nil
}
func (peer) Evaluate(_ context.Context, r *sdk.SubscriptionRequest) (*sdk.SubscriptionResponse, error) {
	out := &sdk.SubscriptionResponse{Decisions: []sdk.Decision{}}
	for _, e := range r.Evaluations {
		var expr struct {
			Kind, Text string
			Terms      []string
		}
		var cfg struct {
			CaseSensitive bool `json:"case_sensitive"`
		}
		_ = json.Unmarshal(e.Expression, &expr)
		_ = json.Unmarshal(e.Configuration, &cfg)
		terms := expr.Terms
		if expr.Kind == "substring" {
			terms = []string{expr.Text}
		}
		keys := []string{}
		for _, p := range r.Record.Parts {
			for _, term := range terms {
				text := p.Text
				if !cfg.CaseSensitive {
					text = strings.ToLower(text)
					term = strings.ToLower(term)
				}
				if strings.Contains(text, term) {
					keys = append(keys, p.Key)
					break
				}
			}
		}
		d := sdk.NoMatch(e.ID)
		if len(keys) > 0 {
			d = sdk.Match(e.ID, "The phrase appears.", keys...)
		}
		out.Decisions = append(out.Decisions, d)
	}
	return out, nil
}
func main() {
	p, err := sdk.New("")
	if err == nil {
		err = p.Normalizer(peer{})
	}
	if err == nil {
		err = p.Subscription(peer{})
	}
	if err == nil {
		err = p.Serve()
	}
	if err != nil {
		log.Fatal(err)
	}
}
