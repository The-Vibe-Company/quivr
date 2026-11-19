package weaviate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	wv "github.com/weaviate/weaviate-go-client/v6"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/The-Vibe-Company/quivr/internal/adapters/weaviate"
)

// index locates the index a test talks to: the real one from the local
// stack, or a wire proxy in front of it.
type index struct {
	URL    string `json:"weaviate_url"`
	GRPC   string `json:"weaviate_grpc_address"`
	APIKey string `json:"weaviate_api_key"`
}

// realIndex reads the stack's configuration, skipping outside `make verify`.
func realIndex(t *testing.T) index {
	t.Helper()
	path := os.Getenv("QUIVR_ADAPTER_CONFIG")
	if path == "" {
		t.Skip("make verify real adapters")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg index
	if err = json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	if cfg.GRPC == "" {
		u, err := url.Parse(cfg.URL)
		if err != nil {
			t.Fatal(err)
		}
		cfg.GRPC = net.JoinHostPort(u.Hostname(), "50051")
	}
	return cfg
}

// open builds the adapter for an index and closes it with the test.
func (i index) open(t *testing.T) *weaviate.Store {
	t.Helper()
	store, err := weaviate.New(weaviate.Config{URL: i.URL, GRPCAddress: i.GRPC, APIKey: i.APIKey})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// client is the client library itself, for assertions on what the adapter
// stored. It never goes through the adapter under test.
func (i index) client(t *testing.T) *wv.Client {
	t.Helper()
	u, err := url.Parse(i.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(i.GRPC)
	if err != nil {
		t.Fatal(err)
	}
	options := []wv.Option{wv.WithScheme(u.Scheme), wv.WithHTTPHost(u.Hostname()), wv.WithHTTPPort(u.Port()), wv.WithGRPCHost(host), wv.WithGRPCPort(port)}
	if i.APIKey != "" {
		options = append(options, wv.WithAPIKey(i.APIKey))
	}
	client, err := wv.NewClient(context.Background(), options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// rest sends one JSON request straight to the index's REST API, to seed or
// read schemas in shapes the adapter no longer writes. It returns the status.
func (i index) rest(t *testing.T, method, path string, in, out any) int {
	t.Helper()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, i.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if i.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+i.APIKey)
	}
	// Bounded, so an unreachable index fails the test instead of hanging it.
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil && res.StatusCode < 300 {
		if err = json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return res.StatusCode
}

// wireRequest is one request the adapter sent, as the index would see it.
type wireRequest struct {
	// Transport is "rest" or "grpc".
	Transport string
	// Name is "METHOD /path" for REST and the full method for gRPC, for
	// example "DELETE /v1/objects/C/id" or "/weaviate.v1.Weaviate/Search".
	Name string
	// Header holds the request headers, or the gRPC metadata with the same
	// canonical keys.
	Header http.Header
}

// wire is a REST and a gRPC listener the adapter can be pointed at. It
// records every request it receives.
type wire struct {
	index
	mu   sync.Mutex
	seen []wireRequest
}

// Requests returns what was received so far.
func (w *wire) Requests() []wireRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]wireRequest(nil), w.seen...)
}

func (w *wire) record(r wireRequest) {
	w.mu.Lock()
	w.seen = append(w.seen, r)
	w.mu.Unlock()
}

// rawFrame carries a gRPC message without decoding it.
type rawFrame struct{ payload []byte }

type rawCodec struct{}

func (rawCodec) Marshal(v any) ([]byte, error) { return v.(*rawFrame).payload, nil }
func (rawCodec) Unmarshal(data []byte, v any) error {
	v.(*rawFrame).payload = append([]byte(nil), data...)
	return nil
}
func (rawCodec) Name() string { return "proto" }

var _ encoding.Codec = rawCodec{}

// serve starts a wire answered by the test: rest handles REST requests and
// call answers one unary gRPC call with the encoded reply (decode and encode
// with the protocol package and proto.Marshal). Both stop with the test.
func serve(t *testing.T, rest func(http.ResponseWriter, *http.Request, wireRequest), call func(ctx context.Context, request wireRequest, in []byte) ([]byte, error)) *wire {
	t.Helper()
	w := &wire{}
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		request := wireRequest{Transport: "rest", Name: r.Method + " " + r.URL.Path, Header: r.Header.Clone()}
		w.record(request)
		rest(rw, r, request)
	}))
	t.Cleanup(server.Close)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// Every call the adapter makes is unary: one message each way.
	grpcServer := grpc.NewServer(grpc.ForceServerCodec(rawCodec{}), grpc.MaxRecvMsgSize(256<<20), grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		method, _ := grpc.MethodFromServerStream(stream)
		incoming, _ := metadata.FromIncomingContext(stream.Context())
		header := http.Header{}
		for key, values := range incoming {
			for _, value := range values {
				header.Add(key, value)
			}
		}
		request := wireRequest{Transport: "grpc", Name: method, Header: header}
		w.record(request)
		var in rawFrame
		if err := stream.RecvMsg(&in); err != nil {
			return err
		}
		out, err := call(stream.Context(), request, in.payload)
		if err != nil {
			return err
		}
		return stream.SendMsg(&rawFrame{payload: out})
	}))
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)
	w.index = index{URL: server.URL, GRPC: listener.Addr().String()}
	return w
}

// wireProxy forwards REST and gRPC to a real index. A test observes every
// request, fails one before it reaches the index, or loses its reply after
// the index applied it, without a seam inside the adapter.
type wireProxy struct {
	*wire
	// Before runs ahead of each request; an error fails the request unsent.
	Before func(wireRequest) error
	// LoseReply runs after the index answered; true drops the answer.
	LoseReply func(wireRequest) bool
}

// proxy starts a wire proxy in front of the index; it stops with the test.
// Set Before and LoseReply before the adapter sends requests.
func (i index) proxy(t *testing.T) *wireProxy {
	t.Helper()
	p := &wireProxy{}
	target, err := url.Parse(i.URL)
	if err != nil {
		t.Fatal(err)
	}
	forward := httputil.NewSingleHostReverseProxy(target)
	transport := insecure.NewCredentials()
	if target.Scheme == "https" {
		transport = credentials.NewTLS(nil)
	}
	backend, err := grpc.NewClient(i.GRPC, grpc.WithTransportCredentials(transport), grpc.WithDefaultCallOptions(grpc.ForceCodec(rawCodec{}), grpc.MaxCallRecvMsgSize(256<<20)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	p.wire = serve(t, func(w http.ResponseWriter, r *http.Request, request wireRequest) {
		if p.Before != nil && p.Before(request) != nil {
			panic(http.ErrAbortHandler)
		}
		recorder := httptest.NewRecorder()
		forward.ServeHTTP(recorder, r)
		if p.LoseReply != nil && p.LoseReply(request) {
			panic(http.ErrAbortHandler)
		}
		for key, values := range recorder.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	}, func(ctx context.Context, request wireRequest, in []byte) ([]byte, error) {
		if p.Before != nil {
			if err := p.Before(request); err != nil {
				return nil, status.Error(codes.Unavailable, err.Error())
			}
		}
		// Forward the caller's own metadata; the connection sets the rest.
		forwarded := metadata.MD{}
		for key, values := range request.Header {
			key = strings.ToLower(key)
			if strings.HasPrefix(key, ":") || strings.HasPrefix(key, "grpc-") || key == "content-type" || key == "user-agent" || key == "te" {
				continue
			}
			forwarded.Set(key, values...)
		}
		var out rawFrame
		if err := backend.Invoke(metadata.NewOutgoingContext(ctx, forwarded), request.Name, &rawFrame{payload: in}, &out); err != nil {
			return nil, err
		}
		if p.LoseReply != nil && p.LoseReply(request) {
			return nil, status.Error(codes.Unavailable, "injected lost reply")
		}
		return out.payload, nil
	})
	p.APIKey = i.APIKey
	return p
}

// errInjected is the failure tests inject at the wire.
var errInjected = errors.New("injected index failure")
