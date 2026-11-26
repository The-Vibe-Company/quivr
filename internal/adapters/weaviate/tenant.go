package weaviate

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

const InitialCollection = "QuivrTextV5"

func tenantName(org, corpusID, generationID string) string {
	// Length framing preserves tuple boundaries and the original bytes. Keep
	// all 256 bits of the digest within Weaviate's 64-character name limit.
	var tuple []byte
	for _, value := range []string{org, corpusID, generationID} {
		tuple = binary.AppendUvarint(tuple, uint64(len(value)))
		tuple = append(tuple, value...)
	}
	return content.Hash(tuple)
}

type tenantKey struct{}

func tenantContext(ctx context.Context, collection, org, corpusID, generationID string) (context.Context, error) {
	if collection != InitialCollection {
		return ctx, nil
	}
	if org == "" || corpusID == "" || generationID == "" {
		return nil, errors.New("incomplete projection tenant route")
	}
	return context.WithValue(ctx, tenantKey{}, tenantName(org, corpusID, generationID)), nil
}

func tenantFrom(ctx context.Context) string {
	tenant, _ := ctx.Value(tenantKey{}).(string)
	return tenant
}

func tenantPath(ctx context.Context, path string) string {
	if tenant := tenantFrom(ctx); tenant != "" {
		separator := "?"
		if strings.Contains(path, "?") {
			separator = "&"
		}
		return path + separator + "tenant=" + tenant
	}
	return path
}

func tenantArgument(ctx context.Context) string {
	if tenant := tenantFrom(ctx); tenant != "" {
		return "tenant:" + quote(tenant) + ","
	}
	return ""
}

type tenantSpec struct {
	Name           string `json:"name"`
	ActivityStatus string `json:"activityStatus"`
}

func tenantsPath(collection string) string {
	return "/v1/schema/" + collection + "/tenants"
}

// EnsureGeneration explicitly provisions the shard before its route can serve.
func (s *Store) EnsureGeneration(ctx context.Context, collection, org, corpusID, generationID string) error {
	if collection != InitialCollection {
		return nil
	}
	ctx, err := tenantContext(ctx, collection, org, corpusID, generationID)
	if err != nil {
		return err
	}
	_, err = s.call(ctx, "POST", tenantsPath(collection), []tenantSpec{{Name: tenantFrom(ctx), ActivityStatus: "ACTIVE"}}, nil)
	return err
}

// SetGenerationActive changes the temperature of a routed generation.
func (s *Store) SetGenerationActive(ctx context.Context, collection, org, corpusID, generationID string, active bool) error {
	if collection != InitialCollection {
		return nil
	}
	ctx, err := tenantContext(ctx, collection, org, corpusID, generationID)
	if err != nil {
		return err
	}
	state := "INACTIVE"
	if active {
		state = "ACTIVE"
	}
	status, err := s.call(ctx, "PUT", tenantsPath(collection), []tenantSpec{{Name: tenantFrom(ctx), ActivityStatus: state}}, nil)
	if err != nil && status != 0 {
		return fmt.Errorf("tenant activity update HTTP %d: %w", status, err)
	}
	return err
}

func (s *Store) tenantExists(ctx context.Context, collection string) (bool, error) {
	status, err := s.call(ctx, "HEAD", tenantsPath(collection)+"/"+tenantFrom(ctx), nil, nil)
	if status == http.StatusNotFound {
		return false, nil
	}
	return err == nil, err
}
