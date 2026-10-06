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
	var missing []string
	for _, kind := range []string{"fixture", "m365_mail", "rss", "x_list"} {
		if !slices.Contains(kinds, kind) {
			missing = append(missing, kind)
		}
	}
	if len(missing) != 0 {
		t.Fatalf("missing startup connector kinds %v; catalog: %v", missing, kinds)
	}
}
