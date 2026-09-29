// Package commands renders the command-line reference from the command tables
// of the `quivr` binary. The caller reads the tables (engine, offline and online
// commands) into a Source; this package only lays them out, so a command added
// to a table appears on the page at the next `make generate`.
package commands

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/reference"
)

// Source is everything the page shows, in order.
type Source struct {
	// From names the tables the page is generated from, for the notice.
	From string
	// Intro is Markdown written under the title.
	Intro string
	// Groups are the kinds of command, each with what it needs to run.
	Groups []Group
}

// Group is one kind of command sharing what it needs, its environment and its
// exit codes.
type Group struct {
	Title string
	// Needs says what a command of the group needs to run, in a few words,
	// for the overview table (for example "a running server").
	Needs string
	// Intro is Markdown written under the group heading.
	Intro     string
	Env       []EnvVar
	ExitCodes []ExitCode
	Commands  []Command
}

// EnvVar is an environment variable every command of a group reads.
type EnvVar struct {
	Name    string
	Meaning string
}

// ExitCode is a process exit code shared by a group.
type ExitCode struct {
	Code    int
	Meaning string
}

// Command is one command line.
type Command struct {
	// Name is the full command, such as "quivr search".
	Name    string
	Usage   string
	Summary string
	// Help is the command's own --help output, shown verbatim when set.
	Help string
}

// Render writes the command-line reference page.
func Render(src Source) ([]byte, error) {
	if len(src.Groups) == 0 {
		return nil, errors.New("no command groups")
	}
	p := reference.NewPage("Command-line reference", src.From)
	p.Para(src.Intro)

	p.Heading(2, "Commands")
	var rows [][]string
	for _, g := range src.Groups {
		if len(g.Commands) == 0 {
			p.Fail(fmt.Errorf("group %q has no commands", g.Title))
		}
		for _, c := range g.Commands {
			rows = append(rows, []string{reference.Link(reference.Code(c.Name), reference.Anchor(c.Name)), g.Needs, c.Summary})
		}
	}
	p.Table([]string{"Command", "Needs", "Summary"}, rows)

	for _, g := range src.Groups {
		p.Heading(2, g.Title)
		p.Para(g.Intro)
		env := make([][]string, len(g.Env))
		for i, e := range g.Env {
			env[i] = []string{reference.Code(e.Name), e.Meaning}
		}
		p.Table([]string{"Environment variable", "Meaning"}, env)
		codes := make([][]string, len(g.ExitCodes))
		for i, e := range g.ExitCodes {
			codes[i] = []string{strconv.Itoa(e.Code), e.Meaning}
		}
		p.Table([]string{"Exit code", "Meaning"}, codes)
		for _, c := range g.Commands {
			if c.Usage == "" {
				p.Fail(fmt.Errorf("command %q has no usage", c.Name))
			}
			p.Heading(3, c.Name)
			p.Para(c.Summary)
			if help := strings.TrimSpace(c.Help); help != "" {
				// The help opens with the usage and adds flags, environment and exit codes.
				p.Para(reference.Code(c.Name+" --help") + " prints:")
				p.CodeBlock("text", help)
			} else {
				p.CodeBlock("text", c.Usage)
			}
		}
	}
	return p.Bytes()
}
