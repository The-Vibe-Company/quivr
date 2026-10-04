package httpapi

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/registry"
)

const maxPublicErrorMessage = 2048

// boundedPublicText bounds operator diagnostics in bytes, without splitting
// a UTF-8 character. Individual values are bounded before building a message.
func boundedPublicText(text string, limit int) string {
	// JSON replaces invalid bytes; normalize first so encoding cannot expand
	// a value beyond the bound after it has been checked.
	text = strings.ToValidUTF8(text, "\uFFFD")
	if len(text) <= limit {
		return text
	}
	text = text[:limit-3]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text + "..."
}

// pluginErrorMessage keeps the issue list and typed operator context. Neither
// Issue.Message nor error text is public: both can contain dependency details.
func pluginErrorMessage(err error, fallback string) string {
	var coverage *registry.CoverageError
	if errors.As(err, &coverage) {
		const recovery = "; backfill the returning owner with POST /v0/admin/backfills, then retry"
		var message strings.Builder
		message.WriteString(fallback)
		for _, gap := range coverage.Gaps {
			fmt.Fprintf(&message, "; owner=%s space=%s missing_documents=%d missing_generations=%d", boundedPublicText(gap.Owner, 128), boundedPublicText(gap.Space, 128), gap.MissingVersions, gap.MissingGenerations)
			if message.Len() >= maxPublicErrorMessage-len(recovery) {
				break
			}
		}
		return boundedPublicText(message.String(), maxPublicErrorMessage-len(recovery)) + recovery
	}
	var issues *registry.IssueError
	if errors.As(err, &issues) {
		var message strings.Builder
		message.WriteString(fallback)
		for _, issue := range issues.Issues {
			fmt.Fprintf(&message, "; %s", boundedPublicText(issue.Code, 64))
			if issue.Path != "" {
				fmt.Fprintf(&message, " %s", boundedPublicText(issue.Path, 256))
			}
			if issue.PluginID != "" {
				fmt.Fprintf(&message, " plugin=%s@%s", boundedPublicText(issue.PluginID, 128), boundedPublicText(issue.PluginVersion, 64))
			}
			if issue.Cause != "" {
				cause := plugins.CauseUnavailable
				switch issue.Cause {
				case plugins.CauseNetwork, plugins.CauseDeadline, plugins.CauseCanceled, plugins.CauseDiscoveryInvalid:
					cause = issue.Cause
				}
				fmt.Fprintf(&message, " cause=%s", cause)
			}
			if message.Len() >= maxPublicErrorMessage {
				break
			}
		}
		return boundedPublicText(message.String(), maxPublicErrorMessage)
	}
	var space *content.SpaceError
	if errors.As(err, &space) {
		code := "space_changed"
		if errors.Is(err, content.ErrSpaceOwner) {
			code = "space_owner_conflict"
		}
		return fallback + "; " + code + " vector space " + boundedPublicText(space.Space, 128)
	}
	return fallback
}
