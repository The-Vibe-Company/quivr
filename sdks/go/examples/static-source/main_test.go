package main

import (
	"path/filepath"
	"testing"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin/plugintest"
)

// TestFixtures replays every fixture in process and checks its expectations,
// as a plugin author would before running quivr plugin test.
func TestFixtures(t *testing.T) {
	plugin, err := quivrplugin.New("quivr-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	plugin.MustConnector("static", static{})
	files, _ := filepath.Glob("fixtures/*.json")
	if len(files) == 0 {
		t.Fatal("no fixtures")
	}
	for _, file := range files {
		t.Run(file, func(t *testing.T) {
			f, err := plugintest.Load(file)
			if err != nil {
				t.Fatal(err)
			}
			if err := plugintest.Verify(plugin, f); err != nil {
				t.Fatal(err)
			}
		})
	}
}
