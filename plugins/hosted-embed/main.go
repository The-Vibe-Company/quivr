// hosted-embed is an ingestion plugin built entirely on the public Go kit.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	// Emit JSON (valid YAML) so a configured space is declared before admission.
	if len(os.Args) == 3 && os.Args[1] == "configure" {
		raw, err := os.ReadFile(os.Args[2])
		if err != nil {
			return fmt.Errorf("cannot read configuration file")
		}
		c, err := parseConfiguration(raw)
		if err != nil {
			return err
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		body, err := c.manifest([]string{exe})
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(append(body, '\n'))
		return err
	}
	if len(os.Args) != 1 {
		return fmt.Errorf("usage: hosted-embed [configure configuration.json]")
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	p, err := quivrplugin.New("", quivrplugin.WithLogger(logger))
	if err != nil {
		return err
	}
	// Reconstruct the settings from the generated configuration's const values.
	var schema struct {
		Properties map[string]struct {
			Const json.RawMessage `json:"const"`
		} `json:"properties"`
	}
	if p.Manifest().Configuration == nil || p.Manifest().Ingestion == nil {
		return fmt.Errorf("generate a manifest with configure first")
	}
	if err := json.Unmarshal(p.Manifest().Configuration.Schema, &schema); err != nil {
		return err
	}
	fields := map[string]json.RawMessage{}
	for k, v := range schema.Properties {
		fields[k] = v.Const
	}
	raw, _ := json.Marshal(fields)
	c, err := parseConfiguration(raw)
	if err != nil {
		return err
	}
	space, ok := p.Manifest().Ingestion.Spaces[c.spaceID()]
	if !ok || space.Dimensions != c.Dimensions || space.Model != c.Model || space.Metric != c.Metric {
		return fmt.Errorf("manifest space differs from configuration; regenerate it")
	}
	ingester := newIngester(c, os.Getenv("EMBED_API_KEY"), logger)
	if tokenizer, ok := ingester.tokenizer.(*localTokenizer); ok {
		defer tokenizer.Close()
		<-tokenizer.ready // Complete parallel helper startup before serving requests.
	}
	if err := ingester.localQueries(context.Background(), os.Getenv("QUIVR_HOSTED_QUERY_URL")); err != nil {
		return err
	}
	if err := p.Ingestion(ingester); err != nil {
		return err
	}
	return p.Serve()
}
