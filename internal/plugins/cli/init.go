package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/scaffold"
)

const initUsage = "quivr plugin init <name> [--kind normalizer|subscription|connector] [--dir <path>]"

// initCommand writes an embedded Python plugin template: a normalizer (the
// default) or an alert rule (--kind subscription).
func initCommand(_ context.Context, args []string, stdout, stderr io.Writer) int {
	var name, dir string
	kind := scaffold.KindNormalizer
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--dir":
			if i+1 >= len(args) {
				fmt.Fprintf(stderr, "--dir needs a path\nusage: %s\n", initUsage)
				return ExitUsage
			}
			i++
			dir = args[i]
		case strings.HasPrefix(arg, "--dir="):
			dir = strings.TrimPrefix(arg, "--dir=")
		case arg == "--kind":
			if i+1 >= len(args) {
				fmt.Fprintf(stderr, "--kind needs a value\nusage: %s\n", initUsage)
				return ExitUsage
			}
			i++
			kind = args[i]
		case strings.HasPrefix(arg, "--kind="):
			kind = strings.TrimPrefix(arg, "--kind=")
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "unknown flag %q\nusage: %s\n", arg, initUsage)
			return ExitUsage
		case name == "":
			name = arg
		default:
			fmt.Fprintf(stderr, "usage: %s\n", initUsage)
			return ExitUsage
		}
	}
	if name == "" {
		fmt.Fprintf(stderr, "usage: %s\n", initUsage)
		return ExitUsage
	}
	if err := scaffold.CheckName(name); err != nil {
		fmt.Fprintf(stderr, "%v\nusage: %s\n", err, initUsage)
		return ExitUsage
	}
	if err := scaffold.CheckKind(kind); err != nil {
		fmt.Fprintf(stderr, "%v\nusage: %s\n", err, initUsage)
		return ExitUsage
	}
	if dir == "" {
		dir = name
	}
	files, err := scaffold.Write(dir, name, kind)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitInvalid
	}
	fmt.Fprintf(stdout, "Created %s plugin %s in %s (%d files).\n\nNext steps:\n", kind, name, dir, len(files))
	fmt.Fprintf(stdout, "  cd %s\n", filepath.Clean(dir))
	fmt.Fprintln(stdout, "  python3 -m venv .venv && . .venv/bin/activate")
	fmt.Fprintln(stdout, "  pip install -e <quivr-v2 checkout>/sdks/python   # the Quivr Plugin SDK")
	fmt.Fprintln(stdout, "  python3 -m unittest discover -s tests")
	if kind != scaffold.KindConnector {
		fmt.Fprintln(stdout, "  quivr plugin dev --fixture fixtures/sample.json")
	}
	fmt.Fprintln(stdout, "  quivr plugin test")
	return ExitOK
}
