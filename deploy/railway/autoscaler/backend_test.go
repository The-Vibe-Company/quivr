package railway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	railway "github.com/The-Vibe-Company/quivr/deploy/railway/autoscaler"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fakeAPI(t *testing.T, respond func(string, json.RawMessage) (int, string)) railway.Backend {
	t.Helper()
	client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.String() != "https://backboard.railway.com/graphql/v2" || r.Header.Get("Project-Access-Token") != "fixture-token" || r.Header.Get("Authorization") != "" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("User-Agent") != "quivr-autoscaler" {
			t.Fatal("incorrect Railway endpoint, project authentication or User-Agent")
		}
		var request struct {
			Query     string
			Variables struct {
				ServiceID     string          `json:"serviceId"`
				EnvironmentID string          `json:"environmentId"`
				Input         json.RawMessage `json:"input"`
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Variables.ServiceID != "fixture-bulk" || request.Variables.EnvironmentID != "fixture-environment" {
			t.Fatalf("incorrect Railway target: %+v", request.Variables)
		}
		status, body := respond(request.Query, request.Variables.Input)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	return railway.Backend{Token: "fixture-token", ServiceID: "fixture-bulk", EnvironmentID: "fixture-environment", Client: client}
}

func assertError(t *testing.T, err error, want bool) {
	t.Helper()
	if (err != nil) != want {
		t.Fatalf("got error %v, want error %v", err, want)
	}
	if err != nil && strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("leaked remote error: %v", err)
	}
}

// This owner test exercises Railway's independent wire contract. The fake
// changes deployed regional metadata only on a deploy, never on a plain write.
func TestRailwayReplicaAPI(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"regional", "legacy", "regional zero"} {
		t.Run(mode+" lifecycle and deploy recovery", func(t *testing.T) {
			applied, staged := 1, 1
			if mode == "regional zero" {
				applied, staged = 0, 0
			}
			initial := applied
			rejectDeploy := true
			var actions []string
			b := fakeAPI(t, func(query string, input json.RawMessage) (int, string) {
				switch {
				case strings.Contains(query, "serviceInstanceUpdate"):
					actions = append(actions, "update")
					var patch struct {
						Regions map[string]struct {
							Replicas int   `json:"numReplicas"`
							Sleep    *bool `json:"sleepApplication"`
						} `json:"multiRegionConfig"`
					}
					if err := json.Unmarshal(input, &patch); err != nil {
						t.Fatal(err)
					}
					if region, ok := patch.Regions["europe-west4"]; ok {
						if len(patch.Regions) != 1 || (applied != 0 && (region.Sleep == nil || *region.Sleep)) {
							t.Fatal("regional update lost unrelated settings")
						}
						staged = region.Replicas
					}
					if mode == "legacy" {
						var plain struct {
							Replicas int `json:"numReplicas"`
						}
						if err := json.Unmarshal(input, &plain); err != nil {
							t.Fatal(err)
						}
						staged = plain.Replicas
					}
					return 200, `{"data":{"serviceInstanceUpdate":true}}`
				case strings.Contains(query, "serviceInstanceDeploy"):
					actions = append(actions, "deploy")
					if rejectDeploy {
						return 200, `{"errors":[{"message":"sensitive deploy error"}]}`
					}
					applied = staged
					return 200, `{"data":{"serviceInstanceDeploy":true}}`
				default:
					if mode == "regional zero" && applied == 0 {
						return 200, `{"data":{"serviceInstance":{"numReplicas":4,"latestDeployment":{"meta":{"serviceManifest":{"deploy":{"multiRegionConfig":{"europe-west4":null}}}}}}}}`
					}
					if mode == "legacy" {
						return 200, fmt.Sprintf(`{"data":{"serviceInstance":{"numReplicas":%d,"latestDeployment":{"meta":{"serviceManifest":{"deploy":{"numReplicas":%d}}}}}}}`, staged, applied)
					}
					return 200, fmt.Sprintf(`{"data":{"serviceInstance":{"numReplicas":4,"latestDeployment":{"meta":{"serviceManifest":{"deploy":{"multiRegionConfig":{"europe-west4":{"numReplicas":%d,"sleepApplication":false}}}}}}}}}`, applied)
				}
			})
			if got, err := b.Replicas(ctx); err != nil || got != initial {
				t.Fatalf("initial count: got %d/error %v, want %d", got, err, initial)
			}
			assertError(t, b.SetReplicas(ctx, 3), true)
			if got, err := b.Replicas(ctx); err != nil || got != initial {
				t.Fatalf("rejected deployment changed applied count: got %d/error %v, want %d", got, err, initial)
			}
			rejectDeploy = false
			assertError(t, b.SetReplicas(ctx, 3), false)
			if got, err := b.Replicas(ctx); err != nil || got != 3 {
				t.Fatalf("applied count: got %d/error %v, want 3", got, err)
			}
			if strings.Join(actions, ",") != "update,deploy,update,deploy" {
				t.Fatalf("scale actions: got %v, want update/deploy then retry", actions)
			}
		})
	}

	for _, tc := range []struct {
		name, instance string
		want           int
		bad            bool
	}{
		{"legacy read", `{"numReplicas":3}`, 3, false},
		{"null deployment", `{"numReplicas":3,"latestDeployment":null}`, 3, false},
		{"empty metadata", `{"numReplicas":3,"latestDeployment":{"meta":{}}}`, 3, false},
		{"missing count", `{}`, 0, true},
		{"null instance", `null`, 0, true},
		{"negative count", `{"numReplicas":-1}`, 0, true},
		{"multiple regions", `{"numReplicas":3,"latestDeployment":{"meta":{"serviceManifest":{"deploy":{"multiRegionConfig":{"europe-west4":{"numReplicas":1},"us-west2":{"numReplicas":2}}}}}}}`, 0, true},
		{"missing regional count", `{"numReplicas":3,"latestDeployment":{"meta":{"serviceManifest":{"deploy":{"multiRegionConfig":{"europe-west4":{}}}}}}}`, 0, true},
		{"null regional count", `{"numReplicas":3,"latestDeployment":{"meta":{"serviceManifest":{"deploy":{"multiRegionConfig":{"europe-west4":{"numReplicas":null}}}}}}}`, 0, true},
		{"negative regional count", `{"numReplicas":3,"latestDeployment":{"meta":{"serviceManifest":{"deploy":{"multiRegionConfig":{"europe-west4":{"numReplicas":-1}}}}}}}`, 0, true},
		{"invalid regional config", `{"numReplicas":3,"latestDeployment":{"meta":{"serviceManifest":{"deploy":{"multiRegionConfig":"invalid"}}}}}`, 0, true},
		{"empty regional config", `{"numReplicas":3,"latestDeployment":{"meta":{"serviceManifest":{"deploy":{"multiRegionConfig":{}}}}}}`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := fakeAPI(t, func(query string, _ json.RawMessage) (int, string) {
				if strings.Contains(query, "mutation") {
					t.Fatal("invalid configuration must not be mutated")
				}
				return 200, `{"data":{"serviceInstance":` + tc.instance + `}}`
			})
			got, err := b.Replicas(ctx)
			assertError(t, err, tc.bad)
			if !tc.bad && got != tc.want {
				t.Fatalf("got replicas %d, want %d", got, tc.want)
			}
			if tc.bad {
				assertError(t, b.SetReplicas(ctx, 5), true)
				if tc.name == "multiple regions" && !strings.Contains(err.Error(), "single-region") {
					t.Fatalf("unclear multi-region refusal: %v", err)
				}
			}
		})
	}

	for _, tc := range []struct {
		name, stage, body string
		status            int
		bad               bool
	}{
		{"legacy write and deploy", "update", `{"data":{"serviceInstanceUpdate":true}}`, 200, false},
		{"read denied", "read", `{"errors":[{"message":"sensitive body"}]}`, 200, true},
		{"write refused", "update", `{"data":{"serviceInstanceUpdate":false}}`, 200, true},
		{"write missing", "update", `{"data":{}}`, 200, true},
		{"write denied", "update", `{"errors":[{"message":"sensitive body"}],"data":{"serviceInstanceUpdate":true}}`, 200, true},
		{"rate limited", "update", `sensitive body`, 429, true},
		{"deploy refused", "deploy", `{"data":{"serviceInstanceDeploy":false}}`, 200, true},
		{"deploy missing", "deploy", `{"data":{}}`, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			updates, deploys := 0, 0
			b := fakeAPI(t, func(query string, input json.RawMessage) (int, string) {
				stage, body := "read", `{"data":{"serviceInstance":{"numReplicas":3}}}`
				switch {
				case strings.Contains(query, "serviceInstanceUpdate"):
					updates++
					stage, body = "update", `{"data":{"serviceInstanceUpdate":true}}`
					if string(input) != `{"numReplicas":5}` {
						t.Fatalf("legacy update: got %s, want numReplicas 5", input)
					}
				case strings.Contains(query, "serviceInstanceDeploy"):
					deploys++
					if updates != 1 {
						t.Fatal("deploy must follow an acknowledged update")
					}
					stage, body = "deploy", `{"data":{"serviceInstanceDeploy":true}}`
				}
				if stage == tc.stage {
					return tc.status, tc.body
				}
				return 200, body
			})
			assertError(t, b.SetReplicas(ctx, 5), tc.bad)
			if tc.bad && tc.stage != "deploy" && deploys != 0 {
				t.Fatal("failed read/update must not deploy")
			}
			if !tc.bad && deploys != 1 {
				t.Fatalf("got %d deploys, want 1", deploys)
			}
		})
	}
}
