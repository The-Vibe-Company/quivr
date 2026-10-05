package s3

import (
	"fmt"

	"github.com/The-Vibe-Company/quivr/internal/outbound"
)

func NewWithTLS(cfg Config, settings outbound.TLS) (*Store, error) {
	config, err := settings.ForURL(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("s3 TLS: %w", err)
	}
	return newWithTransport(cfg, outbound.Transport(config)), nil
}
