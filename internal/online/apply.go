package online

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

const applyUsage = "quivr apply -f sources.json [--state-file journal.json] [--confirm]"

var applyCommand = Command{Name: "apply", Usage: applyUsage, Summary: "Preview or apply a private sources declaration through the public API.", run: runApply}

func runApply(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	var file, state, operator string
	var confirm bool
	var conn connection
	fs.StringVar(&file, "f", "", "JSON sources declaration")
	fs.StringVar(&state, "state-file", "", "private replay journal (default <declaration>.state.json)")
	fs.StringVar(&operator, "operator-key-env", "QUIVR_OPERATOR_KEY", "environment reference for plugin administration")
	fs.BoolVar(&confirm, "confirm", false, "execute the displayed changes (default: preview only)")
	conn.register(fs)
	fs.Usage = func() {
		fmt.Fprintln(env.Stdout, applyUsage)
		var help bytes.Buffer
		fs.SetOutput(&help)
		fs.PrintDefaults()
		fs.SetOutput(env.Stderr)
		fmt.Fprint(env.Stdout, strings.ReplaceAll(help.String(), "\t", "    "))
		fmt.Fprint(env.Stdout, ConnectionHelp)
	}
	rest, err := parseInterspersed(fs, args)
	if err == flag.ErrHelp {
		return ExitOK
	}
	if err != nil {
		return ExitUsage
	}
	if len(rest) != 0 || file == "" {
		return report(env, usageError("apply requires -f sources.json"))
	}
	declaration, err := loadSources(file, env)
	if err != nil {
		return report(env, err)
	}
	base := conn.url
	if base == "" {
		base = env.Getenv(EnvAPIURL)
	}
	u, parseErr := url.Parse(base)
	if parseErr != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return report(env, usageError("apply API URL must not contain credentials, query or fragment"))
	}
	cl, _, err := conn.client(env)
	if err != nil {
		return report(env, err)
	}
	operatorKey := ""
	if len(declaration.Plugins) > 0 {
		if !envReference.MatchString(operator) {
			return report(env, usageError("invalid operator key environment reference"))
		}
		operatorKey = env.Getenv(operator)
		if operatorKey == "" {
			return report(env, usageError("plugins require the operator API key environment reference"))
		}
	}
	operatorConn := connection{url: base, key: operatorKey}
	operatorClient, _, err := operatorConn.client(env)
	if err != nil {
		return report(env, err)
	}
	if state == "" {
		state = file + ".state.json"
	}
	state, err = filepath.Abs(state)
	if err != nil {
		return report(env, usageError("invalid replay journal path"))
	}
	journal, err := openApplyJournal(state, applyScope(conn, env))
	if err != nil {
		return report(env, err)
	}
	defer journal.close()
	a := applyRun{ctx: ctx, env: env, cl: cl, operator: operatorClient, state: journal, desired: declaration}
	if err = a.inspect(); err != nil {
		return report(env, err)
	}
	a.print()
	if a.unsupported {
		return report(env, &Failure{Exit: ExitInvalid, Message: "unsupported changes refused before applying"})
	}
	if !confirm || len(a.changes) == 0 {
		return ExitOK
	}
	if err = a.execute(); err != nil {
		fmt.Fprintln(env.Stderr, "Retain the replay journal; earlier changes may have succeeded.")
		return report(env, err)
	}
	fmt.Fprintln(env.Stdout, "Applied. Keep the private replay journal for subsequent runs.")
	return ExitOK
}
