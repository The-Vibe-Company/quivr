package monitoring

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

// ErrInvalidSecret reports a destination signing secret that is not a
// whsec_-prefixed base64 key of at least 24 bytes.
var ErrInvalidSecret = errors.New("invalid webhook signing secret")

// ParseSecret decodes a Standard Webhooks symmetric secret ("whsec_" + base64).
func ParseSecret(secret string) ([]byte, error) {
	encoded, ok := strings.CutPrefix(secret, "whsec_")
	if !ok {
		return nil, ErrInvalidSecret
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) < 24 || len(key) > 64 {
		return nil, ErrInvalidSecret
	}
	return key, nil
}

// Sign returns the Standard Webhooks v1 signature header value: HMAC-SHA256
// over webhook-id "." webhook-timestamp "." raw body, base64 encoded.
func Sign(key []byte, id, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id))
	mac.Write([]byte{'.'})
	mac.Write([]byte(timestamp))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
