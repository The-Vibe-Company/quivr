package main

import (
	"log/slog"
	"os"
	"strings"

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
		slog.Error(usage())
		os.Exit(2)
	}
	if err := app.Run(os.Args[1]); err != nil {
		slog.Error("process failed", "error", err)
		os.Exit(1)
	}
}

// usage names every command, from the engine and online command tables.
func usage() string {
	names := []string{}
	for _, c := range app.Commands {
		names = append(names, c.Name)
	}
	names = append(names, "plugin")
	for _, c := range online.Commands {
		names = append(names, c.Name)
	}
	return "usage: quivr " + strings.Join(names, "|")
}
