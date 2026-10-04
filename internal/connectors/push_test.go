package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

// pushKind is a push connector kind whose fetch page and receive answer each
// test chooses.
type pushKind struct {
	pushes   bool
	page     Page
	delivery Delivery
	err      error
	received *[]ReceiveRequest
	routes   []APIRoute
	fetched  *[]FetchRequest
}

func (k pushKind) Fetch(_ context.Context, req FetchRequest) (Page, error) {
	if k.fetched != nil {
		*k.fetched = append(*k.fetched, req)
	}
	return k.page, nil
}

func (k pushKind) Receive(_ context.Context, r ReceiveRequest) (Delivery, error) {
	if k.received != nil {
		*k.received = append(*k.received, r)
	}
	return k.delivery, k.err
}

type fakePush struct {
	target      Target
	missing     bool
	outcomes    []DeliveryOutcome
	recordError error
}

func (f *fakePush) LoadDelivery(_ context.Context, id string) (Target, error) {
	if f.missing || id != f.target.ID {
		return Target{}, corpus.ErrNotFound
	}
	return f.target, nil
}
func (f *fakePush) RecordDelivery(_ context.Context, _, _ string, o DeliveryOutcome) error {
	f.outcomes = append(f.outcomes, o)
	return f.recordError
}

var pushed = Item{RecordKey: "alert-7", Revision: "7", Position: "7", Content: content.Text{Kind: "text", Text: "Harbour closed by fog."}}

func newRelay(t *testing.T, kind pushKind) (Relay, *fakePush, *fakeIngest, *[]ReceiveRequest) {
	t.Helper()
	sealer, _ := NewSealer(testDeploymentKey)
	sealed, _ := sealer.Seal("org_a", "connector_1", []byte(`{"signing_secret":"test-signing-0000"}`))
	target := Target{Instance: Instance{Organization: "org_a", ID: "connector_1", CorpusID: "corpus_1", Namespace: "alerts", Kind: "alerts", Config: json.RawMessage(`{}`), Enabled: true},
		Checkpoint: json.RawMessage(`{"since":"6"}`), Sealed: &sealed, ReadsToday: 12}
	received := &[]ReceiveRequest{}
	kind.received = received
	store := &fakePush{target: target}
	ingest := &fakeIngest{}
	registry, err := NewRegistry(kind)
	if err != nil {
		t.Fatal(err)
	}
	return Relay{Store: store, Registry: registry, Sealer: sealer, Ingest: ingest}, store, ingest, received
}

var delivery = Relayed{Method: "POST", Headers: map[string][]string{"x-signature": {"sig"}}, Body: []byte(`{"alert":"7"}`)}

func TestRelayAnswersNotFoundWithoutCallingAPluginForAnInstanceThatDoesNotPush(t *testing.T) {
	for name, edit := range map[string]func(*pushKind, *fakePush){
		"unknown instance":    func(_ *pushKind, s *fakePush) { s.missing = true },
		"disabled instance":   func(_ *pushKind, s *fakePush) { s.target.Enabled = false },
		"kind without push":   func(k *pushKind, _ *fakePush) { k.pushes = false },
		"kind not registered": func(_ *pushKind, s *fakePush) { s.target.Kind = "retired" },
	} {
		t.Run(name, func(t *testing.T) {
			kind := pushKind{pushes: true, delivery: Delivery{Accepted: true, Status: 200}}
			edit(&kind, &fakePush{})
			relay, store, _, received := newRelay(t, kind)
			edit(&kind, store)
			if _, err := relay.Deliver(context.Background(), "connector_1", delivery); !errors.Is(err, ErrNoWebhook) {
				t.Fatalf("err %v", err)
			}
			if len(*received) != 0 || len(store.outcomes) != 0 {
				t.Fatalf("plugin called %d times, %d outcomes recorded", len(*received), len(store.outcomes))
			}
		})
	}
}

func TestRelayedItemsConvergeWithPulledOnesOnTheSameIdempotencyKey(t *testing.T) {
	relay, store, ingest, received := newRelay(t, pushKind{pushes: true, delivery: Delivery{Accepted: true, Status: 204, Items: []Item{pushed}, Reads: 1}})
	answer, err := relay.Deliver(context.Background(), "connector_1", delivery)
	if err != nil || answer.Status != 204 {
		t.Fatalf("answer %+v err %v", answer, err)
	}
	r := (*received)[0]
	if string(r.Credential) != `{"signing_secret":"test-signing-0000"}` || string(r.Request.Body) != `{"alert":"7"}` || string(r.Checkpoint) != `{"since":"6"}` || r.ReadsToday != 12 || r.CorpusID != "corpus_1" {
		t.Fatalf("the plugin received %+v", r)
	}
	if len(store.outcomes) != 1 || !store.outcomes[0].Accepted || !store.outcomes[0].Carried || !store.outcomes[0].Fresh || store.outcomes[0].Reads != 1 || store.outcomes[0].Failure != nil {
		t.Fatalf("outcomes %+v", store.outcomes)
	}
	// A pull run that returns the same item submits the same command key, so
	// the ingestion path replays the Receipt instead of making a Version.
	registry, _ := NewRegistry(pushKind{pushes: true, page: Page{Items: []Item{pushed}, Checkpoint: json.RawMessage(`{"since":"7"}`)}})
	runs := &fakeRuns{target: store.target}
	runs.target.RunSequence = 1
	acquirer := Acquirer{Store: runs, Registry: registry, Sealer: relay.Sealer, Ingest: ingest}
	if err := acquirer.Run(context.Background(), "org_a", "connector_1", 1); err != nil {
		t.Fatal(err)
	}
	if len(ingest.accepted) != 2 || ingest.accepted[0].Key != ingest.accepted[1].Key || runs.items[0] {
		t.Fatalf("pushed %q, pulled %q, pull counted fresh: %v", ingest.accepted[0].Key, ingest.accepted[1].Key, runs.items)
	}
	if ingest.accepted[0].Provenance["producer_version"] != "alerts/v1" || ingest.accepted[0].Source.Namespace != "alerts" {
		t.Fatalf("command %+v", ingest.accepted[0])
	}
}

func TestARefusedDeliveryChangesNothing(t *testing.T) {
	relay, store, ingest, _ := newRelay(t, pushKind{pushes: true, delivery: Delivery{Status: 401, ContentType: "text/plain", Body: "bad signature"}})
	answer, err := relay.Deliver(context.Background(), "connector_1", delivery)
	if err != nil || answer.Status != 401 || answer.Body != "bad signature" {
		t.Fatalf("answer %+v err %v", answer, err)
	}
	if len(store.outcomes) != 0 || len(ingest.accepted) != 0 {
		t.Fatalf("a refused delivery wrote %d outcomes and %d items", len(store.outcomes), len(ingest.accepted))
	}
}

func TestAFailedDeliveryIsRetryableOrTerminalAndRecordedAsPushHealth(t *testing.T) {
	for name, c := range map[string]struct {
		err      error
		ingest   error
		status   int
		class    ErrorClass
		code     string
		edit     func(*fakePush)
		accepted bool
		calls    int
	}{
		"plugin unavailable":    {err: TransientError("plugin_unavailable"), status: 503, class: ClassTransient, code: "plugin_unavailable", calls: 1},
		"credential refused":    {err: AccessError("consumer_secret_missing"), status: 500, class: ClassAccess, code: "consumer_secret_missing", calls: 1},
		"untyped failure":       {err: errors.New("boom"), status: 503, class: ClassTransient, code: "source_unavailable", calls: 1},
		"ingestion unavailable": {ingest: errors.New("database down"), status: 503, class: ClassTransient, code: "ingestion_unavailable", calls: 1},
		"credential expired": {status: 500, class: ClassAccess, code: "credential_expired", edit: func(store *fakePush) {
			expired := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
			store.target.Sealed.ExpiresAt = &expired
		}},
		"credential unreadable": {status: 500, class: ClassAccess, code: "credential_unreadable", edit: func(store *fakePush) {
			store.target.Sealed.Ciphertext[0] ^= 1
		}},
		"corpus missing":   {ingest: corpus.ErrNotFound, status: 500, class: ClassAccess, code: "corpus_unavailable", calls: 1},
		"corpus forbidden": {ingest: corpus.ErrForbidden, status: 500, class: ClassAccess, code: "corpus_unavailable", calls: 1},
		"item rejected":    {ingest: content.ErrInvalid, status: 200, class: ClassSource, code: "item_rejected", accepted: true, calls: 1},
	} {
		t.Run(name, func(t *testing.T) {
			relay, store, ingest, received := newRelay(t, pushKind{pushes: true, err: c.err, delivery: Delivery{Accepted: true, Status: 200, Items: []Item{pushed}}})
			if c.edit != nil {
				c.edit(store)
			}
			ingest.fail = c.ingest
			answer, err := relay.Deliver(context.Background(), "connector_1", delivery)
			if err != nil || answer.Status != c.status || (c.status == 503) != (answer.RetryAfter > 0) {
				t.Fatalf("answer %+v err %v", answer, err)
			}
			if len(*received) != c.calls || len(ingest.accepted) != 0 {
				t.Fatalf("plugin calls %d, want %d; accepted items %d, want 0", len(*received), c.calls, len(ingest.accepted))
			}
			if len(store.outcomes) != 1 || store.outcomes[0].Accepted != c.accepted || store.outcomes[0].Failure == nil || store.outcomes[0].Failure.Class != c.class || store.outcomes[0].Failure.Code != c.code {
				t.Fatalf("outcomes %+v", store.outcomes)
			}
		})
	}
}

func TestPullRunsReportPushAndFlagItemsDeliveriesMissed(t *testing.T) {
	report := &PushStatus{State: PushActive, PollInterval: 15 * time.Minute}
	for name, c := range map[string]struct {
		push   *PushHealth
		missed bool
	}{
		"push active since before the run": {push: &PushHealth{Setup: PushActive}, missed: true},
		"push not set up":                  {push: &PushHealth{Setup: PushPending}},
		"push already degraded":            {push: &PushHealth{Setup: PushActive, DeliveryError: &RunError{Class: ClassTransient, Code: CodeMissedDeliveries}}},
		"no push":                          {},
	} {
		t.Run(name, func(t *testing.T) {
			registry, _ := NewRegistry(pushKind{pushes: true, page: Page{Items: []Item{pushed}, Checkpoint: json.RawMessage(`{}`), Push: report}})
			runs := &fakeRuns{target: Target{Instance: Instance{Organization: "org_a", ID: "connector_1", CorpusID: "corpus_1", Namespace: "alerts", Kind: "alerts", Config: json.RawMessage(`{}`), Enabled: true, Health: Health{Push: c.push}}, RunSequence: 1}}
			a := Acquirer{Store: runs, Registry: registry, Ingest: &fakeIngest{}, PublicURL: "https://quivr.example.com/"}
			if err := a.Run(context.Background(), "org_a", "connector_1", 1); err != nil {
				t.Fatal(err)
			}
			if p := runs.progress[0]; p.Missed != c.missed || p.Push != report {
				t.Fatalf("progress %+v", p)
			}
		})
	}
}

func TestPushHealthState(t *testing.T) {
	access := &RunError{Class: ClassAccess, Code: "webhook_invalid"}
	for name, c := range map[string]struct {
		push                     PushHealth
		state                    string
		healthy, accessRefused   bool
		connectorHealthIsRefused bool
	}{
		"active":                  {push: PushHealth{Setup: PushActive}, state: PushActive, healthy: true},
		"pending":                 {push: PushHealth{Setup: PushPending}, state: PushPending},
		"setup refused access":    {push: PushHealth{Setup: PushFailed, SetupError: access}, state: PushDegraded, accessRefused: true},
		"missed deliveries":       {push: PushHealth{Setup: PushActive, DeliveryError: &RunError{Class: ClassTransient, Code: CodeMissedDeliveries}}, state: PushDegraded},
		"delivery refused access": {push: PushHealth{Setup: PushActive, DeliveryError: access}, state: PushDegraded, accessRefused: true},
		"stale setup error":       {push: PushHealth{Setup: PushActive, SetupError: access}, state: PushActive, healthy: true},
		"refused and missed":      {push: PushHealth{Setup: PushFailed, SetupError: access, DeliveryError: &RunError{Class: ClassTransient, Code: CodeMissedDeliveries}}, state: PushDegraded, accessRefused: true},
	} {
		t.Run(name, func(t *testing.T) {
			p := c.push
			if p.State() != c.state || p.Healthy() != c.healthy || p.AccessRefused() != c.accessRefused || c.accessRefused && p.Error().Class != ClassAccess {
				t.Fatalf("state %s healthy %v refused %v", p.State(), p.Healthy(), p.AccessRefused())
			}
			health := Evaluate(HealthInput{Enabled: true, CreatedAt: time.Now(), SilentAfter: time.Hour, PushAccessRefused: p.AccessRefused()}, time.Now())
			if (health == HealthAccessError) != c.accessRefused {
				t.Fatalf("Connector Health %s", health)
			}
		})
	}
}

func TestWebhookURL(t *testing.T) {
	if got := WebhookURL("https://quivr.example.com/api/", "connector_1"); got != "https://quivr.example.com/api/v0/connector-webhooks/connector_1" {
		t.Fatal(got)
	}
	if WebhookURL("", "connector_1") != "" {
		t.Fatal("a deployment without a public URL has no webhook address")
	}
}

// The relay owns challenge bookkeeping; storage health transitions have their
// own adapter coverage. A challenge counts reads without claiming fresh items.
func TestAPIChallengeRecordsReadsBeforeAnswering(t *testing.T) {
	for _, storageFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "recorded", true: "storage unavailable"}[storageFailure], func(t *testing.T) {
			relay, store, ingest, _ := newRelay(t, pushKind{pushes: true,
				routes:   []APIRoute{{Name: "challenge", Method: "GET", Path: "challenge", Auth: "quivr_key"}},
				delivery: Delivery{Accepted: true, Status: 200, Body: "challenge answer", Reads: 3}})
			if storageFailure {
				store.recordError = errors.New("storage unavailable")
			}
			answer, err := relay.DeliverAPI(context.Background(), corpus.Scope{Organization: "org_a", Actions: []string{corpus.ActionConnectorPush}, Corpora: []string{"corpus_1"}}, "connector_1", "challenge", Relayed{Method: "GET"})
			if err != nil || len(store.outcomes) != 1 || len(ingest.accepted) != 0 {
				t.Fatalf("answer %+v err %v outcomes %+v ingested %+v", answer, err, store.outcomes, ingest.accepted)
			}
			o := store.outcomes[0]
			if !o.Accepted || o.Carried || o.Fresh || o.Reads != 3 || o.Failure != nil {
				t.Fatalf("challenge outcome %+v", o)
			}
			if storageFailure {
				if answer.Status != 503 || answer.RetryAfter == 0 {
					t.Fatalf("storage failure answer %+v", answer)
				}
			} else if answer.Status != 200 || answer.Body != "challenge answer" || answer.Receipts != nil {
				t.Fatalf("challenge answer %+v", answer)
			}
		})
	}
}

// Public presentation and acquisition must give the provider the same
// declared callback address, including deployments mounted under a prefix.
func TestSignedKindAdvertisesDeclaredWebhookForProviderSetup(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"receive", "https://quivr.example.com/base/v0/connectors/connector_1/api/receive"},
		{"events/{category}", ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			var fetched []FetchRequest
			kind := pushKind{pushes: true, page: Page{Checkpoint: json.RawMessage(`{}`)}, fetched: &fetched, routes: []APIRoute{{Name: "receive", Method: "POST", Path: tc.path, Auth: "signature", Signature: &Signature{Header: "x-signature", WindowSeconds: 300}}}}
			registry, err := NewRegistry(kind)
			if err != nil {
				t.Fatal(err)
			}
			base := "https://quivr.example.com/base/"
			in := Instance{Organization: "org_a", ID: "connector_1", CorpusID: "corpus_1", Namespace: "wire", Kind: "alerts", Config: json.RawMessage(`{}`), Enabled: true}
			if got := (Service{Registry: registry, PublicURL: base}).WebhookURL(in); got != tc.want {
				t.Fatalf("advertised URL=%q want %q", got, tc.want)
			}
			runs := &fakeRuns{target: Target{Instance: in, RunSequence: 3}}
			a := Acquirer{Store: runs, Registry: registry, Ingest: &fakeIngest{}, PublicURL: base}
			if err := a.Run(context.Background(), in.Organization, in.ID, 3); err != nil {
				t.Fatal(err)
			}
			if len(fetched) != 1 || fetched[0].WebhookURL != tc.want {
				t.Fatalf("provider setup requests=%+v want webhook %s", fetched, tc.want)
			}
		})
	}
}

func (c pushKind) Descriptor() Descriptor {
	d := Descriptor{}
	d.Kind = "alerts"
	d.ConfigSchema = []byte(`{"type":"object"}`)
	d.CredentialSchema = []byte(`{"type":"object"}`)
	d.DefaultInterval = time.Minute
	if c.pushes {
		d.Receiver = c
	}
	d.APIRoutes = c.routes
	return d
}
