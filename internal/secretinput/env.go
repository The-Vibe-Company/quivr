package secretinput

import (
	"errors"
	"os"
	"strings"
)

var ErrConflict = errors.New("set either the secret variable or its _FILE binding")
var ErrFile = errors.New("cannot read a nonempty secret from its _FILE binding")

// Read resolves a secret binding without including the input value or path in
// diagnostics. One terminal CRLF or LF is removed from mounted files.
func Read(name string) (string, error) {
	value := os.Getenv(name)
	path := os.Getenv(name + "_FILE")
	if path == "" {
		return value, nil
	}
	if value != "" {
		return "", ErrConflict
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ErrFile
	}
	value = string(data)
	if strings.HasSuffix(value, "\r\n") {
		value = strings.TrimSuffix(value, "\r\n")
	} else {
		value = strings.TrimSuffix(value, "\n")
	}
	if strings.TrimSpace(value) == "" || strings.ContainsRune(value, 0) {
		return "", ErrFile
	}
	return value, nil
}
