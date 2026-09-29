// Command quivr-rss is the first-party Quivr connector plugin for RSS and Atom
// feeds (kind rss). It runs the Plugin Protocol v0 connector Contribution with
// the Go plugin SDK; see README.md.
package main

import (
	"fmt"
	"net"
	"os"

	"github.com/The-Vibe-Company/quivr-v2/sdks/go/quivrplugin"
)

func main() {
	plugin, err := quivrplugin.New("")
	if err == nil {
		err = plugin.Connector("rss", feed{})
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
		// One startup line, so an operator sees the sidecar came up and what it serves.
		fmt.Fprintf(os.Stderr, "quivr-rss: serving %s@%s (connector kind rss) on %s\n", m.ID, m.Version, net.JoinHostPort(host, port))
		err = plugin.Serve()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "quivr-rss:", err)
		os.Exit(1)
	}
}
