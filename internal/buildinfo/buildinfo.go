// Package buildinfo contains release metadata injected by the Go linker.
package buildinfo

import "fmt"

// These values are intentionally useful in unversioned developer builds. The
// release builder replaces them with -X linker assignments derived from Git.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// Info is an immutable build metadata snapshot.
type Info struct {
	Version string
	Commit  string
	Date    string
}

// Current returns the metadata compiled into the running program.
func Current() Info {
	return Info{Version: Version, Commit: Commit, Date: Date}
}

// Format returns a consistent version line for all Netspeed binaries.
func Format(program string, info Info) string {
	return fmt.Sprintf("%s %s (commit: %s, built: %s)",
		program, valueOr(info.Version, "dev"), valueOr(info.Commit, "unknown"), valueOr(info.Date, "unknown"))
}

// Line returns a consistent version line using the compiled metadata.
func Line(program string) string {
	return Format(program, Current())
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
