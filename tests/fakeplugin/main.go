// Command fakeplugin runs the shared test plugin for the acceptance harness.
package main

import (
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost/fakeplugin"
	"os"
)

func main() {
	_ = os.Setenv(fakeplugin.EnvEnable, "1")
	fakeplugin.MaybeRun()
}
