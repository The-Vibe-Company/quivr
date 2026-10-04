package publicerr

import (
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/contracts"
	"gopkg.in/yaml.v3"
)

// The OpenAPI declarations independently own the codes and response classes
// clients rely on. This table covers every catalog entry, including the error
// embedded in an evaluator migration result rather than an HTTP refusal.
func TestCatalogMatchesHTTPContract(t *testing.T) {
	type declaration struct {
		Status       int      `yaml:"status_class"`
		Retryable    bool     `yaml:"retryable"`
		ResponseCode string   `yaml:"response_code"`
		Operations   []string `yaml:"operations"`
	}
	var doc struct {
		Errors map[string]declaration          `yaml:"x-public-errors"`
		Paths  map[string]map[string]yaml.Node `yaml:"paths"`
	}
	type operation struct {
		ID string `yaml:"operationId"`
	}
	if err := yaml.Unmarshal(contracts.OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	operations := map[string]bool{}
	for _, methods := range doc.Paths {
		for method, node := range methods {
			if method != "get" && method != "post" && method != "put" && method != "delete" && method != "patch" {
				continue
			}
			var op operation
			if err := node.Decode(&op); err != nil {
				t.Fatal(err)
			}
			if op.ID == "" {
				continue
			}
			operations[op.ID] = true
		}
	}
	for code, sentinel := range catalog {
		t.Run(code, func(t *testing.T) {
			want, ok := doc.Errors[code]
			if !ok {
				t.Fatalf("OpenAPI omits public error %s", code)
			}
			if int(sentinel.Class()) != want.Status || sentinel.Retryable() != want.Retryable {
				t.Errorf("status=%d retryable=%v, contract status=%d retryable=%v", sentinel.Class(), sentinel.Retryable(), want.Status, want.Retryable)
			}
			responseCode := want.ResponseCode
			if responseCode == "" {
				responseCode = code
			}
			if sentinel.ResponseCode() != responseCode {
				t.Errorf("response code=%s, contract response code=%s", sentinel.ResponseCode(), responseCode)
			}
			if len(want.Operations) == 0 {
				t.Fatal("no returning operations declared")
			}
			for _, op := range want.Operations {
				if !operations[op] {
					t.Errorf("operation %s absent from the contract", op)
				}
			}
		})
	}
	for code := range doc.Errors {
		if _, ok := catalog[code]; !ok {
			t.Errorf("OpenAPI declares uncataloged error %s", code)
		}
	}
}
