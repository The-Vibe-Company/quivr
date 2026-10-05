package online

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/client"
)

const recordsUsage = "quivr records --corpus <corpus-id> [--order record_id|accepted_at_desc] [--accepted-after <RFC3339>] [--accepted-before <RFC3339>] [--page-cursor <cursor>] [--limit <1-100>] [--count] [--api-url <url>] [--api-key <key>]"
const recordsSummary = "List one page of a Corpus's Records or get an exact count, with optional time bounds. Prints the API JSON; list pages include a continuation cursor when another page exists."

var recordBoundTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]([01]\d|2[0-3]):[0-5]\d)$`)

var recordsCommand = Command{Name: "records", Usage: recordsUsage, Summary: recordsSummary, run: records}

func records(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("records", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	var conn connection
	corpusID := fs.String("corpus", "", "Corpus ID (required; needs content:read)")
	order := fs.String("order", "", "record_id for resynchronization (default), or accepted_at_desc for newest current Versions first")
	after := fs.String("accepted-after", "", "inclusive current-Version acceptance time, RFC 3339 with an offset, at most 9 fractional digits")
	before := fs.String("accepted-before", "", "exclusive current-Version acceptance time, RFC 3339 with an offset, at most 9 fractional digits")
	cursor := fs.String("page-cursor", "", "next_page_cursor from the preceding response; repeat its order and bounds")
	limit := fs.Int("limit", 100, "page size, 1 to 100")
	count := fs.Bool("count", false, "print the exact count with optional time bounds; omit order, page-cursor and limit")
	conn.register(fs)
	fs.Usage = func() {}
	positional, err := parseInterspersed(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		fs.SetOutput(env.Stdout)
		fmt.Fprintf(env.Stdout, "usage: %s\n\n%s\n\nFlags:\n", recordsUsage, recordsSummary)
		var help bytes.Buffer
		fs.SetOutput(&help)
		fs.PrintDefaults()
		fmt.Fprint(env.Stdout, strings.ReplaceAll(help.String(), "\t", "    "))
		fmt.Fprintln(env.Stdout)
		fmt.Fprint(env.Stdout, ConnectionHelp)
		return ExitOK
	}
	if err != nil {
		fmt.Fprintf(env.Stderr, "usage: %s\nRun quivr records --help for details.\n", recordsUsage)
		return ExitUsage
	}
	if *corpusID == "" || len(positional) > 0 {
		return report(env, usageError("records needs --corpus and no positional arguments"))
	}
	params := client.ListRecordsParams{CorpusId: *corpusID}
	invalid := false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "order":
			o := client.ListRecordsParamsOrder(*order)
			params.Order = &o
			invalid = invalid || *count
		case "page-cursor":
			params.PageCursor = cursor
			invalid = invalid || *count
		case "limit":
			params.Limit = limit
			invalid = invalid || *count
		}
	})
	if invalid {
		return report(env, usageError("--count accepts only corpus, time bounds and connection flags"))
	}
	for _, bound := range []struct {
		name   string
		value  string
		target **time.Time
	}{
		{"accepted-after", *after, &params.AcceptedAfter}, {"accepted-before", *before, &params.AcceptedBefore},
	} {
		var supplied bool
		fs.Visit(func(f *flag.Flag) {
			if f.Name == bound.name {
				supplied = true
			}
		})
		if supplied {
			if !recordBoundTimestamp.MatchString(bound.value) {
				return report(env, usageError("--"+bound.name+" needs an RFC 3339 timestamp with an offset and at most 9 fractional digits"))
			}
			t, err := time.Parse(time.RFC3339Nano, bound.value)
			if err != nil {
				return report(env, usageError("--"+bound.name+" contains an invalid date or time"))
			}
			*bound.target = &t
		}
	}
	cl, base, err := conn.client(env)
	if err != nil {
		return report(env, err)
	}
	var raw []byte
	if *count {
		resp, requestErr := cl.CountRecords(ctx, &client.CountRecordsParams{CorpusId: *corpusID, AcceptedAfter: params.AcceptedAfter, AcceptedBefore: params.AcceptedBefore})
		var result client.RecordCount
		raw, err = decode(ctx, base, resp, requestErr, &result)
	} else {
		resp, requestErr := cl.ListRecords(ctx, &params)
		var result client.RecordPage
		raw, err = decode(ctx, base, resp, requestErr, &result)
	}
	if err != nil {
		return report(env, err)
	}
	env.Stdout.Write(raw)
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		fmt.Fprintln(env.Stdout)
	}
	return ExitOK
}
