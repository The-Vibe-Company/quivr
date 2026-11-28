package multicall

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestDispatchByArgv0SubcommandListAndUnknown(t *testing.T) {
	var called string
	var gotArgs []string
	record := func(name string) Entry {
		return func(args []string) {
			called = name
			gotArgs = append([]string(nil), args...)
		}
	}
	registry := map[string]Entry{
		"rss":          record("rss"),
		"text-embed": record("text-embed"),
	}

	cases := []struct {
		name     string
		argv     []string
		wantName string
		wantArgs []string
	}{
		{name: "symlink with quivr prefix", argv: []string{"/usr/local/bin/quivr-rss"}, wantName: "rss"},
		{name: "symlink with args", argv: []string{"/usr/local/bin/quivr-text-embed", "configure", "/tmp/c.json"}, wantName: "text-embed", wantArgs: []string{"configure", "/tmp/c.json"}},
		{name: "explicit subcommand", argv: []string{"/usr/local/bin/quivr-plugins", "text-embed", "configure", "/tmp/c.json"}, wantName: "text-embed", wantArgs: []string{"configure", "/tmp/c.json"}},
		{name: "explicit subcommand with quivr prefix", argv: []string{"quivr-plugins", "quivr-rss"}, wantName: "rss"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called, gotArgs = "", nil
			var out, errBuf bytes.Buffer
			if code := Execute(tc.argv, registry, &out, &errBuf); code != 0 {
				t.Fatalf("Execute(%v) = %d, stderr=%q", tc.argv, code, errBuf.String())
			}
			if called != tc.wantName || !reflect.DeepEqual(gotArgs, tc.wantArgs) {
				t.Fatalf("dispatched (%q, %v), want (%q, %v)", called, gotArgs, tc.wantName, tc.wantArgs)
			}
		})
	}

	var out, errBuf bytes.Buffer
	if code := Execute([]string{"quivr-plugins", "--list"}, registry, &out, &errBuf); code != 0 {
		t.Fatalf("--list exited %d: %s", code, errBuf.String())
	}
	if got := strings.TrimSpace(out.String()); got != "rss\ntext-embed" {
		t.Fatalf("--list = %q, want sorted plugin names", got)
	}

	out.Reset()
	errBuf.Reset()
	if code := Execute([]string{"quivr-plugins", "unknown"}, registry, &out, &errBuf); code != 2 {
		t.Fatalf("unknown plugin exited %d, want 2", code)
	}
	if !strings.Contains(errBuf.String(), "available plugins: rss, text-embed") {
		t.Fatalf("stderr = %q, want available plugin list", errBuf.String())
	}
}

func TestRoleBinariesListExpectedPluginsAndConfigureTextEmbed(t *testing.T) {
	dir := t.TempDir()
	targets := []struct {
		pkg  string
		want []string
	}{
		{pkg: "./cmd/quivr-plugins", want: []string{"core-ingest", "core-retrieve", "m365-mail", "object-storage-archive", "rss", "text-embed", "x-list"}},
		{pkg: "./cmd/quivr-plugins-api", want: []string{"core-ingest", "core-retrieve", "text-embed", "x-list"}},
		{pkg: "./cmd/quivr-plugins-worker", want: []string{"core-ingest", "m365-mail", "object-storage-archive", "rss", "text-embed", "x-list"}},
	}

	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	example := filepath.Join("..", "..", "plugins", "text-embed", "examples", "tei.json")
	for _, target := range targets {
		name := filepath.Base(target.pkg)
		bin := filepath.Join(dir, name)
		build := exec.Command(goBin, "build", "-trimpath", "-ldflags", "-s -w", "-o", bin, target.pkg)
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("go build %s: %v\n%s", target.pkg, err, out)
		}

		listed, err := exec.Command(bin, "--list").Output()
		if err != nil {
			t.Fatalf("%s --list: %v", name, err)
		}
		got := strings.Fields(string(listed))
		if !reflect.DeepEqual(got, target.want) {
			t.Fatalf("%s --list = %v, want %v", name, got, target.want)
		}

		// Verify text-embed configure works through both symlink dispatch and subcommand dispatch.
		link := filepath.Join(t.TempDir(), "quivr-text-embed")
		if err := os.Symlink(bin, link); err != nil {
			t.Fatal(err)
		}
		for _, cmd := range [][]string{{bin, "text-embed", "configure", example}, {link, "configure", example}} {
			raw, err := exec.Command(cmd[0], cmd[1:]...).Output()
			if err != nil {
				t.Fatalf("%v failed: %v", cmd, err)
			}
			var manifest struct {
				ID  string `json:"id"`
				Run struct {
					Command []string `json:"command"`
				} `json:"run"`
			}
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatalf("%v produced invalid JSON: %v", cmd, err)
			}
			if manifest.ID != "text.embed" {
				t.Fatalf("%v manifest id = %q, want text.embed", cmd, manifest.ID)
			}
			if !reflect.DeepEqual(manifest.Run.Command, []string{bin, "text-embed"}) {
				t.Fatalf("%v run.command = %v, want %v", cmd, manifest.Run.Command, []string{bin, "text-embed"})
			}
		}
	}
}
