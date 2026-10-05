package app

import "errors"

// Audit retention uses calendar months, so the default is twelve months even
// across leap years. Zero selects the default; a configured value is bounded.
func auditRetentionMonths(months int) (int, error) {
	if months == 0 {
		return 12, nil
	}
	if months < 1 || months > 1200 {
		return 0, errors.New("audit_retention_months must be between 1 and 1200")
	}
	return months, nil
}
