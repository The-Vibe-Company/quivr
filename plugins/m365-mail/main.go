// Command m365-mail is the connector.m365_mail plugin: the m365_mail
// connector kind, which collects one Microsoft 365 mailbox folder through
// Microsoft Graph with an app-only (client credentials) registration. Each
// page of the folder's message delta becomes items; the delta link is the
// checkpoint, which the core advances only after the page's items were
// accepted. Attachments are uploaded through core-issued grants.
package main

import (
	"fmt"
	"os"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

func main() {
	p, err := quivrplugin.New("")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := p.MustConnector(Kind, New(nil)).Serve(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
