package workqueue

import (
	"fmt"
	"time"
)

const DefaultRefreshInterval = 15 * time.Second

// ObservationConfig controls installation-wide queue snapshot publication.
type ObservationConfig struct {
	RefreshInterval string `json:"refresh_interval"`
}

func (c ObservationConfig) Resolve() (time.Duration, error) {
	if c.RefreshInterval == "" {
		return DefaultRefreshInterval, nil
	}
	interval, err := time.ParseDuration(c.RefreshInterval)
	if err != nil || interval < time.Second || interval > time.Minute {
		return 0, fmt.Errorf("queue_observation.refresh_interval must be a duration between 1s and 60s")
	}
	return interval, nil
}
