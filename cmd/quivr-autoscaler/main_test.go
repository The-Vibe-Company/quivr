package main

import (
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/autoscaling"
)

// This owner test pins the external variable names: only the selected
// backend's variables are required, and the queue defaults to bulk.
func TestLoadRequiresOnlyTheSelectedBackend(t *testing.T) {
	keys := []string{"QUIVR_AUTOSCALER_BACKEND", "QUIVR_AUTOSCALER_QUEUE", "QUIVR_AUTOSCALER_KUBERNETES_DEPLOYMENT", "RAILWAY_TOKEN", "QUIVR_AUTOSCALER_RAILWAY_SERVICE_ID", "RAILWAY_SERVICE_ID", "RAILWAY_ENVIRONMENT_ID",
		"QUIVR_AUTOSCALER_MIN", "QUIVR_AUTOSCALER_MAX", "QUIVR_AUTOSCALER_DOCUMENTS_PER_REPLICA", "QUIVR_AUTOSCALER_DOWNSCALE_WINDOW", "QUIVR_AUTOSCALER_INTERVAL", "QUIVR_AUTOSCALER_MIN_SCALE_INTERVAL", "QUIVR_AUTOSCALER_REQUEST_TIMEOUT"}
	railway := map[string]string{"QUIVR_AUTOSCALER_BACKEND": "railway", "RAILWAY_TOKEN": "t", "QUIVR_AUTOSCALER_RAILWAY_SERVICE_ID": "s", "RAILWAY_ENVIRONMENT_ID": "e"}
	for _, tc := range []struct {
		name       string
		env        map[string]string
		wantQueue  string
		wantError  string
		wantTarget int
	}{
		{"kubernetes without Railway variables", map[string]string{"QUIVR_AUTOSCALER_BACKEND": "kubernetes", "QUIVR_AUTOSCALER_KUBERNETES_DEPLOYMENT": "quivr-bulk"}, "bulk", "", 2},
		{"kubernetes without a Deployment", map[string]string{"QUIVR_AUTOSCALER_BACKEND": "kubernetes"}, "", "QUIVR_AUTOSCALER_KUBERNETES_DEPLOYMENT is required", 0},
		{"railway", railway, "bulk", "", 2},
		{"railway injected own service id", map[string]string{"QUIVR_AUTOSCALER_BACKEND": "railway", "RAILWAY_TOKEN": "t", "RAILWAY_SERVICE_ID": "s", "RAILWAY_ENVIRONMENT_ID": "e"}, "", "QUIVR_AUTOSCALER_RAILWAY_SERVICE_ID is required", 0},
		{"backend omitted", map[string]string{"RAILWAY_TOKEN": "t", "QUIVR_AUTOSCALER_RAILWAY_SERVICE_ID": "s", "RAILWAY_ENVIRONMENT_ID": "e"}, "", "QUIVR_AUTOSCALER_BACKEND must be kubernetes or railway", 0},
		{"backend unknown", map[string]string{"QUIVR_AUTOSCALER_BACKEND": "nomad"}, "", "QUIVR_AUTOSCALER_BACKEND must be kubernetes or railway", 0},
		{"live queue", map[string]string{"QUIVR_AUTOSCALER_BACKEND": "kubernetes", "QUIVR_AUTOSCALER_KUBERNETES_DEPLOYMENT": "quivr-live", "QUIVR_AUTOSCALER_QUEUE": "live"}, "live", "", 1},
		{"unknown queue", map[string]string{"QUIVR_AUTOSCALER_BACKEND": "kubernetes", "QUIVR_AUTOSCALER_KUBERNETES_DEPLOYMENT": "quivr-bulk", "QUIVR_AUTOSCALER_QUEUE": "Bulk"}, "", "QUIVR_AUTOSCALER_QUEUE must be bulk or live", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("QUIVR_QUEUE_URL", "http://api.example.org/v0/admin/queues")
			t.Setenv("QUIVR_QUEUE_KEY", "fixture-key")
			for _, key := range keys {
				t.Setenv(key, tc.env[key])
			}
			s, err := load()
			if tc.wantError != "" {
				if err == nil || err.Error() != tc.wantError {
					t.Fatalf("got error %v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil || s.queue != tc.wantQueue {
				t.Fatalf("got queue %q/%v, want %q", s.queue, err, tc.wantQueue)
			}
			policy, err := autoscaling.NewPolicy(s.policy)
			if err != nil {
				t.Fatal(err)
			}
			target, err := policy.Decide(1000, 1, time.Unix(1000, 0))
			if err != nil || target != tc.wantTarget {
				t.Fatalf("loaded %s policy at connector bound: got target %d/error %v, want %d", s.queue, target, err, tc.wantTarget)
			}
		})
	}
}
