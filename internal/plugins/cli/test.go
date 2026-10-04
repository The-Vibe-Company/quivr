package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/plugins/runner"
)

const testUsage = "quivr plugin test [--endpoint <url>] [--report <file>] [--fixture <file>]... [--startup-timeout <duration>] [<plugin-dir>]"

// test is the Plugin Contract Runner: it certifies that the engine can safely
// invoke every Contribution the plugin declares. Exit 0 certified, 1 not certified, 2 usage.
func test(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts := runner.Options{Output: stderr}
	var reportPath string
	var positional []string
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, format+"\nusage: %s\n", append(a, testUsage)...)
		return ExitUsage
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := strings.Cut(arg, "=")
		switch name {
		case "--endpoint", "--report", "--fixture", "--startup-timeout":
			if !hasValue {
				if i+1 >= len(args) {
					return fail("%s needs a value", name)
				}
				i++
				value = args[i]
			}
		}
		switch {
		case name == "--endpoint":
			if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
				return fail("--endpoint must be an http:// or https:// URL, got %q", value)
			}
			opts.Endpoint = value
		case name == "--report":
			reportPath = value
		case name == "--fixture":
			opts.Fixtures = append(opts.Fixtures, value)
		case name == "--startup-timeout":
			d, err := time.ParseDuration(value)
			if err != nil || d <= 0 {
				return fail("--startup-timeout must be a positive duration such as 30s, got %q", value)
			}
			opts.StartupTimeout = d
		case strings.HasPrefix(arg, "-"):
			return fail("unknown flag %q", arg)
		default:
			positional = append(positional, arg)
		}
	}
	switch len(positional) {
	case 0:
		opts.Dir = "."
	case 1:
		opts.Dir = positional[0]
	default:
		return fail("test takes at most one plugin directory")
	}
	report := runner.Run(ctx, opts)
	printTestReport(stdout, report)
	if reportPath != "" {
		f, err := os.Create(reportPath)
		if err == nil {
			err = runner.WriteJSON(f, report)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			fmt.Fprintf(stderr, "quivr plugin test: write report: %v\n", err)
			return ExitInvalid
		}
	}
	if !report.Certified {
		return ExitInvalid
	}
	return ExitOK
}

func printTestReport(w io.Writer, r runner.Report) {
	name := "plugin"
	if r.Plugin.ID != "" {
		name = fmt.Sprintf("plugin %s %s", r.Plugin.ID, r.Plugin.Version)
	}
	verdict := "CERTIFIED: the engine can safely invoke this plugin"
	if !r.Certified {
		verdict = "NOT CERTIFIED"
	}
	fmt.Fprintf(w, "Contract Runner: %s, engine %s, Plugin API %s\n", name, r.EngineVersion, r.PluginAPIVersion)
	fmt.Fprintf(w, "  manifest  %s\n", r.Plugin.ManifestPath)
	if r.Target.BaseURL != "" {
		fmt.Fprintf(w, "  target    %s (%s)\n", r.Target.BaseURL, r.Target.Mode)
	}
	for _, c := range r.Checks {
		subject := c.Title
		if c.Fixture != "" {
			subject = c.Fixture + ": " + subject
		}
		if c.Contribution != "" {
			subject = "[" + c.Contribution + "] " + subject
		}
		fmt.Fprintf(w, "  %-4s  %-15s  %s (%d ms)\n", strings.ToUpper(string(c.Status)), c.ID, subject, c.DurationMS)
		if c.Note != "" && c.Status != runner.Pass {
			fmt.Fprintf(w, "          note: %s\n", c.Note)
		}
		for _, issue := range c.Issues {
			path := issue.Path
			if path == "" {
				path = "/"
			}
			fmt.Fprintf(w, "          %s  %s  %s\n", issue.Code, path, issue.Message)
		}
	}
	fmt.Fprintf(w, "%s (%d passed, %d failed, %d skipped)\n", verdict, r.Summary.Passed, r.Summary.Failed, r.Summary.Skipped)
}
