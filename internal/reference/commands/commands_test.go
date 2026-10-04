package commands_test

import (
	"os"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/reference/commands"
)

// The fixture has what a reader of the page needs told apart: a group that
// needs a server with its environment and a command's own --help output, and
// an offline group with exit codes and usage lines only. expected.md is
// written by hand, not produced by the renderer.
var fixture = commands.Source{
	From:  "cmd/tool/table.go",
	Intro: "Every command of `tool`.",
	Groups: []commands.Group{
		{
			Title: "Offline commands",
			Needs: "nothing: works offline",
			Intro: "They need no server.",
			ExitCodes: []commands.ExitCode{
				{Code: 0, Meaning: "success"},
				{Code: 2, Meaning: "invalid | arguments"},
			},
			Commands: []commands.Command{
				{Name: "tool lint", Usage: "tool lint [--fix] <dir>", Summary: "Check a directory."},
			},
		},
		{
			Title: "Online commands",
			Needs: "a running server",
			Env:   []commands.EnvVar{{Name: "TOOL_URL", Meaning: "server URL"}},
			Commands: []commands.Command{
				{Name: "tool fetch", Usage: "tool fetch <id>", Summary: "Fetch one item.", Help: "usage: tool fetch <id>\n\nFlags:\n  -json\n    \tprint JSON\n\nExit codes:\n  0  success\n"},
			},
		},
	},
}

func TestRenderFixtureCommandTable(t *testing.T) {
	got, err := commands.Render(fixture)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/expected.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("rendered page differs from testdata/expected.md:\n%s", got)
	}
}

// A table the page cannot represent must fail generation rather than commit a
// page with a missing usage line or two commands behind one link.
func TestRenderRefusesWhatItCannotRepresent(t *testing.T) {
	cases := map[string]struct {
		cmds []commands.Command
		want string
	}{
		"command without usage": {want: "has no usage", cmds: []commands.Command{{Name: "tool a"}}},
		"two commands with one name": {want: "duplicate heading anchor", cmds: []commands.Command{
			{Name: "tool a", Usage: "tool a"}, {Name: "tool a", Usage: "tool a"},
		}},
		"group without commands": {want: "has no commands"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			src := commands.Source{From: "x", Groups: []commands.Group{{Title: "G", Commands: c.cmds}}}
			if _, err := commands.Render(src); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Render error = %v, want one containing %q", err, c.want)
			}
		})
	}
}
