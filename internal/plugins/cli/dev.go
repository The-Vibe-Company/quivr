package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
)

const devUsage = "quivr plugin dev [--fixture <file>] [--watch] [--port <n>] [--startup-timeout <duration>] [<plugin-dir>]"

const (
	pollInterval = 500 * time.Millisecond
	stopGrace    = 5 * time.Second
)

type devOptions struct {
	dir            string
	fixture        string
	watch          bool
	port           int
	startupTimeout time.Duration
}

func parseDevArgs(args []string, stderr io.Writer) (devOptions, bool) {
	opts := devOptions{startupTimeout: 30 * time.Second}
	var positional []string
	fail := func(format string, a ...any) (devOptions, bool) {
		fmt.Fprintf(stderr, format+"\nusage: %s\n", append(a, devUsage)...)
		return opts, false
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := strings.Cut(arg, "=")
		takesValue := name == "--fixture" || name == "--port" || name == "--startup-timeout"
		if takesValue && !hasValue {
			if i+1 >= len(args) {
				return fail("%s needs a value", name)
			}
			i++
			value = args[i]
		}
		switch {
		case name == "--fixture":
			opts.fixture = value
		case name == "--port":
			port, err := strconv.Atoi(value)
			if err != nil || port < 1 || port > 65535 {
				return fail("--port must be a TCP port, got %q", value)
			}
			opts.port = port
		case name == "--startup-timeout":
			d, err := time.ParseDuration(value)
			if err != nil || d <= 0 {
				return fail("--startup-timeout must be a positive duration such as 30s, got %q", value)
			}
			opts.startupTimeout = d
		case arg == "--watch":
			opts.watch = true
		case strings.HasPrefix(arg, "-"):
			return fail("unknown flag %q", arg)
		default:
			positional = append(positional, arg)
		}
	}
	switch len(positional) {
	case 0:
		opts.dir = "."
	case 1:
		opts.dir = positional[0]
	default:
		return fail("dev takes at most one plugin directory")
	}
	// Without a fixture there is nothing to replay: keep serving and watching.
	if opts.fixture == "" {
		opts.watch = true
	}
	return opts, true
}

// dev runs a plugin locally: launch run.command, wait for health, check
// discovery against quivr-plugin.yaml and, with --fixture, replay the fixture
// and print the validated response on stdout. With --watch (the default
// without --fixture) it restarts the plugin when a source file changes.
func dev(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts, ok := parseDevArgs(args, stderr)
	if !ok {
		return ExitUsage
	}
	// The plugin's output and dev's status lines share stderr.
	stderr = &lockedWriter{w: stderr}
	if opts.port == 0 {
		// One port for the whole session, so restarts keep the same address.
		port, err := devhost.FreePort("127.0.0.1")
		if err != nil {
			fmt.Fprintf(stderr, "quivr plugin dev: assign a port: %v\n", err)
			return ExitInvalid
		}
		opts.port = port
	}
	s := &devSession{devOptions: opts, stdout: stdout, stderr: stderr}
	proc, ok := s.start(ctx)
	if !opts.watch {
		if proc != nil {
			_ = proc.Stop(stopGrace)
		}
		if ok {
			return ExitOK
		}
		return ExitInvalid
	}
	s.status("watching %s for changes (Ctrl-C to stop)", opts.dir)
	watcher := devhost.NewWatcher(opts.dir)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	exited := processDone(proc)
	for {
		select {
		case <-ctx.Done():
			if proc != nil {
				_ = proc.Stop(stopGrace)
			}
			s.status("stopped")
			return ExitOK
		case <-exited:
			s.status("plugin exited (%v); waiting for a source change", proc.ExitError())
			exited = nil
		case <-ticker.C:
			if !watcher.Changed() {
				continue
			}
			s.status("change detected; restarting the plugin")
			if proc != nil {
				_ = proc.Stop(stopGrace)
			}
			proc, _ = s.start(ctx)
			exited = processDone(proc)
		}
	}
}

func processDone(p *devhost.Process) <-chan struct{} {
	if p == nil {
		return nil
	}
	return p.Done()
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

type devSession struct {
	devOptions
	stdout, stderr io.Writer
}

func (s *devSession) status(format string, args ...any) {
	fmt.Fprintf(s.stderr, "quivr plugin dev: "+format+"\n", args...)
}

func (s *devSession) issues(title string, issues []plugins.Issue) {
	s.status("%s", title)
	for _, issue := range issues {
		path := issue.Path
		if path == "" {
			path = "/"
		}
		fmt.Fprintf(s.stderr, "  %s  %s  %s\n", issue.Code, path, issue.Message)
	}
}

// start runs one plugin generation: inspect, launch, health, discovery and the
// optional fixture replay. It returns the running process (nil when it could
// not start) and whether every check passed.
func (s *devSession) start(ctx context.Context) (*devhost.Process, bool) {
	report := plugins.Inspect(s.dir)
	if !report.Valid {
		s.issues(fmt.Sprintf("%s is invalid (see quivr plugin inspect)", report.Path), report.Errors)
		return nil, false
	}
	m := report.Manifest
	if m.Run == nil {
		s.status("%s declares no run.command; add one so dev can start the plugin", report.Path)
		return nil, false
	}
	var request []byte
	var batches []devhost.SubscriptionBatch
	if s.fixture != "" {
		var issues []plugins.Issue
		var err error
		raw, readErr := os.ReadFile(s.fixture)
		unparsable := readErr != nil || !json.Valid(raw)
		if devhost.IsConnectorFixture(raw) {
			issues = []plugins.Issue{{Code: plugins.CodeInvalidManifest, Path: "/connector",
				Message: "quivr plugin dev does not replay connector fixtures; run quivr plugin test, which fetches every page of a connector fixture"}}
		} else if devhost.IsIngestionFixture(raw) {
			issues = []plugins.Issue{{Code: plugins.CodeInvalidManifest, Path: "/ingestion",
				Message: "quivr plugin dev does not replay ingestion fixtures; run quivr plugin test, which segments, embeds and encodes the fixture's queries"}}
		} else if devhost.IsSubscriptionFixture(raw) || (unparsable && m.Contributions.Normalizer == nil) {
			batches, issues, err = devhost.BuildSubscriptionRequests(s.fixture, m)
		} else if m.Contributions.Normalizer == nil {
			issues = []plugins.Issue{{Code: plugins.CodeInvalidManifest, Path: "/contributions/normalizer",
				Message: "this is an invocation fixture for a normalizer, but the manifest declares no normalizer Contribution"}}
		} else {
			request, issues, err = devhost.BuildFixtureRequest(s.fixture, m)
		}
		if err != nil {
			s.status("fixture %s: %v", s.fixture, err)
			return nil, false
		}
		if len(issues) > 0 {
			s.issues(fmt.Sprintf("fixture %s is invalid", s.fixture), issues)
			return nil, false
		}
	}
	proc, err := devhost.Start(devhost.Options{
		Dir: s.dir, Command: m.Run.Command, Manifest: report.Path, Port: s.port, Output: s.stderr,
	})
	if err != nil {
		s.status("%v", err)
		return nil, false
	}
	s.status("started %s %s: %s on %s", m.ID, m.Version, strings.Join(m.Run.Command, " "), proc.BaseURL)
	healthCtx, cancel := context.WithTimeout(ctx, s.startupTimeout)
	err = proc.WaitHealthy(healthCtx)
	cancel()
	if err != nil {
		s.status("%v", err)
		return proc, false
	}
	s.status("plugin healthy")
	issues, err := devhost.CheckDiscovery(ctx, proc.BaseURL, report)
	if err != nil {
		s.status("%v", err)
		return proc, false
	}
	if len(issues) > 0 {
		s.issues("discovery does not match "+report.Path, issues)
		return proc, false
	}
	s.status("discovery matches %s (%s)", filepath.Base(report.Path), report.ManifestDigest)
	if batches != nil {
		return proc, s.replaySubscription(ctx, proc, m, batches)
	}
	if request == nil {
		s.status("plugin ready at %s", proc.BaseURL)
		return proc, true
	}
	n := m.Contributions.Normalizer
	invokeCtx, cancel := context.WithTimeout(ctx, time.Duration(n.TimeoutMS)*time.Millisecond)
	defer cancel()
	input, _ := plugins.InputFromRequest(request)
	result, err := devhost.InvokeNormalizerWith(invokeCtx, proc.BaseURL, request, plugins.MaxResponseBytes(m), func(body []byte) []plugins.Issue {
		return plugins.CheckNormalizerOutput(invokeCtx, body, plugins.OutputContext{Manifest: m, Input: input})
	})
	if err != nil {
		s.status("%v", err)
		return proc, false
	}
	if len(result.Issues) > 0 {
		s.issues(fmt.Sprintf("normalizer answered HTTP %d with an invalid response", result.Status), result.Issues)
		if len(result.Body) > 0 {
			fmt.Fprintf(s.stderr, "%s\n", result.Body)
		}
		return proc, false
	}
	if result.Error != nil {
		kind := "terminal"
		if result.Error.Retryable {
			kind = "retryable"
		}
		s.status("normalizer returned HTTP %d, %s error %s: %s", result.Status, kind, result.Error.Code, result.Error.Message)
		return proc, false
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, result.Body, "", "  "); err != nil {
		pretty.Write(result.Body)
	}
	fmt.Fprintf(s.stdout, "%s\n", pretty.Bytes())
	var summary struct {
		Manifest struct {
			Parts     []json.RawMessage `json:"parts"`
			Relations []json.RawMessage `json:"relations"`
		} `json:"manifest"`
		Warnings []json.RawMessage `json:"warnings"`
	}
	_ = json.Unmarshal(result.Body, &summary)
	s.status("response valid: %d Parts, %d Relations, %d warnings; the engine's Manifest validation accepts it",
		len(summary.Manifest.Parts), len(summary.Manifest.Relations), len(summary.Warnings))
	return proc, true
}

// replaySubscription sends the batches of a subscription fixture, validates
// every answer with the engine's subscription output checks and the fixture's
// expected decisions, and prints the decisions of all batches.
func (s *devSession) replaySubscription(ctx context.Context, proc *devhost.Process, m *plugins.Manifest, batches []devhost.SubscriptionBatch) bool {
	sub := m.Contributions.Subscription
	var decisions []json.RawMessage
	counts := map[string]int{}
	for _, b := range batches {
		invokeCtx, cancel := context.WithTimeout(ctx, time.Duration(sub.TimeoutMS)*time.Millisecond)
		result, err := devhost.InvokeSubscriptionWith(invokeCtx, proc.BaseURL, b.Body, plugins.SubscriptionMaxResponseBytes(m), func(body []byte) []plugins.Issue {
			return plugins.CheckSubscriptionOutput(body, b.View, m)
		})
		cancel()
		if err != nil {
			s.status("%v", err)
			return false
		}
		if len(result.Issues) == 0 && result.Error == nil {
			result.Issues = devhost.ExpectationIssues(result.Body, b.Expect)
		}
		if len(result.Issues) > 0 {
			s.issues(fmt.Sprintf("subscription batch %d answered HTTP %d with an invalid response", b.Index, result.Status), result.Issues)
			if len(result.Body) > 0 {
				fmt.Fprintf(s.stderr, "%s\n", result.Body)
			}
			return false
		}
		if result.Error != nil {
			kind := "terminal"
			if result.Error.Retryable {
				kind = "retryable"
			}
			s.status("subscription batch %d returned HTTP %d, %s error %s: %s", b.Index, result.Status, kind, result.Error.Code, result.Error.Message)
			return false
		}
		var response struct {
			Decisions []json.RawMessage `json:"decisions"`
		}
		_ = json.Unmarshal(result.Body, &response)
		for _, d := range response.Decisions {
			var decision struct {
				Decision string `json:"decision"`
			}
			_ = json.Unmarshal(d, &decision)
			counts[decision.Decision]++
		}
		decisions = append(decisions, response.Decisions...)
	}
	merged, _ := json.Marshal(map[string]any{"decisions": decisions})
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, merged, "", "  "); err != nil {
		pretty.Write(merged)
	}
	fmt.Fprintf(s.stdout, "%s\n", pretty.Bytes())
	s.status("response valid: %d decisions in %d batches (%d match, %d no_match, %d not_ready); the engine's subscription output validation accepts them and they match the fixture's expectations",
		len(decisions), len(batches), counts[plugins.DecisionMatch], counts[plugins.DecisionNoMatch], counts[plugins.DecisionNotReady])
	return true
}
