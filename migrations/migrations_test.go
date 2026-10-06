package migrations

import (
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbeddedMigrationsFollowTheNamingRules(t *testing.T) {
	names, err := Names()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("no embedded migrations")
	}
	if !slices.IsSorted(names) {
		t.Fatalf("Names() not sorted: %v", names)
	}
	if err := Validate(names); err != nil {
		t.Fatal(err)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name  string
		names []string
		want  string // substring of the error; empty means valid
	}{
		{"legacy only", []string{"001_a.sql", "002_b.sql"}, ""},
		{"legacy then stamped", []string{"001_a.sql", "20260928T1130Z_b.sql", "20260928T1131Z_c.sql"}, ""},
		{"unsorted input is ordered first", []string{"20260928T1130Z_b.sql", "001_a.sql"}, ""},
		{"unknown shape", []string{"001_a.sql", "v2_b.sql"}, "v2_b.sql"},
		{"uppercase slug", []string{"20260928T1130Z_Bad.sql"}, "20260928T1130Z_Bad.sql"},
		{"seconds in stamp", []string{"20260928T113000Z_b.sql"}, "20260928T113000Z_b.sql"},
		{"numbered above the closed set", []string{"001_a.sql", "999_late.sql"}, "999_late.sql"},
		{"duplicate numbered key", []string{"001_a.sql", "001_b.sql"}, "001"},
		{"duplicate stamped key", []string{"20260928T1130Z_a.sql", "20260928T1130Z_b.sql"}, "20260928T1130Z"},
		{"impossible stamp", []string{"20261340T2599Z_a.sql"}, "20261340T2599Z_a.sql"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Validate(c.names)
			if c.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error mentioning %q, got %v", c.want, err)
			}
		})
	}
}

func TestPlanDefersContractsUnlessExplicitlySelected(t *testing.T) {
	fsys := fstest.MapFS{
		"20990101T0000Z_expand.sql":   &fstest.MapFile{Data: []byte("CREATE TABLE probe(id int)")},
		"20990101T0001Z_contract.sql": &fstest.MapFile{Data: []byte("-- quivr:contract\nDROP TABLE probe")},
		"20990101T0002Z_expand.sql":   &fstest.MapFile{Data: []byte("-- contract appears in prose\nCREATE TABLE next(id int)")},
	}
	for _, ending := range []string{"\n", "\r\n"} {
		fsys["20990101T0001Z_contract.sql"].Data = []byte("-- quivr:contract" + ending + "DROP TABLE probe")
		for _, include := range []bool{false, true} {
			names, err := Plan(fsys, include)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"20990101T0000Z_expand.sql", "20990101T0002Z_expand.sql"}
			if include {
				want = []string{"20990101T0000Z_expand.sql", "20990101T0001Z_contract.sql", "20990101T0002Z_expand.sql"}
			}
			if !slices.Equal(names, want) {
				t.Fatalf("include=%v: got %v, want %v", include, names, want)
			}
		}
	}
}
