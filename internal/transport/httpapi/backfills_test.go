package httpapi_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/backfill"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

type backfillPlans struct{}

func (backfillPlans) ActiveIngestion(context.Context) (backfill.Ingestion, error) {
	usd := 0.02
	return backfill.Ingestion{RegistrationID: "reg", PluginID: "p", Version: "1.0.0", Prices: map[string]*float64{"p.small@1": nil, "p.large@1": &usd}}, nil
}

// backfillStore keeps dry runs by key; its sizes make any priced backfill
// cost more than the threshold.
type backfillStore struct{ estimates map[string][]byte }

func (backfillStore) RegisteredSpaces(context.Context) ([]content.RegisteredSpace, error) {
	return []content.RegisteredSpace{{VectorSpace: content.VectorSpace{ID: "p.small@1"}, Role: content.SpaceServed}, {VectorSpace: content.VectorSpace{ID: "p.large@1"}, Role: content.SpaceEvaluation}}, nil
}
func (backfillStore) BackfillSize(_ context.Context, _ string, _ operations.Backfill, corpusID string) (backfill.Size, error) {
	if corpusID == "corpus_legacy" {
		return backfill.Size{}, backfill.ErrRebuildRequired
	}
	return backfill.Size{Versions: 2, Segments: 4, CodePoints: 4_000_000}, nil
}
func (s backfillStore) RecordEstimate(_ context.Context, _, _, key string, canonical []byte, _ operations.BackfillEstimate) error {
	s.estimates[key] = canonical
	return nil
}
func (s backfillStore) BackfillEstimate(_ context.Context, _, _, key string) ([]byte, operations.BackfillEstimate, error) {
	canonical, ok := s.estimates[key]
	if !ok {
		return nil, operations.BackfillEstimate{}, corpus.ErrNotFound
	}
	usd := 0.02
	return canonical, operations.BackfillEstimate{RegistrationID: "reg", DurationBasis: "rate", EstimatedCostUSD: &usd, ConfirmationRequired: true}, nil
}
func (backfillStore) BackfillByKey(context.Context, string, string, string) (operations.Operation, error) {
	return operations.Operation{}, corpus.ErrNotFound
}
func (backfillStore) AcceptBackfill(_ context.Context, org, corpusID, _ string, _ []byte, spec operations.Backfill) (operations.Operation, error) {
	if corpusID == "corpus_busy" {
		return operations.Operation{}, backfill.ErrInProgress
	}
	return operations.Operation{ID: "operation_fill", Organization: org, Kind: operations.KindBackfill, CorpusID: corpusID, State: operations.StateQueued, Backfill: &spec}, nil
}

type promotionStore struct{}

func (promotionStore) PromoteSpace(_ context.Context, space string, force bool) (backfill.Promotion, error) {
	switch {
	case space == "p.old@1":
		return backfill.Promotion{}, backfill.ErrNotEvaluation
	case space == "p.unknown@1":
		return backfill.Promotion{}, corpus.ErrNotFound
	case !force:
		return backfill.Promotion{}, &backfill.IncompleteError{Promotion: backfill.Promotion{Served: space, CorporaIncomplete: 1, SegmentsMissing: 3}}
	}
	return backfill.Promotion{Served: space, Previous: "p.small@1", GenerationsSwitched: 2}, nil
}

// Each backfill and promotion outcome has its status and public code; the
// dry run answers its estimate and the backfill its Operation.
func TestBackfillAndPromotionRoutes(t *testing.T) {
	const operator, organization = "backfill-admin-token-0123456789abcdef012345", "organization-token-0123456789abcdef0123456"
	keys := map[string]corpus.Scope{
		operator:     {Organization: "org_a", Actions: []string{operations.BackfillPermission}, Corpora: []string{"*"}},
		organization: {Organization: "org_a", Actions: []string{"operations:write", "corpora:write"}, Corpora: []string{"*"}},
	}
	store := backfillStore{estimates: map[string][]byte{}}
	handler, err := httpapi.New(knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"),
		httpapi.WithBackfills(backfill.Service{Store: store, Plans: backfillPlans{}}, backfill.Promotions{Store: promotionStore{}}))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(checkedAPI(t, handler))
	t.Cleanup(server.Close)
	const dry = `{"idempotency_key":"k","corpus_id":"corpus_a","dry_run":true}`
	res, estimate := operationCall(t, server, "POST", "/v0/admin/backfills", operator, "application/json", dry)
	if res.StatusCode != 200 || estimate["versions"] != float64(2) || estimate["input_tokens"] != float64(1_000_000) || estimate["estimated_cost_usd"] != 0.02 || estimate["confirmation_required"] != true {
		t.Fatalf("dry run %d %v", res.StatusCode, estimate)
	}
	res, op := operationCall(t, server, "POST", "/v0/admin/backfills", operator, "application/json", `{"idempotency_key":"k","corpus_id":"corpus_a","dry_run":false,"confirm_cost":true}`)
	if res.StatusCode != 202 || op["kind"] != "backfill" || res.Header.Get("Location") != "/v0/operations/operation_fill" || op["backfill"].(map[string]any)["registration_id"] != "reg" {
		t.Fatalf("accepted %d %v", res.StatusCode, op)
	}
	if res, _ := operationCall(t, server, "POST", "/v0/admin/backfills", operator, "application/json", `{"idempotency_key":"busy","corpus_id":"corpus_busy","dry_run":true}`); res.StatusCode != 200 {
		t.Fatalf("dry run of a busy Corpus %d", res.StatusCode)
	}
	res, promoted := operationCall(t, server, "POST", "/v0/admin/spaces/p.large@1/promote", operator, "application/json", `{"force":true}`)
	if res.StatusCode != 200 || promoted["served_space_id"] != "p.large@1" || promoted["previous_space_id"] != "p.small@1" || promoted["generations_switched"] != float64(2) {
		t.Fatalf("promoted %d %v", res.StatusCode, promoted)
	}
	for _, tc := range []struct {
		name, path, token, body string
		status                  int
		code                    string
	}{
		{"without plugins:admin", "/v0/admin/backfills", organization, dry, 403, "forbidden"},
		{"no dry run for the key", "/v0/admin/backfills", operator, `{"idempotency_key":"other","corpus_id":"corpus_a","dry_run":false}`, 409, "dry_run_required"},
		{"cost not confirmed", "/v0/admin/backfills", operator, `{"idempotency_key":"k","corpus_id":"corpus_a","dry_run":false}`, 409, "cost_confirmation_required"},
		{"another registration", "/v0/admin/backfills", operator, `{"idempotency_key":"k","corpus_id":"corpus_a","dry_run":true,"registration_id":"reg_b"}`, 409, "registration_not_active"},
		{"another backfill of the Corpus", "/v0/admin/backfills", operator, `{"idempotency_key":"busy","corpus_id":"corpus_busy","dry_run":false,"confirm_cost":true}`, 409, "backfill_in_progress"},
		{"a generation before named spaces", "/v0/admin/backfills", operator, `{"idempotency_key":"k","corpus_id":"corpus_legacy","dry_run":true}`, 409, "rebuild_required"},
		{"a space the plugin does not declare", "/v0/admin/backfills", operator, `{"idempotency_key":"k","corpus_id":"corpus_a","dry_run":true,"spaces":["q.large@1"]}`, 422, "invalid_backfill"},
		{"dry_run missing", "/v0/admin/backfills", operator, `{"idempotency_key":"k","corpus_id":"corpus_a"}`, 422, "invalid_schema"},
		{"promotion without plugins:admin", "/v0/admin/spaces/p.large@1/promote", organization, `{}`, 403, "forbidden"},
		{"incomplete coverage", "/v0/admin/spaces/p.large@1/promote", operator, `{}`, 409, "coverage_incomplete"},
		{"a retired space", "/v0/admin/spaces/p.old@1/promote", operator, `{"force":true}`, 422, "not_evaluation_space"},
		{"an unknown space", "/v0/admin/spaces/p.unknown@1/promote", operator, `{"force":true}`, 404, "not_found"},
	} {
		res, body := operationCall(t, server, "POST", tc.path, tc.token, "application/json", tc.body)
		if res.StatusCode != tc.status || body["code"] != tc.code {
			t.Errorf("%s: %d %v, want %d %s", tc.name, res.StatusCode, body, tc.status, tc.code)
		}
	}
	if res, _ := operationCall(t, server, "GET", "/v0/admin/backfills", operator, "", ""); res.StatusCode != 405 {
		t.Errorf("GET backfills: %d", res.StatusCode)
	}
}
