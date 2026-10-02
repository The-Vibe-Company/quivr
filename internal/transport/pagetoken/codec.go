// Package pagetoken signs opaque list positions in separate route domains.
// Payload shapes belong to callers so existing tokens survive codec changes.
package pagetoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var (
	ErrInvalid = errors.New("invalid page token")
	ErrExpired = errors.New("expired page token")
)

// Encode preserves payload bytes for tokens without an expiry. An optional
// expiry is included in the signed JSON object as exp (UTC Unix seconds).
func Encode(key []byte, domain string, payload any, expiresAt time.Time) (string, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	if !expiresAt.IsZero() {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(b, &object); err != nil || object == nil {
			return "", ErrInvalid
		}
		object["exp"], err = json.Marshal(expiresAt.Unix())
		if err != nil {
			return "", err
		}
		b, err = json.Marshal(object)
		if err != nil {
			return "", err
		}
	}
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(sign(key, domain, b)), nil
}

// Decode authenticates before interpreting the payload. Callers then check
// their version and scope/filter binding, preserving route-specific errors.
// now is the time of the read, shared by all checks for that request.
func Decode(key []byte, domain, token string, payload any, now time.Time) error {
	encoded, signature, ok := strings.Cut(token, ".")
	if !ok {
		return ErrInvalid
	}
	b, e1 := base64.RawURLEncoding.DecodeString(encoded)
	sig, e2 := base64.RawURLEncoding.DecodeString(signature)
	if e1 != nil || e2 != nil || !hmac.Equal(sig, sign(key, domain, b)) {
		return ErrInvalid
	}
	var expiration struct {
		Expires *int64 `json:"exp"`
	}
	if json.Unmarshal(b, &expiration) != nil {
		return ErrInvalid
	}
	if expiration.Expires != nil && now.Unix() >= *expiration.Expires {
		return ErrExpired
	}
	if json.Unmarshal(b, payload) != nil {
		return ErrInvalid
	}
	return nil
}

func sign(key []byte, domain string, payload []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(domain + "\x00"))
	h.Write(payload)
	return h.Sum(nil)
}
