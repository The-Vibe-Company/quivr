package pluginhttp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/call"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

// Retriever is the pinned plugin's retrieval Contribution as search calls
// it. Search judges each answer with the Contract Runner's rules
// (plugins.RetrievalSession) before it serves or returns anything.
type Retriever struct {
	Pin *plugins.Pin
}

var _ retrieval.Ranker = Retriever{}

// Manifest is the pinned plugin's manifest.
func (r Retriever) Manifest() *plugins.Manifest { return &r.Pin.Manifest }

// Configuration is the pin's validated plugin configuration.
func (r Retriever) Configuration() json.RawMessage { return r.Pin.Configuration }

// Round posts one search round within ctx, which carries the profile's
// deadline. A terminal error envelope is content.ErrInvalid (the plugin
// refuses the query); an answer over max_response_bytes is
// retrieval.ErrPluginInvalid; a retryable error, a missing envelope or a
// transport failure is ErrUnavailable.
func (r Retriever) Round(ctx context.Context, request plugins.SearchRequest) ([]byte, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	result, err := call.Invoke(ctx, r.Pin, call.SearchRound, call.Bytes(body), nil, nil)
	observe(r.Pin, request.OrganizationID, OpSearchRound, started, result, err)
	if err != nil {
		return nil, err
	}
	return result.Body, nil
}
