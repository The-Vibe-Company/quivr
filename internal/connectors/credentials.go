package connectors

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// ErrCredentialKey reports a missing deployment key or a Deposited Credential
// sealed under another key or bound to another instance.
var ErrCredentialKey = errors.New("credential_unreadable")

// Sealed is a Deposited Credential secret encrypted at rest.
type Sealed struct {
	KeyID      string
	Nonce      []byte
	Ciphertext []byte
	ExpiresAt  *time.Time
}

// Sealer encrypts Deposited Credentials with the deployment credential key
// (AES-256-GCM) and keys request digests used for idempotent replay, so no
// secret is ever stored or compared in plaintext.
type Sealer struct {
	aead  cipher.AEAD
	keyID string
	mac   []byte
}

// NewSealer derives the encryption and digest keys from the configured
// deployment secret (32+ bytes).
func NewSealer(secret string) (Sealer, error) {
	if len(secret) < 32 {
		return Sealer{}, errors.New("credential_key must be at least 32 bytes")
	}
	derive := func(label string) []byte {
		h := sha256.New()
		h.Write([]byte(label + "\x00"))
		h.Write([]byte(secret))
		return h.Sum(nil)
	}
	key := derive("connector-credential")
	block, err := aes.NewCipher(key)
	if err != nil {
		return Sealer{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return Sealer{}, err
	}
	id := sha256.Sum256(key)
	return Sealer{aead: aead, keyID: hex.EncodeToString(id[:8]), mac: derive("connector-request-digest")}, nil
}

func binding(org, connectorID string) []byte {
	return []byte(org + "\x00" + connectorID)
}

// Seal encrypts a secret bound to its Organization and instance.
func (s Sealer) Seal(org, connectorID string, plaintext []byte) (Sealed, error) {
	if s.aead == nil {
		return Sealed{}, ErrCredentialKey
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Sealed{}, err
	}
	return Sealed{KeyID: s.keyID, Nonce: nonce, Ciphertext: s.aead.Seal(nil, nonce, plaintext, binding(org, connectorID))}, nil
}

// Open decrypts a secret sealed for the same Organization and instance.
func (s Sealer) Open(org, connectorID string, sealed Sealed) ([]byte, error) {
	if s.aead == nil || sealed.KeyID != s.keyID || len(sealed.Nonce) != s.aead.NonceSize() {
		return nil, ErrCredentialKey
	}
	plaintext, err := s.aead.Open(nil, sealed.Nonce, sealed.Ciphertext, binding(org, connectorID))
	if err != nil {
		return nil, ErrCredentialKey
	}
	return plaintext, nil
}

// Digest keys a canonical request for idempotent replay detection.
func (s Sealer) Digest(purpose string, canonical []byte) []byte {
	h := hmac.New(sha256.New, s.mac)
	h.Write([]byte(purpose + "\x00"))
	h.Write(canonical)
	return h.Sum(nil)
}
