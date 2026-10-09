package online

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/client"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// readApply intentionally discards remote diagnostics and request URLs: errors
// from credential validation, proxies and providers may include secret values.
func readApply[T any](resp *http.Response, err error) (T, int, error) {
	var out T
	if err != nil {
		exit := ExitUnavailable
		if err == context.Canceled {
			exit = ExitInterrupted
		}
		return out, 0, &Failure{Exit: exit, Message: "apply API request failed"}
	}
	defer resp.Body.Close()
	status := resp.StatusCode
	if status < 200 || status >= 300 {
		return out, status, responseFailure(status, nil)
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out) != nil {
		return out, status, &Failure{Exit: ExitFailed, Message: "apply API response does not match the contract"}
	}
	return out, status, nil
}

type applyRun struct {
	ctx           context.Context
	env           Env
	cl, operator  *client.ClientWithResponses
	state         *journalFile
	desired       sourcesDeclaration
	corpora       map[string]client.Corpus
	connectors    map[string]client.Connector
	registrations map[string]client.PluginRegistration
	changes       []string
	unsupported   bool
	activations   map[string]bool
}

// The public activation endpoint acquired an optional request body in newer
// clients. Keep this command compatible with both generated client versions.
func activateApply(ctx context.Context, cl any, id, key string) (*http.Response, error) {
	if current, ok := cl.(interface {
		ActivatePluginWithBody(context.Context, string, string, io.Reader, ...client.RequestEditorFn) (*http.Response, error)
	}); ok {
		body, err := json.Marshal(map[string]string{"idempotency_key": key})
		if err != nil {
			return nil, err
		}
		return current.ActivatePluginWithBody(ctx, id, "application/json", strings.NewReader(string(body)))
	}
	if legacy, ok := cl.(interface {
		ActivatePlugin(context.Context, string, ...client.RequestEditorFn) (*http.Response, error)
	}); ok {
		return legacy.ActivatePlugin(ctx, id)
	}
	return nil, fmt.Errorf("public plugin activation client is unavailable")
}

func (a *applyRun) change(format string, args ...any) {
	a.changes = append(a.changes, fmt.Sprintf(format, args...))
}
func (a *applyRun) refuse(kind, key, reason string) {
	a.unsupported = true
	a.change("! %s %q: unsupported — %s", kind, key, reason)
}
func (a *applyRun) save() error {
	if a.state.save() != nil {
		return &Failure{Exit: ExitFailed, Message: "cannot save replay journal; retain it and resume apply"}
	}
	return nil
}
func validateApplySchema(raw map[string]any, value any) bool {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(applySchemaLoader{})
	if compiler.AddResource("https://quivr.invalid/apply-schema", raw) != nil {
		return false
	}
	schema, err := compiler.Compile("https://quivr.invalid/apply-schema")
	return err == nil && schema.Validate(value) == nil
}

type applySchemaLoader struct{}

func (applySchemaLoader) Load(string) (any, error) {
	return nil, fmt.Errorf("external schema references are unavailable")
}
func applyInterval(c sourceConnector) int {
	if c.Schedule == nil || c.Schedule.IntervalSeconds == nil {
		return 0
	}
	return *c.Schedule.IntervalSeconds
}
func queueApply(c client.Connector) string {
	if c.WorkQueue == nil {
		return "live"
	}
	return string(*c.WorkQueue)
}
func credentialVersion(c client.Connector) int {
	if c.Credential == nil {
		return 0
	}
	return c.Credential.Version
}
func (a *applyRun) inspect() error {
	a.corpora = map[string]client.Corpus{}
	a.connectors = map[string]client.Connector{}
	a.registrations = map[string]client.PluginRegistration{}
	j := &a.state.journal
	a.activations = map[string]bool{}
	// Verify organization access before the first mutation, including an empty journal.
	if _, _, err := readApply[client.CorpusPage](a.cl.ListCorpora(a.ctx, nil)); err != nil {
		return err
	}
	desiredCorpora := map[string]bool{}
	desiredConnectors := map[string]bool{}
	desiredPlugins := map[string]bool{}
	for _, c := range a.desired.Corpora {
		desiredCorpora[c.Key] = true
		e := j.Corpora[c.Key]
		if e == nil {
			a.change("+ corpus %q: %q", c.Key, c.Name)
			continue
		}
		if !equalApply(e.Declaration.Profile, c.Profile) {
			a.refuse("corpus", c.Key, "retrieval-profile changes need the public rebuild/routing workflow")
			continue
		}
		if e.ID == "" {
			if !equalApply(e.Declaration, c) {
				a.refuse("corpus", c.Key, "finish the original pending creation before changing it")
			}
			a.change("+ corpus %q (replay pending creation)", c.Key)
			continue
		}
		actual, status, err := readApply[client.Corpus](a.cl.GetCorpus(a.ctx, e.ID))
		if status == 404 {
			a.change("+ corpus %q (missing from this installation)", c.Key)
			continue
		}
		if err != nil {
			return err
		}
		if actual.Archived != nil && *actual.Archived {
			a.refuse("corpus", c.Key, "archived; use the public unarchive action deliberately")
		}
		if !equalApply(actual.EffectiveRetrieval.PluginProfile, c.Profile) {
			a.refuse("corpus", c.Key, "the effective retrieval profile changed outside this declaration")
		}
		a.corpora[c.Key] = actual
		if actual.Name != c.Name {
			a.change("~ corpus %q name: %q -> %q", c.Key, actual.Name, c.Name)
		}
	}
	var catalog client.ConnectorKindCatalog
	if len(a.desired.Connectors) > 0 {
		var err error
		catalog, _, err = readApply[client.ConnectorKindCatalog](a.cl.ListConnectorKinds(a.ctx))
		if err != nil {
			return err
		}
	}
	kinds := map[string]client.ConnectorKindDescription{}
	for _, k := range catalog.Items {
		kinds[k.Kind] = k
	}
	// A declared, locally validated manifest can introduce kinds during this apply.
	for _, p := range a.desired.Plugins {
		if contribution := p.pin.Manifest.Contributions.Connector; contribution != nil {
			for name, kind := range contribution.Kinds {
				description := client.ConnectorKindDescription{Kind: name, DefaultIntervalSeconds: kind.DefaultIntervalSeconds, Credential: "none"}
				if json.Unmarshal(kind.ConfigSchema, &description.ConfigSchema) != nil {
					return usageError("invalid connector manifest schema")
				}
				if kind.CredentialSchema != nil {
					var schema map[string]any
					if json.Unmarshal(kind.CredentialSchema, &schema) != nil {
						return usageError("invalid credential manifest schema")
					}
					description.CredentialSchema = &schema
					description.Credential = "optional"
					if kind.NeedsCredential() {
						description.Credential = "required"
					}
				}
				kinds[name] = description
			}
		}
	}
	for i := range a.desired.Connectors {
		c := &a.desired.Connectors[i]
		desiredConnectors[c.Key] = true
		kind, ok := kinds[c.Kind]
		if !ok {
			a.refuse("connector", c.Key, "kind is not installed; start and activate its provider first")
			continue
		}
		if !validateApplySchema(kind.ConfigSchema, c.Config) {
			return usageError("invalid sources declaration: connector config does not match its public schema")
		}
		if c.secret != nil {
			if catalog.CredentialDeposits != "available" || kind.CredentialSchema == nil || !validateApplySchema(*kind.CredentialSchema, c.secret) {
				return usageError("invalid sources declaration: credentials unavailable or do not match the kind schema")
			}
		} else if kind.Credential == "required" {
			return usageError("invalid sources declaration: connector requires a credential reference")
		}
		if c.Schedule == nil || c.Schedule.IntervalSeconds == nil {
			interval := kind.DefaultIntervalSeconds
			c.Schedule = &client.ConnectorSchedule{IntervalSeconds: &interval}
		}
		if applyInterval(*c) < catalog.MinIntervalSeconds {
			return usageError("invalid sources declaration: connector schedule is below the deployment minimum")
		}
		e := j.Connectors[c.Key]
		if e == nil {
			a.change("+ connector %q: %s on corpus %q", c.Key, c.Kind, c.Corpus)
			continue
		}
		if e.ID == "" {
			if !equalApply(e.Declaration, *c) || e.Digest != j.digest(c.secret) {
				a.refuse("connector", c.Key, "finish the original pending creation before changing it")
			}
			a.change("+ connector %q (replay pending creation)", c.Key)
			continue
		}
		actual, status, err := readApply[client.Connector](a.cl.GetConnector(a.ctx, e.ID))
		if status == 404 {
			a.change("+ connector %q (missing from this installation)", c.Key)
			continue
		}
		if err != nil {
			return err
		}
		a.connectors[c.Key] = actual
		if !actual.Enabled || actual.PausedAt != nil {
			a.refuse("connector", c.Key, "disabled or paused; use the public lifecycle actions deliberately")
		}
		if actual.Kind != client.ConnectorKind(c.Kind) || actual.SourceNamespace != c.Namespace || !equalApply(actual.Config, c.Config) || queueApply(actual) != c.Queue || e.Declaration.Corpus != c.Corpus {
			a.refuse("connector", c.Key, "kind, config, corpus, source_namespace and work_queue have no in-place update API")
		}
		if parent, ok := a.corpora[c.Corpus]; !ok || actual.CorpusId != parent.CorpusId {
			a.refuse("connector", c.Key, "its corpus identity no longer matches the declaration")
		}
		version := credentialVersion(actual)
		if version != e.CredentialVersion {
			a.refuse("connector", c.Key, "credential version changed outside this journal")
		}
		if actual.Schedule.IntervalSeconds != applyInterval(*c) {
			a.change("~ connector %q interval_seconds: %d -> %d", c.Key, actual.Schedule.IntervalSeconds, applyInterval(*c))
		}
		if e.Digest != j.digest(c.secret) {
			a.change("~ connector %q credential: referenced secret changed (redacted)", c.Key)
			a.refuse("connector", c.Key, "credential change requires a conditional version API; rotate explicitly through the public API or reset")
		}
	}
	if len(a.desired.Plugins) > 0 {
		plan, status, err := readApply[client.PipelinePlan](a.operator.GetActivePipelinePlan(a.ctx))
		if err != nil && status != 404 {
			return err
		}
		registrations, _, err := readApply[client.PluginRegistrationList](a.operator.ListPluginRegistrations(a.ctx))
		if err != nil {
			return err
		}
		for _, p := range registrations.Items {
			a.registrations[p.RegistrationId] = p
		}
		for _, p := range a.desired.Plugins {
			desiredPlugins[p.Key] = true
			e := j.Plugins[p.Key]
			if e == nil {
				a.activations[p.Key] = true
				a.change("+ plugin %q: register and activate", p.Key)
				continue
			}
			if e.Digest != j.digest(p.request) {
				a.refuse("plugin", p.Key, "registration keys are immutable; settings need a new public registration")
			}
			actual, ok := a.registrations[e.ID]
			if !ok {
				a.activations[p.Key] = true
				a.change("+ plugin %q: register and activate", p.Key)
			} else if actual.State != client.PluginRegistrationStateActive || !applyPluginRoles(plan, p.roles, e.ID) {
				a.activations[p.Key] = true
				a.change("~ plugin %q: validate and activate", p.Key)
			}
		}
	}
	for k := range j.Corpora {
		if !desiredCorpora[k] {
			a.refuse("corpus", k, "removal requires the public archive/delete workflow")
		}
	}
	for k := range j.Connectors {
		if !desiredConnectors[k] {
			a.refuse("connector", k, "removal requires an explicit public disable action")
		}
	}
	for k := range j.Plugins {
		if !desiredPlugins[k] {
			a.refuse("plugin", k, "removal requires an explicit public routing action")
		}
	}
	return nil
}
func (a *applyRun) execute() error {
	j := &a.state.journal
	// A successful response is checkpointed immediately. Before each creation or
	// rotation, save its nonsecret intent so losing a reply can replay its key.
	for _, p := range a.desired.Plugins {
		e := j.Plugins[p.Key]
		if e == nil {
			e = &pluginEntry{Digest: j.digest(p.request)}
			j.Plugins[p.Key] = e
			if err := a.save(); err != nil {
				return err
			}
		}
		registration, exists := a.registrations[e.ID]
		if !exists {
			value, _, err := readApply[client.PluginRegistration](a.operator.RegisterPlugin(a.ctx, p.request))
			if err != nil {
				return err
			}
			registration = value
			e.ID = value.RegistrationId
			if err = a.save(); err != nil {
				return err
			}
		}
		deadline, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
		for registration.State == client.PluginRegistrationStateRegistered {
			timer := time.NewTimer(200 * time.Millisecond)
			select {
			case <-deadline.Done():
				timer.Stop()
				cancel()
				return &Failure{Exit: ExitUnavailable, Message: "plugin validation is pending; resume apply with the same journal"}
			case <-timer.C:
			}
			value, _, err := readApply[client.PluginRegistration](a.operator.GetPluginRegistration(deadline, e.ID))
			if err != nil {
				cancel()
				return err
			}
			registration = value
		}
		cancel()
		if registration.State == client.PluginRegistrationStateRejected {
			return &Failure{Exit: ExitInvalid, Message: "plugin registration rejected; inspect its public validation report"}
		}
		if registration.State != client.PluginRegistrationStateActive || a.activations[p.Key] {
			// Accept asynchronous activation Operations as well as the earlier plan response.
			_, _, err := readApply[json.RawMessage](activateApply(a.ctx, a.operator.ClientInterface, e.ID, j.digest(map[string]string{"activate_registration": e.ID})))
			if err != nil {
				return err
			}
			wait, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
			for {
				value, _, err := readApply[client.PluginRegistration](a.operator.GetPluginRegistration(wait, e.ID))
				if err != nil {
					cancel()
					return err
				}
				if value.State == client.PluginRegistrationStateActive {
					plan, _, err := readApply[client.PipelinePlan](a.operator.GetActivePipelinePlan(wait))
					if err != nil {
						cancel()
						return err
					}
					if applyPluginRoles(plan, p.roles, e.ID) {
						break
					}
				}
				timer := time.NewTimer(200 * time.Millisecond)
				select {
				case <-wait.Done():
					timer.Stop()
					cancel()
					return &Failure{Exit: ExitUnavailable, Message: "plugin activation is pending; resume apply"}
				case <-timer.C:
				}
			}
			cancel()
		}
	}
	for _, c := range a.desired.Corpora {
		e := j.Corpora[c.Key]
		actual, exists := a.corpora[c.Key]
		if !exists {
			if e == nil || e.ID != "" {
				e = &corpusEntry{Declaration: c}
				j.Corpora[c.Key] = e
				if err := a.save(); err != nil {
					return err
				}
			}
			req := client.CorpusRequest{IdempotencyKey: e.Declaration.Key, Name: e.Declaration.Name}
			if e.Declaration.Profile != nil {
				req.Retrieval = &client.RetrievalConfig{PluginProfile: e.Declaration.Profile}
			}
			value, _, err := readApply[client.Corpus](a.cl.CreateCorpus(a.ctx, req))
			if err != nil {
				return err
			}
			actual = value
			e.ID = value.CorpusId
			if err = a.save(); err != nil {
				return err
			}
		}
		latest, _, err := readApply[client.Corpus](a.cl.GetCorpus(a.ctx, e.ID))
		if err != nil {
			return err
		}
		if latest.Archived != nil && *latest.Archived || !equalApply(latest.EffectiveRetrieval.PluginProfile, c.Profile) || exists && latest.Name != actual.Name {
			return &Failure{Exit: ExitInvalid, Message: "corpus changed since preview; preview again"}
		}
		actual = latest
		if actual.Name != c.Name {
			value, _, err := readApply[client.Corpus](a.cl.RenameCorpus(a.ctx, e.ID, client.CorpusRenameRequest{Name: c.Name}))
			if err != nil {
				return err
			}
			actual = value
		}
		e.Declaration = c
		a.corpora[c.Key] = actual
		if err := a.save(); err != nil {
			return err
		}
	}
	for _, c := range a.desired.Connectors {
		e := j.Connectors[c.Key]
		actual, exists := a.connectors[c.Key]
		if !exists {
			if e == nil || e.ID != "" {
				e = &connectorEntry{Declaration: c, Digest: j.digest(c.secret)}
				j.Connectors[c.Key] = e
				if err := a.save(); err != nil {
					return err
				}
			}
			parent := a.corpora[c.Corpus]
			q := client.ConnectorCreateWorkQueue(c.Queue)
			req := client.ConnectorCreate{IdempotencyKey: c.Key, CorpusId: parent.CorpusId, SourceNamespace: c.Namespace, Kind: client.ConnectorKind(c.Kind), Config: c.Config, WorkQueue: &q, Schedule: c.Schedule}
			if c.secret != nil {
				req.Credential = &client.CredentialDeposit{Secret: &c.secret}
			}
			value, _, err := readApply[client.Connector](a.cl.CreateConnector(a.ctx, req))
			if err != nil {
				return err
			}
			actual = value
			e.ID = value.ConnectorId
			e.CredentialVersion = 0
			if c.secret != nil {
				e.CredentialVersion = 1
			}
			if err = a.save(); err != nil {
				return err
			}
		}
		latest, _, err := readApply[client.Connector](a.cl.GetConnector(a.ctx, e.ID))
		if err != nil {
			return err
		}
		if credentialVersion(latest) != e.CredentialVersion || !latest.Enabled || latest.PausedAt != nil {
			return &Failure{Exit: ExitInvalid, Message: "connector changed since preview; inspect the public resource before replay"}
		}
		if exists && latest.Schedule.IntervalSeconds != actual.Schedule.IntervalSeconds {
			return &Failure{Exit: ExitInvalid, Message: "connector schedule changed since preview; preview again"}
		}
		actual = latest
		if actual.Schedule.IntervalSeconds != applyInterval(c) {
			value, _, err := readApply[client.Connector](a.cl.ChangeConnectorSchedule(a.ctx, e.ID, client.ScheduleChange{IntervalSeconds: applyInterval(c)}))
			if err != nil {
				return err
			}
			actual = value
		}
		e.Declaration = c
		if credentialVersion(actual) != e.CredentialVersion {
			return &Failure{Exit: ExitInvalid, Message: "connector credential changed during apply; retain the journal and inspect the public resource"}
		}
		if err := a.save(); err != nil {
			return err
		}
	}
	return nil
}
func (a *applyRun) print() {
	for _, line := range a.changes {
		fmt.Fprintln(a.env.Stdout, line)
	}
	if len(a.changes) == 0 {
		fmt.Fprintln(a.env.Stdout, "No changes.")
	}
}
func applyScope(conn connection, env Env) string {
	base := conn.url
	if base == "" {
		base = env.Getenv(EnvAPIURL)
	}
	key := conn.key
	if key == "" {
		key = env.Getenv(EnvAPIKey)
	}
	return strings.TrimRight(base, "/") + "\x00" + key
}

// Registration state can remain active after only some of its roles were replaced.
func applyPluginRoles(plan client.PipelinePlan, roles []string, id string) bool {
	for _, role := range roles {
		found := false
		for _, actual := range plan.Roles {
			if actual.Role == role && actual.RegistrationId == id {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return len(roles) > 0
}
