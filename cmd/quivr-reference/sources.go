package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/The-Vibe-Company/quivr/internal/app"
	"github.com/The-Vibe-Company/quivr/internal/buildinfo"
	"github.com/The-Vibe-Company/quivr/internal/online"
	plugincli "github.com/The-Vibe-Company/quivr/internal/plugins/cli"
	"github.com/The-Vibe-Company/quivr/internal/reference/commands"
	"github.com/The-Vibe-Company/quivr/internal/reference/mcp"
)

// cliSource reads the three command tables of the quivr binary. Online
// commands contribute their own --help output, so their flags, environment
// and exit codes are exactly what the binary prints.
func cliSource() (commands.Source, error) {
	engine := commands.Group{
		Title: "Engine commands",
		Needs: "a configuration file (`" + app.ConfigEnv + "`)",
		Intro: "Engine commands run Quivr itself and read the JSON configuration file named by `" + app.ConfigEnv + "`; `api`, `worker` and `migrate` connect to PostgreSQL, Temporal, object storage and Weaviate. `api` and `worker` run until interrupted; `migrate` exits when done. `migrate --contract` also applies deferred contract migrations and closes the application rollback window. `api` and `worker` take no flags. `storage status` connects only to PostgreSQL and checks schema readiness.",
		Env:   []commands.EnvVar{{Name: app.ConfigEnv, Meaning: "path of the JSON configuration file"}},
		ExitCodes: []commands.ExitCode{
			{Code: 0, Meaning: "stopped cleanly, or migrate finished"},
			{Code: 1, Meaning: "the process failed, or the command is unknown; the reason is logged as JSON on stderr"},
			{Code: 2, Meaning: "invalid command arguments"},
		},
	}
	for _, c := range app.Commands {
		usage := "quivr " + c.Name
		if c.Name == "migrate" {
			usage += " [--contract]"
		}
		if c.Name == "storage" {
			usage += " status"
		}
		engine.Commands = append(engine.Commands, commands.Command{Name: "quivr " + c.Name, Usage: usage, Summary: c.Summary})
	}

	offline := commands.Group{
		Title: "Offline commands",
		Needs: "nothing: works offline",
		Intro: "`quivr --version` prints the distribution release, revision, API version and plugin engine compatibility version. `quivr plugin` commands help write and certify a plugin. They need no configuration file and no running server. Run `quivr plugin` alone to list them.",
		ExitCodes: []commands.ExitCode{
			{Code: plugincli.ExitOK, Meaning: "success; for `test`, the plugin is certified"},
			{Code: plugincli.ExitInvalid, Meaning: "for plugin commands only: an invalid plugin or manifest, a target directory `init` cannot write, a plugin `dev` cannot start; for `test`, the plugin is not certified"},
			{Code: plugincli.ExitUsage, Meaning: "invalid flags or arguments"},
		},
	}
	offline.Commands = append(offline.Commands, commands.Command{Name: "quivr " + buildinfo.VersionFlag, Usage: "quivr " + buildinfo.VersionFlag, Summary: "Print build identity without configuration or a running server."})
	for _, c := range plugincli.Commands() {
		offline.Commands = append(offline.Commands, commands.Command{Name: "quivr plugin " + c.Name, Usage: c.Usage, Summary: c.Summary})
	}

	onlineGroup := commands.Group{
		Title: "Online commands",
		Needs: "a running server",
		Intro: "Online commands reach a running Quivr through its public HTTP API only, with an API key. They need no configuration file. Each prints its flags, environment and exit codes with `--help`, shown below.",
		Env: []commands.EnvVar{
			{Name: online.EnvAPIURL, Meaning: "base URL of the Quivr API; `--api-url` overrides it"},
			{Name: online.EnvAPIKey, Meaning: "API key sent as a bearer token; `--api-key` overrides it"},
		},
	}
	for _, c := range online.Commands {
		help, err := onlineHelp(c.Name)
		if err != nil {
			return commands.Source{}, err
		}
		onlineGroup.Commands = append(onlineGroup.Commands, commands.Command{Name: "quivr " + c.Name, Usage: c.Usage, Summary: c.Summary, Help: help})
	}

	return commands.Source{
		From:   "cmd/quivr-reference/sources.go",
		Intro:  "Every command of the `quivr` binary, from the tables the binary itself dispatches on, in `internal/app/run.go` (engine), `internal/plugins/cli/cli.go` (offline) and `internal/online/online.go` (online). The **Needs** column says whether a command runs the engine, works offline, or needs a running server. For the online commands in use, see [Search from the command line](https://docs.quivr.thevibecompany.co/guides/search#search-from-the-command-line); for AI agents, see [Connect an AI agent](https://docs.quivr.thevibecompany.co/guides/ai-agents) and the [MCP reference](mcp.md).",
		Groups: []commands.Group{engine, offline, onlineGroup},
	}, nil
}

// onlineHelp returns what `quivr <name> --help` prints.
func onlineHelp(name string) (string, error) {
	var stdout, stderr bytes.Buffer
	env := online.Env{Getenv: func(string) string { return "" }, Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr}
	if code := online.RunContext(context.Background(), name, []string{"--help"}, env); code != online.ExitOK {
		return "", fmt.Errorf("quivr %s --help exited %d: %s", name, code, stderr.String())
	}
	return stdout.String(), nil
}

func mcpSource() mcp.Source {
	return mcp.Source{
		From:     "internal/online/mcp_catalogue.go",
		Intro:    "`quivr mcp --profile <profile>` serves the tools of one profile to an AI agent over MCP (Model Context Protocol) on stdin and stdout. It needs a running server and an API key, like every online command. How to start it, cite a source and who can see what: [Connect an AI agent](https://docs.quivr.thevibecompany.co/guides/ai-agents). The command itself: [`quivr mcp`](cli.md#quivr-mcp).",
		Profiles: online.MCPProfiles,
		Tools:    online.MCPTools,
	}
}
