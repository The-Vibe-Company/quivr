// Package logging provides the engine's structured logging boundary.
package logging

import (
	"context"
	"errors"
	"go.opentelemetry.io/otel/trace"
	"io"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	redactedValue = "[REDACTED]"
	defaultValue  = "unspecified"
)

// Options controls the JSON logger returned by New.
//
// Level accepts exactly debug, info, warn or error. An empty level selects
// info. Empty identity fields receive safe process defaults.
type Options struct {
	Level       string
	Service     string
	Version     string
	Instance    string
	Environment string
	Secrets     []string
}

// Diagnostic marks engine-owned codes, field names and safe explanations.
// Use only text derived from engine definitions, never configuration values,
// request data or raw errors. Unlike values, diagnostic metadata must survive
// a coincidental match with the process's secret inventory.
type Diagnostic string

type requestIDKey struct{}

// WithRequestID returns a context carrying id for the logging handler to add
// to every event emitted with that context.
func WithRequestID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the request ID attached by WithRequestID, or an empty
// string when the context has no request ID.
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// New constructs a JSON logger with a stable engine identity and a
// conservative redaction boundary. Records are written to writer.
func New(writer io.Writer, options Options) (*slog.Logger, error) {
	if writer == nil {
		return nil, errors.New("log writer is nil")
	}
	level, err := parseLevel(options.Level)
	if err != nil {
		return nil, err
	}
	redactor := sanitizer{secrets: normalizeSecrets(options.Secrets)}

	service := options.Service
	if service == "" {
		service = "quivr"
	}
	version := options.Version
	if version == "" {
		version = defaultValue
	}
	instance := options.Instance
	if instance == "" {
		instance = processInstance()
	}
	environment := options.Environment
	if environment == "" {
		environment = defaultValue
	}

	jsonHandler := slog.NewJSONHandler(writer, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key == "time" {
				attr.Key = "ts"
				attr.Value = slog.TimeValue(attr.Value.Time().UTC())
			}
			return attr
		},
	})
	base := []slog.Attr{
		slog.String("service", redactor.sanitizeString(service)),
		slog.String("version", redactor.sanitizeString(version)),
		slog.String("instance", redactor.sanitizeString(instance)),
		slog.String("environment", redactor.sanitizeString(environment)),
	}
	return slog.New(&safeHandler{
		next:    jsonHandler.WithAttrs(base),
		secrets: redactor.secrets,
	}), nil
}

func parseLevel(raw string) (slog.Level, error) {
	switch raw {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		// Do not include the supplied value: it may have come from a secret
		// environment variable and this error is often logged during startup.
		return 0, errors.New("invalid log level")
	}
}

func processInstance() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return host + ":" + strconv.Itoa(os.Getpid())
}

type safeHandler struct {
	next        slog.Handler
	secrets     []string
	redactGroup bool
	attrs       []slog.Attr
	groups      []string
}

func (h *safeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *safeHandler) Handle(ctx context.Context, record slog.Record) error {
	clean := slog.NewRecord(record.Time, record.Level, h.sanitizeString(record.Message), record.PC)
	id := RequestID(ctx)
	sc := trace.SpanContextFromContext(ctx)
	for _, attr := range h.attrs {
		clean.AddAttrs(withoutRequestID(attr, id != ""))
	}
	var attrs []slog.Attr
	record.Attrs(func(attr slog.Attr) bool {
		attrs = append(attrs, withoutRequestID(h.sanitizeAttr(attr, h.redactGroup), id != ""))
		return true
	})
	clean.AddAttrs(wrapGroups(h.groups, attrs)...)
	if id != "" {
		clean.AddAttrs(slog.String("request_id", h.sanitizeString(id)))
	}
	clean.AddAttrs(slog.String("trace_id", traceID(sc)), slog.String("span_id", spanID(sc)))
	return h.next.Handle(ctx, clean)
}

func withoutRequestID(attr slog.Attr, remove bool) slog.Attr {
	if !remove && attr.Key != "trace_id" && attr.Key != "span_id" {
		return attr
	}
	if attr.Key == "trace_id" || attr.Key == "span_id" {
		return slog.Attr{}
	}
	if attr.Key == "request_id" {
		return slog.Attr{}
	}
	if attr.Value.Kind() == slog.KindGroup {
		var children []slog.Attr
		for _, child := range attr.Value.Group() {
			children = append(children, withoutRequestID(child, true))
		}
		attr.Value = slog.GroupValue(children...)
	}
	return attr
}

func wrapGroups(groups []string, attrs []slog.Attr) []slog.Attr {
	for i := len(groups) - 1; i >= 0; i-- {
		attrs = []slog.Attr{{Key: groups[i], Value: slog.GroupValue(attrs...)}}
	}
	return attrs
}

func (h *safeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	copy := *h
	var clean []slog.Attr
	for _, attr := range attrs {
		clean = append(clean, h.sanitizeAttr(attr, h.redactGroup))
	}
	copy.attrs = append(append([]slog.Attr(nil), h.attrs...), wrapGroups(h.groups, clean)...)
	return &copy
}

func (h *safeHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	copy := *h
	copy.groups = append(append([]string(nil), h.groups...), fieldName(h.sanitizeString(name)))
	copy.redactGroup = h.redactGroup || sensitiveKey(name)
	return &copy
}

// Envelope fields remain authoritative even if a dependency logs its own
// version, level or service. Preserve that metadata under a distinct name.
func fieldName(key string) string {
	switch key {
	case "service", "version", "instance", "environment", "ts", "time", "level", "msg":
		return "event_" + key
	}
	return key
}

type sanitizer struct {
	secrets []string
}

func (h *safeHandler) sanitizer() sanitizer {
	return sanitizer{secrets: h.secrets}
}

func (h *safeHandler) sanitizeString(value string) string {
	return h.sanitizer().sanitizeString(value)
}

func (h *safeHandler) sanitizeAttr(attr slog.Attr, redactAll bool) slog.Attr {
	if attr.Equal(slog.Attr{}) {
		return attr
	}
	if redactAll || sensitiveKey(attr.Key) || errorKey(attr.Key) {
		return slog.String(attr.Key, redactedValue)
	}
	return slog.Attr{Key: fieldName(attr.Key), Value: h.sanitizeValue(attr.Value)}
}

func (h *safeHandler) sanitizeValue(value slog.Value) slog.Value {
	return h.sanitizer().sanitizeValue(value)
}

func (s sanitizer) sanitizeValue(value slog.Value) slog.Value {
	switch value.Kind() {
	case slog.KindString:
		return slog.StringValue(s.sanitizeString(value.String()))
	case slog.KindBool, slog.KindInt64, slog.KindUint64, slog.KindFloat64,
		slog.KindDuration, slog.KindTime:
		return value
	case slog.KindGroup:
		attrs := value.Group()
		clean := make([]slog.Attr, 0, len(attrs))
		for _, attr := range attrs {
			clean = append(clean, s.sanitizeAttr(attr, false))
		}
		return slog.GroupValue(clean...)
	case slog.KindLogValuer:
		// Resolving an arbitrary LogValuer would allow provider-owned objects
		// to serialize configuration fields before this boundary sees them.
		return slog.StringValue(redactedValue)
	case slog.KindAny:
		if diagnostic, ok := value.Any().(Diagnostic); ok {
			return slog.StringValue(string(diagnostic))
		}
		if value.Any() == nil {
			return slog.AnyValue(nil)
		}
		// Unknown Any values may be maps, structs, provider objects or
		// Stringers containing credentials. Keep the boundary conservative.
		return slog.StringValue(redactedValue)
	default:
		return slog.StringValue(redactedValue)
	}
}

func (s sanitizer) sanitizeAttr(attr slog.Attr, redactAll bool) slog.Attr {
	if attr.Equal(slog.Attr{}) {
		return attr
	}
	if redactAll || sensitiveKey(attr.Key) || errorKey(attr.Key) || valueIsError(attr.Value) {
		return slog.String(attr.Key, redactedValue)
	}
	return slog.Attr{Key: fieldName(attr.Key), Value: s.sanitizeValue(attr.Value)}
}

func valueIsError(value slog.Value) bool {
	if value.Kind() != slog.KindAny {
		return false
	}
	_, ok := value.Any().(error)
	return ok
}

func normalizeSecrets(secrets []string) []string {
	seen := make(map[string]struct{}, len(secrets))
	clean := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if _, ok := seen[secret]; ok {
			continue
		}
		seen[secret] = struct{}{}
		clean = append(clean, secret)
	}
	sort.Slice(clean, func(i, j int) bool { return len(clean[i]) > len(clean[j]) })
	return clean
}

var (
	urlPattern              = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s"'<>]+`)
	authorizationPattern    = regexp.MustCompile(`(?i)((?:"(?:authorization|cookie|set[-_]?cookie)"|\b(?:authorization|cookie|set[-_]?cookie)\b)\s*[:=]\s*)[^\r\n]*`)
	secretAssignmentPattern = regexp.MustCompile(`(?i)((?:"[^"\r\n]*(?:authorization|password|token|secret|credential|api[-_]?key|private[-_]?key|cookie)[^"\r\n]*"|'[^'\r\n]*(?:authorization|password|token|secret|credential|api[-_]?key|private[-_]?key|cookie)[^'\r\n]*'|\b[\w.-]*(?:authorization|password|token|secret|credential|api[-_]?key|private[-_]?key|cookie)[\w.-]*\b)\s*[:=]\s*)(\[REDACTED\]|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\s,;}\]]+)`)
)

func (s sanitizer) sanitizeString(value string) string {
	for _, secret := range s.secrets {
		value = strings.ReplaceAll(value, secret, redactedValue)
	}
	value = urlPattern.ReplaceAllStringFunc(value, sanitizeURL)
	value = secretAssignmentPattern.ReplaceAllString(value, `${1}`+redactedValue)
	return authorizationPattern.ReplaceAllString(value, `${1}`+redactedValue)
}

func sanitizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return redactedValue
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	return u.String()
}

func sensitiveKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	lower = strings.ReplaceAll(lower, "-", "_")
	// These are operator-facing metadata, not credential values. In
	// particular, an opaque API-key fingerprint is safe to retain in access
	// records, and the startup summary reports whether deposits are enabled.
	switch lower {
	case "api_key_id", "credential_deposits":
		return false
	}
	lower = strings.ReplaceAll(lower, "_", "")
	for _, term := range []string{"authorization", "password", "token", "secret", "credential", "configuration", "apikey", "privatekey", "cookie"} {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

func errorKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	switch lower {
	case "error", "errors", "err", "cause", "causes", "exception", "exceptions":
		return true
	}
	return strings.HasSuffix(lower, "_error") || strings.HasSuffix(lower, ".error")
}

func traceID(sc trace.SpanContext) string {
	if sc.IsValid() {
		return sc.TraceID().String()
	}
	return ""
}
func spanID(sc trace.SpanContext) string {
	if sc.IsValid() {
		return sc.SpanID().String()
	}
	return ""
}
