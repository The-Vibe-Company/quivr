// Package corpus owns scoped Corpus creation and reads.
package corpus

import (
	"context"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
)

var (
	ErrForbidden          = publicerr.Forbidden
	ErrNotFound           = publicerr.NotFound
	ErrInvalidMapping     = publicerr.InvalidMapping
	ErrUnsupportedProfile = publicerr.UnsupportedProfile
)

type Corpus struct {
	ID        string         `json:"corpus_id"`
	Name      string         `json:"name"`
	Retrieval map[string]any `json:"effective_retrieval"`
}

// ActionConnectorPush authorizes ingress on connector instances in the scope.
const ActionConnectorPush = "connector:push"

type Scope struct {
	Organization string   `json:"organization"`
	Actions      []string `json:"actions"`
	Corpora      []string `json:"corpora"`
}

func (s Scope) Allows(action string) bool {
	for _, v := range s.Actions {
		if v == action {
			return true
		}
	}
	return false
}
func (s Scope) Contains(id string) bool {
	for _, v := range s.Corpora {
		if v == "*" || v == id {
			return true
		}
	}
	return false
}
func (s Scope) AllCorpora() bool {
	for _, v := range s.Corpora {
		if v == "*" {
			return true
		}
	}
	return false
}

type CreateInput struct {
	Key       string         `json:"idempotency_key"`
	Name      string         `json:"name"`
	Retrieval map[string]any `json:"retrieval"`
	// Resolved is the validated effective configuration; the canonical request
	// remains the requested Retrieval so replay identity is unchanged.
	Resolved Retrieval `json:"-"`
}
type Store interface {
	Create(context.Context, string, CreateInput) (Corpus, bool, error)
	Read(context.Context, string, string) (Corpus, error)
	List(context.Context, Scope, string, int) ([]Corpus, error)
}
type Service struct {
	Store Store
	// Namespaces reports declared extension namespaces that mappings may
	// address; nil declares none.
	Namespaces func(string) bool
}

// Resolve validates a requested retrieval configuration and resolves its profile.
func (s Service) Resolve(raw map[string]any) (Retrieval, error) {
	return ResolveRetrieval(raw, s.Namespaces)
}

func (s Service) Create(ctx context.Context, scope Scope, input CreateInput) (Corpus, bool, error) {
	if !scope.Allows("corpora:write") || !scope.AllCorpora() {
		return Corpus{}, false, ErrForbidden
	}
	if input.Retrieval == nil {
		input.Retrieval = map[string]any{}
	}
	resolved, err := s.Resolve(input.Retrieval)
	if err != nil {
		return Corpus{}, false, err
	}
	input.Resolved = resolved
	return s.Store.Create(ctx, scope.Organization, input)
}
func validPointer(p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] == '~' {
			i++
			if i == len(p) || (p[i] != '0' && p[i] != '1') {
				return false
			}
		}
	}
	return true
}
func (s Service) Read(ctx context.Context, scope Scope, id string) (Corpus, error) {
	if !scope.Allows("corpora:read") {
		return Corpus{}, ErrForbidden
	}
	if !scope.Contains(id) {
		return Corpus{}, ErrNotFound
	}
	return s.Store.Read(ctx, scope.Organization, id)
}
func (s Service) List(ctx context.Context, scope Scope, after string, limit int) ([]Corpus, error) {
	if !scope.Allows("corpora:read") {
		return nil, ErrForbidden
	}
	return s.Store.List(ctx, scope, after, limit)
}
