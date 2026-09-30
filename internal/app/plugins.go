package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
)

// seedPluginRegistry fills an empty plugin registry from the startup pins. On
// a registry that is not empty it changes nothing and warns when the pins
// differ from the active Pipeline Plan; the engine still resolves plugins from
// the pins in this slice (THE-780).
func seedPluginRegistry(ctx context.Context, service registry.Service, pins *plugins.PinSet) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	seeded, differences, err := service.SeedFrom(ctx, registry.FromPins(pins))
	if err != nil {
		return fmt.Errorf("seed the plugin registry: %w", err)
	}
	if seeded {
		slog.Info("plugin registry seeded from the configuration", "plugins", pins.Describe())
		return nil
	}
	if len(differences) > 0 {
		roles := make([]string, len(differences))
		for i, d := range differences {
			roles[i] = fmt.Sprintf("%s: configured %q, active %q", d.Role, d.Configured, d.Active)
		}
		slog.Warn("plugin configuration differs from the active pipeline plan; the plan is unchanged", "differences", roles)
	}
	return nil
}
