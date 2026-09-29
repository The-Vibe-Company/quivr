package main

import (
	"log/slog"
	"os"

	"github.com/The-Vibe-Company/quivr-v2/internal/app"
	"github.com/The-Vibe-Company/quivr-v2/internal/online"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/cli"
)

func main() {
	// Plugin tooling runs without QUIVR_CONFIG or a running stack.
	if len(os.Args) >= 2 && os.Args[1] == "plugin" {
		os.Exit(cli.Run(os.Args[2:], os.Stdout, os.Stderr))
	}
	// Online commands reach a running installation through its public API only.
	if len(os.Args) >= 2 {
		if _, ok := online.Lookup(os.Args[1]); ok {
			os.Exit(online.Run(os.Args[1], os.Args[2:]))
		}
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if len(os.Args) != 2 {
		slog.Error("usage: quivr api|worker|migrate|plugin|search")
		os.Exit(2)
	}
	if err := app.Run(os.Args[1]); err != nil {
		slog.Error("process failed", "error", err)
		os.Exit(1)
	}
}
