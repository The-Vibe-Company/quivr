package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestFixtureStepsThroughItsScriptThenFallsSilent(t *testing.T) {
	f := Fixture{}
	config := json.RawMessage(`{"script":[{"items":[{"record_key":"a","text":"Alpha"}]},{"items":[{"record_key":"a","text":"Alpha corrected","revision":"r2"}]}]}`)
	var checkpoint json.RawMessage
	var seen []string
	for i := 0; i < 4; i++ {
		page, err := f.Fetch(context.Background(), FetchRequest{Config: config, Checkpoint: checkpoint})
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
	page, err := f.Fetch(context.Background(), FetchRequest{Config: config, Checkpoint: json.RawMessage(`{"step":1}`)})
	if err != nil || len(page.Items) != 1 || page.Items[0].Revision != "r2" {
		t.Fatalf("resume: %v %+v", err, page)
	}
}

func TestFixtureRefusesARevokedCredentialAsAccessError(t *testing.T) {
	f := Fixture{}
	config := json.RawMessage(`{"requires_credential":true,"script":[{"items":[{"record_key":"a","text":"Alpha"}]}]}`)
	for name, credential := range map[string]json.RawMessage{
		"missing": nil,
		"revoked": json.RawMessage(`{"token":"fixture-revoked-test-token"}`),
	} {
		_, err := f.Fetch(context.Background(), FetchRequest{Config: config, Credential: credential})
		var typed *Error
		if !errors.As(err, &typed) || typed.Class != ClassAccess {
			t.Fatalf("%s: got %v", name, err)
		}
	}
	page, err := f.Fetch(context.Background(), FetchRequest{Config: config, Credential: json.RawMessage(`{"token":"fixture-test-token"}`)})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("valid credential: %v %+v", err, page)
	}
}

func TestRegistryValidatesKindSchemas(t *testing.T) {
	r, err := NewRegistry(Fixture{})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.validate("fixture", json.RawMessage(`{"script":[]}`), json.RawMessage(`{"token":"fixture-test-token"}`), "/credential/secret"); err != nil {
		t.Fatal(err)
	}
	if err = r.validate("fixture", json.RawMessage(`{"script":"nope"}`), nil, "/credential/secret"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("config: %v", err)
	}
	if err = r.validate("fixture", json.RawMessage(`{"script":[]}`), json.RawMessage(`{"password":1}`), "/credential/secret"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("credential: %v", err)
	}
	if err = r.validate("rss", json.RawMessage(`{}`), nil, "/credential/secret"); !errors.Is(err, ErrUnsupportedKind) {
		t.Fatalf("kind: %v", err)
	}
}
