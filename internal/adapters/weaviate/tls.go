package weaviate

import (
	"fmt"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"

	"github.com/The-Vibe-Company/quivr/internal/outbound"
)

func NewWithTLS(endpoint string, settings outbound.TLS, apiKey ...string) (*Store, error) {
	config, err := settings.ForURL(endpoint)
	if err != nil {
		return nil, fmt.Errorf("weaviate TLS: %w", err)
	}
	store := New(endpoint, apiKey...)
	store.Client.Transport = telemetry.Transport(store.countRequests(store.authenticate(outbound.Transport(config), apiKey)), "weaviate.request")
	store.Client.CheckRedirect = outbound.CheckRedirect
	return store, nil
}
