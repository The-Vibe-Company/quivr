package monitoring_test

import (
	"encoding/json"
	"testing"

	contract "github.com/The-Vibe-Company/quivr-v2/contracts/http/v0"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

// The signer must reproduce the independently computed contract vector.
func TestSignMatchesContractVector(t *testing.T) {
	var v struct{ Secret, Timestamp, EventID, Body, Signature string }
	if err := json.Unmarshal(contract.WebhookVector, &v); err != nil {
		t.Fatal(err)
	}
	var raw map[string]string
	_ = json.Unmarshal(contract.WebhookVector, &raw)
	v.EventID = raw["event_id"]
	key, err := monitoring.ParseSecret(v.Secret)
	if err != nil || len(key) != 32 {
		t.Fatal("parse vector secret", err, len(key))
	}
	if got := monitoring.Sign(key, v.EventID, v.Timestamp, []byte(v.Body)); got != v.Signature {
		t.Fatalf("signature %q, want %q", got, v.Signature)
	}
	for _, tampered := range []string{
		monitoring.Sign(key, v.EventID, v.Timestamp, []byte(v.Body+" ")),
		monitoring.Sign(key, v.EventID+"_changed", v.Timestamp, []byte(v.Body)),
		monitoring.Sign(key, v.EventID, "1789387201", []byte(v.Body)),
	} {
		if tampered == v.Signature {
			t.Fatal("tampering must change the signature")
		}
	}
}

func TestParseSecretRejectsInvalidSecrets(t *testing.T) {
	for _, s := range []string{"", "secret", "whsec_", "whsec_not-base64!", "whsec_c2hvcnQ=", "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="} {
		if _, err := monitoring.ParseSecret(s); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}
