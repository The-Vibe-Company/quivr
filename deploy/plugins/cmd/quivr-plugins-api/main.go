// Command quivr-plugins-api bundles only the first-party Go plugins run by the
// API service (push relay, query encoding, retrieval).
package main

import (
	multicall "github.com/The-Vibe-Company/quivr/deploy/plugins"
	coreingest "github.com/The-Vibe-Company/quivr/plugins/core-ingest/plugin"
	coreretrieve "github.com/The-Vibe-Company/quivr/plugins/core-retrieve/plugin"
	xlist "github.com/The-Vibe-Company/quivr/plugins/x-list/plugin"
)

func main() {
	multicall.Run(map[string]multicall.Entry{
		"core-ingest":   multicall.MainFunc(coreingest.Main),
		"core-retrieve": multicall.MainFunc(coreretrieve.Main),
		"text-embed":  multicall.TextEmbed,
		"x-list":        multicall.MainFunc(xlist.Main),
	})
}
