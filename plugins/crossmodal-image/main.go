package main

import (
	"fmt"
	"net"
	"os"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

func main() {
	plugin, err := quivrplugin.New("")
	if err == nil {
		err = plugin.Normalizer(crossModalPlugin{})
	}
	if err == nil {
		err = plugin.Ingestion(crossModalPlugin{})
	}
	if err == nil {
		m := plugin.Manifest()
		host, port := os.Getenv(quivrplugin.EnvHost), os.Getenv(quivrplugin.EnvPort)
		if host == "" {
			host = "127.0.0.1"
		}
		if port == "" {
			port = "8080"
		}
		fmt.Fprintf(os.Stderr, "quivr-crossmodal-image: serving %s@%s (normalizer, ingestion) on %s\n", m.ID, m.Version, net.JoinHostPort(host, port))
		err = plugin.Serve()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "quivr-crossmodal-image:", err)
		os.Exit(1)
	}
}
