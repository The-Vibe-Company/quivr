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

// mcpCommand is `quivr mcp --profile <profile>` from the built binary, with
// only the given API key.
func mcpCommand(t *testing.T, ctx context.Context, bin, profile, key string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, "mcp", "--profile", profile)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "QUIVR_API_URL=" + os.Getenv("QUIVR_TEST_URL"), "QUIVR_API_KEY=" + key}
	cmd.Stderr = os.Stderr
	return cmd
}

// connectMCP starts `quivr mcp` and connects an MCP client to it over stdio.
func connectMCP(t *testing.T, bin, profile, key string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	cmd := mcpCommand(t, context.Background(), bin, profile, key)
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

// toolNames lists a session's tools, sorted, and checks that none declares itself destructive.
func toolNames(t *testing.T, s *mcp.ClientSession) ([]string, map[string]*mcp.Tool) {
	t.Helper()
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	byName := map[string]*mcp.Tool{}
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		byName[tool.Name] = tool
		if tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
			t.Errorf("tool %s is not declared non-destructive", tool.Name)
		}
	}
	slices.Sort(names)
	return names, byName
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

	agent := connectMCP(t, bin, "read", admin)
	names, tools := toolNames(t, agent)
	for _, tool := range tools {
		if !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %s is not declared read-only", tool.Name)
		}
	}
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
	scoped := connectMCP(t, bin, "read", os.Getenv("QUIVR_TEST_SCOPED"))
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

	for _, args := range [][]string{{"mcp"}, {"mcp", "--profile", "write"}} {
		if got := runCLI(t, bin, []string{"QUIVR_API_URL=" + os.Getenv("QUIVR_TEST_URL")}, t.TempDir(), args...); got.exit != 2 || got.stdout != "" {
			t.Fatalf("%v: exit %d stdout %q, want exit 2 and no output", args, got.exit, got.stdout)
		}
	}
}

type mcpReceipt struct {
	ReceiptID    string `json:"receipt_id"`
	State        string `json:"state"`
	Outcome      string `json:"outcome"`
	RecordID     string `json:"record_id"`
	VersionID    string `json:"version_id"`
	Availability *struct {
		Searchable bool `json:"searchable"`
	} `json:"availability"`
}

// TestCLIMCPIngestProfile drives `quivr mcp --profile ingest` as an agent would:
// it sees the read tools plus ingestion, adds text, follows the Receipt until the
// text is searchable and finds it with search. A retry replays the same Receipt,
// and a key that may not write is refused by the server.
func TestCLIMCPIngestProfile(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	bin := quivrBinary(t)
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := monitoringRun()
	c := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "MCP ingest", "idempotency_key": "mcp-ingest-corpus-" + run}, 201)["corpus_id"].(string)

	agent := connectMCP(t, bin, "ingest", admin)
	names, tools := toolNames(t, agent)
	if want := []string{"ingest_text", "list_corpora", "read_receipt", "read_record", "search"}; !slices.Equal(names, want) {
		t.Fatalf("tools %v, want %v", names, want)
	}
	for _, name := range names {
		if readOnly := tools[name].Annotations.ReadOnlyHint; readOnly != (name != "ingest_text") {
			t.Errorf("tool %s read-only %v", name, readOnly)
		}
	}

	args := map[string]any{"corpus_id": c, "record_key": "field-notes-" + run, "text": "Field notes 🦉\nThe barn owl hunts over the heather moorland at dusk."}
	var accepted, replayed mcpReceipt
	decodeTool(t, agent, "ingest_text", args, &accepted)
	decodeTool(t, agent, "ingest_text", args, &replayed)
	if accepted.ReceiptID == "" || replayed.ReceiptID != accepted.ReceiptID {
		t.Fatalf("retry returned Receipt %q, want the first one %q", replayed.ReceiptID, accepted.ReceiptID)
	}

	// Follow the Receipt, as the tool descriptions tell the agent to, until the text is searchable.
	receipt := awaitMCPSearchable(t, agent, accepted.ReceiptID)

	if hit := awaitMCPHit(t, agent, c, "heather moorland"); hit.RecordID != receipt.RecordID || hit.VersionID != receipt.VersionID {
		t.Fatalf("search after searchable Receipt: %+v, want record %s version %s", hit, receipt.RecordID, receipt.VersionID)
	}

	// Another text under the same record_key corrects the Record; going back to the first text makes it current again.
	corrected := map[string]any{"corpus_id": c, "record_key": args["record_key"], "text": "Field notes 🦉\nThe tawny owl calls from the beech woodland."}
	var second, reverted, revertRetry mcpReceipt
	decodeTool(t, agent, "ingest_text", corrected, &second)
	if r := awaitMCPSearchable(t, agent, second.ReceiptID); r.RecordID != receipt.RecordID || r.VersionID == receipt.VersionID {
		t.Fatalf("correction Receipt %+v, want a new Version of record %s", r, receipt.RecordID)
	}
	decodeTool(t, agent, "ingest_text", args, &reverted)
	decodeTool(t, agent, "ingest_text", args, &revertRetry)
	if reverted.ReceiptID == accepted.ReceiptID || revertRetry.ReceiptID != reverted.ReceiptID {
		t.Fatalf("going back to the first text: Receipt %q then %q, want a new Receipt replayed on retry (first was %q)", reverted.ReceiptID, revertRetry.ReceiptID, accepted.ReceiptID)
	}
	back := awaitMCPSearchable(t, agent, reverted.ReceiptID)
	decodeTool(t, agent, "ingest_text", args, &revertRetry)
	if revertRetry.ReceiptID != reverted.ReceiptID {
		t.Fatalf("retry once the reverted text is searchable returned Receipt %q, want %q", revertRetry.ReceiptID, reverted.ReceiptID)
	}
	if hit := awaitMCPHit(t, agent, c, "heather moorland"); hit.VersionID != back.VersionID {
		t.Fatalf("search after going back: hit Version %s, want %s", hit.VersionID, back.VersionID)
	}

	// An explicit idempotency_key reused with other arguments is refused, not silently replayed.
	keyed := map[string]any{"corpus_id": c, "record_key": "keyed-" + run, "text": "First keyed text.", "idempotency_key": "agent-key-" + run}
	var first mcpReceipt
	decodeTool(t, agent, "ingest_text", keyed, &first)
	keyed["text"] = "Second keyed text."
	if out, isError := callTool(t, agent, "ingest_text", keyed); !isError || !strings.HasPrefix(out, "idempotency_conflict:") {
		t.Fatalf("reused idempotency_key with other text: error %v, %q", isError, out)
	}

	// A key that may read Corpora but not write content gets the public forbidden code, even in the ingest profile.
	reader := connectMCP(t, bin, "ingest", os.Getenv("QUIVR_TEST_READER"))
	if out, isError := callTool(t, reader, "ingest_text", args); !isError || !strings.HasPrefix(out, "forbidden:") {
		t.Fatalf("ingest with a read-only key: error %v, %q", isError, out)
	}
}

// awaitMCPSearchable follows a Receipt through read_receipt until its Record Version is searchable.
func awaitMCPSearchable(t *testing.T, s *mcp.ClientSession, receiptID string) mcpReceipt {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var r mcpReceipt
		decodeTool(t, s, "read_receipt", map[string]any{"receipt_id": receiptID}, &r)
		if r.State == "resolved" && r.Availability != nil && r.Availability.Searchable {
			if r.Outcome != "created" || r.RecordID == "" || r.VersionID == "" {
				t.Fatalf("resolved Receipt %+v, want a created Record Version", r)
			}
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("Receipt never made the text searchable: %+v", r)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// awaitMCPHit searches one Corpus through the search tool until the query has a hit, and returns the first one.
func awaitMCPHit(t *testing.T, s *mcp.ClientSession, corpusID, query string) mcpHit {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var found struct {
			Items []mcpHit `json:"items"`
		}
		decodeTool(t, s, "search", map[string]any{"query": query, "corpus_ids": []string{corpusID}, "mode": "lexical"}, &found)
		if len(found.Items) > 0 {
			return found.Items[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("search tool never returned a hit for %q", query)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestCLIMCPAnswersEveryRequestWhenInputCloses pipes requests into `quivr mcp`
// and closes its input at once, as a script does: every request still gets its
// answer before the process exits 0.
func TestCLIMCPAnswersEveryRequestWhenInputCloses(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	bin := quivrBinary(t)
	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"script","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_corpora","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"list_corpora","arguments":{}}}`,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := mcpCommand(t, ctx, bin, "read", os.Getenv("QUIVR_TEST_ADMIN"))
	// exec closes the process's stdin as soon as it has copied the whole reader.
	cmd.Stdin = strings.NewReader(strings.Join(requests, "\n") + "\n")
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("quivr mcp with closed input: %v, want exit 0; stdout:\n%s", err, out.String())
	}

	answered := map[int]bool{}
	dec := json.NewDecoder(strings.NewReader(out.String()))
	for dec.More() {
		var answer struct {
			ID     int             `json:"id"`
			Error  json.RawMessage `json:"error"`
			Result struct {
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if err := dec.Decode(&answer); err != nil {
			t.Fatalf("stdout is not a stream of JSON-RPC answers: %v\n%s", err, out.String())
		}
		if answered[answer.ID] || answer.Error != nil || answer.Result.IsError {
			t.Fatalf("answer to request %d is repeated or failed; stdout:\n%s", answer.ID, out.String())
		}
		answered[answer.ID] = true
	}
	for id := 1; id <= 4; id++ {
		if !answered[id] {
			t.Fatalf("request %d got no answer before quivr mcp exited; stdout:\n%s", id, out.String())
		}
	}

	// An agent that follows tool-list changes keeps a request open for the whole
	// session. Closing stdin without cancelling it still ends quivr mcp at once.
	cmd = mcpCommand(t, ctx, bin, "read", os.Getenv("QUIVR_TEST_ADMIN"))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	opts := &mcp.ClientOptions{ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {}}
	// Connect sends the tool-list subscription; the list_corpora answer below
	// proves the server has read it before stdin closes.
	agent, err := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "v0"}, opts).Connect(ctx, &mcp.IOTransport{Reader: stdout, Writer: stdin}, nil)
	if err != nil {
		t.Fatalf("connect quivr mcp: %v", err)
	}
	defer agent.Close()
	decodeTool(t, agent, "list_corpora", nil, &struct{}{})
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("quivr mcp with an open tool-list subscription and closed input: %v, want exit 0 at once", err)
	}
}
