package postgres

import (
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

// Per-format plan changes must preserve independently pinned corpus owners.
func TestRoutingPlanDeltaPreservesUnchangedFormats(t *testing.T) {
	current := content.IngestionRouting{Default: "historical", Routes: map[string]string{"application/pdf": "historical-pdf"}}
	previous := content.IngestionRouting{Default: "current", Routes: map[string]string{"text/plain": "text-owner", "application/pdf": "pdf-owner"}}
	next := content.IngestionRouting{Default: "replacement", Routes: map[string]string{"text/plain": "text-owner", "application/pdf": "new-pdf"}}
	got := changedIngestionRouting(current, previous, next)
	for media, want := range map[string]string{"text/plain": "historical", "application/pdf": "new-pdf", "image/png": "replacement"} {
		if owner := got.For(media); owner != want {
			t.Errorf("%s owner = %q, want %q", media, owner, want)
		}
	}
	// Removing an explicit route changes that format to the new default.
	removed := changedIngestionRouting(current, previous, content.IngestionRouting{Default: "current"})
	if got := removed.For("application/pdf"); got != "current" {
		t.Fatalf("removed PDF route = %q, want current", got)
	}
	if current.For("application/pdf") != "historical-pdf" {
		t.Fatal("plan delta mutated the current route")
	}
}
