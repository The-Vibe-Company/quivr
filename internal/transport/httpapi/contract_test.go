package httpapi_test

import (
	"net/http"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/testutil/apicontract"
)

// All memory-backed HTTP harnesses validate every response, not just examples.
func checkedAPI(t *testing.T, handler http.Handler) http.Handler {
	t.Helper()
	return apicontract.Handler(t, handler)
}
