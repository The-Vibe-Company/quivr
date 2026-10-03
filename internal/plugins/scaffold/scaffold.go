// Package scaffold writes the Python plugin templates embedded in the quivr
// binary (`quivr plugin init`): normalizers, alert rules and connectors.
// The templates depend only on the Quivr Plugin SDK (sdks/python).
package scaffold

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed all:templates
var templates embed.FS

// Template kinds, one per Contribution.
const (
	KindConnector    = "connector"
	KindNormalizer   = "normalizer"
	KindSubscription = "subscription"
)

// Kinds lists the template kinds; the first is the default.
var Kinds = []string{KindNormalizer, KindSubscription, KindConnector}

const (
	placeholderID     = "__PLUGIN_ID__"
	placeholderModule = "__PLUGIN_MODULE__"
)

var validName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// pythonKeywords cannot be used as a package name.
var pythonKeywords = map[string]bool{
	"and": true, "as": true, "assert": true, "async": true, "await": true, "break": true, "class": true,
	"continue": true, "def": true, "del": true, "elif": true, "else": true, "except": true, "finally": true,
	"for": true, "from": true, "global": true, "if": true, "import": true, "in": true, "is": true,
	"lambda": true, "nonlocal": true, "not": true, "or": true, "pass": true, "raise": true, "return": true,
	"try": true, "while": true, "with": true, "yield": true,
}

// shadowedModules would be shadowed by, or would shadow, the generated package:
// `python3 -m <module>` runs from the plugin directory, which comes first on
// sys.path. They cover the template's own directories, the SDK and its
// dependencies, and the standard library modules a plugin commonly imports.
var shadowedModules = map[string]bool{
	"tests": true, "fixtures": true, "quivr_plugin": true, "yaml": true, "jsonschema": true, "referencing": true,
	"abc": true, "argparse": true, "array": true, "ast": true, "asyncio": true, "base64": true, "binascii": true,
	"bisect": true, "builtins": true, "calendar": true, "cmd": true, "code": true, "codecs": true, "collections": true,
	"concurrent": true, "configparser": true, "contextlib": true, "contextvars": true, "copy": true, "csv": true,
	"ctypes": true, "dataclasses": true, "datetime": true, "decimal": true, "difflib": true, "dis": true,
	"email": true, "encodings": true, "enum": true, "errno": true, "fnmatch": true, "fractions": true,
	"functools": true, "gc": true, "getpass": true, "gettext": true, "glob": true, "gzip": true, "hashlib": true,
	"heapq": true, "hmac": true, "html": true, "http": true, "importlib": true, "inspect": true, "io": true,
	"ipaddress": true, "itertools": true, "json": true, "keyword": true, "linecache": true, "locale": true,
	"logging": true, "lzma": true, "math": true, "mimetypes": true, "multiprocessing": true, "numbers": true,
	"operator": true, "os": true, "pathlib": true, "pickle": true, "platform": true, "posixpath": true,
	"pprint": true, "queue": true, "random": true, "re": true, "runpy": true, "secrets": true, "select": true,
	"selectors": true, "shlex": true, "shutil": true, "signal": true, "site": true, "socket": true,
	"socketserver": true, "sqlite3": true, "ssl": true, "stat": true, "statistics": true, "string": true,
	"struct": true, "subprocess": true, "sys": true, "tempfile": true, "test": true, "textwrap": true,
	"threading": true, "time": true, "token": true, "tokenize": true, "traceback": true, "types": true,
	"typing": true, "unicodedata": true, "unittest": true, "urllib": true, "uuid": true, "venv": true,
	"warnings": true, "weakref": true, "xml": true, "zipfile": true, "zlib": true, "zoneinfo": true,
}

// ModuleName is the Python package name for a plugin id.
func ModuleName(id string) string { return strings.ReplaceAll(id, "-", "_") }

// CheckName validates a plugin name for init: a single-segment plugin id that
// also yields a valid Python package name.
func CheckName(name string) error {
	if !validName.MatchString(name) || strings.HasSuffix(name, "-") {
		return fmt.Errorf("plugin name %q must start with a lowercase letter and contain only lowercase letters, digits and dashes (at most 64 characters)", name)
	}
	if pythonKeywords[ModuleName(name)] {
		return fmt.Errorf("plugin name %q is a Python keyword; choose another name", name)
	}
	if shadowedModules[ModuleName(name)] {
		return fmt.Errorf("plugin name %q would clash with the Python module %q; choose another name", name, ModuleName(name))
	}
	return nil
}

// CheckKind validates a template kind.
func CheckKind(kind string) error {
	for _, k := range Kinds {
		if k == kind {
			return nil
		}
	}
	return fmt.Errorf("unknown template kind %q; use %s", kind, strings.Join(Kinds, " or "))
}

// Write creates the template of the given kind for plugin id in dir, which
// must not exist or be empty. It returns the written paths relative to dir.
func Write(dir, id, kind string) ([]string, error) {
	return write(dir, id, kind, false)
}

// WritePush writes a Python source that handles an instance-token API route.
func WritePush(dir, id string) ([]string, error) {
	return write(dir, id, KindConnector, true)
}

func write(dir, id, kind string, push bool) ([]string, error) {
	if err := CheckName(id); err != nil {
		return nil, err
	}
	if err := CheckKind(kind); err != nil {
		return nil, err
	}
	root := "templates/" + kind
	if push {
		root = "templates/push"
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return nil, fmt.Errorf("%s already exists and is not empty", dir)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	replace := strings.NewReplacer(placeholderID, id, placeholderModule, ModuleName(id))
	var written []string
	err := fs.WalkDir(templates, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == "__pycache__" {
			return fs.SkipDir
		}
		if d.IsDir() || strings.HasSuffix(p, ".pyc") {
			return nil
		}
		rel := replace.Replace(strings.TrimPrefix(p, root+"/"))
		data, err := templates.ReadFile(p)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(replace.Replace(string(data))), 0o644); err != nil {
			return err
		}
		written = append(written, path.Clean(rel))
		return nil
	})
	return written, err
}
