package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/The-Vibe-Company/quivr/internal/app"
	"github.com/The-Vibe-Company/quivr/internal/buildinfo"
	"github.com/The-Vibe-Company/quivr/internal/logging"
	"github.com/The-Vibe-Company/quivr/internal/online"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/cli"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == buildinfo.VersionFlag {
		fmt.Printf("quivr %s (revision %s; API v0; plugin engine %s)\n", buildinfo.Version, buildinfo.Revision, plugins.EngineVersion)
		return
	}
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
	slog.SetDefault(bootstrapLogger())
	if len(os.Args) != 2 {
		slog.Error(usage())
		os.Exit(2)
	}
	if err := app.Run(os.Args[1]); err != nil {
		// Keep startup failures useful without serializing dependency or
		// configuration diagnostics that may contain credentials.
		app.LogFailure(err)
		os.Exit(1)
	}
}

func bootstrapLogger() *slog.Logger {
	options := logging.Options{
		Level:       os.Getenv("QUIVR_LOG_LEVEL"),
		Service:     "quivr",
		Version:     buildinfo.Version,
		Instance:    os.Getenv("QUIVR_INSTANCE"),
		Environment: os.Getenv("QUIVR_ENVIRONMENT"),
	}
	logger, err := logging.New(os.Stdout, options)
	if err == nil {
		return logger
	}
	// An invalid environment value must never echo back into startup output.
	options.Level = ""
	logger, err = logging.New(os.Stdout, options)
	if err == nil {
		return logger
	}
	// os.Stdout is a valid writer and the fallback level is valid, so this is
	// unreachable unless the constructor's contract changes.
	return logger
}

// usage names every command, from the engine and online command tables.
func usage() string {
	names := []string{buildinfo.VersionFlag}
	for _, c := range app.Commands {
		names = append(names, c.Name)
	}
	names = append(names, "plugin")
	for _, c := range online.Commands {
		names = append(names, c.Name)
	}
	return "usage: quivr " + strings.Join(names, "|")
}
