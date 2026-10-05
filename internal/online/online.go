// Package online implements the `quivr` commands that talk to a running
// installation, such as `quivr search`. They reach it only through the public
// HTTP API with the generated Go client in package client: never PostgreSQL,
// Temporal, object storage or Weaviate, and they need no QUIVR_CONFIG.
//
// Every online command shares the same connection settings, error messages
// and exit codes, so scripts can react to each failure class the same way
// whichever command they run.
package online

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/The-Vibe-Company/quivr/client"
)

// Environment variables every online command reads. The --api-url and
// --api-key flags override them.
const (
	EnvAPIURL = "QUIVR_API_URL"
	EnvAPIKey = "QUIVR_API_KEY"
)

// Exit codes shared by every online command.
const (
	// ExitOK: the command succeeded.
	ExitOK = 0
	// ExitFailed: an unexpected failure, such as a response the contract does not describe.
	ExitFailed = 1
	// ExitUsage: invalid flags or arguments, or a missing or malformed API URL.
	ExitUsage = 2
	// ExitDenied: the server rejected the API key (401) or its permissions or Corpus scope (403).
	ExitDenied = 3
	// ExitInvalid: the server rejected the request itself (400, 404, 409, 422).
	ExitInvalid = 4
	// ExitUnavailable: no server answered, or it or a dependency is unavailable (5xx).
	ExitUnavailable = 5
	// ExitInterrupted: an interrupt or SIGTERM cancelled the command (shell convention 128+SIGINT).
	ExitInterrupted = 130
)

// requestTimeout bounds one API call so an unresponsive server cannot hang a script.
const requestTimeout = 60 * time.Second

// Command is one online command. The table drives dispatch and help, and is
// the source a generated CLI reference can read.
type Command struct {
	Name    string
	Usage   string
	Summary string
	run     func(ctx context.Context, env Env, args []string) int
}

// Env is what a command run sees of its process.
type Env struct {
	Getenv func(string) string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Commands lists the online commands in help order.
var Commands []Command

func init() {
	Commands = []Command{searchCommand, recordsCommand, mcpCommand}
}

// Lookup returns the online command called name.
func Lookup(name string) (Command, bool) {
	for _, c := range Commands {
		if c.Name == name {
			return c, true
		}
	}
	return Command{}, false
}

// Run executes the online command called name with args and returns the
// process exit code. An interrupt or SIGTERM cancels the request in flight.
func Run(name string, args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return RunContext(ctx, name, args, Env{Getenv: os.Getenv, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr})
}

// RunContext is Run with an explicit context and process environment.
func RunContext(ctx context.Context, name string, args []string, env Env) int {
	cmd, ok := Lookup(name)
	if !ok {
		fmt.Fprintf(env.Stderr, "quivr: unknown command %q\n", name)
		return ExitUsage
	}
	return cmd.run(ctx, env, args)
}

// ConnectionHelp is appended to every online command's help.
const ConnectionHelp = `This command needs a running Quivr server. It reads the server address from
` + EnvAPIURL + ` and the API key from ` + EnvAPIKey + `; --api-url and --api-key
override them. It talks to the server only through its public HTTP API.

Exit codes:
  0  success
  1  unexpected failure
  2  invalid arguments, or a missing or malformed API URL
  3  API key rejected, or not allowed on a requested resource (401, 403)
  4  request rejected as invalid (400, 404, 409, 422)
  5  server unreachable or unavailable (connection error, timeout, 5xx)
  130  interrupted
`

// connection holds the --api-url and --api-key flags of one command run.
type connection struct {
	url, key string
}

func (c *connection) register(fs *flag.FlagSet) {
	fs.StringVar(&c.url, "api-url", "", "Quivr API base URL (default $"+EnvAPIURL+")")
	fs.StringVar(&c.key, "api-key", "", "API key sent as a bearer token (default $"+EnvAPIKey+")")
}

// client resolves the connection settings and builds the generated client.
// A missing key is allowed: a keyless installation accepts requests without one.
// The returned display URL has any user:password redacted, so it is safe to print.
func (c *connection) client(env Env) (*client.ClientWithResponses, string, error) {
	base := c.url
	if base == "" {
		base = env.Getenv(EnvAPIURL)
	}
	key := c.key
	if key == "" {
		key = env.Getenv(EnvAPIKey)
	}
	if base == "" {
		return nil, "", usageError("no API URL: set " + EnvAPIURL + " or pass --api-url")
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", usageError("invalid API URL: want http:// or https:// and a host")
	}
	display := u.Redacted()
	hc := &http.Client{Timeout: requestTimeout}
	opts := []client.ClientOption{client.WithHTTPClient(hc)}
	if key != "" {
		opts = append(opts, client.WithRequestEditorFn(func(_ context.Context, r *http.Request) error {
			r.Header.Set("Authorization", "Bearer "+key)
			return nil
		}))
	}
	cl, err := client.NewClientWithResponses(strings.TrimRight(base, "/"), opts...)
	if err != nil {
		return nil, "", usageError(fmt.Sprintf("invalid API URL %s: %v", display, err))
	}
	return cl, display, nil
}

// Failure is a classified online-command failure.
type Failure struct {
	Exit    int
	Code    string // public error code, or a local one such as unreachable
	Message string
	Hint    string
}

func (f *Failure) Error() string {
	if f.Code == "" {
		return f.Message
	}
	return f.Code + ": " + f.Message
}

func usageError(msg string) *Failure { return &Failure{Exit: ExitUsage, Message: msg} }

// hints explain the public codes an operator most often meets.
var hints = map[string]string{
	"invalid_api_key":           "the server rejected the API key; check " + EnvAPIKey + " or --api-key",
	"forbidden":                 "the API key lacks a permission or Corpus scope this request needs",
	"invalid_schema":            "the request does not match the API contract; check the arguments",
	"unsupported_search":        "the server does not support this search mode or query",
	"unsupported_profile":       "the deployment does not answer this profile; GET /v0/search/profiles lists the ones it does",
	"query_too_long":            "the query is longer than the profile accepts; the message names the limit: shorten the query",
	"retrieval_plugin_invalid":  "the deployment's retrieval plugin answered something the engine refuses; an operator checks the plugin",
	"search_deadline_exceeded":  "the search outran its profile's hard time limit; retry, or use a faster profile",
	"search_unavailable":        "the search backend is unavailable; retry later",
	"source_filter_unavailable": "a requested Corpus predates source filtering; an operator rebuilds it once (POST /v0/corpora/{id}/rebuilds)",
}

// unreachable classifies a transport error: nothing usable answered.
func unreachable(base string, err error) *Failure {
	return &Failure{
		Exit:    ExitUnavailable,
		Code:    "unreachable",
		Message: err.Error(),
		Hint:    "no Quivr server answered at " + base + "; check " + EnvAPIURL + " or --api-url and that the server is running",
	}
}

// responseFailure classifies a non-2xx response from its status and the
// public error envelope, when the body carries one.
func responseFailure(status int, e *client.Error) *Failure {
	f := &Failure{Message: fmt.Sprintf("HTTP %d", status)}
	if e != nil && e.Code != "" {
		f.Code, f.Message = e.Code, e.Message
		if e.Field != nil {
			f.Message += " (" + *e.Field + ")"
		}
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		f.Exit = ExitDenied
	case status >= 500:
		f.Exit = ExitUnavailable
	case status >= 400:
		f.Exit = ExitInvalid
	default:
		f.Exit = ExitFailed
	}
	f.Hint = hints[f.Code]
	return f
}

// decode reads the outcome of one generated-client call. A transport error
// means nothing usable answered; a 2xx body is decoded into out; any other
// status becomes a classified Failure. It returns the raw body so --json can
// print the public response unchanged.
func decode(ctx context.Context, base string, resp *http.Response, err error, out any) ([]byte, error) {
	if err != nil {
		if ctx.Err() != nil {
			return nil, &Failure{Exit: ExitInterrupted, Code: "interrupted", Message: "the command was cancelled"}
		}
		return nil, unreachable(base, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, unreachable(base, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e client.Error
		_ = json.Unmarshal(body, &e) // a non-JSON body (a proxy page) keeps the bare status
		return nil, responseFailure(resp.StatusCode, &e)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return nil, &Failure{Exit: ExitFailed, Message: fmt.Sprintf("HTTP %d with a body the API contract does not describe: %v", resp.StatusCode, err)}
	}
	return body, nil
}

// report prints err on stderr and returns its exit code.
func report(env Env, err error) int {
	var f *Failure
	if !errors.As(err, &f) {
		f = &Failure{Exit: ExitFailed, Message: err.Error()}
	}
	fmt.Fprintln(env.Stderr, "quivr: "+f.Error())
	if f.Hint != "" {
		fmt.Fprintln(env.Stderr, "  "+f.Hint)
	}
	return f.Exit
}

// parseInterspersed parses flags that may appear before or after positional
// arguments, and returns the positional arguments in order.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if consumed := args[:len(args)-len(rest)]; len(consumed) > 0 && consumed[len(consumed)-1] == "--" {
			return append(positional, rest...), nil
		}
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}
