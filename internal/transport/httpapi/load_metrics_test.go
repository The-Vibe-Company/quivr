package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
)

// Owns HTTP metric labels and lifecycle at the real middleware boundary.
// Response/access-log tests cannot catch leaked metric labels or a stuck gauge.
func TestHTTPMetricsUseTemplatesAndTrackRequestLifetimes(t *testing.T) {
	m := telemetry.NewLoadMetrics()
	h, err := httpapi.New(knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{}, nil, catalogCursorKey, httpapi.WithLoadMetrics(m))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v0/corpora/private-one", "/v0/corpora/private-two"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path+"?secret=query", nil))
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("CUSTOM-SECRET", "/private-unmatched", nil))
	check := func(wants ...string) {
		t.Helper()
		var b strings.Builder
		m.Write(&b)
		for _, want := range wants {
			if !strings.Contains(b.String(), want) {
				t.Fatalf("missing %q in\n%s", want, b.String())
			}
		}
		for _, secret := range []string{"private-one", "private-two", "private-unmatched", "CUSTOM-SECRET", "secret=query"} {
			if strings.Contains(b.String(), secret) {
				t.Fatalf("metric leaked %q: %s", secret, b.String())
			}
		}
	}
	check(`quivr_http_requests_total{route="/v0/corpora/{corpus_id}",method="GET",status_class="4xx"} 2`,
		`quivr_http_request_duration_seconds_count{route="/v0/corpora/{corpus_id}",method="GET"} 2`,
		`quivr_http_requests_in_flight{route="/v0/corpora/{corpus_id}",method="GET"} 0`,
		`quivr_http_requests_total{route="unmatched",method="OTHER",status_class="4xx"} 1`)
	// Probe templates must be resolved before dispatch; flush and cancellation
	// must preserve the original response and release the streaming request.
	probes := http.NewServeMux()
	probes.HandleFunc("GET /stream/{id}", func(w http.ResponseWriter, r *http.Request) {
		check(`quivr_http_requests_in_flight{route="/stream/{id}",method="GET"} 1`)
		http.NewResponseController(w).Flush()
		<-r.Context().Done()
	})
	entered, release := make(chan struct{}), make(chan struct{})
	probes.HandleFunc("GET /panic-committed", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(202); panic("sentinel") })
	probes.HandleFunc("GET /panic", func(http.ResponseWriter, *http.Request) { panic("sentinel") })
	probes.HandleFunc("GET /held", func(http.ResponseWriter, *http.Request) { close(entered); <-release })
	m.RegisterRoutes("/stream/{id}", "/panic", "/panic-committed", "/held")
	p := httpapi.AccessLog(probes, m)
	p.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/stream/private-two", nil))
	check(`quivr_http_requests_total{route="/stream/{id}",method="POST",status_class="4xx"} 1`)
	ctx, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	// Flush forwards to the recorder. Cancel on its first actual flush.
	p.ServeHTTP(cancelOnFlush{rec, cancel}, httptest.NewRequest("GET", "/stream/private-one", nil).WithContext(ctx))
	if !rec.Flushed || rec.Code != 200 {
		t.Fatalf("stream response: %v", rec)
	}
	check(`quivr_http_requests_in_flight{route="/stream/{id}",method="GET"} 0`, `quivr_http_requests_total{route="/stream/{id}",method="GET",status_class="2xx"} 1`)
	for _, path := range []string{"/panic", "/panic-committed"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("middleware swallowed panic")
				}
			}()
			p.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
		}()
	}
	check(`quivr_http_requests_in_flight{route="/panic",method="GET"} 0`, `quivr_http_requests_total{route="/panic",method="GET",status_class="5xx"} 1`,
		`quivr_http_requests_in_flight{route="/panic-committed",method="GET"} 0`, `quivr_http_requests_total{route="/panic-committed",method="GET",status_class="2xx"} 1`)
	done := make(chan struct{})
	go func() { p.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/held", nil)); close(done) }()
	<-entered
	check(`quivr_http_requests_in_flight{route="/held",method="GET"} 1`)
	close(release)
	<-done
	check(`quivr_http_requests_in_flight{route="/held",method="GET"} 0`)
}

type cancelOnFlush struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w cancelOnFlush) Flush() { w.ResponseRecorder.Flush(); w.cancel() }
