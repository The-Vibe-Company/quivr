package main

import "testing"

func TestMapPostEmitsCommonMetadata(t *testing.T) {
	item := MapPost(Post{
		ID:        "7300000000000000001",
		Text:      "A post",
		CreatedAt: "2026-09-28T10:00:00Z",
		AuthorID:  "42",
		Lang:      "fr",
		Entities:  map[string]any{"hashtags": []any{map[string]any{"tag": "news"}, map[string]any{"tag": "science"}}},
	}, Includes{Users: []User{{ID: "42", Username: "newsdesk", Name: "News Desk"}}}, Scope{CorpusID: "corpus", Namespace: "x"})

	metadata, ok := item.Extensions["quivr.metadata"]
	if !ok {
		t.Fatalf("common metadata missing: %+v", item.Extensions)
	}
	if got, want := metadata.Data["language"], "fr"; got != want {
		t.Fatalf("language %v, want %v", got, want)
	}
	if got, want := metadata.Data["published_at"], "2026-09-28T10:00:00Z"; got != want {
		t.Fatalf("published_at %v, want %v", got, want)
	}
	if got, want := metadata.Data["source_type"], "social"; got != want {
		t.Fatalf("source_type %v, want %v", got, want)
	}
	if got, want := metadata.Data["source"], "https://x.com/newsdesk/status/7300000000000000001"; got != want {
		t.Fatalf("source %v, want %v", got, want)
	}
	authors, ok := metadata.Data["author"].([]string)
	if !ok || len(authors) != 1 || authors[0] != "newsdesk" {
		t.Fatalf("author %v", metadata.Data["author"])
	}
	tags, ok := metadata.Data["tags"].([]string)
	if !ok || len(tags) != 2 || tags[0] != "news" || tags[1] != "science" {
		t.Fatalf("tags %v", metadata.Data["tags"])
	}
}
