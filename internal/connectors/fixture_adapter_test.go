package connectors

import (
	"context"
	"errors"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost/fakeplugin/scriptedsource"
)

// Fixture is a test-only adapter to the canonical fake plugin's script.
type Fixture struct{ scriptedsource.Source }

func (Fixture) Fetch(ctx context.Context, req FetchRequest) (Page, error) {
	page, err := (scriptedsource.Source{}).Fetch(ctx, scriptedsource.FetchRequest{Config: req.Config, Credential: req.Credential, Checkpoint: req.Checkpoint})
	if err != nil {
		var failure *scriptedsource.Error
		if errors.As(err, &failure) {
			return Page{}, &Error{Class: ErrorClass(failure.Class), Code: failure.Code}
		}
		return Page{}, err
	}
	result := Page{Checkpoint: page.Checkpoint}
	for _, item := range page.Items {
		result.Items = append(result.Items, Item{RecordKey: item.RecordKey, Revision: item.Revision, Content: item.Content, Withdraw: item.Withdraw})
	}
	return result, nil
}

func (c Fixture) Descriptor() Descriptor {
	return Descriptor{Kind: c.Source.Kind(), ConfigSchema: c.Source.ConfigSchema(), CredentialSchema: c.Source.CredentialSchema(), DefaultInterval: c.Source.DefaultInterval()}
}
