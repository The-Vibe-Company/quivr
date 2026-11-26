// Package multicall dispatches first-party Go plugins from a single multi-call
// binary by argv[0] symlink name (quivr-<id> or <id>) or explicit subcommand.
package multicall

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	hostedembed "github.com/The-Vibe-Company/quivr/plugins/hosted-embed/plugin"
)

// Entry runs one bundled plugin with its command arguments (excluding argv[0]
// and any explicit subcommand).
type Entry func(args []string)

// MainFunc adapts a zero-argument plugin Main into an Entry.
func MainFunc(fn func()) Entry {
	return func(_ []string) { fn() }
}

// HostedEmbed runs the hosted-embed plugin, emitting [executable, "hosted-embed"]
// as run.command when invoked with configure.
func HostedEmbed(args []string) {
	var command []string
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		if normalize(filepath.Base(exe)) == "hosted-embed" {
			command = []string{exe}
		} else {
			command = []string{exe, "hosted-embed"}
		}
	}
	if err := hostedembed.Run(args, command); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func normalize(name string) string {
	name = strings.TrimSuffix(name, ".exe")
	return strings.TrimPrefix(name, "quivr-")
}

// Names returns the sorted plugin ids registered in plugins.
func Names(plugins map[string]Entry) []string {
	names := make([]string, 0, len(plugins))
	for name := range plugins {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Resolve selects a registered plugin from argv[0] or argv[1].
func Resolve(argv []string, plugins map[string]Entry) (name string, args []string, fn Entry, ok bool) {
	if len(argv) == 0 {
		return "", nil, nil, false
	}
	base := normalize(filepath.Base(argv[0]))
	if entry, exists := plugins[base]; exists {
		return base, argv[1:], entry, true
	}
	if len(argv) >= 2 {
		sub := normalize(argv[1])
		if entry, exists := plugins[sub]; exists {
			return sub, argv[2:], entry, true
		}
	}
	return "", nil, nil, false
}

// Execute runs the selected plugin or handles --list; returns a process exit code.
func Execute(argv []string, plugins map[string]Entry, stdout, stderr io.Writer) int {
	if len(argv) == 2 && argv[1] == "--list" {
		for _, name := range Names(plugins) {
			fmt.Fprintln(stdout, name)
		}
		return 0
	}
	_, args, entry, ok := Resolve(argv, plugins)
	if !ok {
		fmt.Fprintf(stderr, "usage: quivr-plugins <plugin> [args...]\navailable plugins: %s\n", strings.Join(Names(plugins), ", "))
		return 2
	}
	entry(args)
	return 0
}

// Run is the standard main entrypoint for role-specific multi-call binaries.
func Run(plugins map[string]Entry) {
	if code := Execute(os.Args, plugins, os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}
