package plugins_test

import (
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

// The public Plugin API history is the independent contract for this table.
// Wrong introduction versions can send new fields to strict older plugins.
func TestPluginAPIFeatureIntroductions(t *testing.T) {
	for _, tc := range []struct {
		feature       plugins.Feature
		before, since string
	}{
		{plugins.FeatureNormalizer, "", "0.1.0"},
		{plugins.FeatureSubscription, "0.1.0", "0.2.0"},
		{plugins.FeatureConnector, "0.2.0", "0.3.0"},
		{plugins.FeatureInstanceScope, "0.3.0", "0.3.1"},
		{plugins.FeatureAttachments, "0.3.1", "0.4.0"},
		{plugins.FeaturePush, "0.4.0", "0.5.0"},
		{plugins.FeatureIngestion, "0.5.0", "0.6.0"},
		{plugins.FeatureRetrieval, "0.6.0", "0.7.0"},
		{plugins.FeatureSegmentsOnly, "0.7.0", "0.8.0"},
		{plugins.FeatureInputPrice, "0.8.0", "0.9.0"},
		{plugins.FeatureSubscriptionVectors, "0.9.0", "0.10.0"},
		{plugins.FeatureConnectorAPI, "0.10.0", "0.11.0"},
		{plugins.FeatureConnectorSignature, "0.11.0", "0.12.0"},
		{plugins.FeatureInstanceToken, "0.11.0", "0.12.0"},
	} {
		t.Run(string(tc.feature), func(t *testing.T) {
			if got := plugins.FeatureSince(tc.feature); got != tc.since {
				t.Fatalf("introduced in %q, want %q", got, tc.since)
			}
			for _, version := range []string{tc.before, "invalid", "999.0.0"} {
				if plugins.ResolveAPI(version).Speaks(tc.feature) {
					t.Fatalf("API %q speaks feature before introduction or without support", version)
				}
			}
			pin := &plugins.Pin{Manifest: plugins.Manifest{Compatibility: plugins.Compatibility{PluginAPI: ">=" + tc.since + " <1.0.0"}}}
			if !plugins.ResolveAPI(tc.since).Speaks(tc.feature) || !pin.Speaks(tc.feature) {
				t.Fatalf("feature missing at introduction or from compatible pin")
			}
			if tc.before != "" {
				old := &plugins.Pin{Manifest: plugins.Manifest{Compatibility: plugins.Compatibility{PluginAPI: "=" + tc.before}}}
				if old.Speaks(tc.feature) {
					t.Fatalf("pin speaking %s enables newer feature", tc.before)
				}
			}
		})
	}
}
