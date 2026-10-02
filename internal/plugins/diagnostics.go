package plugins

// IssueCause classifies why a plugin could not answer discovery. Transport
// messages use these fixed codes rather than a dependency's error text.
type IssueCause string

const (
	CauseUnavailable      IssueCause = "unavailable"
	CauseNetwork          IssueCause = "network_error"
	CauseDeadline         IssueCause = "deadline_exceeded"
	CauseCanceled         IssueCause = "canceled"
	CauseDiscoveryInvalid IssueCause = "discovery_invalid"
)
