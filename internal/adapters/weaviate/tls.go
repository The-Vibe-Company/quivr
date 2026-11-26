package weaviate

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/The-Vibe-Company/quivr/internal/outbound"
)

var ErrGRPCAddress = errors.New("weaviate_grpc_address must be a host and port")

func NewWithTLS(endpoint string, settings outbound.TLS, apiKey ...string) (*Store, error) {
	store, _, err := NewWithTransport(endpoint, "", settings, apiKey...)
	return store, err
}

// NewWithTransport uses the HTTP endpoint scheme to select TLS for both
// protocols. The caller owns the returned cleanup function.
func NewWithTransport(endpoint, grpcAddress string, settings outbound.TLS, apiKey ...string) (*Store, func(), error) {
	config, err := settings.ForURL(endpoint)
	if err != nil {
		return nil, nil, fmt.Errorf("weaviate TLS: %w", err)
	}
	store := newStore(endpoint, outbound.Transport(config), apiKey)
	store.Client.CheckRedirect = outbound.CheckRedirect
	if grpcAddress == "" {
		return store, func() {}, nil
	}
	host, port, err := net.SplitHostPort(grpcAddress)
	portNumber, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || host == "" || strings.ContainsAny(host, "/?#@ \\%\t\n\r") || portErr != nil || portNumber == 0 {
		return nil, nil, ErrGRPCAddress
	}
	transport := insecure.NewCredentials()
	if config != nil {
		transport = credentials.NewTLS(config)
	}
	conn, err := grpc.NewClient("dns:///"+grpcAddress, grpc.WithTransportCredentials(transport))
	if err != nil {
		return nil, nil, ErrGRPCAddress
	}
	store.grpc = pb.NewWeaviateClient(conn)
	return store, func() { _ = conn.Close() }, nil
}
