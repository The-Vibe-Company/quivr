package acceptance

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectMCP starts `quivr mcp --profile read` from the built binary with only
// the given API key, and connects an MCP client to it over stdio.
func connectMCP(t *testing.T, bin, key string) *mcp.ClientSession {
	t.Helper()
	cmd := exec.Command(bin, "mcp", "--profile", "read")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "QUIVR_API_URL=" + os.Getenv("QUIVR_TEST_URL"), "QUIVR_API_KEY=" + key}
	cmd.Stderr = os.Stderr
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "v0"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect quivr mcp: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// callTool calls a tool and returns its text result and whether it is a tool error.
func callTool(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("call %s: %d content blocks", name, len(res.Content))
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("call %s: content %T", name, res.Content[0])
	}
	return text.Text, res.IsError
}

func decodeTool(t *testing.T, s *mcp.ClientSession, name string, args map[string]any, out any) {
	t.Helper()
	text, isError := callTool(t, s, name, args)
	if isError {
		t.Fatalf("%s failed: %s", name, text)
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		t.Fatalf("%s result is not JSON: %v\n%s", name, err, text)
	}
}

type mcpHit struct {
	RecordID  string `json:"record_id"`
	VersionID string `json:"version_id"`
	PartKey   string `json:"part_key"`
	Excerpt   struct {
		Text  string `json:"text"`
		Start int    `json:"start"`
		End   int    `json:"end"`
	} `json:"excerpt"`
}

type mcpRecordRead struct {
	Record struct {
		RecordID         string `json:"record_id"`
		CurrentVersionID string `json:"current_version_id"`
	} `json:"record"`
	Version struct {
		VersionID string `json:"version_id"`
		Manifest  struct {
			Parts []struct {
				Key     string `json:"key"`
				Content struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"parts"`
		} `json:"manifest"`
	} `json:"version"`
}

// TestCLIMCPReadProfile drives `quivr mcp --profile read` as an agent would:
// it sees only read-only tools, finds a passage with citable provenance, opens
// the Record from the hit, and never reaches a Corpus outside its key's scope.
func TestCLIMCPReadProfile(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	bin := quivrBinary(t)
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := monitoringRun()
	c := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "MCP read", "idempotency_key": "mcp-corpus-" + run}, 201)["corpus_id"].(string)
	text := "Tidal lagoon 🌊 surveys\nThe glacier meltwater feeds the valley river."
	r := request(t, "POST", "/v0/records", admin, inlineCommand(c, "mcp-first-"+run, "lagoon", text), 202)
	r = awaitReceipt(t, r["receipt_id"].(string))
	awaitSearchable(t, r)

	agent := connectMCP(t, bin, admin)
	tools, err := agent.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %s is not declared read-only", tool.Name)
		}
	}
	slices.Sort(names)
	if want := []string{"list_corpora", "read_record", "search"}; !slices.Equal(names, want) {
		t.Fatalf("tools %v, want %v", names, want)
	}

	var found struct {
		Items []mcpHit `json:"items"`
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		decodeTool(t, agent, "search", map[string]any{"query": "glacier meltwater", "corpus_ids": []string{c}, "mode": "lexical"}, &found)
		if len(found.Items) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("searchable text never returned by the search tool")
		}
		time.Sleep(200 * time.Millisecond)
	}
	hit := found.Items[0]
	if hit.RecordID != r["record_id"] || hit.VersionID != r["version_id"] || hit.PartKey != "body" {
		t.Fatalf("hit provenance %+v, want record %v version %v part body", hit, r["record_id"], r["version_id"])
	}
	if !strings.Contains(hit.Excerpt.Text, "glacier meltwater") {
		t.Fatalf("excerpt %q does not contain the query", hit.Excerpt.Text)
	}

	// The hit expands into the cited Version: its Part holds the excerpt at the hit's code-point offsets.
	var read mcpRecordRead
	decodeTool(t, agent, "read_record", map[string]any{"record_id": hit.RecordID, "version_id": hit.VersionID}, &read)
	if read.Record.RecordID != hit.RecordID || read.Version.VersionID != hit.VersionID {
		t.Fatalf("read_record returned record %s version %s", read.Record.RecordID, read.Version.VersionID)
	}
	var cited []rune
	for _, p := range read.Version.Manifest.Parts {
		if p.Key == hit.PartKey {
			cited = []rune(p.Content.Text)
		}
	}
	if hit.Excerpt.End > len(cited) || string(cited[hit.Excerpt.Start:hit.Excerpt.End]) != hit.Excerpt.Text {
		t.Fatalf("Part %s does not hold excerpt %q at [%d,%d): %q", hit.PartKey, hit.Excerpt.Text, hit.Excerpt.Start, hit.Excerpt.End, string(cited))
	}
	var current mcpRecordRead
	decodeTool(t, agent, "read_record", map[string]any{"record_id": hit.RecordID}, &current)
	if current.Version.VersionID != read.Record.CurrentVersionID || current.Version.VersionID != hit.VersionID {
		t.Fatalf("read_record without version_id read %s, want current %s", current.Version.VersionID, read.Record.CurrentVersionID)
	}

	// A key scoped to another Corpus never sees this one, and searching it is refused, not empty.
	scoped := connectMCP(t, bin, os.Getenv("QUIVR_TEST_SCOPED"))
	var page struct {
		Items []struct {
			CorpusID string `json:"corpus_id"`
		} `json:"items"`
	}
	decodeTool(t, scoped, "list_corpora", nil, &page)
	if len(page.Items) != 1 || page.Items[0].CorpusID == c {
		t.Fatalf("scoped key lists %+v, want only its own Corpus", page.Items)
	}
	if out, isError := callTool(t, scoped, "search", map[string]any{"query": "glacier", "corpus_ids": []string{c}}); !isError || !strings.HasPrefix(out, "forbidden:") {
		t.Fatalf("scoped search outside its Corpus: error %v, %q", isError, out)
	}

	for _, args := range [][]string{{"mcp"}, {"mcp", "--profile", "ingest"}} {
		if got := runCLI(t, bin, []string{"QUIVR_API_URL=" + os.Getenv("QUIVR_TEST_URL")}, t.TempDir(), args...); got.exit != 2 || got.stdout != "" {
			t.Fatalf("%v: exit %d stdout %q, want exit 2 and no output", args, got.exit, got.stdout)
		}
	}
}
