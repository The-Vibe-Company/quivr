package plugins_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
)

func TestSigningKeysRejectAmbiguousConfiguration(t *testing.T) {
	secret := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	valid := `{"active":"current","keys":[{"id":"current","secret":"` + secret + `"}]}`
	if _, err := plugins.ParseSigningKeys([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"duplicate selection": strings.Replace(valid, `"active":"current"`, `"active":"current","active":"current"`, 1),
		"duplicate secret":    strings.Replace(valid, `"secret":"`+secret+`"`, `"secret":"`+secret+`","secret":"`+secret+`"`, 1),
		"null start":          strings.Replace(valid, `"id":"current"`, `"id":"current","not_before":null`, 1),
		"null expiry":         strings.Replace(valid, `"id":"current"`, `"id":"current","not_after":null`, 1),
		"noncanonical secret": strings.Replace(valid, secret, secret+`\n`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := plugins.ParseSigningKeys([]byte(raw)); err != plugins.ErrSigningKeys {
				t.Fatalf("error = %v, want generic configuration rejection", err)
			}
		})
	}
}
