package app

import (
	"errors"
	"io"
	"testing"
)

// The storage command owns refusal of removed subcommands and flags. An
// invalid database URL makes an accidental connection/setup attempt visible:
// every obsolete form must fail in argument validation first.
func TestRunStorageRejectsRemovedCommandsAndFlagsBeforeDatabaseAccess(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code string
	}{
		{name: "activate", args: []string{"activate"}, code: "storage_unknown_command"},
		{name: "activate flag", args: []string{"activate", "--writers-drained"}, code: "storage_unknown_command"},
		{name: "compact", args: []string{"compact"}, code: "storage_unknown_command"},
		{name: "compact id flag", args: []string{"compact", "--id", "old"}, code: "storage_unknown_command"},
		{name: "status id flag", args: []string{"status", "--id", "old"}, code: "storage_invalid_arguments"},
		{name: "status batch flag", args: []string{"status", "--batch", "1"}, code: "storage_invalid_arguments"},
		{name: "status audit flag", args: []string{"status", "--retire-audit-detail"}, code: "storage_invalid_arguments"},
		{name: "status drain flag", args: []string{"status", "--writers-drained"}, code: "storage_invalid_arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runStorage(Config{DatabaseURL: "postgres://%"}, tc.args, io.Discard)
			var storage *storageError
			if !errors.As(err, &storage) || storage.code != tc.code {
				t.Fatalf("runStorage(%v) = %v, want engine error %s", tc.args, err, tc.code)
			}
		})
	}
}
