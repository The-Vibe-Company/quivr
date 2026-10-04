// Command x-list is the first-party X list connector plugin (kind x_list),
// built only on the Quivr Go plugin SDK.
package main

import (
	"fmt"
	"os"

	"github.com/The-Vibe-Company/quivr-v2/sdks/go/quivrplugin"
)

func main() {
	plugin, err := quivrplugin.New("")
	if err == nil {
		err = plugin.Connector("x_list", XList{})
	}
	if err == nil {
		err = plugin.Serve()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "x-list:", err)
		os.Exit(1)
	}
}
