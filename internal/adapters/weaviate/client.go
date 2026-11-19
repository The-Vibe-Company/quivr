package weaviate

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	wv "github.com/weaviate/weaviate-go-client/v6"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/The-Vibe-Company/quivr/internal/buildinfo"
	"github.com/The-Vibe-Company/quivr/internal/outbound"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
)

// IntegrationHeader identifies Quivr to the index on every request.
const IntegrationHeader = "X-Weaviate-Client-Integration"

// requestTimeout bounds one index request outside purge.
const requestTimeout = 4 * time.Second

// defaultGRPCPort is where the index serves gRPC unless configured otherwise.
const defaultGRPCPort = "50051"

// Config locates the index. The client library reaches it over REST (URL) and
// gRPC (GRPCAddress) with one scheme for both.
type Config struct {
	// URL is the REST endpoint: http or https, a host and an optional port.
	URL string
	// GRPCAddress is host:port; empty selects the URL host on port 50051.
	GRPCAddress string
	APIKey      string
	TLS         outbound.TLS
}

// ErrGRPCAddress reports an invalid Config.GRPCAddress, so a caller can name
// the setting at fault.
var ErrGRPCAddress = errors.New("weaviate: gRPC address must be host:port")

// New validates cfg without opening a connection; the first request connects.
func New(cfg Config) (*Store, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("weaviate: endpoint must be an http or https URL")
	}
	if strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errors.New("weaviate: endpoint must be a scheme, host and port only")
	}
	if _, err = cfg.TLS.ForURL(cfg.URL); err != nil {
		return nil, fmt.Errorf("weaviate TLS: %w", err)
	}
	if cfg.TLS.HasSettings() {
		// The client library builds its own HTTP client and gRPC channel.
		return nil, errors.New("weaviate TLS: a custom CA, client certificate or server name is not supported; use a certificate the system trusts")
	}
	httpPort := u.Port()
	if httpPort == "" {
		httpPort = "80"
		if u.Scheme == "https" {
			httpPort = "443"
		}
	}
	grpcHost, grpcPort := u.Hostname(), defaultGRPCPort
	if cfg.GRPCAddress != "" {
		if grpcHost, grpcPort, err = net.SplitHostPort(cfg.GRPCAddress); err != nil || grpcHost == "" || grpcPort == "" {
			return nil, ErrGRPCAddress
		}
	}
	options := []wv.Option{
		wv.WithScheme(u.Scheme),
		wv.WithHTTPHost(clientHost(u.Hostname())), wv.WithHTTPPort(httpPort),
		wv.WithGRPCHost(clientHost(grpcHost)), wv.WithGRPCPort(grpcPort),
		wv.WithHeader(http.Header{IntegrationHeader: {"quivr/" + buildinfo.Version}}),
		// A ceiling only: every request carries its own shorter deadline.
		wv.WithTimeout(retrieval.MaximumPurgeTimeout),
	}
	if cfg.APIKey != "" {
		options = append(options, wv.WithAPIKey(cfg.APIKey))
	}
	return &Store{apiKey: cfg.APIKey, options: options, vectors: &sync.Map{}}, nil
}

// clientHost brackets an IPv6 literal: the client library joins host and port.
func clientHost(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

// connect opens the client on first use, within the caller's deadline. A
// failed attempt is retried by the next request, so a process may start
// before the index does.
func (s *Store) connect(ctx context.Context) (*wv.Client, error) {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	if s.client != nil {
		return s.client, nil
	}
	client, err := wv.NewClient(ctx, s.options...)
	if err != nil {
		return nil, err
	}
	s.client = client
	return client, nil
}

// Close releases the index connections.
func (s *Store) Close() error {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	if s.client == nil {
		return nil
	}
	client := s.client
	s.client = nil
	return client.Close()
}

// request runs one index request under its own deadline and trace span; the
// first one also opens the connection inside both. Errors keep the provider's
// explanation with the API key removed; callers that face users replace them
// with fixed messages.
func (s *Store) request(ctx context.Context, timeout time.Duration, call func(context.Context, *wv.Client) error) error {
	caller := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ctx, span := telemetry.Start(ctx, "weaviate.request")
	defer span.End()
	client, err := s.connect(ctx)
	connected := err == nil
	if connected {
		err = call(ctx, client)
	}
	telemetry.Fail(span, err)
	if err == nil {
		return nil
	}
	if caller.Err() == nil && (ctx.Err() == context.DeadlineExceeded || status.Code(err) == codes.DeadlineExceeded) {
		return requestError{message: s.redact(err.Error()), timeout: true, cause: context.DeadlineExceeded}
	}
	failure := requestError{message: s.redact(err.Error()), cause: sentinel(err), unconnected: !connected, http: httpStatus(err)}
	if failure.cause == nil {
		failure.cause = sentinel(caller.Err())
	}
	// Only the index's own gRPC status says how a call ended.
	if answered, ok := status.FromError(err); ok {
		failure.grpc, failure.code = true, answered.Code()
	}
	return failure
}

// call is request with the default deadline.
func (s *Store) call(ctx context.Context, call func(context.Context, *wv.Client) error) error {
	return s.request(ctx, requestTimeout, call)
}

// requestError carries what callers classify on without an unwrap chain that
// could hold the API key.
type requestError struct {
	message string
	timeout bool
	cause   error
	// grpc is set when the index answered with a gRPC status, held in code.
	grpc bool
	code codes.Code
	http int
	// unconnected is set when the client could not be opened at all.
	unconnected bool
}

func (e requestError) Error() string   { return e.message }
func (e requestError) Timeout() bool   { return e.timeout }
func (e requestError) Temporary() bool { return e.timeout }
func (e requestError) Is(target error) bool {
	return e.cause != nil && target == e.cause
}

var _ net.Error = requestError{}

func sentinel(err error) error {
	if err == nil {
		return nil
	}
	for _, target := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, target) {
			return target
		}
	}
	return nil
}

func httpStatus(err error) int {
	var failed *wv.HTTPError
	if errors.As(err, &failed) {
		return failed.Code
	}
	return 0
}

// notFound reports a definite HTTP 404 from the index.
func notFound(err error) bool {
	var failed requestError
	return errors.As(err, &failed) && failed.http == http.StatusNotFound
}

// lost reports that the index may have applied a request whose reply never
// arrived: the request timed out, was cancelled, or the connection dropped
// with a gRPC status that says so. An error raised before sending, such as a
// value the client could not encode, is a definite failure.
func lost(err error) bool {
	var failed requestError
	if !errors.As(err, &failed) || failed.unconnected {
		return false
	}
	if failed.timeout || failed.cause != nil {
		return true
	}
	return failed.grpc && (failed.code == codes.Unavailable || failed.code == codes.Canceled || failed.code == codes.DeadlineExceeded)
}

// unreachable reports a failure to reach the index at all, as opposed to a
// request it refused.
func unreachable(err error) bool {
	var failed requestError
	if !errors.As(err, &failed) {
		return false
	}
	return failed.unconnected || lost(err) || (!failed.grpc && failed.http == 0)
}

// tooLarge reports a reply over the index's gRPC message limit.
func tooLarge(err error) bool {
	var failed requestError
	return errors.As(err, &failed) && failed.grpc && failed.code == codes.ResourceExhausted
}
