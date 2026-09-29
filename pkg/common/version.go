package common

import "fmt"

// Version, Commit, and Date are set at build time via goreleaser's -X ldflags (see
// .goreleaser.yml: pkg/common.Version/.Commit/.Date). Keep these identifiers -- package
// path and variable names -- in exact sync with .goreleaser.yml; a mismatch produces a
// released binary that reports "dev/unknown" forever, silently.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// VersionString renders the version identifiers the version command prints.
func VersionString() string {
	return fmt.Sprintf("oci-nuke %s (commit %s, built %s)", Version, Commit, Date)
}
