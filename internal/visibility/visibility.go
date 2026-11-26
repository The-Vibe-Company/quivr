// Package visibility defines the opaque principals used by delegated reads.
package visibility

import (
	"context"
	"slices"
	"strings"
)

const (
	None           = "none"
	Enforce        = "enforce"
	MaxPrincipals  = 64
	MaxHeaderBytes = 8192
)

func ValidMode(mode string) bool {
	return mode == "" || mode == None || mode == Enforce
}

// Normalize returns a sorted copy of a bounded set of opaque, typed identifiers.
func Normalize(values []string) ([]string, bool) {
	if len(values) > MaxPrincipals {
		return nil, false
	}
	out := slices.Clone(values)
	slices.Sort(out)
	size := len(out)
	for i, value := range out {
		kind, id, found := strings.Cut(value, ":")
		size += len(value)
		if !found || !token(kind, 32) || !token(id, 128) || i > 0 && out[i-1] == value {
			return nil, false
		}
	}
	return out, size <= MaxHeaderBytes
}

func token(value string, limit int) bool {
	if len(value) == 0 || len(value) > limit {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

type callerKey struct{}

func WithPrincipals(ctx context.Context, principals []string) context.Context {
	return context.WithValue(ctx, callerKey{}, principals)
}

func Principals(ctx context.Context) ([]string, bool) {
	principals, enforced := ctx.Value(callerKey{}).([]string)
	return principals, enforced
}

const (
	Public     = "public"
	Restricted = "restricted"
)

type Grant struct {
	Visibility        string
	AllowedPrincipals []string
}

func (g Grant) Allows(principals []string) bool {
	return g.Visibility == Public || g.Visibility == Restricted &&
		slices.ContainsFunc(principals, func(p string) bool { return slices.Contains(g.AllowedPrincipals, p) })
}

// Sources is the caller's source partition for one Corpus. Record ACLs
// override the source policy in either direction when present.
type Sources struct {
	Revision       int64
	Allowed        []string
	Denied         []string
	RecordOverride bool
}

type Resolver interface {
	Sources(ctx context.Context, organization, corpusID string, principals []string) (Sources, error)
	// Current checks that policy and the source universe remained stable while
	// the index ranked candidates. It invalidates mismatched local snapshots.
	Current(ctx context.Context, organization string, revisions map[string]int64) (bool, error)
	// SourceVersions provides exact identities when a projection's namespace
	// index cannot represent a source partition.
	SourceVersions(ctx context.Context, organization, corpusID string, namespaces []string, limit int) ([]string, error)
}

type Store interface {
	Grant(ctx context.Context, organization, corpusID, namespace string) (Grant, error)
	SetGrant(ctx context.Context, organization, corpusID, namespace string, grant Grant) error
	DeleteGrant(ctx context.Context, organization, corpusID, namespace string) error
}
