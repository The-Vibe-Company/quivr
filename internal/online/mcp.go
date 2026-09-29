package online

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime/debug"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpUsage = "quivr mcp --profile read|ingest [--api-url <url>] [--api-key <key>]"

const mcpSummary = "Serve Quivr to an AI agent over MCP on stdin and stdout, with the tools of one profile."

// mcpConnectionHelp replaces the online exit codes: a failed API call is a
// tool error the agent sees, not the end of the process.
const mcpConnectionHelp = `This command needs a running Quivr server. It reads the server address from
` + EnvAPIURL + ` and the API key from ` + EnvAPIKey + `; --api-url and --api-key
override them. It talks to the server only through its public HTTP API, and
the API key decides which Corpora every tool may reach.

An MCP client starts it and speaks JSON-RPC on stdin and stdout; diagnostics go
to stderr. A refused or failed API call is returned to the agent as a tool
error carrying the public error code, such as forbidden or unreachable.

Exit codes:
  0  the client closed the connection
  1  unexpected failure
  2  invalid arguments, or a missing or malformed API URL
  130  interrupted
`

var mcpCommand = Command{Name: "mcp", Usage: mcpUsage, Summary: mcpSummary, run: serveMCP}

// mcpCaller runs one tool call through the generated client and returns the
// JSON the agent receives, or a classified Failure.
type mcpCaller func(ctx context.Context, cl *client.ClientWithResponses, base string, args json.RawMessage) (any, error)

// mcpHandlers binds each catalogue tool to its implementation. Each one goes
// through the public API only; none withdraws, deletes or rebuilds anything.
var mcpHandlers = map[string]mcpCaller{
	"list_corpora": listCorporaTool,
	"search":       searchTool,
	"read_record":  readRecordTool,
	"ingest_text":  ingestTextTool,
	"read_receipt": readReceiptTool,
}

func serveMCP(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	var conn connection
	profileName := fs.String("profile", "", "tool profile to serve (required): "+mcpProfileNames())
	conn.register(fs)
	fs.Usage = func() {}
	err := fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		fs.SetOutput(env.Stdout)
		fmt.Fprintf(env.Stdout, "usage: %s\n\n%s\n\nFlags:\n", mcpUsage, mcpSummary)
		fs.PrintDefaults()
		fmt.Fprintln(env.Stdout, "\nProfiles:")
		for _, p := range MCPProfiles {
			fmt.Fprintf(env.Stdout, "  %s  %s\n", p.Name, p.Summary)
			for _, t := range MCPToolsFor(p.Name) {
				fmt.Fprintf(env.Stdout, "    %s  %s\n", t.Name, t.Title)
			}
		}
		fmt.Fprintln(env.Stdout)
		fmt.Fprint(env.Stdout, mcpConnectionHelp)
		return ExitOK
	}
	if err != nil || fs.NArg() > 0 {
		fmt.Fprintf(env.Stderr, "usage: %s\nRun quivr mcp --help for details.\n", mcpUsage)
		return ExitUsage
	}
	profile, ok := LookupMCPProfile(*profileName)
	if !ok {
		fmt.Fprintf(env.Stderr, "quivr: mcp needs --profile %s\nusage: %s\n", mcpProfileNames(), mcpUsage)
		return ExitUsage
	}
	cl, base, err := conn.client(env)
	if err != nil {
		return report(env, err)
	}
	server, err := newMCPServer(profile, cl, base)
	if err != nil {
		return report(env, err)
	}
	transport := &mcp.IOTransport{Reader: io.NopCloser(env.Stdin), Writer: nopWriteCloser{env.Stdout}}
	if err := server.Run(ctx, transport); err != nil {
		if ctx.Err() != nil {
			return ExitInterrupted
		}
		return report(env, err)
	}
	return ExitOK
}

// newMCPServer registers the profile's catalogue tools on an MCP server.
func newMCPServer(profile MCPProfile, cl *client.ClientWithResponses, base string) (*mcp.Server, error) {
	server := mcp.NewServer(&mcp.Implementation{Name: "quivr", Title: "Quivr", Version: buildVersion()}, &mcp.ServerOptions{Instructions: profile.Instructions})
	for _, tool := range MCPToolsFor(profile.Name) {
		call, ok := mcpHandlers[tool.Name]
		if !ok {
			return nil, fmt.Errorf("mcp tool %s has no implementation", tool.Name)
		}
		if profile.Name == "read" && !tool.ReadOnly {
			return nil, fmt.Errorf("mcp tool %s is not read-only and cannot join the read profile", tool.Name)
		}
		// No catalogue tool is destructive, and none reaches outside Quivr.
		notDestructive, closedWorld := false, false
		t := &mcp.Tool{
			Name:        tool.Name,
			Title:       tool.Title,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
			Annotations: &mcp.ToolAnnotations{
				Title:           tool.Title,
				ReadOnlyHint:    tool.ReadOnly,
				IdempotentHint:  tool.Idempotent,
				DestructiveHint: &notDestructive,
				OpenWorldHint:   &closedWorld,
			},
		}
		mcp.AddTool(server, t, func(ctx context.Context, _ *mcp.CallToolRequest, args json.RawMessage) (*mcp.CallToolResult, any, error) {
			// The SDK has validated args against the catalogue schema. The result
			// is the public API JSON unchanged, as structured and text content.
			out, err := call(ctx, cl, base, args)
			if err != nil {
				return toolFailure(err), nil, nil
			}
			return nil, out, nil
		})
	}
	return server, nil
}

// toolFailure reports a failed call to the agent as a tool error, with the
// public error code and hint, so it can correct the call instead of guessing.
func toolFailure(err error) *mcp.CallToolResult {
	var f *Failure
	if !errors.As(err, &f) {
		f = &Failure{Exit: ExitFailed, Message: err.Error()}
	}
	text := f.Error()
	if f.Hint != "" {
		text += "\n" + f.Hint
	}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func listCorporaTool(ctx context.Context, cl *client.ClientWithResponses, base string, args json.RawMessage) (any, error) {
	var in struct {
		PageCursor *string `json:"page_cursor"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	var page client.CorpusPage
	resp, err := cl.ListCorpora(ctx, &client.ListCorporaParams{PageCursor: in.PageCursor})
	raw, err := decode(ctx, base, resp, err, &page)
	return json.RawMessage(raw), err
}

func searchTool(ctx context.Context, cl *client.ClientWithResponses, base string, args json.RawMessage) (any, error) {
	var body client.SearchRequest
	if err := json.Unmarshal(args, &body); err != nil {
		return nil, err
	}
	var result client.SearchResponse
	resp, err := cl.SearchRecords(ctx, body)
	raw, err := decode(ctx, base, resp, err, &result)
	return json.RawMessage(raw), err
}

func readRecordTool(ctx context.Context, cl *client.ClientWithResponses, base string, args json.RawMessage) (any, error) {
	var in struct {
		RecordID  string `json:"record_id"`
		VersionID string `json:"version_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	var record client.Record
	resp, err := cl.GetRecord(ctx, in.RecordID)
	rawRecord, err := decode(ctx, base, resp, err, &record)
	if err != nil {
		return nil, err
	}
	versionID := in.VersionID
	if versionID == "" {
		if record.CurrentVersionId == nil {
			return nil, &Failure{Exit: ExitInvalid, Code: "no_current_version", Message: "the Record has no current Version", Hint: "it is withdrawn or not yet published; pass the version_id of a search hit to read that Version"}
		}
		versionID = *record.CurrentVersionId
	}
	var version client.Version
	resp, err = cl.GetVersion(ctx, in.RecordID, versionID)
	rawVersion, err := decode(ctx, base, resp, err, &version)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"record": rawRecord, "version": rawVersion}, nil
}

// mcpNamespace is the source namespace of text an agent ingests without naming one.
const mcpNamespace = "mcp"

func ingestTextTool(ctx context.Context, cl *client.ClientWithResponses, base string, args json.RawMessage) (any, error) {
	var in struct {
		CorpusID       string `json:"corpus_id"`
		RecordKey      string `json:"record_key"`
		Text           string `json:"text"`
		Namespace      string `json:"namespace"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	if in.Namespace == "" {
		in.Namespace = mcpNamespace
	}
	var content client.IngestCommand_Content
	if err := content.FromTextContent(client.TextContent{Kind: client.Text, Text: in.Text}); err != nil {
		return nil, err
	}
	body := client.IngestCommand{
		IdempotencyKey: in.IdempotencyKey,
		Source:         client.SourceIdentity{CorpusId: in.CorpusID, Namespace: in.Namespace, RecordKey: in.RecordKey},
		Content:        content,
	}
	if in.IdempotencyKey != "" {
		raw, _, err := ingest(ctx, cl, base, body)
		return json.RawMessage(raw), err
	}
	// A derived key replays the Receipt of the same arguments. When that Receipt
	// created a Version the Record has since left (text A, then B, then A again),
	// the call means "make this text current again": it chains a new key from the
	// superseded Version, so the text is ingested anew and a retry of this call
	// still follows the same chain to the same Receipt.
	body.IdempotencyKey = ingestIdempotencyKey(in.CorpusID, in.Namespace, in.RecordKey, in.Text)
	for hop := 0; ; hop++ {
		raw, receipt, err := ingest(ctx, cl, base, body)
		if err != nil || !superseded(receipt) {
			return json.RawMessage(raw), err
		}
		if hop == maxIngestHops {
			return nil, &Failure{Exit: ExitInvalid, Code: "conflict", Message: "this text went back and forth too often on this record_key to derive a retry key", Hint: "pass your own idempotency_key"}
		}
		body.IdempotencyKey = ingestIdempotencyKey(body.IdempotencyKey, *receipt.VersionId)
	}
}

// maxIngestHops bounds how many superseded Receipts one ingest_text call follows.
const maxIngestHops = 16

func ingest(ctx context.Context, cl *client.ClientWithResponses, base string, body client.IngestCommand) ([]byte, client.Receipt, error) {
	var receipt client.Receipt
	resp, err := cl.IngestRecord(ctx, body)
	raw, err := decode(ctx, base, resp, err, &receipt)
	return raw, receipt, err
}

// superseded reports a replayed Receipt whose Version became retrieval ready
// but is not the Record's current one: another text replaced it. A Version still
// building its baseline is not current yet either, and is not superseded.
func superseded(r client.Receipt) bool {
	return r.State == "resolved" && r.Outcome != nil && (*r.Outcome == "created" || *r.Outcome == "duplicate") &&
		r.VersionId != nil && r.Availability != nil && r.Availability.State == "retrieval_ready" && !r.Availability.IsCurrent
}

// ingestIdempotencyKey derives the retry key of an ingest_text call from its
// arguments, so an agent repeating the same call replays the same Receipt.
// Each field is length-prefixed so no two argument sets share a digest input.
func ingestIdempotencyKey(fields ...string) string {
	h := sha256.New()
	for _, f := range fields {
		fmt.Fprintf(h, "%d:%s", len(f), f)
	}
	return "mcp-ingest-" + hex.EncodeToString(h.Sum(nil))
}

func readReceiptTool(ctx context.Context, cl *client.ClientWithResponses, base string, args json.RawMessage) (any, error) {
	var in struct {
		ReceiptID string `json:"receipt_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	var receipt client.Receipt
	resp, err := cl.GetReceipt(ctx, in.ReceiptID)
	raw, err := decode(ctx, base, resp, err, &receipt)
	return json.RawMessage(raw), err
}

func mcpProfileNames() string {
	names := make([]string, len(MCPProfiles))
	for i, p := range MCPProfiles {
		names[i] = p.Name
	}
	return strings.Join(names, "|")
}

// buildVersion names the running binary to the agent: its module version when
// built from a tagged module, else its VCS revision.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return "devel"
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
