package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"encoding/hex"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"
	"io"
	"sync"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "loopback address")
	flag.Parse()
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		log.Fatal("listen must be a loopback IP address")
	}
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Handler: collectorHandler(), ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		if err := server.Serve(l); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	if err := json.NewEncoder(os.Stdout).Encode(map[string]string{"url": "http://" + l.Addr().String()}); err != nil {
		log.Fatal(err)
	}
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
}

// This receiver accepts real OTLP protobuf. Its bounded read view is only test
// evidence; it does not synthesize tracing behavior or inspect engine storage.
type observedSpan struct {
	Name       string         `json:"name"`
	TraceID    string         `json:"trace_id"`
	SpanID     string         `json:"span_id"`
	ParentID   string         `json:"parent_span_id"`
	Attributes map[string]any `json:"attributes"`
}

func collectorHandler() http.Handler {
	var mu sync.Mutex
	spans := map[string][]observedSpan{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/traces", func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
		if err != nil {
			http.Error(w, "invalid export", 400)
			return
		}
		var exported collector.ExportTraceServiceRequest
		if proto.Unmarshal(raw, &exported) != nil {
			http.Error(w, "invalid export", 400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, resource := range exported.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				for _, span := range scope.Spans {
					id := hex.EncodeToString(span.TraceId)
					if _, ok := spans[id]; !ok && len(spans) >= 2048 {
						continue
					}
					if len(spans[id]) >= 4096 {
						continue
					}
					attrs := map[string]any{}
					for _, a := range span.Attributes {
						attrs[a.Key] = decodedValue(a.Value)
					}
					spans[id] = append(spans[id], observedSpan{span.Name, id, hex.EncodeToString(span.SpanId), hex.EncodeToString(span.ParentSpanId), attrs})
				}
			}
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	})
	mux.HandleFunc("POST /v1/metrics", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, 4<<20))
		w.Header().Set("Content-Type", "application/x-protobuf")
	})
	mux.HandleFunc("GET /traces/{trace_id}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"spans": spans[r.PathValue("trace_id")]})
	})
	return mux
}

func decodedValue(value *common.AnyValue) any {
	if value == nil {
		return nil
	}
	switch v := value.Value.(type) {
	case *common.AnyValue_StringValue:
		return v.StringValue
	case *common.AnyValue_BoolValue:
		return v.BoolValue
	case *common.AnyValue_IntValue:
		return v.IntValue
	case *common.AnyValue_DoubleValue:
		return v.DoubleValue
	case *common.AnyValue_BytesValue:
		return string(v.BytesValue)
	case *common.AnyValue_ArrayValue:
		values := make([]any, len(v.ArrayValue.Values))
		for i, item := range v.ArrayValue.Values {
			values[i] = decodedValue(item)
		}
		return values
	case *common.AnyValue_KvlistValue:
		values := map[string]any{}
		for _, item := range v.KvlistValue.Values {
			values[item.Key] = decodedValue(item.Value)
		}
		return values
	default:
		return nil
	}
}
