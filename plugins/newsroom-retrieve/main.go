// Command quivr-newsroom-retrieve runs the experimental editorial retrieval plugin.
package main

import (
	"fmt"
	"os"

	"github.com/The-Vibe-Company/quivr/plugins/newsroom-retrieve/plugin"
	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

func main() {
	p, err := quivrplugin.New("")
	if err == nil {
		err = p.Retrieval(plugin.New())
	}
	if err == nil {
		m := p.Manifest()
		err = p.ServeReady(func(addr string) {
			fmt.Fprintf(os.Stderr, "quivr-newsroom-retrieve: serving %s@%s (retrieval) on %s\n", m.ID, m.Version, addr)
		})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "quivr-newsroom-retrieve:", err)
		os.Exit(1)
	}
}
