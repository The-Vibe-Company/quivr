// Command quivr-plugins bundles every first-party Go plugin into one multi-call
// binary for the combined core image.
package main

import (
	multicall "github.com/The-Vibe-Company/quivr/deploy/plugins"
	coreingest "github.com/The-Vibe-Company/quivr/plugins/core-ingest/plugin"
	coreretrieve "github.com/The-Vibe-Company/quivr/plugins/core-retrieve/plugin"
	m365mail "github.com/The-Vibe-Company/quivr/plugins/m365-mail/plugin"
	archive "github.com/The-Vibe-Company/quivr/plugins/object-storage-archive/plugin"
	rss "github.com/The-Vibe-Company/quivr/plugins/rss/plugin"
	xlist "github.com/The-Vibe-Company/quivr/plugins/x-list/plugin"
)

func main() {
	multicall.Run(map[string]multicall.Entry{
		"core-ingest":            multicall.MainFunc(coreingest.Main),
		"core-retrieve":          multicall.MainFunc(coreretrieve.Main),
		"text-embed":           multicall.TextEmbed,
		"m365-mail":              multicall.MainFunc(m365mail.Main),
		"object-storage-archive": multicall.MainFunc(archive.Main),
		"rss":                    multicall.MainFunc(rss.Main),
		"x-list":                 multicall.MainFunc(xlist.Main),
	})
}
