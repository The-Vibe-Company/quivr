// Package indexwarmup warms an index independently of user search deadlines.
// It is used by the deployment service, not by the engine's request path.
package indexwarmup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

const sampleLimit = 32
const responseLimit = 16 << 20

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z_0-9]*$`)

type Config struct {
	URL, APIKey                 string
	RequestTimeout, PassTimeout time.Duration
}

// Report contains counts only; object text, vectors, tenants and credentials
// must not reach deployment logs.
type Report struct {
	Scopes         int `json:"scopes"`
	NearVector     int `json:"near_vector"`
	BM25           int `json:"bm25"`
	Errors         int `json:"errors"`
	PendingScopes  int `json:"pending_scopes"`
	MissingVectors int `json:"missing_vectors"`
}

// Warmer runs serial passes. Sampling cursors and round-robin scope scheduling
// prevent sparse spaces and slow tenants from indefinitely hiding later work.
type Warmer struct {
	config Config
	client *http.Client
	after  map[string]string
	probe  map[string]int
	next   string
}

func New(config Config) (*Warmer, error) {
	u, err := url.Parse(config.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("warm-up requires an HTTP(S) index URL without credentials or query")
	}
	if config.RequestTimeout <= 0 || config.RequestTimeout > 2*time.Minute || config.PassTimeout <= 0 || config.PassTimeout > 10*time.Minute {
		return nil, errors.New("warm-up requires positive request/pass budgets within 2/10 minutes")
	}
	config.URL = strings.TrimRight(config.URL, "/")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // Private dependency traffic must not inherit a proxy.
	return &Warmer{config: config, after: map[string]string{}, probe: map[string]int{}, client: &http.Client{
		Transport: transport, Timeout: config.RequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

type property struct {
	Name            string   `json:"name"`
	DataType        []string `json:"dataType"`
	IndexSearchable *bool    `json:"indexSearchable"`
}

type vectorConfig struct {
	Type   string `json:"vectorIndexType"`
	Config struct {
		Skip bool `json:"skip"`
	} `json:"vectorIndexConfig"`
}

type collection struct {
	Name       string                  `json:"class"`
	Vectors    map[string]vectorConfig `json:"vectorConfig"`
	Type       string                  `json:"vectorIndexType"`
	Config     struct{ Skip bool }     `json:"vectorIndexConfig"`
	Properties []property              `json:"properties"`
	Tenancy    struct{ Enabled bool }  `json:"multiTenancyConfig"`
}

type scope struct {
	collection collection
	tenant     string
}

func (s scope) key() string { return s.collection.Name + "/" + s.tenant }

func (w *Warmer) request(ctx context.Context, method, path string, body, result any) error {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, w.config.URL+path, bytes.NewReader(data))
	if err != nil {
		return errors.New("invalid warm-up request")
	}
	if w.config.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+w.config.APIKey)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		return errors.New("index warm-up request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("index warm-up returned a non-success status")
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, responseLimit+1))
	if err != nil || len(data) > responseLimit {
		return errors.New("index warm-up response exceeded its read budget")
	}
	if err := json.Unmarshal(data, result); err != nil {
		return errors.New("invalid index warm-up response")
	}
	return nil
}

func (c collection) targets() []string {
	var targets []string
	for name, vector := range c.Vectors {
		if vector.Type == "hnsw" && !vector.Config.Skip && identifier.MatchString(name) {
			targets = append(targets, name)
		}
	}
	if len(c.Vectors) == 0 && c.Type == "hnsw" && !c.Config.Skip {
		targets = append(targets, "") // Legacy unnamed vector.
	}
	sort.Strings(targets)
	return targets
}

func (w *Warmer) discover(ctx context.Context, report *Report) []scope {
	var schema struct {
		Classes []collection `json:"classes"`
	}
	if err := w.request(ctx, http.MethodGet, "/v1/schema", nil, &schema); err != nil {
		report.Errors++
		return nil
	}
	var scopes []scope
	for _, class := range schema.Classes {
		if !identifier.MatchString(class.Name) || len(class.targets()) == 0 {
			continue
		}
		if !class.Tenancy.Enabled {
			scopes = append(scopes, scope{collection: class})
			continue
		}
		var tenants []struct {
			Name   string `json:"name"`
			Status string `json:"activityStatus"`
		}
		if err := w.request(ctx, http.MethodGet, "/v1/schema/"+class.Name+"/tenants", nil, &tenants); err != nil {
			report.Errors++
			continue // An unavailable collection must not prevent other warming.
		}
		for _, tenant := range tenants {
			if tenant.Status == "HOT" || tenant.Status == "ACTIVE" {
				scopes = append(scopes, scope{collection: class, tenant: tenant.Name})
			}
		}
	}
	sort.Slice(scopes, func(i, j int) bool { return scopes[i].key() < scopes[j].key() })
	return scopes
}

type object struct {
	ID         string               `json:"id"`
	Properties map[string]any       `json:"properties"`
	Vector     []float32            `json:"vector"`
	Vectors    map[string][]float32 `json:"vectors"`
}

func quote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func (w *Warmer) query(ctx context.Context, s scope, branch string) error {
	args := "limit:1," + branch
	if s.tenant != "" {
		args += ",tenant:" + quote(s.tenant)
	}
	var result struct {
		Data   json.RawMessage `json:"data"`
		Errors []any           `json:"errors"`
	}
	query := "{Get{" + s.collection.Name + "(" + args + "){_additional{id}}}}"
	if err := w.request(ctx, http.MethodPost, "/v1/graphql", map[string]string{"query": query}, &result); err != nil {
		return err
	}
	if len(result.Errors) != 0 || len(result.Data) == 0 || string(result.Data) == "null" {
		return errors.New("index warm-up query failed")
	}
	return nil
}

func lexical(objects []object, properties []property) (string, string) {
	for _, prop := range properties {
		if !identifier.MatchString(prop.Name) || len(prop.DataType) != 1 || (prop.DataType[0] != "text" && prop.DataType[0] != "text[]") || (prop.IndexSearchable != nil && !*prop.IndexSearchable) {
			continue
		}
		for _, obj := range objects {
			values := []any{obj.Properties[prop.Name]}
			if array, ok := values[0].([]any); ok {
				values = array
			}
			for _, value := range values {
				text, _ := value.(string)
				words := strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
				if len(words) > 0 {
					return words[0], prop.Name
				}
			}
		}
	}
	return "", ""
}

func (w *Warmer) warm(ctx context.Context, s scope, report *Report) {
	params := url.Values{"class": {s.collection.Name}, "limit": {"32"}, "include": {"vector"}}
	if s.tenant != "" {
		params.Set("tenant", s.tenant)
	}
	if cursor := w.after[s.key()]; cursor != "" {
		params.Set("after", cursor)
	}
	var sample struct {
		Objects []object `json:"objects"`
	}
	if err := w.request(ctx, http.MethodGet, "/v1/objects?"+params.Encode(), nil, &sample); err != nil {
		report.Errors++
		delete(w.after, s.key()) // A deleted cursor must not strand this scope.
		return
	}
	if len(sample.Objects) == sampleLimit {
		w.after[s.key()] = sample.Objects[len(sample.Objects)-1].ID
	} else {
		delete(w.after, s.key())
	}
	if len(sample.Objects) == 0 {
		return
	}
	targets := s.collection.targets()
	word, prop := lexical(sample.Objects, s.collection.Properties)
	// Include BM25 in the same rotating queue, so slow vector targets cannot
	// consume every pass before keyword pages or later targets are visited.
	probes := len(targets) + 1
	start := w.probe[s.key()] % probes
	for i := 0; i < probes; i++ {
		if ctx.Err() != nil {
			break
		}
		position := (start + i) % probes
		w.probe[s.key()] = (position + 1) % probes
		if position == len(targets) {
			if word != "" {
				if err := w.query(ctx, s, "bm25:{query:"+quote(word)+",properties:["+quote(prop)+"]}"); err != nil {
					report.Errors++
				} else {
					report.BM25++
				}
			}
			continue
		}
		target := targets[position]
		var vector []float32
		for _, obj := range sample.Objects {
			vector = obj.Vectors[target]
			if target == "" {
				vector = obj.Vector
			}
			if len(vector) > 0 {
				break
			}
		}
		if len(vector) == 0 {
			report.MissingVectors++
			continue
		}
		data, _ := json.Marshal(vector)
		branch := "nearVector:{vector:" + string(data)
		if target != "" {
			branch += ",targetVectors:[" + quote(target) + "]"
		}
		if err := w.query(ctx, s, branch+"}"); err != nil {
			report.Errors++
		} else {
			report.NearVector++
		}
	}
}

// Pass discovers new collections/tenants each time. It never changes schema,
// activates an inactive tenant, or calls a model provider. A busy index is
// retried by the next deployment pass, outside the user search budget.
func (w *Warmer) Pass(parent context.Context) Report {
	ctx, cancel := context.WithTimeout(parent, w.config.PassTimeout)
	defer cancel()
	var report Report
	scopes := w.discover(ctx, &report)
	live := map[string]bool{}
	for _, scope := range scopes {
		live[scope.key()] = true
	}
	for key := range w.after {
		if !live[key] {
			delete(w.after, key)
		}
	}
	for key := range w.probe {
		if !live[key] {
			delete(w.probe, key)
		}
	}
	start := sort.Search(len(scopes), func(i int) bool { return scopes[i].key() >= w.next })
	for i := 0; i < len(scopes); i++ {
		scope := scopes[(start+i)%len(scopes)]
		w.next = scope.key()
		if ctx.Err() != nil {
			report.PendingScopes = len(scopes) - i
			break
		}
		w.warm(ctx, scope, &report)
		report.Scopes++
		w.next = scopes[(start+i+1)%len(scopes)].key()
	}
	return report
}

// Run makes a pass immediately, then waits interval after each bounded pass.
// Shutdown cancels both in-flight requests and the wait; passes never overlap.
func (w *Warmer) Run(ctx context.Context, interval time.Duration, report func(Report)) {
	for ctx.Err() == nil {
		report(w.Pass(ctx))
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
