// Command quivr-reference writes the generated reference pages, or with -check
// fails when a committed page differs from what its source generates.
//
//	go run ./cmd/quivr-reference          # regenerate (make generate)
//	go run ./cmd/quivr-reference -check   # freshness check (make contracts, in make verify)
//
// Add a page by adding an entry to pages with its adapter.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/The-Vibe-Company/quivr-v2/internal/reference/commands"
	"github.com/The-Vibe-Company/quivr-v2/internal/reference/mcp"
	"github.com/The-Vibe-Company/quivr-v2/internal/reference/openapi"
)

type page struct {
	path   string
	render func(root fs.FS) ([]byte, error)
}

var pages = []page{
	{"docs/reference/http-api.md", func(root fs.FS) ([]byte, error) {
		return openapi.Render(openapi.Source{FS: root, Contract: "contracts/http/v0/openapi.yaml", Examples: "contracts/http/v0/examples.json"})
	}},
	{"docs/reference/cli.md", func(fs.FS) ([]byte, error) {
		src, err := cliSource()
		if err != nil {
			return nil, err
		}
		return commands.Render(src)
	}},
	{"docs/reference/mcp.md", func(fs.FS) ([]byte, error) {
		return mcp.Render(mcpSource())
	}},
}

func main() {
	root := flag.String("root", ".", "repository root")
	check := flag.Bool("check", false, "fail when a committed page is stale instead of writing it")
	flag.Parse()
	stale := false
	for _, p := range pages {
		want, err := p.render(os.DirFS(*root))
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", p.path, err)
			os.Exit(1)
		}
		target := filepath.Join(*root, filepath.FromSlash(p.path))
		if *check {
			if got, err := os.ReadFile(target); err != nil || !bytes.Equal(got, want) {
				fmt.Fprintf(os.Stderr, "%s is stale or was edited by hand%s. Fix: change its source, then run `make generate` (or `go run ./cmd/quivr-reference`).\n", p.path, firstDifference(got, want))
				stale = true
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.WriteFile(target, want, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if stale {
		os.Exit(1)
	}
}

// firstDifference names the first line where the committed page differs.
func firstDifference(got, want []byte) string {
	if got == nil {
		return " (missing)"
	}
	g, w := bytes.Split(got, []byte("\n")), bytes.Split(want, []byte("\n"))
	for i := 0; i < len(g) || i < len(w); i++ {
		if i >= len(g) || i >= len(w) || !bytes.Equal(g[i], w[i]) {
			return fmt.Sprintf(" (first difference at line %d)", i+1)
		}
	}
	return ""
}
