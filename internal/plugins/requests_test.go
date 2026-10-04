package plugins_test

import (
	"encoding/json"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

func TestStableKeysUseDocumentedIdentityBytes(t *testing.T) {
	if got, want := plugins.NormalizerKey("generation", "normalizer", "org", "version", "input"), "nk_8590da965b56853421e18da05a55894f25aec3be697cfab5bd064cea654c981b"; got != want {
		t.Fatalf("normalizer key %q, want %q", got, want)
	}
	if got, want := plugins.IngestionKey("generation", "org", "version", []string{"z", "a"}), "ingestion_2cc460cd25884d0b0c45c20882e1b8b7829a87798d8824ebe22262cff4be6bc6"; got != want {
		t.Fatalf("ingestion key %q, want %q", got, want)
	}
	evaluation := plugins.SubscriptionEvaluation{ID: "e1", Expression: json.RawMessage(`{"kind":"substring","text":"x"}`), Configuration: json.RawMessage(`{}`)}
	got := plugins.SubscriptionKey(plugins.SubscriptionKeyInput{Generation: "generation", OrganizationID: "org", VersionID: "version", Enriched: true, Evaluations: []plugins.SubscriptionEvaluation{evaluation}})
	want := "sk_a54a935238fb1086d7efc67ad635800c460504b7a1a1d222145cd4fbf8e8edb1"
	if got != want {
		t.Fatalf("subscription key %q, want %q", got, want)
	}
	large := plugins.SubscriptionEvaluation{ID: "e1", Expression: json.RawMessage(`{"count":9007199254740993}`), Configuration: json.RawMessage(`{}`)}
	got = plugins.SubscriptionKey(plugins.SubscriptionKeyInput{Generation: "generation", OrganizationID: "org", VersionID: "version", Evaluations: []plugins.SubscriptionEvaluation{large}})
	want = "sk_cd6ab931ec1adfe9fb28bc988e3b2b89ac296ece7b5f0193d7b1fffbc4ccf560"
	if got != want {
		t.Fatalf("subscription key lost large integer precision: %q, want %q", got, want)
	}
}
