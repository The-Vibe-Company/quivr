package app

import (
	"os"
	"strconv"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

const hydrationBlobCacheBytesEnv = "QUIVR_HYDRATION_BLOB_CACHE_BYTES"

func (c RetrievalConfig) hydrationBlobCacheBytes() (int64, error) {
	if raw := os.Getenv(hydrationBlobCacheBytesEnv); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 || n > content.MaxHydrationBlobCacheBytes {
			return 0, badConfig(configInvalid, "retrieval.hydration_blob_cache_bytes", "QUIVR_HYDRATION_BLOB_CACHE_BYTES must be an integer between 0 and 1073741824")
		}
		return n, nil
	}
	if c.HydrationBlobCacheBytes == nil {
		return content.DefaultHydrationBlobCacheBytes, nil
	}
	n := *c.HydrationBlobCacheBytes
	if n < 0 || n > content.MaxHydrationBlobCacheBytes {
		return 0, badConfig(configInvalid, "retrieval.hydration_blob_cache_bytes", "retrieval.hydration_blob_cache_bytes must be between 0 and 1073741824 (or omitted for the 64 MiB default)")
	}
	return n, nil
}
