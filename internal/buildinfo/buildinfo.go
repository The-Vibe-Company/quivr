// Package buildinfo identifies the distribution build independently of the
// API and plugin compatibility versions.
package buildinfo

import (
	"fmt"
	"io"
)

// Release builds inject these values with go build -ldflags -X.
var Version = "dev"
var Revision = "unknown"

const VersionFlag = "--version"

// WriteMetric renders the immutable process identity in Prometheus format.
func WriteMetric(w io.Writer) {
	fmt.Fprintln(w, "# HELP quivr_build_info Quivr distribution build identity.")
	fmt.Fprintln(w, "# TYPE quivr_build_info gauge")
	fmt.Fprintf(w, "quivr_build_info{version=%q,revision=%q} 1\n", Version, Revision)
}
