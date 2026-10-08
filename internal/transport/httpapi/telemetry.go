package httpapi

import "github.com/The-Vibe-Company/quivr/internal/telemetry"

// WithCommands counts accepted durable commands on the given counter (THE-662).
func WithCommands(commands telemetry.Commands) Option {
	return func(a *API) { a.Commands = commands }
}

// WithLoadMetrics records process-local HTTP load and search admission pressure.
func WithLoadMetrics(metrics *telemetry.LoadMetrics) Option {
	return func(a *API) { a.loadMetrics = metrics }
}
