// Command quivr-rss is the first-party Quivr connector plugin for RSS and Atom
// feeds (kind rss). It runs the Plugin Protocol v0 connector Contribution with
// the Go plugin SDK; see README.md.
package plugin

import (
	"fmt"
	"os"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

func Main() {
	plugin, err := quivrplugin.New("")
	if err == nil {
		err = plugin.Connector("rss", feed{})
	}
	if err == nil {
		m := plugin.Manifest()
		err = plugin.ServeReady(func(addr string) {
			// One startup line after bind succeeds, so an operator sees the sidecar came up and what it serves.
			fmt.Fprintf(os.Stderr, "quivr-rss: serving %s@%s (connector kind rss) on %s\n", m.ID, m.Version, addr)
		})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "quivr-rss:", err)
		os.Exit(1)
	}
}
