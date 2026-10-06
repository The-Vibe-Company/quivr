package temporal

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"

	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"

	"go.temporal.io/sdk/workflow"
)

// Dial is shared by worker startup and adapter handshake proof.
func Dial(ctx context.Context, address string, config *tls.Config) (client.Client, error) {
	interceptors := []interceptor.ClientInterceptor{&correlationInterceptor{}}
	if telemetry.Enabled() {
		tracing, err := tracingInterceptor()
		if err != nil {
			return nil, err
		}
		interceptors = append(interceptors, tracing)
	}
	c, err := client.DialContext(ctx, client.Options{HostPort: address, Interceptors: interceptors, ContextPropagators: []workflow.ContextPropagator{requestIDPropagation{}}, Logger: newTemporalLogger(slog.Default()), ConnectionOptions: client.ConnectionOptions{TLS: config}})
	if err != nil {
		return nil, fmt.Errorf("temporal connection: %w", err)
	}
	return c, nil
}
