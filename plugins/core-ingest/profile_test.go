package main

import (
	_ "embed"
	"testing"

	"github.com/The-Vibe-Company/quivr/plugins/core-ingest/plugin"
)

//go:embed profile.json
var profileJSON string

func TestEmbeddedProfileMatchesCanonicalFile(t *testing.T) {
	if plugin.ProfileJSON != profileJSON {
		t.Fatalf("plugins/core-ingest/plugin.ProfileJSON drifted from plugins/core-ingest/profile.json")
	}
}
