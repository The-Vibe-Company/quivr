package connectors

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

const testDeploymentKey = "test-credential-key-0123456789abcdef-not-a-secret"

func TestSealedCredentialRoundTripAndBinding(t *testing.T) {
	sealer, err := NewSealer(testDeploymentKey)
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte(`{"token":"fixture-test-secret-do-not-use"}`)
	sealed, err := sealer.Seal("org_a", "conn_1", secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed.Ciphertext, []byte("fixture-test-secret")) || sealed.KeyID == "" || len(sealed.Nonce) == 0 {
		t.Fatalf("ciphertext leaks or lacks metadata: %+v", sealed)
	}
	opened, err := sealer.Open("org_a", "conn_1", sealed)
	if err != nil || !bytes.Equal(opened, secret) {
		t.Fatalf("round trip failed: %v %q", err, opened)
	}
	again, _ := sealer.Seal("org_a", "conn_1", secret)
	if bytes.Equal(again.Nonce, sealed.Nonce) || bytes.Equal(again.Ciphertext, sealed.Ciphertext) {
		t.Fatal("sealing must use a fresh nonce")
	}
	// The ciphertext is bound to its Organization and instance.
	if _, err = sealer.Open("org_b", "conn_1", sealed); err == nil {
		t.Fatal("opened under another Organization")
	}
	if _, err = sealer.Open("org_a", "conn_2", sealed); err == nil {
		t.Fatal("opened under another instance")
	}
	other, _ := NewSealer(strings.Repeat("z", 40))
	if _, err = other.Open("org_a", "conn_1", sealed); err == nil {
		t.Fatal("opened with another deployment key")
	}
	if _, err = NewSealer("short"); err == nil {
		t.Fatal("accepted a short deployment key")
	}
}

func digest(t *testing.T, s Sealer, purpose, canonical string) []byte {
	t.Helper()
	d, err := s.Digest(purpose, []byte(canonical))
	if err != nil || len(d) == 0 {
		t.Fatalf("digest failed: %v", err)
	}
	return d
}

func TestRequestDigestIsKeyedAndStable(t *testing.T) {
	a, _ := NewSealer(testDeploymentKey)
	b, _ := NewSealer(strings.Repeat("y", 40))
	one := digest(t, a, "create", `{"secret":"x"}`)
	if !bytes.Equal(one, digest(t, a, "create", `{"secret":"x"}`)) {
		t.Fatal("digest not stable")
	}
	if bytes.Equal(one, digest(t, a, "credential", `{"secret":"x"}`)) || bytes.Equal(one, digest(t, a, "create", `{"secret":"y"}`)) || bytes.Equal(one, digest(t, b, "create", `{"secret":"x"}`)) {
		t.Fatal("digest not separated by purpose, content and key")
	}
}

const testCursorKey = "test-cursor-key-0123456789abcdef-not-a-secret"

func TestKeylessSealerRefusesSealingButKeepsDigestsKeyed(t *testing.T) {
	keyless, err := NewKeylessSealer(testCursorKey)
	if err != nil {
		t.Fatal(err)
	}
	if keyless.CanSeal() {
		t.Fatal("keyless sealer claims it can seal")
	}
	if _, err = keyless.Seal("org_a", "conn_1", []byte(`{"token":"x"}`)); !errors.Is(err, ErrCredentialKey) {
		t.Fatalf("keyless seal: %v", err)
	}
	keyed, _ := NewSealer(testDeploymentKey)
	if !keyed.CanSeal() {
		t.Fatal("keyed sealer cannot seal")
	}
	sealed, _ := keyed.Seal("org_a", "conn_1", []byte(`{"token":"x"}`))
	if _, err = keyless.Open("org_a", "conn_1", sealed); !errors.Is(err, ErrCredentialKey) {
		t.Fatalf("keyless open: %v", err)
	}
	// Secret-free requests are still digested under a secret key: stable, and
	// distinct from the keyed digest, from another cursor_key and from a
	// digest under the cursor_key used directly as a key.
	one := digest(t, keyless, "create", `{"kind":"rss"}`)
	if !bytes.Equal(one, digest(t, keyless, "create", `{"kind":"rss"}`)) {
		t.Fatal("keyless digest not stable")
	}
	other, _ := NewKeylessSealer(strings.Repeat("c", 40))
	direct, _ := NewSealer(testCursorKey)
	if bytes.Equal(one, digest(t, keyed, "create", `{"kind":"rss"}`)) || bytes.Equal(one, digest(t, other, "create", `{"kind":"rss"}`)) || bytes.Equal(one, digest(t, direct, "create", `{"kind":"rss"}`)) {
		t.Fatal("keyless digest not separated by key and label")
	}
	if _, err = NewKeylessSealer("short"); err == nil {
		t.Fatal("accepted a short cursor key")
	}
}

func TestZeroSealerNeverProducesAnUnkeyedDigest(t *testing.T) {
	var zero Sealer
	if zero.CanSeal() {
		t.Fatal("zero sealer claims it can seal")
	}
	if d, err := zero.Digest("create", []byte(`{"secret":"x"}`)); !errors.Is(err, ErrCredentialKey) || d != nil {
		t.Fatalf("zero sealer digested: %x %v", d, err)
	}
}
