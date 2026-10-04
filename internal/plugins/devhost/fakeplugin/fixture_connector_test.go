package fakeplugin_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost/fakeplugin"
	"testing"
)

func TestFixtureStepsThroughItsScriptThenFallsSilent(t *testing.T) {
	f := fakeplugin.FixtureConnector{}
	config := json.RawMessage(`{"script":[{"items":[{"record_key":"a","text":"Alpha"}]},{"items":[{"record_key":"a","text":"Alpha corrected","revision":"r2"}]}]}`)
	var checkpoint json.RawMessage
	var seen []string
	for i := 0; i < 4; i++ {
		page, err := f.Fetch(context.Background(), connectors.FetchRequest{Config: config, Checkpoint: checkpoint})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			seen = append(seen, item.RecordKey+"="+item.Content.Text+"@"+item.Revision)
		}
		checkpoint = page.Checkpoint
	}
	want := []string{"a=Alpha@", "a=Alpha corrected@r2"}
	if len(seen) != len(want) || seen[0] != want[0] || seen[1] != want[1] {
		t.Fatalf("got %v want %v", seen, want)
	}
	// Resuming from a checkpoint continues after it, not from the start.
	page, err := f.Fetch(context.Background(), connectors.FetchRequest{Config: config, Checkpoint: json.RawMessage(`{"step":1}`)})
	if err != nil || len(page.Items) != 1 || page.Items[0].Revision != "r2" {
		t.Fatalf("resume: %v %+v", err, page)
	}
}

func TestFixtureRefusesARevokedCredentialAsAccessError(t *testing.T) {
	f := fakeplugin.FixtureConnector{}
	config := json.RawMessage(`{"requires_credential":true,"script":[{"items":[{"record_key":"a","text":"Alpha"}]}]}`)
	for name, credential := range map[string]json.RawMessage{
		"missing": nil,
		"revoked": json.RawMessage(`{"token":"fixture-revoked-test-token"}`),
	} {
		_, err := f.Fetch(context.Background(), connectors.FetchRequest{Config: config, Credential: credential})
		var typed *connectors.Error
		if !errors.As(err, &typed) || typed.Class != connectors.ClassAccess {
			t.Fatalf("%s: got %v", name, err)
		}
	}
	page, err := f.Fetch(context.Background(), connectors.FetchRequest{Config: config, Credential: json.RawMessage(`{"token":"fixture-test-token"}`)})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("valid credential: %v %+v", err, page)
	}
}
