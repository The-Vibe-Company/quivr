package weaviate

import (
	"fmt"

	"github.com/The-Vibe-Company/quivr/internal/outbound"
)

func NewWithTLS(endpoint string, settings outbound.TLS) (*Store, error) {
	config, err := settings.ForURL(endpoint)
	if err != nil {
		return nil, fmt.Errorf("weaviate TLS: %w", err)
	}
	store := New(endpoint)
	store.Client.Transport = outbound.Transport(config)
	store.Client.CheckRedirect = outbound.CheckRedirect
	return store, nil
}
