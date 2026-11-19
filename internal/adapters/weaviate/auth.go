package weaviate

import "strings"

// redact removes the API key from provider text before it is logged or returned.
func (s *Store) redact(text string) string {
	if s.apiKey == "" {
		return text
	}
	return strings.ReplaceAll(text, s.apiKey, "[REDACTED]")
}
