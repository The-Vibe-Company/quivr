package plugins

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version is a SemVer 2.0.0 version. Build metadata is ignored for precedence.
type Version struct {
	Major, Minor, Patch uint64
	Pre                 []string
}

var semverPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// ParseVersion parses a SemVer 2.0.0 version.
func ParseVersion(s string) (Version, error) {
	m := semverPattern.FindStringSubmatch(s)
	if m == nil {
		return Version{}, fmt.Errorf("%q is not a SemVer 2.0.0 version", s)
	}
	var v Version
	var err error
	for i, target := range []*uint64{&v.Major, &v.Minor, &v.Patch} {
		if *target, err = strconv.ParseUint(m[i+1], 10, 64); err != nil {
			return Version{}, fmt.Errorf("%q: %w", s, err)
		}
	}
	if m[4] != "" {
		v.Pre = strings.Split(m[4], ".")
	}
	return v, nil
}

func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	return s
}

// Compare orders versions by SemVer precedence.
func (v Version) Compare(w Version) int {
	for _, pair := range [][2]uint64{{v.Major, w.Major}, {v.Minor, w.Minor}, {v.Patch, w.Patch}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(v.Pre) == 0 && len(w.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1
	case len(w.Pre) == 0:
		return -1
	}
	for i := 0; i < len(v.Pre) && i < len(w.Pre); i++ {
		if c := compareIdentifier(v.Pre[i], w.Pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(v.Pre) < len(w.Pre):
		return -1
	case len(v.Pre) > len(w.Pre):
		return 1
	}
	return 0
}

func compareIdentifier(a, b string) int {
	an, aErr := strconv.ParseUint(a, 10, 64)
	bn, bErr := strconv.ParseUint(b, 10, 64)
	switch {
	case aErr == nil && bErr == nil:
		if an < bn {
			return -1
		} else if an > bn {
			return 1
		}
		return 0
	case aErr == nil:
		return -1
	case bErr == nil:
		return 1
	}
	return strings.Compare(a, b)
}

type comparator struct {
	op string
	v  Version
}

// Range is a set of comparators that must all hold. The grammar is
// language-neutral and deliberately small: whitespace-separated comparators,
// each an optional operator (>=, >, <=, <, =) directly followed by a
// MAJOR.MINOR.PATCH version. See contracts/plugins/v0/README.md.
type Range struct {
	raw   string
	comps []comparator
}

var comparatorPattern = regexp.MustCompile(`^(>=|<=|>|<|=)?((?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*))$`)

// ParseRange parses a range and rejects ranges no release version satisfies.
func ParseRange(s string) (Range, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return Range{}, errors.New("range is empty")
	}
	r := Range{raw: s}
	for _, field := range fields {
		m := comparatorPattern.FindStringSubmatch(field)
		if m == nil {
			return Range{}, fmt.Errorf("%q is not a comparator (use >=, >, <=, < or = followed by MAJOR.MINOR.PATCH, separated by spaces)", field)
		}
		v, err := ParseVersion(m[2])
		if err != nil {
			return Range{}, err
		}
		op := m[1]
		if op == "" {
			op = "="
		}
		r.comps = append(r.comps, comparator{op: op, v: v})
	}
	if !r.satisfiable() {
		return Range{}, fmt.Errorf("range %q is not satisfied by any release version", s)
	}
	return r, nil
}

// satisfiable reports whether some release version (no pre-release) satisfies
// every comparator: the smallest release meeting all lower bounds must also
// meet every upper bound.
func (r Range) satisfiable() bool {
	low := Version{}
	for _, c := range r.comps {
		candidate := c.v
		switch c.op {
		case ">":
			candidate.Patch++
		case ">=", "=":
		default:
			continue
		}
		if candidate.Compare(low) > 0 {
			low = candidate
		}
	}
	return r.Contains(low)
}

// Contains reports whether v satisfies every comparator by SemVer precedence.
func (r Range) Contains(v Version) bool {
	for _, c := range r.comps {
		cmp := v.Compare(c.v)
		ok := false
		switch c.op {
		case ">=":
			ok = cmp >= 0
		case ">":
			ok = cmp > 0
		case "<=":
			ok = cmp <= 0
		case "<":
			ok = cmp < 0
		case "=":
			ok = cmp == 0
		}
		if !ok {
			return false
		}
	}
	return true
}

func (r Range) String() string { return strings.Join(strings.Fields(r.raw), " ") }
