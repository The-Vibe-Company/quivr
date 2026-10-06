package main

import "testing"

func TestMapMessageEmitsCommonMetadata(t *testing.T) {
	m := message{ID: "message-1", SentDateTime: "2026-09-28T10:00:00Z", ReceivedDateTime: "2026-09-28T10:01:00Z"}
	m.From = &address{}
	m.From.EmailAddress.Name = "Desk"
	m.From.EmailAddress.Address = "desk@example.org"
	m.Sender = &address{}
	m.Sender.EmailAddress.Name = "Delegate"
	m.Sender.EmailAddress.Address = "delegate@example.org"
	s := session{cfg: config{Mailbox: "reader@example.org", Folder: "archive"}}

	item := s.mapMessage(m, nil)
	metadata, ok := item.Extensions["quivr.metadata"]
	if !ok {
		t.Fatalf("common metadata missing: %+v", item.Extensions)
	}
	if metadata.SchemaVersion != "1" {
		t.Fatalf("schema version %q", metadata.SchemaVersion)
	}
	if got, want := metadata.Data["published_at"], "2026-09-28T10:00:00Z"; got != want {
		t.Fatalf("published_at %v, want %v", got, want)
	}
	if got, want := metadata.Data["source_type"], "mail"; got != want {
		t.Fatalf("source_type %v, want %v", got, want)
	}
	if got, want := metadata.Data["source"], "reader@example.org/archive"; got != want {
		t.Fatalf("source %v, want %v", got, want)
	}
	authors, ok := metadata.Data["author"].([]string)
	if !ok || len(authors) != 1 || authors[0] != "Desk" {
		t.Fatalf("author %v", metadata.Data["author"])
	}

	// A message sent on behalf of the From author keeps that author; Sender is
	// only the fallback when From is absent.
	m.From = nil
	fallback := s.mapMessage(m, nil).Extensions["quivr.metadata"].Data["author"]
	if got, ok := fallback.([]string); !ok || len(got) != 1 || got[0] != "Delegate" {
		t.Fatalf("sender fallback author %v", fallback)
	}
}
