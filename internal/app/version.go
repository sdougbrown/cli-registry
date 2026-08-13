// Package app holds build-time version variables injected by GoReleaser.
package app

// Commit and Version are set via -ldflags at release time (see
// .goreleaser.yaml). They are empty in local/development builds.
var (
	Commit  string
	Version string
)
