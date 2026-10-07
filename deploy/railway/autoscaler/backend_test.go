package railway_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	railway "github.com/The-Vibe-Company/quivr/deploy/railway/autoscaler"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// This transport fake owns Railway's independent wire contract: project auth,
// target IDs, numeric replicas, Boolean acknowledgement and GraphQL failures.
func TestRailwayReplicaAPI(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		write, bad bool
		want       int
	}{
		{"read", `{"data":{"serviceInstance":{"numReplicas":3}}}`, 200, false, false, 3},
		{"missing count", `{"data":{"serviceInstance":{}}}`, 200, false, true, 0},
		{"null instance", `{"data":{"serviceInstance":null}}`, 200, false, true, 0},
		{"negative count", `{"data":{"serviceInstance":{"numReplicas":-1}}}`, 200, false, true, 0},
		{"read denied", `{"errors":[{"message":"sensitive body"}],"data":{"serviceInstance":{"numReplicas":3}}}`, 200, false, true, 0},
		{"write", `{"data":{"serviceInstanceUpdate":true}}`, 200, true, false, 0},
		{"write refused", `{"data":{"serviceInstanceUpdate":false}}`, 200, true, true, 0},
		{"write missing", `{"data":{}}`, 200, true, true, 0},
		{"write denied", `{"errors":[{"message":"sensitive body"}],"data":{"serviceInstanceUpdate":true}}`, 200, true, true, 0},
		{"rate limited", `sensitive body`, 429, true, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
				if r.Method != "POST" || r.URL.String() != "https://backboard.railway.com/graphql/v2" || r.Header.Get("Project-Access-Token") != "fixture-token" || r.Header.Get("Authorization") != "" || r.Header.Get("Content-Type") != "application/json" {
					t.Fatal("incorrect Railway endpoint or project authentication")
				}
				var request struct {
					Query     string
					Variables struct {
						ServiceID     string `json:"serviceId"`
						EnvironmentID string `json:"environmentId"`
						Input         struct {
							Replicas int `json:"numReplicas"`
						} `json:"input"`
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				if request.Variables.ServiceID != "fixture-bulk" || request.Variables.EnvironmentID != "fixture-environment" {
					t.Fatalf("incorrect target: %+v", request.Variables)
				}
				if tc.write {
					if !strings.Contains(request.Query, "serviceInstanceUpdate") || request.Variables.Input.Replicas != 5 {
						t.Fatal("expected replica mutation to five")
					}
				} else if !strings.Contains(request.Query, "numReplicas") {
					t.Fatal("expected current replica selection")
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			b := railway.Backend{Token: "fixture-token", ServiceID: "fixture-bulk", EnvironmentID: "fixture-environment", Client: client}
			var got int
			var err error
			if tc.write {
				err = b.SetReplicas(context.Background(), 5)
			} else {
				got, err = b.Replicas(context.Background())
			}
			if (err != nil) != tc.bad || (!tc.write && !tc.bad && got != tc.want) {
				t.Fatalf("got replicas %d/error %v; want %d/error %v", got, err, tc.want, tc.bad)
			}
			if err != nil && strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("leaked remote error: %v", err)
			}
		})
	}
}
