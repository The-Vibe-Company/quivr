package pluginhttp

import (
	"regexp"
	"sync/atomic"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
)

// Operations of the Contribution invocations the observer is told about.
const (
	OpNormalize            = "normalize"
	OpSegmentAndEmbed      = "segment_and_embed"
	OpEmbedQuery           = "embed_query"
	OpSearchRound          = "search_round"
	OpConnectorFetch       = "connector_fetch"
	OpConnectorReceive     = "connector_receive"
	OpCheckCredential      = "check_credential"
	OpDescribeAttachment   = "describe_attachment"
	OpUploadAttachment     = "upload_attachment"
	OpEvaluateSubscription = "evaluate_subscription"
)

// Error codes of an observed call that the plugin did not declare.
const (
	callUnavailable   = "plugin_unavailable"
	callInvalidOutput = "invalid_output"
	callPluginError   = "plugin_error"
)

// Call is one Contribution invocation of a pinned plugin, made on behalf of
// an Organization. ErrorCode is empty when the plugin answered a valid
// result; otherwise it is the code the plugin declared in its error
// envelope, plugin_unavailable or invalid_output. Nothing else the plugin
// wrote is passed on.
type Call struct {
	Organization, PluginID, Version, Operation string
	Duration                                   time.Duration
	ErrorCode                                  string
}

var observer atomic.Pointer[func(Call)]

// Observe sends every later Contribution invocation of every pinned plugin
// to f. The engine sets it once at startup; until then calls are not
// observed. f runs on the caller's goroutine and must not block.
func Observe(f func(Call)) { observer.Store(&f) }

// declaredCode bounds a code a plugin declared: it becomes a stored value.
var declaredCode = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// observe reports one invocation that started at started and ended with
// result or err, the outcome of the devhost call.
func observe(pin *plugins.Pin, org, operation string, started time.Time, result *devhost.Result, err error) {
	f := observer.Load()
	if f == nil {
		return
	}
	call := Call{Organization: org, PluginID: pin.Manifest.ID, Version: pin.Manifest.Version, Operation: operation, Duration: time.Since(started)}
	switch {
	case result == nil:
		call.ErrorCode = callUnavailable
	case result.Error != nil && declaredCode.MatchString(result.Error.Code):
		call.ErrorCode = result.Error.Code
	case result.Error != nil:
		call.ErrorCode = callPluginError
	case len(result.Issues) > 0 && (result.Status != 200 || result.Issues[0].Code == devhost.CodeInvalidErrorEnvelope):
		call.ErrorCode = callUnavailable
	case len(result.Issues) > 0:
		call.ErrorCode = callInvalidOutput
	}
	(*f)(call)
}
