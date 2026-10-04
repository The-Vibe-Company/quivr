// Package pluginhttp is the engine's Plugin Protocol v0 client: it checks a
// pinned plugin's discovery document and invokes its normalizer over
// JSON-over-HTTP. It reuses the protocol helpers of the local plugin host
// (package devhost), so the engine, `quivr plugin dev` and the Contract
// Runner judge a plugin with the same code. It never imports plugin code.
package pluginhttp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/call"
)

// Protocol errors and normalizer data belong to the plugin port.
var ErrUnavailable = plugins.ErrUnavailable

type PluginError = plugins.PluginError
type InvalidOutput = plugins.InvalidOutput
type Warning = plugins.NormalizerWarning
type Response = plugins.NormalizerResponse

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
	return call.Discover(ctx, c.Pin)
}

// Normalize posts one normalizer request and judges a 200 answer with
// plugins.CheckNormalizerOutput in the invocation context oc, the check the
// Contract Runner applies: at most plugins.MaxResponseBytes are read. ctx
// carries the invocation deadline.
func (c Client) Normalize(ctx context.Context, request []byte, oc plugins.OutputContext) (Response, error) {
	started := time.Now()
	result, err := call.Invoke(ctx, c.Pin, call.Normalize, call.Bytes(request), func(ctx context.Context, body []byte) []plugins.Issue {
		return plugins.CheckNormalizerOutput(ctx, body, oc)
	}, nil)
	observe(c.Pin, requestOrganization(request), OpNormalize, started, result, err)
	if err != nil {
		return Response{}, err
	}
	var response Response
	if err := json.Unmarshal(result.Body, &response); err != nil {
		return Response{}, &InvalidOutput{Issues: []plugins.Issue{{Code: plugins.CodeSchema, Message: err.Error()}}}
	}
	return response, nil
}

// requestOrganization reads the organization_id of a request body built by
// the caller; empty when it has none.
func requestOrganization(request []byte) string {
	var r struct {
		OrganizationID string `json:"organization_id"`
	}
	_ = json.Unmarshal(request, &r)
	return r.OrganizationID
}

// Normalizer implements the normalization invocation port for resolved pins.
type Normalizer struct{}

func (Normalizer) CheckDiscovery(ctx context.Context, pin *plugins.Pin) error {
	return (Client{Pin: pin}).CheckDiscovery(ctx)
}
func (Normalizer) Normalize(ctx context.Context, pin *plugins.Pin, request []byte, oc plugins.OutputContext) (plugins.NormalizerResponse, error) {
	return (Client{Pin: pin}).Normalize(ctx, request, oc)
}
