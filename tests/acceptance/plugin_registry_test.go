package acceptance

import (
	"os"
	"testing"
)

// TestPluginRegistry reads the plugin registry the stack seeded from its
// startup pins: every pinned plugin is an active registration, the active
// plan maps each role to one of them (the first-party RSS connector serves
// connector:rss), and an Organization key with every other action is refused.
func TestPluginRegistry(t *testing.T) {
	operator := os.Getenv("QUIVR_TEST_OPERATOR")
	if os.Getenv("QUIVR_TEST_URL") == "" || operator == "" {
		t.Skip("make verify seeds the registry and sets the operator key")
	}
	for _, path := range []string{"/v0/admin/plugins", "/v0/admin/plugins/plan"} {
		request(t, "GET", path, os.Getenv("QUIVR_TEST_ADMIN"), nil, 403)
	}

	list := request(t, "GET", "/v0/admin/plugins", operator, nil, 200)
	registrations := map[string]map[string]any{}
	rss := ""
	for _, raw := range list["items"].([]any) {
		item := raw.(map[string]any)
		if item["state"] != "active" {
			t.Fatalf("seeded registration %v is not active", item)
		}
		registrations[item["registration_id"].(string)] = item
		if item["plugin_id"] == "connector.rss" {
			rss = item["registration_id"].(string)
		}
	}
	if rss == "" {
		t.Fatalf("the pinned connector.rss plugin is not registered: %v", list)
	}

	plan := request(t, "GET", "/v0/admin/plugins/plan", operator, nil, 200)
	roles := map[string]string{}
	for _, raw := range plan["roles"].([]any) {
		role := raw.(map[string]any)
		id := role["registration_id"].(string)
		if registrations[id] == nil {
			t.Fatalf("plan role %v names a registration the list does not have", role)
		}
		roles[role["role"].(string)] = id
	}
	if roles["connector:rss"] != rss {
		t.Fatalf("connector:rss is served by %q, want the connector.rss registration %q (plan %v)", roles["connector:rss"], rss, plan)
	}
}
