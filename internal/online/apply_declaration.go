package online

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/The-Vibe-Company/quivr/client"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
)

type sourcesDeclaration struct {
	Version    int               `json:"version"`
	Corpora    []sourceCorpus    `json:"corpora"`
	Connectors []sourceConnector `json:"connectors,omitempty"`
	Plugins    []sourcePlugin    `json:"plugins,omitempty"`
}
type sourceCorpus struct {
	Key     string  `json:"idempotency_key"`
	Name    string  `json:"name"`
	Profile *string `json:"retrieval_profile,omitempty"`
}
type sourceConnector struct {
	Key            string                    `json:"idempotency_key"`
	Corpus         string                    `json:"corpus"`
	Namespace      string                    `json:"source_namespace"`
	Kind           string                    `json:"kind"`
	Config         map[string]any            `json:"config"`
	Queue          string                    `json:"work_queue,omitempty"`
	Schedule       *client.ConnectorSchedule `json:"schedule,omitempty"`
	CredentialEnv  map[string]string         `json:"credential_env,omitempty"`
	CredentialFile string                    `json:"credential_file,omitempty"`
	secret         map[string]any
}
type sourcePlugin struct {
	Key           string            `json:"idempotency_key"`
	Endpoint      string            `json:"endpoint"`
	ManifestFile  string            `json:"manifest_file"`
	Configuration *map[string]any   `json:"configuration,omitempty"`
	Fixtures      map[string]string `json:"fixture_files,omitempty"`
	Kinds         *[]string         `json:"kinds,omitempty"`
	Routes        *[]struct {
		MediaType string                                      `json:"media_type"`
		Mode      *client.PluginRegistrationRequestRoutesMode `json:"mode,omitempty"`
	} `json:"routes,omitempty"`
	Spaces  *map[string]client.PluginRegistrationRequestSpaces `json:"spaces,omitempty"`
	pin     *plugins.Pin
	roles   []string
	request client.PluginRegistrationRequest
}

var envReference = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validDeclarationText(s string) bool {
	return strings.TrimSpace(s) != "" && !strings.ContainsRune(s, 0)
}
func decodeSources(r io.Reader, out any) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if dec.Decode(new(any)) != io.EOF {
		return errors.New("extra JSON value")
	}
	return nil
}
func relativeFile(base, name string) string {
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(base, name)
}
func loadSources(file string, env Env) (sourcesDeclaration, error) {
	var d sourcesDeclaration
	f, err := os.Open(file)
	if err != nil {
		return d, usageError("cannot read sources declaration")
	}
	defer f.Close()
	invalid := func() (sourcesDeclaration, error) {
		return d, usageError("invalid sources declaration: check version, keys, identities and references")
	}
	if decodeSources(io.LimitReader(f, 8<<20), &d) != nil || d.Version != 1 || len(d.Corpora) == 0 {
		return invalid()
	}
	corpora := map[string]bool{}
	connectors := map[string]bool{}
	namespaces := map[[2]string]bool{}
	pluginKeys := map[string]bool{}
	var pins []*plugins.Pin
	for _, c := range d.Corpora {
		if !validDeclarationText(c.Key) || !validDeclarationText(c.Name) || corpora[c.Key] || c.Profile != nil && !validDeclarationText(*c.Profile) {
			return invalid()
		}
		corpora[c.Key] = true
	}
	for i := range d.Connectors {
		c := &d.Connectors[i]
		ns := [2]string{c.Corpus, c.Namespace}
		if !validDeclarationText(c.Key) || !corpora[c.Corpus] || !validDeclarationText(c.Namespace) || !validDeclarationText(c.Kind) || c.Config == nil || connectors[c.Key] || namespaces[ns] {
			return invalid()
		}
		if c.Queue == "" {
			c.Queue = "live"
		}
		if c.Queue != "live" && c.Queue != "bulk" {
			return invalid()
		}
		if c.Schedule != nil && c.Schedule.IntervalSeconds != nil && (*c.Schedule.IntervalSeconds < 1 || *c.Schedule.IntervalSeconds > 86400) {
			return invalid()
		}
		if len(c.CredentialEnv) > 0 && c.CredentialFile != "" {
			return invalid()
		}
		if len(c.CredentialEnv) > 0 {
			c.secret = map[string]any{}
			for field, name := range c.CredentialEnv {
				if !validDeclarationText(field) || !envReference.MatchString(name) || env.Getenv(name) == "" {
					return d, usageError("invalid sources declaration: a credential environment reference is missing or invalid")
				}
				c.secret[field] = env.Getenv(name)
			}
		}
		if c.CredentialFile != "" {
			path := relativeFile(filepath.Dir(file), c.CredentialFile)
			if privateJournalPath(path) != nil {
				return d, usageError("credential file must be a private regular file (0600)")
			}
			b, err := os.ReadFile(path)
			if err != nil || json.Unmarshal(b, &c.secret) != nil || c.secret == nil {
				return d, usageError("invalid sources declaration: cannot read credential JSON object")
			}
		}
		connectors[c.Key] = true
		namespaces[ns] = true
	}
	for i := range d.Plugins {
		p := &d.Plugins[i]
		if !validDeclarationText(p.Key) || !validDeclarationText(p.Endpoint) || p.ManifestFile == "" || pluginKeys[p.Key] {
			return invalid()
		}
		manifest, err := os.ReadFile(relativeFile(filepath.Dir(file), p.ManifestFile))
		if err != nil || len(manifest) == 0 || len(manifest) > 262144 {
			return d, usageError("invalid sources declaration: cannot read plugin manifest")
		}
		fixtures := map[string][]byte{}
		for name, path := range p.Fixtures {
			data, err := os.ReadFile(relativeFile(filepath.Dir(file), path))
			if err != nil {
				return d, usageError("invalid sources declaration: cannot read plugin fixture")
			}
			fixtures[name] = data
		}
		p.request = client.PluginRegistrationRequest{IdempotencyKey: p.Key, Endpoint: p.Endpoint, Manifest: string(manifest), Configuration: p.Configuration, Kinds: p.Kinds, Routes: p.Routes, Spaces: p.Spaces}
		if len(fixtures) > 0 {
			p.request.Fixtures = &fixtures
		}
		var cfg plugins.PinConfig
		settings, _ := json.Marshal(p.request)
		// PinConfig shares the public registration settings' JSON keys.
		if json.Unmarshal(settings, &cfg) != nil {
			return invalid()
		}
		cfg.Manifest = p.ManifestFile
		pin, err := plugins.LoadPinManifest(manifest, p.ManifestFile, cfg)
		if err != nil {
			return d, usageError("invalid sources declaration: plugin settings do not match its manifest")
		}
		p.pin = pin
		for _, route := range cfg.Routes {
			p.roles = append(p.roles, "normalizer:"+route.MediaType)
		}
		if pin.Manifest.Contributions.Subscription != nil {
			p.roles = append(p.roles, "subscription:"+pin.Manifest.ID)
		}
		if contribution := pin.Manifest.Contributions.Connector; contribution != nil {
			for kind := range contribution.Kinds {
				p.roles = append(p.roles, "connector:"+kind)
			}
		}
		if pin.Manifest.Contributions.Ingestion != nil {
			p.roles = append(p.roles, "ingestion:"+pin.Manifest.ID)
		}
		if pin.Manifest.Contributions.Retrieval != nil {
			p.roles = append(p.roles, "retrieval:"+pin.Manifest.ID)
		}
		pins = append(pins, pin)
		pluginKeys[p.Key] = true
	}
	if _, err := plugins.NewPinSet(pins); err != nil {
		return d, usageError("invalid sources declaration: conflicting plugin roles")
	}
	return d, nil
}
