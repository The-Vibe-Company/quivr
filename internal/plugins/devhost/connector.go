package devhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

// Connector Contribution routes (Plugin API 0.3).
const (
	ConnectorFetchRoute           = "/v0/contributions/connector/fetch"
	ConnectorCheckCredentialRoute = "/v0/contributions/connector/check_credential"
)

// Connector fixture defaults (contracts/plugins/v0/connector-fixture.schema.json).
const (
	DevConnectorNow          = "2026-01-01T00:00:00Z"
	DefaultConnectorMaxPages = 10
)

// IsConnectorFixture reports whether a fixture file is a connector fixture:
// it has a top-level connector property.
func IsConnectorFixture(raw []byte) bool {
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) != nil {
		return false
	}
	_, ok := probe["connector"]
	return ok
}

// ConnectorExpectedPage is one page a connector fixture expects.
type ConnectorExpectedPage struct {
	RecordKeys []string `json:"record_keys"`
	More       *bool    `json:"more,omitempty"`
}

// ConnectorExpectedError is an error class (and optionally code) a connector
// fixture expects.
type ConnectorExpectedError struct {
	Class string `json:"error_class"`
	Code  string `json:"code,omitempty"`
}

// ConnectorExpectedCredential is the expected check_credential answer: status
// ok, or an error class.
type ConnectorExpectedCredential struct {
	Status string `json:"status,omitempty"`
	Class  string `json:"error_class,omitempty"`
	Code   string `json:"code,omitempty"`
}

// ConnectorRun is a development connector run built from a connector fixture.
type ConnectorRun struct {
	Kind       string
	Config     json.RawMessage
	Credential json.RawMessage
	Checkpoint json.RawMessage
	Now        string
	MaxPages   int
	Expect     struct {
		Pages           []ConnectorExpectedPage      `json:"pages,omitempty"`
		Error           *ConnectorExpectedError      `json:"error,omitempty"`
		CheckCredential *ConnectorExpectedCredential `json:"check_credential,omitempty"`
	}

	configuration json.RawMessage
	short         string
}

type connectorFixture struct {
	Connector struct {
		Kind   string          `json:"kind"`
		Config json.RawMessage `json:"config"`
	} `json:"connector"`
	Credential    json.RawMessage `json:"credential,omitempty"`
	Configuration json.RawMessage `json:"configuration,omitempty"`
	Checkpoint    json.RawMessage `json:"checkpoint,omitempty"`
	Now           string          `json:"now,omitempty"`
	MaxPages      int             `json:"max_pages,omitempty"`
	Expect        json.RawMessage `json:"expect,omitempty"`
}

type connectorRef struct {
	InstanceID string          `json:"instance_id"`
	Kind       string          `json:"kind"`
	Config     json.RawMessage `json:"config"`
}

type connectorFetchRequest struct {
	InvocationID   string          `json:"invocation_id"`
	Contribution   string          `json:"contribution"`
	OrganizationID string          `json:"organization_id"`
	Configuration  json.RawMessage `json:"configuration"`
	Connector      connectorRef    `json:"connector"`
	Credential     json.RawMessage `json:"credential"`
	Checkpoint     json.RawMessage `json:"checkpoint"`
	Now            string          `json:"now"`
	PageInRun      int             `json:"page_in_run"`
	ReadsToday     int64           `json:"reads_today"`
}

type connectorCredentialRequest struct {
	InvocationID   string          `json:"invocation_id"`
	Contribution   string          `json:"contribution"`
	OrganizationID string          `json:"organization_id"`
	Configuration  json.RawMessage `json:"configuration"`
	Connector      connectorRef    `json:"connector"`
	Credential     json.RawMessage `json:"credential"`
	Now            string          `json:"now"`
}

// BuildConnectorRun reads a connector fixture and validates it: the fixture
// schema, the plugin configuration, and the Connector Instance configuration
// and credential against the kind's declared schemas. The Connector Instance
// id is dev-connector-<digest> and invocation ids dev-invocation-<digest>-<suffix>,
// from the first 16 hex digits of the SHA-256 of the fixture bytes; the
// Organization is dev-organization. Issues report an invalid fixture; err
// reports I/O failures.
func BuildConnectorRun(path string, m *plugins.Manifest) (*ConnectorRun, []plugins.Issue, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	if issues := plugins.ValidateDocument("connector-fixture.schema.json", raw); len(issues) > 0 {
		return nil, issues, nil
	}
	var f connectorFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(raw)
	run := &ConnectorRun{Kind: f.Connector.Kind, Config: f.Connector.Config, Credential: f.Credential, Checkpoint: f.Checkpoint,
		Now: f.Now, MaxPages: f.MaxPages, configuration: f.Configuration, short: hex.EncodeToString(sum[:])[:16]}
	if len(run.Credential) == 0 {
		run.Credential = json.RawMessage("null")
	}
	if len(run.Checkpoint) == 0 {
		run.Checkpoint = json.RawMessage("null")
	}
	if run.Now == "" {
		run.Now = DevConnectorNow
	}
	if run.MaxPages == 0 {
		run.MaxPages = DefaultConnectorMaxPages
	}
	if len(run.configuration) == 0 {
		run.configuration = json.RawMessage(`{}`)
	}
	if len(f.Expect) > 0 {
		if err := json.Unmarshal(f.Expect, &run.Expect); err != nil {
			return nil, nil, err
		}
	}
	issues := plugins.ValidateConfiguration(m, run.configuration)
	issues = append(issues, plugins.ValidateConnectorInstance(m, run.Kind, run.Config, run.Credential)...)
	if len(issues) > 0 {
		return nil, issues, nil
	}
	return run, nil, nil
}

func (r *ConnectorRun) ref(kind string) connectorRef {
	return connectorRef{InstanceID: "dev-connector-" + r.short, Kind: kind, Config: r.Config}
}

// FetchRequest builds the fetch request of one page of the run. suffix makes
// the invocation id unique per attempt.
func (r *ConnectorRun) FetchRequest(checkpoint json.RawMessage, page int, readsToday int64, suffix string) []byte {
	body, _ := json.Marshal(connectorFetchRequest{
		InvocationID: fmt.Sprintf("dev-invocation-%s-%s", r.short, suffix), Contribution: "connector",
		OrganizationID: "dev-organization", Configuration: r.configuration, Connector: r.ref(r.Kind),
		Credential: r.Credential, Checkpoint: checkpoint, Now: r.Now, PageInRun: page, ReadsToday: readsToday,
	})
	return body
}

// CheckCredentialRequest builds the check_credential request of the run.
func (r *ConnectorRun) CheckCredentialRequest(suffix string) []byte {
	body, _ := json.Marshal(connectorCredentialRequest{
		InvocationID: fmt.Sprintf("dev-invocation-%s-%s", r.short, suffix), Contribution: "connector",
		OrganizationID: "dev-organization", Configuration: r.configuration, Connector: r.ref(r.Kind),
		Credential: r.Credential, Now: r.Now,
	})
	return body
}

// InvokeConnectorFetch posts a fetch request and applies check to a 200 body,
// such as a closure over plugins.CheckConnectorOutput.
func InvokeConnectorFetch(ctx context.Context, baseURL string, request []byte, maxResponseBytes int, check func(body []byte) []plugins.Issue) (*Result, error) {
	return invoke(ctx, baseURL, ConnectorFetchRoute, request, maxResponseBytes, check)
}

// InvokeCheckCredential posts a check_credential request and validates a 200
// body with plugins.CheckCredentialOutput.
func InvokeCheckCredential(ctx context.Context, baseURL string, request []byte) (*Result, error) {
	return invoke(ctx, baseURL, ConnectorCheckCredentialRoute, request, 64<<10, plugins.CheckCredentialOutput)
}
