package temporal

import (
	"context"
	"crypto/tls"
	"fmt"
	"go.temporal.io/sdk/client"
)

// Dial is shared by worker startup and adapter handshake proof.
func Dial(ctx context.Context, address string, config *tls.Config) (client.Client, error) {
	c, err := client.DialContext(ctx, client.Options{HostPort: address, ConnectionOptions: client.ConnectionOptions{TLS: config}})
	if err != nil {
		return nil, fmt.Errorf("temporal connection: %w", err)
	}
	return c, nil
}
