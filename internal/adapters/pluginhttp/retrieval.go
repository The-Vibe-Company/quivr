package pluginhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
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
	result, err := devhost.InvokeSearch(ctx, r.Pin.Endpoint, body, plugins.RetrievalMaxResponseBytes(&r.Pin.Manifest), func([]byte) []plugins.Issue { return nil })
	observe(r.Pin, request.OrganizationID, OpSearchRound, started, result, err)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	switch {
	case result.Error != nil && !result.Error.Retryable:
		slog.Info("retrieval plugin refused a search", "component", "search", "plugin", r.Pin.Manifest.ID, "code", result.Error.Code, "status", result.Status)
		return nil, fmt.Errorf("%w: %s: %s", content.ErrInvalid, result.Error.Code, result.Error.Message)
	case result.Error != nil:
		return nil, &PluginError{Status: result.Status, Code: result.Error.Code, Message: result.Error.Message, Retryable: true}
	case len(result.Issues) > 0 && result.Issues[0].Code == devhost.CodeResponseTooLarge:
		return nil, fmt.Errorf("%w: %s", retrieval.ErrPluginInvalid, describe(result.Issues))
	case len(result.Issues) > 0:
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, describe(result.Issues))
	}
	return result.Body, nil
}
