package online

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/client"
)

const searchUsage = "quivr search --corpus <corpus-id> [--corpus <corpus-id>]... [--mode lexical|semantic|hybrid] [--profile fast|balanced|deep] [--limit <1-50>] [--json] [--api-url <url>] [--api-key <key>] <query>"

const searchSummary = "Search one or more Corpora of a running Quivr and print ranked hits with their provenance."

var searchCommand = Command{Name: "search", Usage: searchUsage, Summary: searchSummary, run: search}

// corpusList collects repeated --corpus flags; each may also be a comma-separated list.
type corpusList []string

func (c *corpusList) String() string { return strings.Join(*c, ",") }
func (c *corpusList) Set(v string) error {
	for _, id := range strings.Split(v, ",") {
		if id = strings.TrimSpace(id); id != "" {
			*c = append(*c, id)
		}
	}
	return nil
}

func search(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	var (
		conn    connection
		corpora corpusList
		mode    = fs.String("mode", "", "search mode: lexical, semantic or hybrid (server default: hybrid)")
		profile = fs.String("profile", "", "retrieval profile: fast, balanced or deep (server default: balanced)")
		limit   = fs.Int("limit", 0, "maximum number of hits, 1 to 50 (server default: 10)")
		asJSON  = fs.Bool("json", false, "print the public API response unchanged")
	)
	fs.Var(&corpora, "corpus", "Corpus ID to search; repeat or separate with commas (required)")
	conn.register(fs)
	fs.Usage = func() {} // help and usage errors are printed below
	positional, err := parseInterspersed(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		fs.SetOutput(env.Stdout)
		fmt.Fprintf(env.Stdout, "usage: %s\n\n%s\n\nFlags:\n", searchUsage, searchSummary)
		fs.PrintDefaults()
		fmt.Fprintln(env.Stdout)
		fmt.Fprint(env.Stdout, ConnectionHelp)
		return ExitOK
	}
	if err != nil {
		fmt.Fprintf(env.Stderr, "usage: %s\nRun quivr search --help for details.\n", searchUsage)
		return ExitUsage
	}
	query := strings.TrimSpace(strings.Join(positional, " "))
	if query == "" || len(corpora) == 0 {
		fmt.Fprintf(env.Stderr, "quivr: search needs a query and at least one --corpus\nusage: %s\n", searchUsage)
		return ExitUsage
	}
	cl, base, err := conn.client(env)
	if err != nil {
		return report(env, err)
	}
	body := client.SearchRecordsJSONRequestBody{Query: query, CorpusIds: corpora}
	if *mode != "" {
		m := client.SearchRequestMode(*mode)
		body.Mode = &m
	}
	if *profile != "" {
		p := client.SearchRequestProfile(*profile)
		body.Profile = &p
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "limit" {
			body.Limit = limit
		}
	})
	var result client.SearchResponse
	resp, err := cl.SearchRecords(ctx, body)
	raw, err := decode(ctx, base, resp, err, &result)
	if err != nil {
		return report(env, err)
	}
	if *asJSON {
		env.Stdout.Write(raw)
		if len(raw) == 0 || raw[len(raw)-1] != '\n' {
			fmt.Fprintln(env.Stdout)
		}
		return ExitOK
	}
	printHits(env.Stdout, &result)
	return ExitOK
}

// printHits renders ranked hits for a person: rank and provenance on one
// line, then the exact excerpt, indented.
func printHits(w io.Writer, r *client.SearchResponse) {
	fmt.Fprintf(w, "%d hit%s (profile %s, version %s)\n", len(r.Items), plural(len(r.Items)), r.RetrievalProfile.Name, r.RetrievalProfile.Version)
	for _, h := range r.Items {
		fmt.Fprintf(w, "\n%d. record %s  version %s  part %s  [%d,%d)\n", h.Rank, h.RecordId, h.VersionId, h.PartKey, h.Excerpt.Start, h.Excerpt.End)
		for _, line := range strings.Split(strings.TrimRight(h.Excerpt.Text, "\n"), "\n") {
			fmt.Fprintln(w, "   "+line)
		}
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
