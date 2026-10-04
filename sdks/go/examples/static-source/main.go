// Command static-source is a sample connector built with the Quivr Go plugin
// SDK. Its items are listed in the Connector Instance config and returned
// page by page; the checkpoint is the offset of the next item. A token that
// starts with "revoked" is refused, as a real source would refuse it.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

type config struct {
	Items []struct {
		Key    string `json:"key"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		Author string `json:"author,omitempty"`
	} `json:"items"`
	PageSize int `json:"page_size,omitempty"`
}

type credential struct {
	Token string `json:"token"`
}

type checkpoint struct {
	Offset int `json:"offset"`
}

type static struct{}

// authorize stands for the source's authentication.
func authorize(c quivrplugin.Credential) error {
	var cred credential
	if err := c.Decode(&cred); err != nil {
		return quivrplugin.AccessError("invalid_credential", "the credential cannot be read")
	}
	if strings.HasPrefix(cred.Token, "revoked") {
		return quivrplugin.AccessError("token_rejected", "the source refused the token")
	}
	return nil
}

func (static) Fetch(_ context.Context, req *quivrplugin.FetchRequest) (*quivrplugin.Page, error) {
	if err := authorize(req.Credential); err != nil {
		return nil, err
	}
	var cfg config
	if err := req.Connector.DecodeConfig(&cfg); err != nil {
		return nil, quivrplugin.SourceError("invalid_config", err.Error())
	}
	var at checkpoint
	if err := req.DecodeCheckpoint(&at); err != nil {
		return nil, quivrplugin.SourceError("invalid_checkpoint", "the checkpoint is not an offset")
	}
	size := cfg.PageSize
	if size == 0 {
		size = 10
	}
	start := min(at.Offset, len(cfg.Items))
	end := min(start+size, len(cfg.Items))
	page := &quivrplugin.Page{Checkpoint: checkpoint{Offset: end}, More: end < len(cfg.Items), Reads: int64(end - start)}
	for _, item := range cfg.Items[start:end] {
		out := quivrplugin.Item{
			RecordKey: item.Key,
			Revision:  "1",
			Content: quivrplugin.NewManifest(
				quivrplugin.TextPart("title", "title", item.Title),
				quivrplugin.TextPart("body", "body", item.Body),
			),
		}
		if item.Author != "" {
			out.Extensions = map[string]quivrplugin.Extension{"example.static-source": {SchemaVersion: "1", Data: map[string]any{"author": item.Author}}}
		}
		page.Items = append(page.Items, out)
	}
	// The credential prints as [redacted], even here.
	req.Logger().Info("fetched a page", "items", len(page.Items), "offset", start, "credential", req.Credential)
	return page, nil
}

func (static) CheckCredential(_ context.Context, req *quivrplugin.CredentialRequest) (*quivrplugin.CredentialStatus, error) {
	if err := authorize(req.Credential); err != nil {
		return nil, err
	}
	return &quivrplugin.CredentialStatus{}, nil
}

func main() {
	plugin, err := quivrplugin.New("")
	if err == nil {
		err = plugin.Connector("static", static{})
	}
	if err == nil {
		err = plugin.Serve()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "static-source:", err)
		os.Exit(1)
	}
}
