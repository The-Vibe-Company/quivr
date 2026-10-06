package app

import (
	"time"
)

// ChangePruneConfig configures the worker's change-journal prune (THE-697).
// By default it prunes every Organization after change_retention, the same
// setting the API applies to Change Cursors, every minute. Retention may only
// lengthen that window: a shorter one would expire cursors earlier than the
// API promises, so it is refused unless AllowShortRetention explicitly
// confines it to named Organizations. This can cause data loss for consumers.
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
		field string
		raw   string
		to    *time.Duration
	}{{"change_prune.interval", c.Interval, &p.Interval}, {"change_prune.retention", c.Retention, &p.Retention}} {
		if v.raw == "" {
			continue
		}
		d, err := time.ParseDuration(v.raw)
		if err != nil || d <= 0 {
			return changePrune{}, badConfig(configInvalid, v.field, "change_prune durations must be positive Go durations")
		}
		*v.to = d
	}
	for _, org := range c.Organizations {
		if org == "" {
			return changePrune{}, badConfig(configInvalid, "change_prune.organizations", "change_prune.organizations must not contain an empty Organization")
		}
	}
	if p.Retention < changeRetention && (!c.AllowShortRetention || len(c.Organizations) == 0) {
		return changePrune{}, badConfig(configConflict, "change_prune.retention", "change_prune.retention is shorter than change_retention; set allow_short_retention with named organizations only when early event loss is acceptable")
	}
	return p, nil
}
