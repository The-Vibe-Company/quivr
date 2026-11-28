package weaviate

import (
	"context"
	"strings"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/visibility"
	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
	"golang.org/x/sync/errgroup"
)

const (
	recordACLProperty           = "recordAcl"
	recordACLRestrictedProperty = "recordAclRestricted"
	// Keep the rare Version-ID fallback within a bounded index request.
	maxVisibilityVersionIDs = 10000
	// Bound concurrent per-route visibility resolver lookups.
	maxVisibilityConcurrency = 4
)

var visibilityPropertyNames = []string{recordACLProperty, recordACLRestrictedProperty}

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
	var override filter
	if recordOverride && len(principals) > 0 {
		override = containsAny(recordACLProperty, principals)
	}
	if !sourceAllowed {
		return override, override.operator != 0
	}
	unrestricted := filter{field: recordACLRestrictedProperty, operator: pb.Filters_OPERATOR_NOT_EQUAL, jsonKey: "valueBoolean", value: true}
	visibleSource := and(source, unrestricted)
	if override.operator == 0 {
		return visibleSource, true
	}
	return or(visibleSource, override), true
}

type resolvedRouteVisibility struct {
	revision  int64
	predicate filter
	possible  bool
}

func (s *Store) resolveRouteVisibility(ctx context.Context, route retrieval.Route, org string, principals []string) (resolvedRouteVisibility, error) {
	sources, err := s.Visibility.Sources(ctx, org, route.CorpusID, principals)
	if err != nil {
		return resolvedRouteVisibility{}, err
	}
	predicate, possible, err := s.routeVisibilityFilter(ctx, org, route, sources, principals)
	if err != nil {
		return resolvedRouteVisibility{}, err
	}
	return resolvedRouteVisibility{
		revision:  sources.Revision,
		predicate: predicate,
		possible:  possible,
	}, nil
}

func (s *Store) resolveVisibility(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, principals []string) ([]retrieval.Route, map[string]filter, map[string]int64, error) {
	for _, route := range routes {
		if !className.MatchString(route.Generation.Collection) || !scope.Contains(route.CorpusID) {
			return nil, nil, nil, retrieval.ErrUnavailable
		}
	}
	resolved := make([]resolvedRouteVisibility, len(routes))
	if len(routes) == 1 {
		res, err := s.resolveRouteVisibility(ctx, routes[0], scope.Organization, principals)
		if err != nil {
			return nil, nil, nil, err
		}
		resolved[0] = res
	} else {
		group, gctx := errgroup.WithContext(ctx)
		group.SetLimit(maxVisibilityConcurrency)
		for i, route := range routes {
			if gctx.Err() != nil {
				break
			}
			goRecover(group, func() error {
				if err := gctx.Err(); err != nil {
					return err
				}
				res, err := s.resolveRouteVisibility(gctx, route, scope.Organization, principals)
				if err != nil {
					return err
				}
				resolved[i] = res
				return nil
			})
		}
		if err := group.Wait(); err != nil {
			return nil, nil, nil, err
		}
	}
	allowed := make([]retrieval.Route, 0, len(routes))
	filters := make(map[string]filter, len(routes))
	revisions := make(map[string]int64, len(routes))
	var ensuredCollections map[string]struct{}
	for i, route := range routes {
		res := resolved[i]
		revisions[route.CorpusID] = res.revision
		if !res.possible {
			continue
		}
		collection := route.Generation.Collection
		alreadyEnsured := false
		if ensuredCollections != nil {
			_, alreadyEnsured = ensuredCollections[collection]
		}
		if !alreadyEnsured {
			if err := s.ensurePropertiesLazy(ctx, collection, visibilityPropertyNames, visibilityProperties); err != nil {
				return nil, nil, nil, err
			}
			if len(routes) > 1 {
				if ensuredCollections == nil {
					ensuredCollections = make(map[string]struct{}, 2)
				}
				ensuredCollections[collection] = struct{}{}
			}
		}
		filters[route.CorpusID] = res.predicate
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
