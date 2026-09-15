package main

import (
	"github.com/The-Vibe-Company/quivr-v2/internal/app"
	"log/slog"
	"os"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if len(os.Args) != 2 {
		slog.Error("usage: quivr api|worker|migrate")
		os.Exit(2)
	}
	if err := app.Run(os.Args[1]); err != nil {
		slog.Error("process failed", "error", err)
		os.Exit(1)
	}
}
