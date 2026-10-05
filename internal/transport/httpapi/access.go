package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
	"github.com/The-Vibe-Company/quivr/internal/logging"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
)

func (a *API) serveAccess(w http.ResponseWriter, r *http.Request) {
	if a.processWork != nil {
		r = r.WithContext(lifecycle.WithWorkContext(r.Context(), a.processWork))
	}
	keyID := ""
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		if _, valid := a.Keys[token]; valid {
			digest := sha256.Sum256([]byte(token))
			keyID = hex.EncodeToString(digest[:16])
		}
	}
	r.Pattern = a.router.Pattern(r)
	logRequest(w, r, keyID, a.pushClientIP(r).String(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.draining != nil && a.draining() {
			writeError(w, publicerr.ContentUnavailable, nil)
			return
		}
		a.servePushAudited(w, r)
	}))
}

// WithDraining stops public request admissions during process shutdown.
func WithDraining(draining func() bool) Option { return func(a *API) { a.draining = draining } }

// WithLifecycle also binds durable request cleanup to the process grace budget.
func WithLifecycle(group *lifecycle.Group) Option {
	return func(a *API) {
		a.draining = group.Draining
		a.processWork = lifecycle.WorkContext(group.Context())
	}
}

// AccessLog adds the same event envelope to private probe HTTP requests.
// The handler must use registered route patterns rather than raw request paths.
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		logRequest(w, r, "", ip, next)
	})
}

func logRequest(w http.ResponseWriter, r *http.Request, keyID, ip string, next http.Handler) {
	start := time.Now()
	var requestID [16]byte
	_, _ = rand.Read(requestID[:])
	id := hex.EncodeToString(requestID[:])
	w.Header().Set("X-Request-ID", id)
	r = r.WithContext(logging.WithRequestID(r.Context(), id))
	observed := &responseWriter{ResponseWriter: w}
	defer func() {
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		method := r.Method
		switch method {
		case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE":
		default:
			method = "OTHER"
		}
		status := observed.status
		if status == 0 {
			status = http.StatusOK
		}
		slog.InfoContext(r.Context(), "http request", "request_id", id, "route", route, "method", method,
			"status", status, "duration_ms", float64(time.Since(start).Microseconds())/1000,
			"response_size", observed.size, "client_ip", ip, "api_key_id", keyID, "error_code", observed.errorCode)
	}()
	next.ServeHTTP(observed, r)
}

type responseWriter struct {
	http.ResponseWriter
	status, size int
	errorCode    string
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responseWriter) SetErrorCode(code string)    { w.errorCode = code }
func (w *responseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	if status >= 200 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *responseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.size += n
	return n, err
}
func (w *responseWriter) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}
