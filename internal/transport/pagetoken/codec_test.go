package pagetoken_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/transport/pagetoken"
)

// This is the owner of signed page-token integrity, route isolation and expiry.
// Route tests own filter binding and the public status/code of a refusal.
func TestSignedPageToken(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	type position struct {
		After string `json:"after"`
		Scope string `json:"scope"`
	}
	token, err := pagetoken.Encode(key, "records", position{"record_7", "scope_2"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	expiring, err := pagetoken.Encode(key, "records", position{"record_7", "scope_2"}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	tampered := "eyJhZnRlciI6InJlY29yZF84Iiwic2NvcGUiOiJzY29wZV8yIn0." + parts[1]
	for _, tc := range []struct {
		name, token, domain string
		key                 []byte
		at                  time.Time
		want                error
	}{
		{"round trip", token, "records", key, now, nil},
		{"no expiry", token, "records", key, now.AddDate(20, 0, 0), nil},
		{"before expiry", expiring, "records", key, now.Add(59 * time.Second), nil},
		{"at expiry", expiring, "records", key, now.Add(time.Minute), pagetoken.ErrExpired},
		{"after expiry", expiring, "records", key, now.Add(2 * time.Minute), pagetoken.ErrExpired},
		{"tampered payload", tampered, "records", key, now, pagetoken.ErrInvalid},
		{"tampered signature", parts[0] + ".AA", "records", key, now, pagetoken.ErrInvalid},
		{"wrong route", token, "corpora", key, now, pagetoken.ErrInvalid},
		{"wrong key", token, "records", []byte("different secret"), now, pagetoken.ErrInvalid},
		{"missing signature", parts[0], "records", key, now, pagetoken.ErrInvalid},
		{"invalid base64", "%!.%", "records", key, now, pagetoken.ErrInvalid},
		{"extra component", token + ".extra", "records", key, now, pagetoken.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got position
			err := pagetoken.Decode(tc.key, tc.domain, tc.token, &got, tc.at)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Decode = %v; want %v", err, tc.want)
			}
			if err == nil && (got.After != "record_7" || got.Scope != "scope_2") {
				t.Fatalf("position = %+v; want record_7 in scope_2", got)
			}
		})
	}
}
