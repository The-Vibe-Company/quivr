package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

// testdata/golden holds what the engine's built-in m365_mail connector
// answered for every fake-Graph scenario before it moved into this plugin
// (THE-716): the scenario's inputs and the built-in's pages. Replaying them
// proves the plugin returns the same Record Keys, revisions, Manifests,
// extensions, attachment bytes and checkpoints, so an instance collected by
// the built-in continues here with no duplicates.

type goldenOp struct {
	Op          string          `json:"op"`
	Message     map[string]any  `json:"message,omitempty"`
	Attachments []goldenFile    `json:"attachments,omitempty"`
	ID          string          `json:"id,omitempty"`
	N           int             `json:"n,omitempty"`
	Checkpoint  json.RawMessage `json:"checkpoint,omitempty"`
	Want        *goldenPage     `json:"want,omitempty"`
}

type goldenFile struct {
	Meta  map[string]any `json:"meta"`
	Bytes string         `json:"bytes_b64"`
}

type goldenPage struct {
	Items      []goldenItem    `json:"items,omitempty"`
	Checkpoint json.RawMessage `json:"checkpoint,omitempty"`
	More       bool            `json:"more,omitempty"`
	Error      *goldenError    `json:"error,omitempty"`
}

type goldenError struct {
	Class string `json:"class"`
	Code  string `json:"code"`
}

type goldenItem struct {
	RecordKey   string             `json:"record_key"`
	Revision    string             `json:"revision"`
	Content     json.RawMessage    `json:"content"`
	Extensions  json.RawMessage    `json:"extensions"`
	Attachments []goldenAttachment `json:"attachments"`
}

type goldenAttachment struct {
	Key        string          `json:"key"`
	ParentKey  string          `json:"parent_key,omitempty"`
	Role       string          `json:"role"`
	MediaType  string          `json:"media_type"`
	Extensions json.RawMessage `json:"extensions,omitempty"`
	Size       int64           `json:"size"`
	SHA256     string          `json:"sha256"`
}

type goldenScenario struct {
	Name       string          `json:"name"`
	Now        time.Time       `json:"now"`
	Config     json.RawMessage `json:"config"`
	Credential json.RawMessage `json:"credential"`
	Ops        []goldenOp      `json:"ops"`
}

const graphPlaceholder = "http://graph.invalid"

func TestTheBuiltInConnectorsAnswersAreReproduced(t *testing.T) {
	paths, _ := filepath.Glob("testdata/golden/*.json")
	if len(paths) == 0 {
		t.Fatal("no golden scenario")
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var sc goldenScenario
		if err := json.Unmarshal(raw, &sc); err != nil {
			t.Fatal(err)
		}
		t.Run(sc.Name, func(t *testing.T) { replay(t, sc) })
	}
}

func replay(t *testing.T, sc goldenScenario) {
	g := newFakeGraph(t)
	c := g.connector()
	local := func(raw json.RawMessage) json.RawMessage {
		return json.RawMessage(strings.ReplaceAll(string(raw), graphPlaceholder, g.url))
	}
	var previous json.RawMessage
	for i, op := range sc.Ops {
		switch op.Op {
		case "add":
			var files []fakeAttachment
			for _, f := range op.Attachments {
				b, _ := base64.StdEncoding.DecodeString(f.Bytes)
				files = append(files, fakeAttachment{meta: f.Meta, bytes: string(b)})
			}
			g.addRaw(op.Message, files...)
		case "remove":
			g.control("removed", map[string]any{"id": op.ID})
		case "partial":
			g.control("partial", map[string]any{"id": op.ID})
		case "page_size":
			g.control("mailbox", map[string]any{"page_size": op.N})
		case "expire_delta":
			g.control("expire-delta", nil)
		case "fail":
			g.failNext("/messages/delta", failure{status: op.N, code: op.ID})
		case "fetch":
			checkpoint := op.Checkpoint
			if string(checkpoint) == `"previous"` {
				checkpoint = previous
			}
			var r quivrplugin.FetchRequest
			body := map[string]any{"invocation_id": "inv", "contribution": "connector", "organization_id": "org_a", "configuration": g.configuration(),
				"connector":  map[string]any{"instance_id": "connector_1", "kind": Kind, "config": sc.Config},
				"credential": sc.Credential, "checkpoint": orNull(local(checkpoint)), "now": sc.Now.Format(time.RFC3339), "page_in_run": 0, "reads_today": 0}
			b, _ := json.Marshal(body)
			if err := json.Unmarshal(b, &r); err != nil {
				t.Fatal(err)
			}
			page, err := c.Fetch(context.Background(), &r)
			got := goldenPage{}
			if err != nil {
				var e *quivrplugin.Error
				if !errors.As(err, &e) {
					t.Fatalf("op %d: unclassified error %v", i, err)
				}
				got.Error = &goldenError{Class: string(e.Class), Code: e.Code}
			} else {
				cp, _ := json.Marshal(page.Checkpoint)
				previous = cp
				got.Checkpoint = json.RawMessage(strings.ReplaceAll(string(cp), g.url, graphPlaceholder))
				got.More = page.More
				for _, item := range page.Items {
					got.Items = append(got.Items, goldenOf(t, g, c, sc, item))
				}
			}
			wantPage := *op.Want
			wantPage.Checkpoint = comparableCheckpoint(t, wantPage.Checkpoint)
			got.Checkpoint = comparableCheckpoint(t, got.Checkpoint)
			if !sameJSON(t, got, wantPage) {
				want, _ := json.MarshalIndent(op.Want, "", " ")
				have, _ := json.MarshalIndent(got, "", " ")
				t.Fatalf("op %d differs from the built-in connector\nwant %s\ngot  %s", i, want, have)
			}
		}
	}
}

func orNull(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return raw
}

// goldenOf reads every attachment through its ref, as the SDK does for the
// core, and records its bytes' size and digest. A descriptor with exact
// bytes (the HTML body) must announce them.
func goldenOf(t *testing.T, g *fakeGraph, c *Mail, sc goldenScenario, item quivrplugin.Item) goldenItem {
	t.Helper()
	content, _ := json.Marshal(item.Content)
	exts, _ := json.Marshal(item.Extensions)
	out := goldenItem{RecordKey: item.RecordKey, Revision: item.Revision, Content: content, Extensions: exts, Attachments: []goldenAttachment{}}
	for _, at := range item.Attachments {
		var r quivrplugin.AttachmentRequest
		body := map[string]any{"invocation_id": "inv", "contribution": "connector", "organization_id": "org_a", "configuration": g.configuration(),
			"connector":  map[string]any{"instance_id": "connector_1", "kind": Kind, "config": sc.Config},
			"credential": sc.Credential, "now": sc.Now.Format(time.RFC3339), "item": map[string]any{"record_key": item.RecordKey}, "attachment": at}
		b, _ := json.Marshal(body)
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatal(err)
		}
		rc, err := c.OpenAttachment(context.Background(), &r)
		if err != nil {
			t.Fatalf("open %s: %v", at.Key, err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		if at.SHA256 != "" && (at.SHA256 != hash(data) || at.SizeBytes == nil || *at.SizeBytes != int64(len(data))) {
			t.Fatalf("%s announces other bytes than it reads", at.Key)
		}
		a := goldenAttachment{Key: at.Key, ParentKey: at.ParentKey, Role: at.Role, MediaType: at.MediaType, Size: int64(len(data)), SHA256: hash(data)}
		if len(at.Extensions) > 0 {
			a.Extensions, _ = json.Marshal(at.Extensions)
		}
		out.Attachments = append(out.Attachments, a)
	}
	return out
}

func sameJSON(t *testing.T, a, b any) bool {
	t.Helper()
	var x, y any
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	_ = json.Unmarshal(ab, &x)
	_ = json.Unmarshal(bb, &y)
	return reflect.DeepEqual(x, y)
}

// Graph continuation tokens are opaque. Retain the link host/path, query
// options and token kind; the existing replay follows the unmodified token
// to verify paging and checkpoint resumption through the provider boundary.
func comparableCheckpoint(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	if len(raw) == 0 {
		return raw
	}
	var cp map[string]any
	if err := json.Unmarshal(raw, &cp); err != nil {
		t.Fatal(err)
	}
	if link, ok := cp["link"].(string); ok {
		u, err := url.Parse(link)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		for _, key := range []string{"$skiptoken", "$deltatoken"} {
			if q.Get(key) != "" {
				q.Set(key, "opaque")
			}
		}
		u.RawQuery = q.Encode()
		cp["link"] = u.String()
	}
	b, err := json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
