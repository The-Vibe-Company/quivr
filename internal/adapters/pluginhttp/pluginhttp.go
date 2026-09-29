// Package pluginhttp is the engine's Plugin Protocol v0 client: it checks a
// pinned plugin's discovery document and invokes its normalizer over
// JSON-over-HTTP. It reuses the protocol helpers of the local plugin host
// (package devhost), so the engine, `quivr plugin dev` and the Contract
// Runner judge a plugin with the same code. It never imports plugin code.
package pluginhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
)

// ErrUnavailable is plugin unavailability: a connection failure, a timeout, a
// non-2xx answer without a valid error envelope or a discovery document that
// does not match the pinned manifest. It is never a plugin decision.
var ErrUnavailable = errors.New("plugin_unavailable")

// PluginError is an error the plugin declared through the error envelope.
type PluginError struct {
	Status    int
	Code      string
	Message   string
	Retryable bool
}

func (e *PluginError) Error() string {
	return fmt.Sprintf("plugin error %s (HTTP %d, retryable=%t): %s", e.Code, e.Status, e.Retryable, e.Message)
}

// InvalidOutput is a 200 answer the engine refuses: schema, structural
// Manifest rules or the response size bound.
type InvalidOutput struct {
	Issues []plugins.Issue
}

func (e *InvalidOutput) Error() string { return "invalid normalizer output: " + describe(e.Issues) }

func describe(issues []plugins.Issue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, strings.TrimSpace(issue.Code+" "+issue.Path+": "+issue.Message))
	}
	return strings.Join(parts, "; ")
}

// Warning is one bounded normalizer warning.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Response is a schema-valid normalizer answer.
type Response struct {
	Manifest   content.Manifest   `json:"manifest"`
	Extensions content.Extensions `json:"extensions,omitempty"`
	Language   string             `json:"language,omitempty"`
	Warnings   []Warning          `json:"warnings,omitempty"`
}

// Client talks to one pinned plugin.
type Client struct {
	Pin *plugins.Pin
}

// CheckDiscovery compares GET /v0/discovery with the pinned manifest: digest,
// id, version, Plugin API version and Contributions.
func (c Client) CheckDiscovery(ctx context.Context) error {
	_, err := c.Discover(ctx)
	return err
}

// Discover is CheckDiscovery that also returns the Plugin API version the
// plugin serves.
func (c Client) Discover(ctx context.Context) (string, error) {
	served, issues, err := devhost.Discover(ctx, c.Pin.Endpoint, c.Pin.Report())
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if len(issues) > 0 {
		return "", fmt.Errorf("%w: discovery does not match the pinned manifest: %s", ErrUnavailable, describe(issues))
	}
	return served, nil
}

// Normalize posts one normalizer request and judges a 200 answer with
// plugins.CheckNormalizerOutput in the invocation context oc, the check the
// Contract Runner applies: at most plugins.MaxResponseBytes are read. ctx
// carries the invocation deadline.
func (c Client) Normalize(ctx context.Context, request []byte, oc plugins.OutputContext) (Response, error) {
	check := func(body []byte) []plugins.Issue { return plugins.CheckNormalizerOutput(ctx, body, oc) }
	result, err := devhost.InvokeNormalizerWith(ctx, c.Pin.Endpoint, request, plugins.MaxResponseBytes(oc.Manifest), check)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if result.Error != nil {
		return Response{}, &PluginError{Status: result.Status, Code: result.Error.Code, Message: result.Error.Message, Retryable: result.Error.Retryable}
	}
	if len(result.Issues) > 0 {
		if result.Status != 200 || result.Issues[0].Code == devhost.CodeInvalidErrorEnvelope {
			return Response{}, fmt.Errorf("%w: %s", ErrUnavailable, describe(result.Issues))
		}
		return Response{}, &InvalidOutput{Issues: result.Issues}
	}
	var response Response
	if err := json.Unmarshal(result.Body, &response); err != nil {
		return Response{}, &InvalidOutput{Issues: []plugins.Issue{{Code: plugins.CodeSchema, Message: err.Error()}}}
	}
	return response, nil
}
