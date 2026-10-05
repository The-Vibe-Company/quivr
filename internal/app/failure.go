package app

import (
	"errors"
	"log/slog"
	"strings"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
)

// LogFailure reports stable, engine-owned issue codes without serializing the
// raw error, which can include a configuration value or provider response.
func LogFailure(err error) {
	code := "process_failed"
	var pin *plugins.PinError
	if errors.As(err, &pin) {
		var codes []string
		for _, issue := range pin.Issues {
			switch issue.Code {
			case plugins.CodeInvalidPin, plugins.CodeUnreadable, plugins.CodeInvalidYAML, plugins.CodeSchema,
				plugins.CodeInvalidRange, plugins.CodeIncompatibleEngine, plugins.CodeIncompatiblePluginAPI,
				plugins.CodeReservedContribution, plugins.CodeForeignNamespace, plugins.CodeInvalidConfigSchema,
				plugins.CodeInvalidExtensionSchema, plugins.CodeDuplicateSecret, plugins.CodeInvalidManifest,
				plugins.CodeInvalidConfiguration, plugins.CodeInvalidExpressionSchema, plugins.CodeReservedField,
				plugins.CodeInvalidCredentialSchema, plugins.CodeInvalidModes, plugins.CodeForeignSpace,
				plugins.CodePluginConflict, plugins.CodeKindConflict, plugins.CodeSpaceConflict,
				plugins.CodeNamespaceConflict, plugins.CodeRouteConflict, plugins.CodePluginDependency:
				codes = append(codes, issue.Code)
			}
		}
		if len(codes) > 0 {
			code = strings.Join(codes, ",")
		}
	}
	slog.Error("process failed", "event", "quivr.failed", "error_code", code)
}
