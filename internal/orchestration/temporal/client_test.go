package temporal_test

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"time"

	orchestration "github.com/The-Vibe-Company/quivr/internal/orchestration/temporal"
	"github.com/The-Vibe-Company/quivr/internal/outbound"
	"github.com/The-Vibe-Company/quivr/internal/testutil/tlsfixture"
	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

type workflowServer struct {
	workflowservice.UnimplementedWorkflowServiceServer
}

func (workflowServer) GetSystemInfo(context.Context, *workflowservice.GetSystemInfoRequest) (*workflowservice.GetSystemInfoResponse, error) {
	return &workflowservice.GetSystemInfoResponse{}, nil
}

// Real SDK startup and health RPCs must traverse TLS, rather than merely store
// a tls.Config. Handshake failures are bounded by the RPC deadline, no sleeps.
func TestTemporalClientOverTLS(t *testing.T) {
	f := tlsfixture.New(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{f.Server}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: f.Roots})))
	workflowservice.RegisterWorkflowServiceServer(server, workflowServer{})
	healthServer := health.NewServer()
	healthServer.SetServingStatus("temporal.api.workflowservice.v1.WorkflowService", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(server, healthServer)
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	defer func() { server.Stop(); <-done }()
	for _, tc := range []struct {
		name, ca, hostname string
		ok                 bool
	}{
		{"private CA and client certificate", f.CAFile, "dependency.test", true},
		{"untrusted CA", "", "dependency.test", false},
		{"wrong server name", f.CAFile, "wrong.test", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, err := (outbound.TLS{CAFile: tc.ca, ServerName: tc.hostname, CertFile: f.CertFile, KeyFile: f.KeyFile}).Build(true)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			c, err := orchestration.Dial(ctx, listener.Addr().String(), config)
			if err == nil {
				defer c.Close()
				_, err = c.CheckHealth(ctx, nil)
			}
			if (err == nil) != tc.ok {
				t.Fatalf("Temporal TLS RPC error=%v, want success=%v", err, tc.ok)
			}
		})
	}
}
