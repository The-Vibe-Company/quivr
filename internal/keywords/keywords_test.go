package keywords_test

import (
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/keywords"
)

func TestAnalyzers(t *testing.T) {
	for _, tc := range []struct{ analyzer, in, want string }{
		{"french_light", "Les élections françaises", "election francais"},
		{"french_light", "chevaux cheval", "cheval cheval"},
		{"french_light", "actrices acteurs", "acteu acteu"},
		{"french_light", "LE port et la mer", "port mer"},
		{"french_light", "cœur coeur ŒUVRES oeuvres", "coeu coeu oeuvr oeuvr"},
		{"french_light", "organisation", "organ"},
		// Folded is language-neutral: no stopwords and no stemming.
		{"folded", "Les élections françaises", "les elections francaises"},
		{"folded", "Café, CRÈME-brûlée; cœur", "cafe creme brulee coeur"},
		{"folded", "The organisation of organs", "the organisation of organs"},
	} {
		a, ok := keywords.Lookup(tc.analyzer)
		if !ok {
			t.Fatalf("analyzer %q is not registered", tc.analyzer)
		}
		if got := a.Analyze(tc.in); got != tc.want {
			t.Errorf("%s %q: %q, want %q", tc.analyzer, tc.in, got, tc.want)
		}
	}
	// An empty name means the field has no analyzer and no keyword copy.
	for _, name := range []string{"", "unknown"} {
		if _, ok := keywords.Lookup(name); ok {
			t.Fatalf("analyzer %q registered", name)
		}
	}
}
