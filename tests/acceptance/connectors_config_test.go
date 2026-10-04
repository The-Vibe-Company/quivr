package acceptance

import (
	"testing"
	"time"
)

// TestConnectorConfigurationSurface covers what a configuration client reads
// and changes: the kind catalog, field-located validation errors and the
// polling interval with its change event.
func TestConnectorConfigurationSurface(t *testing.T) {
	token := connectorToken(t)
	catalog := request(t, "GET", "/v0/connector-kinds", token, nil, 200)
	if catalog["credential_deposits"] != "available" || catalog["min_interval_seconds"].(float64) != 1 {
		t.Fatalf("catalog %v", catalog)
	}
	kinds := map[string]map[string]any{}
	for _, raw := range catalog["items"].([]any) {
		k := raw.(map[string]any)
		kinds[k["kind"].(string)] = k
	}
	for kind, credential := range map[string]string{"fixture": "optional", "rss": "optional", "m365_mail": "required", "x_list": "required"} {
		if k := kinds[kind]; k == nil || k["credential"] != credential || k["config_schema"] == nil || k["title"] == "" {
			t.Errorf("%s: %v", kind, k)
		}
	}

	corpusID, cursor := connectorCorpus(t, token, "configuration")
	invalid := fixtureConnector("config-invalid", corpusID, "config-wire", nil, map[string]any{"config": map[string]any{"script": "not a list"}})
	if e := request(t, "POST", "/v0/connectors", token, invalid, 422); e["code"] != "invalid_config" || e["field"] != "/config/script" {
		t.Fatalf("invalid config: %v", e)
	}
	created := request(t, "POST", "/v0/connectors", token, fixtureConnector("config-schedule", corpusID, "config-wire", []any{}, map[string]any{"schedule": map[string]any{"interval_seconds": 3600}}), 201)
	noSecret(t, created)
	id := created["connector_id"].(string)
	path := "/v0/connectors/" + id + "/schedule"
	changed := request(t, "PUT", path, token, map[string]any{"interval_seconds": 2}, 200)
	if changed["schedule"].(map[string]any)["interval_seconds"].(float64) != 2 {
		t.Fatalf("schedule %v", changed)
	}
	request(t, "PUT", path, token, map[string]any{"interval_seconds": 2}, 200)
	if e := request(t, "PUT", path, token, map[string]any{"interval_seconds": 0}, 422); e["field"] != "/interval_seconds" {
		t.Fatalf("invalid interval: %v", e)
	}
	events := 0
	for deadline := time.Now().Add(20 * time.Second); events == 0; time.Sleep(200 * time.Millisecond) {
		page := request(t, "GET", changesPath(corpusID, cursor, 100), token, nil, 200)
		for _, raw := range page["items"].([]any) {
			e := raw.(map[string]any)
			if e["type"] == "connector.schedule_changed" && e["resource"].(map[string]any)["id"] == id {
				events++
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("connector.schedule_changed never committed")
		}
	}
	if events != 1 {
		t.Fatalf("repeating the same interval committed %d events", events)
	}
	request(t, "POST", "/v0/connectors/"+id+"/disable", token, map[string]any{"idempotency_key": "config-disable-" + connectorRun}, 200)
	if e := request(t, "PUT", path, token, map[string]any{"interval_seconds": 5}, 409); e["code"] != "connector_disabled" {
		t.Fatalf("disabled: %v", e)
	}
}
