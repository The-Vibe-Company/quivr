// Command quivr-plugins-worker bundles only the first-party Go plugins run by
// worker services (connectors and ingestion, excluding API-only retrieval).
package main

import (
	multicall "github.com/The-Vibe-Company/quivr/deploy/plugins"
	coreingest "github.com/The-Vibe-Company/quivr/plugins/core-ingest/plugin"
	m365mail "github.com/The-Vibe-Company/quivr/plugins/m365-mail/plugin"
	archive "github.com/The-Vibe-Company/quivr/plugins/object-storage-archive/plugin"
	rss "github.com/The-Vibe-Company/quivr/plugins/rss/plugin"
	xlist "github.com/The-Vibe-Company/quivr/plugins/x-list/plugin"
)

func main() {
	multicall.Run(map[string]multicall.Entry{
		"core-ingest":            multicall.MainFunc(coreingest.Main),
		"text-embed":           multicall.TextEmbed,
		"m365-mail":              multicall.MainFunc(m365mail.Main),
		"object-storage-archive": multicall.MainFunc(archive.Main),
		"rss":                    multicall.MainFunc(rss.Main),
		"x-list":                 multicall.MainFunc(xlist.Main),
	})
}
