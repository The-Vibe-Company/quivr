package acceptance

import (
	"slices"
	"testing"
)

// The assembled stack publishes the connector kinds enabled by its startup pins.
func TestConnectorConfigurationSurface(t *testing.T) {
	catalog := request(t, "GET", "/v0/connector-kinds", connectorToken(t), nil, 200)
	var kinds []string
	for _, raw := range catalog["items"].([]any) {
		kinds = append(kinds, raw.(map[string]any)["kind"].(string))
	}
	slices.Sort(kinds)
	if !slices.Equal(kinds, []string{"fixture", "m365_mail", "rss", "x_list"}) {
		t.Fatalf("enabled connector kinds: %v", kinds)
	}
}
