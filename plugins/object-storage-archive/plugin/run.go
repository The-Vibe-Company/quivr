package plugin

import (
	"fmt"
	"os"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

func Main() {
	p, err := quivrplugin.New("")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	connector := NewArchiveConnector()
	defer connector.Close()
	if err = p.MustConnector(Kind, connector).Serve(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
