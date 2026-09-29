package app

import (
	"errors"
	"time"
)

// ChangePruneConfig configures the worker's change-journal prune (THE-697).
// By default it prunes every Organization after change_retention, the same
// setting the API applies to Change Cursors, every minute. Retention may only
// lengthen that window: a shorter one would expire cursors earlier than the
// API promises, so it is refused unless AllowShortRetention confines it to
// named Organizations (local and CI harness isolation only).
type ChangePruneConfig struct {
	Interval            string   `json:"interval"`
	Retention           string   `json:"retention"`
	Organizations       []string `json:"organizations"`
	AllowShortRetention bool     `json:"allow_short_retention"`
}

type changePrune struct {
	Interval, Retention time.Duration
	Organizations       []string
}

func (c ChangePruneConfig) parse(changeRetention time.Duration) (changePrune, error) {
	p := changePrune{Interval: time.Minute, Retention: changeRetention, Organizations: c.Organizations}
	for _, v := range []struct {
		raw string
		to  *time.Duration
	}{{c.Interval, &p.Interval}, {c.Retention, &p.Retention}} {
		if v.raw == "" {
			continue
		}
		d, err := time.ParseDuration(v.raw)
		if err != nil || d <= 0 {
			return changePrune{}, errors.New("change_prune durations must be positive Go durations")
		}
		*v.to = d
	}
	for _, org := range c.Organizations {
		if org == "" {
			return changePrune{}, errors.New("change_prune.organizations must not contain an empty Organization")
		}
	}
	if p.Retention < changeRetention && (!c.AllowShortRetention || len(c.Organizations) == 0) {
		return changePrune{}, errors.New("change_prune.retention is shorter than change_retention; set allow_short_retention with named organizations only for isolated test Organizations")
	}
	return p, nil
}
