package acceptance

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// alertRegistration finds the registration of the alert-rule template at a
// version and endpoint.
func alertRegistration(t *testing.T, operator, id, version, endpoint string) map[string]any {
	t.Helper()
	for _, raw := range request(t, "GET", "/v0/admin/plugins", operator, nil, 200)["items"].([]any) {
		r := raw.(map[string]any)
		if r["plugin_id"] == id && r["version"] == version && r["endpoint"] == endpoint && r["state"] != "rejected" {
			return r
		}
	}
	t.Fatalf("no registration of %s@%s at %s", id, version, endpoint)
	return nil
}

// migrated moves the operator's Organization's Subscriptions off from and
// returns the ids it moved (or would move in a dry run), by new Version id.
func migrated(t *testing.T, key, id, from string, dryRun bool, wantTo string) map[string]string {
	t.Helper()
	out := map[string]string{}
	body := map[string]any{"plugin_id": id, "from_version": from, "dry_run": dryRun, "limit": 500}
	for {
		page := request(t, "POST", "/v0/admin/subscriptions/evaluator-migrations", key, body, 200)
		if page["to_version"] != wantTo || len(page["refused"].([]any)) != 0 {
			t.Fatalf("migration from %s: %v, want to %s with nothing refused", from, page, wantTo)
		}
		for _, raw := range page["moved"].([]any) {
			m := raw.(map[string]any)
			version, _ := m["version_id"].(string)
			out[m["subscription_id"].(string)] = version
		}
		next, ok := page["next_after"].(string)
		if !ok {
			return out
		}
		body["after"] = next
	}
}

// TestAlertPluginUpgrade activates a second version of the pinned alert-rule
// template (THE-805). A Subscription created on 0.1.0 keeps matching through
// 0.1.0 while new ones pin 0.2.0, and 0.1.0 reads as draining with the
// Subscriptions that use it. The operator migrates them to 0.2.0, after a dry
// run that moves nothing. Rolling back serves 0.1.0 again for new
// Subscriptions while the migrated ones stay on 0.2.0, until they are
// migrated back and 0.2.0 has nothing left.
func TestAlertPluginUpgrade(t *testing.T) {
	id, version := alertEvaluator(t)
	operator, backfiller, admin := os.Getenv("QUIVR_TEST_OPERATOR"), os.Getenv("QUIVR_TEST_BACKFILLER"), os.Getenv("QUIVR_TEST_ADMIN")
	endpoint, manifest, pinned := os.Getenv("QUIVR_TEST_ALERT_UPGRADE_ENDPOINT"), os.Getenv("QUIVR_TEST_ALERT_UPGRADE_MANIFEST"), os.Getenv("QUIVR_TEST_ALERT_PINNED_ENDPOINT")
	if endpoint == "" || manifest == "" || pinned == "" {
		t.Skip("scripts/subscription_plugin.py runs the next version of the alert-rule template")
	}
	next, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	run := monitoringRun()
	c := changeCorpus(t, "alert-upgrade-"+run)
	start := request(t, "GET", changesPath(c, "", 0), admin, nil, 200)["next_cursor"].(string)
	phrase := "upgrade-" + run
	expression := map[string]any{"kind": "substring", "text": phrase}
	_, before := pinnedSubscription(t, id, version, "upgrade-before-"+run, c, expression, nil, destinationA, 201)

	accepted := request(t, "POST", "/v0/admin/plugins", operator, map[string]any{"idempotency_key": "alert-upgrade-" + run, "endpoint": endpoint, "manifest": string(next),
		"fixtures": pluginFixtures(t, filepath.Dir(manifest))}, 202)
	registered := awaitCheck(t, operator, "/v0/admin/plugins/"+accepted["registration_id"].(string))
	if registered["state"] != "validated" || registered["version"] != "0.2.0" {
		t.Fatalf("the 0.2.0 build: %v, want validated", registered)
	}
	plan := routingRequest(t, "POST", "/v0/admin/plugins/"+registered["registration_id"].(string)+"/activate", operator, map[string]any{}, 200)
	if got := planRoles(plan)["subscription:"+id]; got != id+"@0.2.0" {
		t.Fatalf("after the activation the alert rule is served by %q, want 0.2.0 (plan %v)", got, plan)
	}
	old := alertRegistration(t, operator, id, version, pinned)
	if old["state"] != "draining" || old["subscriptions"].(float64) < 1 {
		t.Fatalf("%s@%s with Subscriptions pinning it: %v, want draining", id, version, old)
	}

	// New Subscriptions pin the served version only.
	pinnedQuery := before["current_version"].(map[string]any)
	if refused := request(t, "POST", "/v0/subscriptions", admin, map[string]any{"idempotency_key": "upgrade-refused-" + run, "name": "refused",
		"saved_query_id": pinnedQuery["saved_query_id"], "saved_query_version_id": pinnedQuery["saved_query_version_id"],
		"evaluator": map[string]any{"plugin_id": id, "version": version, "configuration": map[string]any{}}, "destination_id": destinationA}, 422); refused["code"] != "unsupported_evaluator" {
		t.Fatalf("a new Subscription on %s: %v", version, refused)
	}
	_, after := pinnedSubscription(t, id, "0.2.0", "upgrade-after-"+run, c, expression, nil, destinationA, 201)

	// Each Subscription is judged by the version it pinned.
	awaitReady(t, ingest(t, c, "upgrade-hit-"+run, "Le port annonce "+phrase+" pour demain"))
	seen, _ := awaitMatches(t, admin, c, start, 2)
	for sub, want := range map[string]string{before["subscription_id"].(string): version, after["subscription_id"].(string): "0.2.0"} {
		created := matchCreatedFor(seen, sub)
		if len(created) != 1 {
			t.Fatalf("Matches of %s: %v, want one", sub, created)
		}
		match := request(t, "GET", "/v0/matches/"+created[0]["monitoring"].(map[string]any)["match_id"].(string), admin, nil, 200)
		if got := match["evidence"].(map[string]any)["evaluator"].(map[string]any)["version"]; got != want {
			t.Fatalf("the Match of %s was decided by %v, want %s", sub, got, want)
		}
	}

	beforeID := before["subscription_id"].(string)
	if dry := migrated(t, backfiller, id, version, true, "0.2.0"); func() bool { v, listed := dry[beforeID]; return !listed || v != "" }() {
		t.Fatalf("dry run: %v, want %s listed without a new Version", dry, beforeID)
	}
	if read := request(t, "GET", "/v0/subscriptions/"+beforeID, admin, nil, 200); read["current_version"].(map[string]any)["evaluator"].(map[string]any)["version"] != version {
		t.Fatalf("a dry run moved %s: %v", beforeID, read)
	}
	moved := migrated(t, backfiller, id, version, false, "0.2.0")
	current := request(t, "GET", "/v0/subscriptions/"+beforeID, admin, nil, 200)["current_version"].(map[string]any)
	if moved[beforeID] == "" || current["version_id"] != moved[beforeID] || current["evaluator"].(map[string]any)["version"] != "0.2.0" {
		t.Fatalf("migration: moved %v, %s now %v", moved, beforeID, current)
	}

	// Rollback: 0.1.0 serves new Subscriptions again; the migrated ones stay
	// on 0.2.0, which drains until they are migrated back.
	back := routingRequest(t, "POST", "/v0/admin/plugins/plan/rollback", operator, map[string]any{"idempotency_key": "alert-upgrade-rollback-" + run}, 200)
	if got := planRoles(back)["subscription:"+id]; got != id+"@"+version {
		t.Fatalf("after the rollback the alert rule is served by %q, want %s", got, version)
	}
	if r := alertRegistration(t, operator, id, "0.2.0", endpoint); r["state"] != "draining" || r["subscriptions"].(float64) < 2 {
		t.Fatalf("0.2.0 after the rollback: %v, want draining with both Subscriptions", r)
	}
	if returned := migrated(t, backfiller, id, "0.2.0", false, version); returned[beforeID] == "" || returned[after["subscription_id"].(string)] == "" {
		t.Fatalf("migrating back: %v, want both Subscriptions moved", returned)
	}
	deadline := time.Now().Add(monitoringWait)
	for {
		r := alertRegistration(t, operator, id, "0.2.0", endpoint)
		if r["state"] == "inactive" && r["subscriptions"].(float64) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("0.2.0 once nothing pins it: %v, want inactive", r)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
