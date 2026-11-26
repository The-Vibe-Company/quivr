package weaviate

import (
	"context"
	"strings"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/visibility"
	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
)

const (
	recordACLProperty           = "recordAcl"
	recordACLRestrictedProperty = "recordAclRestricted"
	// Keep the rare Version-ID fallback within a bounded index request.
	maxVisibilityVersionIDs = 10000
)

func visibilityProperties() []map[string]any {
	acl := filterable(recordACLProperty)
	acl["dataType"] = []string{"text[]"}
	return []map[string]any{
		acl,
		{"name": recordACLRestrictedProperty, "dataType": []string{"boolean"}, "indexFilterable": true},
	}
}

func addRecordACL(properties map[string]any, acl []string) {
	if len(acl) > 0 {
		properties[recordACLProperty] = acl
		properties[recordACLRestrictedProperty] = true
	}
}

type routeVisibilityKey struct{}

func routeVisibility(ctx context.Context, corpusID string) filter {
	filters, _ := ctx.Value(routeVisibilityKey{}).(map[string]filter)
	return filters[corpusID]
}

// compileVisibility returns false when neither the source policy nor a
// record override can authorize a candidate. The inverse equality bitmap
// includes older objects with no record ACL marker.
func compileVisibility(sources visibility.Sources, principals []string) (filter, bool) {
	if len(sources.Allowed) == 0 {
		return compileRecordVisibility(filter{}, false, sources.RecordOverride, principals)
	}
	var source filter
	switch {
	case len(sources.Denied) == 0:
	case len(sources.Allowed) > len(sources.Denied):
		exclude := make([]filter, 0, len(sources.Denied))
		for _, ns := range sources.Denied {
			exclude = append(exclude, notEqual(sourceNamespaceProperty, ns))
		}
		source = and(exclude...)
	default:
		source = containsAny(sourceNamespaceProperty, sources.Allowed)
	}
	return compileRecordVisibility(source, true, sources.RecordOverride, principals)
}

func compileRecordVisibility(source filter, sourceAllowed, recordOverride bool, principals []string) (filter, bool) {
	override := containsAny(recordACLProperty, principals)
	if !sourceAllowed {
		return override, recordOverride
	}
	unrestricted := filter{field: recordACLRestrictedProperty, operator: pb.Filters_OPERATOR_NOT_EQUAL, jsonKey: "valueBoolean", value: true}
	return or(and(source, unrestricted), override), true
}

func (s *Store) resolveVisibility(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, principals []string) ([]retrieval.Route, map[string]filter, map[string]int64, error) {
	allowed := make([]retrieval.Route, 0, len(routes))
	filters := make(map[string]filter, len(routes))
	revisions := make(map[string]int64, len(routes))
	for _, route := range routes {
		if !className.MatchString(route.Generation.Collection) || !scope.Contains(route.CorpusID) {
			return nil, nil, nil, retrieval.ErrUnavailable
		}
		sources, err := s.Visibility.Sources(ctx, scope.Organization, route.CorpusID, principals)
		if err != nil {
			return nil, nil, nil, err
		}
		revisions[route.CorpusID] = sources.Revision
		predicate, possible, err := s.routeVisibilityFilter(ctx, scope.Organization, route, sources, principals)
		if err != nil {
			return nil, nil, nil, err
		}
		if !possible {
			continue
		}
		if err := s.ensureProperties(ctx, route.Generation.Collection, visibilityProperties()); err != nil {
			return nil, nil, nil, err
		}
		filters[route.CorpusID] = predicate
		allowed = append(allowed, route)
	}
	return allowed, filters, revisions, nil
}

// With a mixed partition, a namespace whose indexed spelling is ambiguous
// uses its canonical Version IDs. Other sources still use one inclusion term
// list. Legacy generations without the namespace property use only Version IDs.
func (s *Store) routeVisibilityFilter(ctx context.Context, org string, route retrieval.Route, sources visibility.Sources, principals []string) (filter, bool, error) {
	if len(sources.Allowed) == 0 || len(sources.Denied) == 0 {
		f, ok := compileVisibility(sources, principals)
		return f, ok, nil
	}
	aliases := make(map[string]bool, len(sources.Denied))
	for _, ns := range sources.Denied {
		aliases[strings.TrimSpace(ns)] = true
	}
	var inclusion, exact []string
	for _, ns := range sources.Allowed {
		key := strings.TrimSpace(ns)
		if !route.Generation.SourceNamespaceProjected || key == "" || aliases[key] {
			exact = append(exact, ns)
		} else {
			inclusion = append(inclusion, ns)
		}
	}
	if len(exact) == 0 && (!aliases[""] || len(sources.Allowed) <= len(sources.Denied)) {
		f, ok := compileVisibility(sources, principals)
		return f, ok, nil
	}
	var operands []filter
	if len(inclusion) > 0 {
		operands = append(operands, containsAny(sourceNamespaceProperty, inclusion))
	}
	if len(exact) > 0 {
		versions, err := s.Visibility.SourceVersions(ctx, org, route.CorpusID, exact, maxVisibilityVersionIDs+1)
		if err != nil {
			return filter{}, false, err
		}
		if len(versions) > maxVisibilityVersionIDs {
			return filter{}, false, retrieval.ErrSourceFilterUnavailable
		}
		if len(versions) > 0 {
			operands = append(operands, containsAny("versionId", versions))
		}
	}
	source := filter{}
	if len(operands) > 0 {
		source = or(operands...)
	}
	f, ok := compileRecordVisibility(source, len(operands) > 0, sources.RecordOverride, principals)
	return f, ok, nil
}
