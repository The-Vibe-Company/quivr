package migrations

import (
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// lastNumbered closes the legacy numbered set: no numbered migration above it
// may be added. New migrations use a UTC timestamp prefix instead.
const lastNumbered = 16

const stampLayout = "20060102T1504Z"

var (
	numberedName = regexp.MustCompile(`^([0-9]{3})_[a-z0-9_]+\.sql$`)
	stampedName  = regexp.MustCompile(`^([0-9]{8}T[0-9]{4}Z)_[a-z0-9_]+\.sql$`)
)

// Names returns the embedded migration names in apply order.
func Names() ([]string, error) {
	return NamesIn(Files)
}

// NamesIn returns the .sql migration names at the root of fsys in apply order.
func NamesIn(fsys fs.FS) ([]string, error) {
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

// Validate reports the first migration name that breaks the naming rules:
// numbered legacy files up to lastNumbered, then UTC-stamped files, each with a
// unique ordering key. Apply order is lexical, which places every numbered
// file before every stamped one.
func Validate(names []string) error {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	seen := map[string]string{}
	for _, name := range sorted {
		key, err := orderingKey(name)
		if err != nil {
			return err
		}
		if other, ok := seen[key]; ok {
			return fmt.Errorf("migrations %s and %s share ordering key %s", other, name, key)
		}
		seen[key] = name
	}
	return nil
}

func orderingKey(name string) (string, error) {
	if m := numberedName.FindStringSubmatch(name); m != nil {
		if n, _ := strconv.Atoi(m[1]); n > lastNumbered {
			return "", fmt.Errorf("migration %s: numbered migrations are closed at %03d; name new migrations <UTC YYYYMMDDTHHMMZ>_<slug>.sql (make migration name=<slug>)", name, lastNumbered)
		}
		return m[1], nil
	}
	if m := stampedName.FindStringSubmatch(name); m != nil {
		if _, err := time.Parse(stampLayout, m[1]); err != nil {
			return "", fmt.Errorf("migration %s: %s is not a valid UTC time", name, m[1])
		}
		return m[1], nil
	}
	return "", fmt.Errorf("migration %s: name must be <UTC YYYYMMDDTHHMMZ>_<slug>.sql with a lowercase [a-z0-9_] slug", name)
}

// Plan selects migrations in filename order. Contract migrations are optional
// until an operator explicitly closes the rollback window. Their first line
// is exactly "-- quivr:contract"; the lint validates all policy tags.
func Plan(fsys fs.FS, includeContract bool) ([]string, error) {
	names, err := NamesIn(fsys)
	if err != nil {
		return nil, err
	}
	selected := make([]string, 0, len(names))
	for _, name := range names {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		first, _, _ := strings.Cut(string(data), "\n")
		first = strings.TrimSuffix(first, "\r")
		if !includeContract && first == "-- quivr:contract" {
			continue
		}
		selected = append(selected, name)
	}
	return selected, nil
}
