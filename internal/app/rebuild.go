package app

import "github.com/The-Vibe-Company/quivr/internal/retrieval"

// RebuildConfig bounds Version work inside each rebuild activity.
type RebuildConfig struct {
	Concurrency int `json:"concurrency"`
}

func (c RebuildConfig) concurrency() (int, error) {
	if c.Concurrency < 0 || c.Concurrency > retrieval.MaxRebuildConcurrency {
		return 0, badConfig(configInvalid, "rebuild.concurrency", "rebuild.concurrency must be between 1 and 256 (or 0 for the default 8)")
	}
	if c.Concurrency == 0 {
		return retrieval.DefaultRebuildConcurrency, nil
	}
	return c.Concurrency, nil
}
