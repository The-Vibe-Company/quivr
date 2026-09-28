package httpapi

import "github.com/The-Vibe-Company/quivr-v2/internal/telemetry"

// WithCommands counts accepted durable commands on the given counter (THE-662).
func WithCommands(commands telemetry.Commands) Option {
	return func(a *API) { a.Commands = commands }
}
