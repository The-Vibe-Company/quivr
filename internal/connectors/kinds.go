package connectors

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

// Credential requirement of a connector kind, as published to clients.
const (
	CredentialNone     = "none"
	CredentialOptional = "optional"
	CredentialRequired = "required"
)

// CredentialRequirer is optionally implemented by a Connector that cannot
// collect without a Deposited Credential. Kinds with a credential schema that
// do not implement it take the credential as optional.
type CredentialRequirer interface {
	CredentialRequired() bool
}

// KindDescription publishes what a client needs to configure one enabled
// kind. Title and Description are the config schema's own annotations, so a
// provider describes itself through its schemas alone.
type KindDescription struct {
	Kind             string
	Title            string
	Description      string
	ConfigSchema     json.RawMessage
	CredentialSchema json.RawMessage
	Credential       string
	DefaultInterval  time.Duration
}

// Catalog is the deployment's connector configuration surface.
type Catalog struct {
	Kinds []KindDescription
	// CredentialDeposits is false on a deployment without credential_key,
	// where every deposit and rotation is refused.
	CredentialDeposits bool
	MinInterval        time.Duration
}

// Describe lists the enabled kinds in kind order.
func (r *Registry) Describe() []KindDescription {
	out := []KindDescription{}
	if r == nil {
		return out
	}
	current := r.current()
	for _, name := range r.Enabled() {
		c := current[name].connector
		d := KindDescription{Kind: name, Title: name, ConfigSchema: json.RawMessage(c.ConfigSchema()), Credential: CredentialNone, DefaultInterval: c.DefaultInterval()}
		var annotations struct{ Title, Description string }
		if json.Unmarshal(d.ConfigSchema, &annotations) == nil {
			if annotations.Title != "" {
				d.Title = annotations.Title
			}
			d.Description = annotations.Description
		}
		if describer, ok := c.(interface{ Description() string }); ok && d.Description == "" {
			// A plugin kind's manifest description, when its schema has none.
			d.Description = describer.Description()
		}
		if schema := c.CredentialSchema(); schema != nil {
			d.CredentialSchema = json.RawMessage(schema)
			d.Credential = CredentialOptional
			if req, ok := c.(CredentialRequirer); ok && req.CredentialRequired() {
				d.Credential = CredentialRequired
			}
		}
		out = append(out, d)
	}
	return out
}

// fieldError locates a validation failure in the request as a JSON Pointer
// without changing the public code of the error it wraps.
type fieldError struct {
	err     error
	pointer string
}

func (e *fieldError) Error() string       { return e.err.Error() + " at " + e.pointer }
func (e *fieldError) Unwrap() error       { return e.err }
func (e *fieldError) PublicField() string { return e.pointer }

// WithField attaches the JSON Pointer of the offending request member.
func WithField(err error, pointer string) error {
	if err == nil {
		return nil
	}
	return &fieldError{err: err, pointer: pointer}
}

// Field returns the JSON Pointer attached to err, or "" when there is none.
func Field(err error) string {
	var f *fieldError
	if errors.As(err, &f) {
		return f.pointer
	}
	return ""
}

// pointer renders instance location tokens as an RFC 6901 JSON Pointer.
func pointer(tokens []string) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteByte('/')
		b.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(t))
	}
	return b.String()
}

// SchemaPointer returns the JSON Pointer of the instance member that failed a
// JSON Schema validation, or "" when err is not a validation failure.
func SchemaPointer(err error) string { return location(err) }

// location finds the most specific instance location of a schema failure.
// Alternatives (oneOf/anyOf) stop the descent: no single branch is the
// operator's intent, so the failure points at the value they constrain.
func location(err error) string {
	var v *jsonschema.ValidationError
	if !errors.As(err, &v) {
		return ""
	}
	for {
		switch k := v.ErrorKind.(type) {
		case *kind.OneOf, *kind.AnyOf:
			return pointer(v.InstanceLocation)
		case *kind.Required:
			if len(k.Missing) > 0 {
				return pointer(append(append([]string{}, v.InstanceLocation...), k.Missing[0]))
			}
		case *kind.AdditionalProperties:
			if len(k.Properties) > 0 {
				return pointer(append(append([]string{}, v.InstanceLocation...), k.Properties[0]))
			}
		}
		if len(v.Causes) == 0 {
			return pointer(v.InstanceLocation)
		}
		v = v.Causes[0]
	}
}
