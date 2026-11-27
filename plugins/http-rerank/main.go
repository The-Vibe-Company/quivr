// Command quivr-http-rerank runs the HTTP cross-encoder retrieval plugin.
package main

import (
	"log"

	"github.com/The-Vibe-Company/quivr/plugins/http-rerank/plugin"
	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

func main() {
	p, err := quivrplugin.New("")
	if err == nil {
		err = p.Retrieval(plugin.New())
	}
	if err == nil {
		err = p.Serve()
	}
	if err != nil {
		log.Fatal(err)
	}
}
