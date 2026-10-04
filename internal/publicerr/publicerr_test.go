package publicerr_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
)

func TestCodeSurvivesDetail(t *testing.T) {
	sentinel := publicerr.New("invalid_input")
	for name, err := range map[string]error{
		"bare":        sentinel,
		"fmt wrap":    fmt.Errorf("%w: duplicate Part key %q", sentinel, "a"),
		"with detail": publicerr.WithDetail(sentinel, "duplicate Part key %q", "a"),
		"nested":      fmt.Errorf("accept: %w", publicerr.WithDetail(sentinel, "too large")),
	} {
		code, ok := publicerr.Code(err)
		if !ok || code != "invalid_input" {
			t.Errorf("%s: Code = %q, %v; want invalid_input", name, code, ok)
		}
		if !errors.Is(err, sentinel) {
			t.Errorf("%s: errors.Is lost the sentinel", name)
		}
	}
}

func TestDetailIsReadableButNotTheCode(t *testing.T) {
	err := publicerr.WithDetail(publicerr.New("invalid_input"), "Part %d needs a key", 2)
	if got := err.Error(); got != "invalid_input: Part 2 needs a key" {
		t.Fatalf("Error() = %q", got)
	}
	if got := publicerr.Detail(err); got != "Part 2 needs a key" {
		t.Fatalf("Detail = %q", got)
	}
}

func TestUncodedError(t *testing.T) {
	if code, ok := publicerr.Code(errors.New("invalid_input")); ok || code != "" {
		t.Fatalf("plain error resolved to %q", code)
	}
	if code, ok := publicerr.Code(nil); ok || code != "" {
		t.Fatalf("nil resolved to %q", code)
	}
}

func TestSentinelsWithSameCodeShareIdentity(t *testing.T) {
	a, b := publicerr.New("idempotency_conflict"), publicerr.New("idempotency_conflict")
	if !errors.Is(a, b) || !errors.Is(fmt.Errorf("%w", a), b) {
		t.Fatal("one public code must have one sentinel identity, including through wrapping")
	}
}

func TestWithDetailKeepsNil(t *testing.T) {
	if err := publicerr.WithDetail(nil, "ignored"); err != nil {
		t.Fatalf("WithDetail(nil) = %v", err)
	}
}
