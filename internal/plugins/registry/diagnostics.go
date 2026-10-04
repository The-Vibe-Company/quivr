package registry

import (
	"context"
	"errors"
	"net"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

func discoveryCause(err error) plugins.IssueCause {
	var issues *IssueError
	var network net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return plugins.CauseDeadline
	case errors.Is(err, context.Canceled):
		return plugins.CauseCanceled
	case errors.As(err, &issues):
		return plugins.CauseDiscoveryInvalid
	case errors.As(err, &network):
		if network.Timeout() {
			return plugins.CauseDeadline
		}
		return plugins.CauseNetwork
	default:
		return plugins.CauseUnavailable
	}
}
