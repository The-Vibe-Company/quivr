package weaviate

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"

	"github.com/The-Vibe-Company/quivr/internal/outbound"
)

const (
	defaultGRPCPoolSize       = 4
	grpcInitialWindowSize     = 1 << 20 // 1 MiB per HTTP/2 stream
	grpcInitialConnWindowSize = 4 << 20 // 4 MiB per HTTP/2 connection
	grpcKeepaliveTime         = 30 * time.Second
	grpcKeepaliveTimeout      = 10 * time.Second
)

var ErrGRPCAddress = errors.New("weaviate_grpc_address must be a host and port")

type grpcConnPool struct {
	conns    []*grpc.ClientConn
	next     atomic.Uint64
	inFlight atomic.Int32
}

func isUsableConn(conn *grpc.ClientConn) bool {
	state := conn.GetState()
	return state != connectivity.TransientFailure && state != connectivity.Shutdown
}

func (p *grpcConnPool) pickRoundRobin() *grpc.ClientConn {
	n := len(p.conns)
	start := p.next.Add(1) - 1
	for offset := range uint64(n) {
		conn := p.conns[(start+offset)%uint64(n)]
		if isUsableConn(conn) {
			return conn
		}
	}
	return p.conns[start%uint64(n)]
}

func (p *grpcConnPool) pick() *grpc.ClientConn {
	if len(p.conns) == 1 {
		return p.conns[0]
	}
	if p.inFlight.Load() <= 4 && isUsableConn(p.conns[0]) {
		return p.conns[0]
	}
	return p.pickRoundRobin()
}

// prepareProbe resets reconnect backoff across pooled connections so a
// half-open breaker probe attempts an immediate dial instead of failing on a
// cached TRANSIENT_FAILURE state.
func (p *grpcConnPool) prepareProbe(ctx context.Context) {
	for _, conn := range p.conns {
		conn.ResetConnectBackoff()
		conn.Connect()
	}
	probeConn := p.conns[0]
	if probeConn.GetState() != connectivity.TransientFailure {
		return
	}
	waitCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	probeConn.WaitForStateChange(waitCtx, connectivity.TransientFailure)
}

func (p *grpcConnPool) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	p.inFlight.Add(1)
	defer p.inFlight.Add(-1)
	return p.pick().Invoke(ctx, method, args, reply, opts...)
}

func (p *grpcConnPool) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return p.pickRoundRobin().NewStream(ctx, desc, method, opts...)
}

type grpcPooledClient struct {
	pb.WeaviateClient
	pool      *grpcConnPool
	breaker   *circuitBreaker
	authMD    metadata.MD
	closeOnce sync.Once
}

func (c *grpcPooledClient) close() {
	c.closeOnce.Do(func() {
		for _, conn := range c.pool.conns {
			_ = conn.Close()
		}
	})
}

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
	target := "dns:///" + grpcAddress
	conns := make([]*grpc.ClientConn, 0, defaultGRPCPoolSize)
	for range defaultGRPCPoolSize {
		transport := insecure.NewCredentials()
		if config != nil {
			transport = credentials.NewTLS(config.Clone())
		}
		conn, dialErr := grpc.NewClient(
			target,
			grpc.WithTransportCredentials(transport),
			grpc.WithInitialWindowSize(grpcInitialWindowSize),
			grpc.WithInitialConnWindowSize(grpcInitialConnWindowSize),
			grpc.WithConnectParams(grpc.ConnectParams{
				Backoff: backoff.Config{
					BaseDelay:  100 * time.Millisecond,
					Multiplier: 1.6,
					Jitter:     0.2,
					MaxDelay:   5 * time.Second,
				},
				MinConnectTimeout: 2 * time.Second,
			}),
			grpc.WithKeepaliveParams(keepalive.ClientParameters{
				Time:                grpcKeepaliveTime,
				Timeout:             grpcKeepaliveTimeout,
				PermitWithoutStream: false,
			}),
		)
		if dialErr != nil {
			for _, openConn := range conns {
				_ = openConn.Close()
			}
			return nil, nil, ErrGRPCAddress
		}
		conn.Connect()
		conns = append(conns, conn)
	}
	pool := &grpcConnPool{conns: conns}
	var authMD metadata.MD
	if store.apiKey != "" {
		authMD = metadata.Pairs("authorization", "Bearer "+store.apiKey)
	}
	client := &grpcPooledClient{
		WeaviateClient: pb.NewWeaviateClient(pool),
		pool:           pool,
		breaker:        newCircuitBreaker(defaultBreakerThreshold, defaultBreakerCooldown, nil),
		authMD:         authMD,
	}
	store.grpc = client
	return store, client.close, nil
}
