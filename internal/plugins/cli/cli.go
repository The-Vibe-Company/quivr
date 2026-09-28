// Package cli implements the `quivr plugin` command group. It needs no running
// stack and no QUIVR_CONFIG.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

// Exit codes.
const (
	ExitOK      = 0
	ExitInvalid = 1
	ExitUsage   = 2
)

type command struct {
	usage string
	run   func(ctx context.Context, args []string, stdout, stderr io.Writer) int
}

// commands is the `quivr plugin` subcommand table; the Contract Runner adds
// test here.
var commands map[string]command

const inspectUsage = "quivr plugin inspect [--json] <plugin-dir|quivr-plugin.yaml>"

func init() {
	commands = map[string]command{
		"inspect": {usage: inspectUsage, run: inspect},
		"init":    {usage: initUsage, run: initCommand},
		"dev":     {usage: devUsage, run: dev},
	}
}

func usage(stderr io.Writer) int {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Fprintln(stderr, "usage:")
	for _, name := range names {
		fmt.Fprintln(stderr, "  "+commands[name].usage)
	}
	return ExitUsage
}

// Run executes `quivr plugin <args>` and returns the process exit code. An
// interrupt or SIGTERM cancels long-running commands such as dev.
func Run(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return RunContext(ctx, args, stdout, stderr)
}

// RunContext is Run with an explicit context; cancelling it stops dev.
func RunContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return usage(stderr)
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "unknown plugin command %q\n", args[0])
		return usage(stderr)
	}
	return cmd.run(ctx, args[1:], stdout, stderr)
}

func inspect(_ context.Context, args []string, stdout, stderr io.Writer) int {
	asJSON := false
	var targets []string
	for _, arg := range args {
		switch {
		case arg == "--json":
			asJSON = true
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "unknown flag %q\nusage: %s\n", arg, inspectUsage)
			return ExitUsage
		default:
			targets = append(targets, arg)
		}
	}
	if len(targets) != 1 {
		fmt.Fprintf(stderr, "usage: %s\n", inspectUsage)
		return ExitUsage
	}
	report := plugins.Inspect(targets[0])
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		_ = enc.Encode(report)
	} else {
		printReport(stdout, report)
	}
	if !report.Valid {
		return ExitInvalid
	}
	return ExitOK
}

func printReport(w io.Writer, r plugins.Report) {
	m := r.Manifest
	status := "valid"
	if !r.Valid {
		status = fmt.Sprintf("invalid (%d error%s)", len(r.Errors), plural(len(r.Errors)))
	}
	if m != nil {
		fmt.Fprintf(w, "Plugin %s %s: %s\n", m.ID, m.Version, status)
	} else {
		fmt.Fprintf(w, "Plugin manifest: %s\n", status)
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	row := func(label, value string) { fmt.Fprintf(tw, "  %s\t%s\n", label, value) }
	fmt.Fprintln(tw, "Manifest")
	row("path", r.Path)
	if r.ManifestDigest != "" {
		row("digest", r.ManifestDigest)
	}
	if m != nil && m.Description != "" {
		row("description", m.Description)
	}
	fmt.Fprintln(tw, "Compatibility")
	for _, c := range []struct {
		label, against string
		check          *plugins.RangeCheck
	}{{"engine", "engine " + r.EngineVersion, r.Compatibility.Engine}, {"plugin_api", "Plugin API " + r.PluginAPIVersion, r.Compatibility.PluginAPI}} {
		if c.check == nil {
			row(c.label, "(invalid or missing range) against "+c.against)
			continue
		}
		verdict := "compatible with "
		if !c.check.Compatible {
			verdict = "NOT compatible with "
		}
		row(c.label, fmt.Sprintf("%s  %s%s", c.check.Range, verdict, c.against))
	}
	if m != nil {
		if n := m.Contributions.Normalizer; n != nil {
			fmt.Fprintln(tw, "Contribution normalizer")
			row("media types", strings.Join(n.MediaTypes, ", "))
			row("timeout", fmt.Sprintf("%d ms", n.TimeoutMS))
			row("retry intent", fmt.Sprintf("up to %d attempts on retryable errors", n.Retry.MaxAttempts))
			row("declared max response", fmt.Sprintf("%d bytes", n.Limits.MaxResponseBytes))
			row("declared max parts", fmt.Sprint(n.Limits.MaxParts))
		}
		fmt.Fprintln(tw, "Configuration schema")
		if m.Configuration == nil {
			row("(none)", "")
		} else {
			for _, line := range describeSchema(m.Configuration.Schema) {
				row(line[0], line[1])
			}
		}
		fmt.Fprintln(tw, "Secrets")
		if len(m.Secrets) == 0 {
			row("(none)", "")
		}
		for _, s := range m.Secrets {
			need := "required"
			if s.Required != nil && !*s.Required {
				need = "optional"
			}
			row(s.Name, strings.TrimSpace(need+"  "+s.Description))
		}
		fmt.Fprintln(tw, "Extension namespaces")
		if len(m.Extensions) == 0 {
			row("(none)", "")
		}
		for _, ns := range sortedKeys(m.Extensions) {
			row(ns, "schema versions "+strings.Join(sortedKeys(m.Extensions[ns]), ", "))
		}
		fmt.Fprintln(tw, "Run command")
		if m.Run == nil {
			row("(none)", "")
		} else {
			row("argv", strings.Join(m.Run.Command, " "))
		}
	}
	if len(r.Errors) > 0 {
		fmt.Fprintln(tw, "Errors")
		for _, issue := range r.Errors {
			path := issue.Path
			if path == "" {
				path = "/"
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", issue.Code, path, issue.Message)
		}
	}
	_ = tw.Flush()
}

// describeSchema lists the top-level properties of an object schema.
func describeSchema(raw json.RawMessage) [][2]string {
	var schema struct {
		Type       any                        `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil || len(schema.Properties) == 0 {
		return [][2]string{{"schema", string(raw)}}
	}
	required := map[string]bool{}
	for _, name := range schema.Required {
		required[name] = true
	}
	var out [][2]string
	for _, name := range sortedKeys(schema.Properties) {
		var prop struct {
			Type any `json:"type"`
		}
		_ = json.Unmarshal(schema.Properties[name], &prop)
		detail := "any"
		if prop.Type != nil {
			detail = fmt.Sprint(prop.Type)
		}
		if required[name] {
			detail += ", required"
		} else {
			detail += ", optional"
		}
		out = append(out, [2]string{name, detail})
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
