package connectors

import (
	"bytes"
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

func TestRequestDigestIsKeyedAndStable(t *testing.T) {
	a, _ := NewSealer(testDeploymentKey)
	b, _ := NewSealer(strings.Repeat("y", 40))
	one := a.Digest("create", []byte(`{"secret":"x"}`))
	if !bytes.Equal(one, a.Digest("create", []byte(`{"secret":"x"}`))) {
		t.Fatal("digest not stable")
	}
	if bytes.Equal(one, a.Digest("credential", []byte(`{"secret":"x"}`))) || bytes.Equal(one, a.Digest("create", []byte(`{"secret":"y"}`))) || bytes.Equal(one, b.Digest("create", []byte(`{"secret":"x"}`))) {
		t.Fatal("digest not separated by purpose, content and key")
	}
}
