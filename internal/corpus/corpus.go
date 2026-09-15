// Package corpus owns scoped Corpus creation and reads.
package corpus

import (
	"context"
	"errors"
	"strings"
)

var (
	ErrForbidden          = errors.New("forbidden")
	ErrNotFound           = errors.New("not_found")
	ErrInvalidMapping     = errors.New("invalid_mapping")
	ErrUnsupportedProfile = errors.New("unsupported_profile")
)

type Corpus struct {
	ID        string         `json:"corpus_id"`
	Name      string         `json:"name"`
	Retrieval map[string]any `json:"effective_retrieval"`
}
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
}
type Store interface {
	Create(context.Context, string, CreateInput) (Corpus, bool, error)
	Read(context.Context, string, string) (Corpus, error)
	List(context.Context, Scope, string, int) ([]Corpus, error)
}
type Service struct{ Store Store }

func (s Service) Create(ctx context.Context, scope Scope, input CreateInput) (Corpus, bool, error) {
	if !scope.Allows("corpora:write") || !scope.AllCorpora() {
		return Corpus{}, false, ErrForbidden
	}
	if input.Retrieval == nil {
		input.Retrieval = map[string]any{}
	}
	if _, ok := input.Retrieval["plugin_profile"]; ok {
		return Corpus{}, false, ErrUnsupportedProfile
	}
	names := map[string]bool{}
	if fields, ok := input.Retrieval["fields"].([]any); ok {
		for _, v := range fields {
			f, ok := v.(map[string]any)
			if !ok {
				return Corpus{}, false, ErrInvalidMapping
			}
			name, _ := f["name"].(string)
			pointer, _ := f["source_pointer"].(string)
			if name == "" || names[name] || !validPointer(pointer) {
				return Corpus{}, false, ErrInvalidMapping
			}
			names[name] = true
			roles, ok := f["roles"].([]any)
			if !ok {
				return Corpus{}, false, ErrInvalidMapping
			}
			for _, role := range roles {
				if role == "search" && f["type"] != "string" && f["type"] != "string_array" {
					return Corpus{}, false, ErrInvalidMapping
				}
			}
		}
	}
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
