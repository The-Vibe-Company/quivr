package retrieval

import "github.com/The-Vibe-Company/quivr-v2/internal/corpus"

func (s Service) ProfilesScoped(scope corpus.Scope) ([]Profile, error) {
	if err := scope.Require(corpus.ActionSearchProfiles); err != nil {
		return nil, err
	}
	return s.Profiles(), nil
}
