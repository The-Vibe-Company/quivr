package acceptance

import (
	"net/url"
	"os"
	"testing"
)

// subscriptionsOf lists the active Subscriptions of owner (or "none"),
// following every page, and returns them by ID.
func subscriptionsOf(t *testing.T, token, owner string) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	q := url.Values{"owner": {owner}, "limit": {"1"}}
	for {
		page := request(t, "GET", "/v0/subscriptions?"+q.Encode(), token, nil, 200)
		for _, item := range page["items"].([]any) {
			s := item.(map[string]any)
			out[s["subscription_id"].(string)] = s
		}
		next, ok := page["next_page_cursor"].(string)
		if !ok {
			return out
		}
		q.Set("page_cursor", next)
	}
}

// TestMonitoringSubscriptionOwner drives THE-727 through the public API. A
// client application creates an alert for one of its users and a global one
// on the same Corpus. Each is listed under its owner (or none) only, and the
// Match, webhook and change feed of the owned alert carry the owner so the
// client can route it, while the global alert's carry none. The owner is
// fixed: a new Subscription Version keeps it and a replay cannot change it.
func TestMonitoringSubscriptionOwner(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	receiver := startReceiver(t)
	run := monitoringRun()
	owner := "user-" + run
	corpusID := changeCorpus(t, "owner-"+run)
	cursor := request(t, "GET", changesPath(corpusID, "", 0), admin, nil, 200)["next_cursor"].(string)
	query := request(t, "POST", "/v0/saved-queries", admin, savedQueryCommand("owner-"+run, corpusID), 201)

	ownedCommand := subscriptionCommand("owner-owned-"+run, query, destinationCapture)
	ownedCommand["owner"] = owner
	owned := request(t, "POST", "/v0/subscriptions", admin, ownedCommand, 201)
	ownedID := owned["subscription_id"].(string)
	if owned["owner"] != owner || owned["current_version"].(map[string]any)["owner"] != owner {
		t.Fatal("owner on creation", owned)
	}
	if replay := request(t, "POST", "/v0/subscriptions", admin, ownedCommand, 201); replay["subscription_id"] != ownedID {
		t.Fatal("replay", replay)
	}
	changed := subscriptionCommand("owner-owned-"+run, query, destinationCapture)
	changed["owner"] = owner + "-other"
	if refused := request(t, "POST", "/v0/subscriptions", admin, changed, 409); refused["code"] != "idempotency_conflict" {
		t.Fatal("a replay cannot change the owner", refused)
	}
	global := request(t, "POST", "/v0/subscriptions", admin, subscriptionCommand("owner-global-"+run, query, destinationCapture), 201)
	globalID := global["subscription_id"].(string)
	if _, has := global["owner"]; has {
		t.Fatal("a global Subscription has no owner", global)
	}
	reserved := subscriptionCommand("owner-reserved-"+run, query, destinationCapture)
	reserved["owner"] = "none"
	if refused := request(t, "POST", "/v0/subscriptions", admin, reserved, 422); refused["code"] != "invalid_owner" {
		t.Fatal("reserved owner", refused)
	}

	// Each alert is listed under its own owner only.
	mine := subscriptionsOf(t, admin, owner)
	if len(mine) != 1 || mine[ownedID] == nil {
		t.Fatal("listing by owner", mine)
	}
	if globals := subscriptionsOf(t, admin, "none"); globals[globalID] == nil || globals[ownedID] != nil {
		t.Fatal("listing of global Subscriptions", globals)
	}

	// A new Version keeps the owner.
	edit := map[string]any{"idempotency_key": "owner-edit-" + run, "saved_query_version_id": owned["current_version"].(map[string]any)["saved_query_version_id"],
		"evaluator": ownedCommand["evaluator"], "destination_id": destinationCapture}
	if version := request(t, "POST", "/v0/subscriptions/"+ownedID+"/versions", admin, edit, 201); version["owner"] != owner {
		t.Fatal("a new Version keeps the owner", version)
	}

	// One Record matches both: the owned alert's Match, feed event and webhook
	// carry the owner; the global one's none.
	awaitReady(t, ingestCorrection(t, corpusID, "owner-record-"+run, "owner-record-"+run, "Dépêche élection"))
	_, created := awaitMatches(t, admin, corpusID, cursor, 2)
	events := map[string]map[string]any{}
	for _, event := range created {
		events[refsOf(event)["subscription_id"].(string)] = event
	}
	if refsOf(events[ownedID])["owner"] != owner || refsOf(events[globalID])["owner"] != nil {
		t.Fatal("feed owners", events)
	}
	for id, want := range map[string]any{ownedID: owner, globalID: nil} {
		body := deliveredAsPolled(t, receiver, events[id])
		if body["references"].(map[string]any)["owner"] != want {
			t.Fatal("webhook owner", id, body)
		}
		match := request(t, "GET", "/v0/matches/"+refsOf(events[id])["match_id"].(string), admin, nil, 200)
		if match["owner"] != want {
			t.Fatal("Match owner", id, match)
		}
	}

	// A disabled alert is no longer active: it leaves the listing.
	request(t, "POST", "/v0/subscriptions/"+ownedID+"/disable", admin, map[string]any{"idempotency_key": "owner-disable-" + run}, 200)
	if mine = subscriptionsOf(t, admin, owner); len(mine) != 0 {
		t.Fatal("a disabled Subscription is not listed", mine)
	}
}
