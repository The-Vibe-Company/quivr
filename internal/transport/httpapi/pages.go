package httpapi

import (
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/transport/pagetoken"
)

func (a *API) encodePage(domain string, payload any) string {
	// Every page payload is a concrete struct of JSON scalars; encoding cannot
	// fail. No TTL is imposed on existing list cursors.
	token, _ := pagetoken.Encode(a.CursorKey, domain, payload, time.Time{})
	return token
}

func (a *API) decodePage(domain, token string, payload any) error {
	return pagetoken.Decode(a.CursorKey, domain, token, payload, time.Now())
}
