package plugins

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

// ErrUnavailable means the pinned plugin could not serve an invocation.
var ErrUnavailable = errors.New("plugin_unavailable")

// ErrCallDeadline distinguishes a plugin invocation deadline from an outage.
var ErrCallDeadline = errors.New("plugin call reached its deadline")

// PluginError is a valid error envelope declared by the plugin.
type PluginError struct {
	Status        int
	Code, Message string
	Retryable     bool
}

func (e *PluginError) Error() string {
	return fmt.Sprintf("plugin error %s (HTTP %d, retryable=%t): %s", e.Code, e.Status, e.Retryable, e.Message)
}

// InvalidOutput is an answer the engine refuses under the plugin contract.
type InvalidOutput struct{ Issues []Issue }

func (e *InvalidOutput) Error() string {
	return "invalid normalizer output: " + DescribeIssues(e.Issues)
}

// DescribeIssues formats contract violations as a bounded diagnostic.
func DescribeIssues(issues []Issue) string {
	parts := make([]string, 0, len(issues))
	for _, i := range issues {
		parts = append(parts, strings.TrimSpace(i.Code+" "+i.Path+": "+i.Message))
	}
	return BoundedDiagnostic(strings.Join(parts, "; "), 1000)
}

// BoundedDiagnostic preserves valid text and marks truncation within the bound.
func BoundedDiagnostic(s string, limit int) string {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, "�"), "\x00", "")
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	const suffix = "… [truncated]"
	marker := []rune(suffix)
	if limit <= len(marker) {
		return string(marker[:max(0, limit)])
	}
	return string(r[:limit-len(marker)]) + suffix
}

// NormalizerWarning is a warning from a checked normalizer response.
type NormalizerWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// NormalizerResponse contains a checked normalizer output.
type NormalizerResponse struct {
	Manifest   content.Manifest    `json:"manifest"`
	Extensions content.Extensions  `json:"extensions,omitempty"`
	Language   string              `json:"language,omitempty"`
	Warnings   []NormalizerWarning `json:"warnings,omitempty"`
}

// InvocationID identifies one attempt, while a request's key identifies work.
func InvocationID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "inv_" + hex.EncodeToString(b[:])
}
