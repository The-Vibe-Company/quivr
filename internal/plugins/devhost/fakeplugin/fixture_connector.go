package fakeplugin

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost/fakeplugin/scriptedsource"

	"github.com/The-Vibe-Company/quivr/internal/connectors"
)

// FixtureConnector is a deterministic connector for local and CI acceptance. Each run
// consumes one script step; the Acquisition Checkpoint is the next step index,
// and past the end the source is silent. A credential whose token starts with
// "fixture-revoked" (or a missing one when required) is refused as an access
// error, so tests can cut and restore access by replacing the credential.
type FixtureConnector struct{ scriptedsource.Source }

// FixtureConnector adapts the shared scripted test source to the engine port.
// Script mechanics are independent of the engine, so its same-package tests
// can use them without an import cycle.
func fixtureFetch(body []byte, checkCredential bool) (int, any) {
	var req struct {
		Connector struct {
			Config json.RawMessage `json:"config"`
		} `json:"connector"`
		Credential json.RawMessage `json:"credential"`
		Checkpoint json.RawMessage `json:"checkpoint"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return 400, map[string]any{"code": "invalid_request", "message": "invalid JSON", "retryable": false, "error_class": "source"}
	}
	if string(req.Credential) == "null" {
		req.Credential = nil
	}
	if string(req.Checkpoint) == "null" {
		req.Checkpoint = nil
	}
	page, err := (FixtureConnector{}).Fetch(context.Background(), connectors.FetchRequest{Config: req.Connector.Config, Credential: req.Credential, Checkpoint: req.Checkpoint})
	if err != nil {
		var failure *connectors.Error
		if errors.As(err, &failure) {
			return 403, map[string]any{"code": failure.Code, "message": "fixture source refused the request", "retryable": false, "error_class": failure.Class}
		}
		return 422, map[string]any{"code": "source_failed", "message": "fixture source failed", "retryable": false, "error_class": "source"}
	}
	if checkCredential {
		return 200, map[string]any{"status": "ok"}
	}
	items := []any{}
	for _, item := range page.Items {
		out := map[string]any{"record_key": item.RecordKey}
		if item.Revision != "" {
			out["revision"] = item.Revision
		}
		if item.Withdraw {
			out["withdraw"] = true
		} else {
			out["content"] = item.Content
		}
		items = append(items, out)
	}
	return 200, map[string]any{"items": items, "checkpoint": page.Checkpoint, "more": false}
}

func (FixtureConnector) Fetch(ctx context.Context, req connectors.FetchRequest) (connectors.Page, error) {
	page, err := (scriptedsource.Source{}).Fetch(ctx, scriptedsource.FetchRequest{Config: req.Config, Credential: req.Credential, Checkpoint: req.Checkpoint})
	if err != nil {
		var failure *scriptedsource.Error
		if errors.As(err, &failure) {
			return connectors.Page{}, &connectors.Error{Class: connectors.ErrorClass(failure.Class), Code: failure.Code}
		}
		return connectors.Page{}, err
	}
	result := connectors.Page{Checkpoint: page.Checkpoint}
	for _, item := range page.Items {
		result.Items = append(result.Items, connectors.Item{RecordKey: item.RecordKey, Revision: item.Revision, Content: item.Content, Withdraw: item.Withdraw})
	}
	return result, nil
}

func (c FixtureConnector) Descriptor() connectors.Descriptor {
	return connectors.Descriptor{Kind: c.Source.Kind(), ConfigSchema: c.Source.ConfigSchema(), CredentialSchema: c.Source.CredentialSchema(), DefaultInterval: c.Source.DefaultInterval()}
}
