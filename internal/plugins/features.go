package plugins

import "slices"

// Feature names a versioned addition to the Plugin API.
type Feature string

const (
	FeatureNormalizer          Feature = "normalizer"
	FeatureSubscription        Feature = "subscription"
	FeatureConnector           Feature = "connector"
	FeatureInstanceScope       Feature = "instance_scope"
	FeatureAttachments         Feature = "attachments"
	FeaturePush                Feature = "push"
	FeatureIngestion           Feature = "ingestion"
	FeatureRetrieval           Feature = "retrieval"
	FeatureSegmentsOnly        Feature = "segments_only"
	FeatureInputPrice          Feature = "input_price"
	FeatureSubscriptionVectors Feature = "subscription_vectors"
	FeatureConnectorAPI        Feature = "connector_api"
	FeatureInstanceToken       Feature = "instance_token"
)

// FeatureDefinition records the public history and manifest admission rules.
// Since is the only source of Plugin API versions, including SDK declarations
// and the generated reference sections (make generate).
type FeatureDefinition struct {
	Feature             Feature
	Since, Description  string
	Contribution, Field string
}

var featureTable = []FeatureDefinition{
	{FeatureNormalizer, "0.1.0", "Normalizer Contribution", "normalizer", ""},
	{FeatureSubscription, "0.2.0", "Subscription Contribution", "subscription", ""},
	{FeatureConnector, "0.3.0", "Connector Contribution", "connector", ""},
	{FeatureInstanceScope, "0.3.1", "Connector instance scope, declared checkpoint bound, underscores in identifiers", "", ""},
	{FeatureAttachments, "0.4.0", "Connector attachments", "", "/contributions/connector/attachments"},
	{FeaturePush, "0.5.0", "Connector push mode and webhook URL", "", ""},
	{FeatureIngestion, "0.6.0", "Ingestion Contribution", "ingestion", ""},
	{FeatureRetrieval, "0.7.0", "Retrieval Contribution", "retrieval", ""},
	{FeatureSegmentsOnly, "0.8.0", "Segment-only segment_and_embed requests", "", ""},
	{FeatureInputPrice, "0.9.0", "Vector space input_price", "", ""},
	{FeatureSubscriptionVectors, "0.10.0", "Subscription Part and query vectors", "", "/contributions/subscription/vectors"},
	{FeatureConnectorAPI, "0.11.0", "Connector API routes secured by a Quivr key", "", ""},
	{FeatureInstanceToken, "0.12.0", "Instance-scoped bearer tokens for connector API routes", "", ""},
}

// FeatureTable returns the introduction history, oldest first.
func FeatureTable() []FeatureDefinition { return slices.Clone(featureTable) }

// FeatureSince returns the version that introduced feature, or empty if unknown.
func FeatureSince(feature Feature) string {
	for _, row := range featureTable {
		if row.Feature == feature {
			return row.Since
		}
	}
	return ""
}

// ContributionFeature identifies the versioned feature for a contribution.
func ContributionFeature(name string) (Feature, bool) {
	for _, row := range featureTable {
		if row.Contribution == name {
			return row.Feature, true
		}
	}
	return "", false
}

// SupportedPluginAPIVersions are the versions the engine serves, oldest first.
var SupportedPluginAPIVersions = supportedAPIs()

func supportedAPIs() []string {
	var versions []string
	for _, row := range featureTable {
		if !slices.Contains(versions, row.Since) {
			versions = append(versions, row.Since)
		}
	}
	return versions
}

// PluginAPIVersion is the newest Plugin API this engine implements.
var PluginAPIVersion = SupportedPluginAPIVersions[len(SupportedPluginAPIVersions)-1]

// API is an immutable, resolved Plugin API version. Its features are computed
// once for each supported version; callers ask Speaks instead of comparing versions.
type API struct {
	Version  string
	features map[Feature]bool
}

func (a API) Speaks(feature Feature) bool { return a.features[feature] }

var resolvedAPIs = func() map[string]API {
	apis := make(map[string]API, len(SupportedPluginAPIVersions))
	for _, version := range SupportedPluginAPIVersions {
		v, err := ParseVersion(version)
		if err != nil {
			panic(err)
		}
		features := make(map[Feature]bool, len(featureTable))
		for _, row := range featureTable {
			since, err := ParseVersion(row.Since)
			if err != nil {
				panic(err)
			}
			features[row.Feature] = v.Compare(since) >= 0
		}
		apis[version] = API{Version: version, features: features}
	}
	return apis
}()

// ResolveAPI returns the features of a supported version. Unsupported or
// malformed versions speak no features; discovery validation refuses them.
func ResolveAPI(version string) API { return resolvedAPIs[version] }

// NegotiatePluginAPI returns the highest supported version the range admits.
func NegotiatePluginAPI(r Range) (string, bool) {
	for i := len(SupportedPluginAPIVersions) - 1; i >= 0; i-- {
		version := SupportedPluginAPIVersions[i]
		v, _ := ParseVersion(version)
		if r.Contains(v) {
			return version, true
		}
	}
	return "", false
}

func admitsFeature(r Range, feature Feature) (Version, bool) {
	minimum, _ := ParseVersion(FeatureSince(feature))
	version, ok := NegotiatePluginAPI(r)
	return minimum, ok && ResolveAPI(version).Speaks(feature)
}
