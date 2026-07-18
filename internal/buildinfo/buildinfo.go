// Package buildinfo holds build-time metadata injected via ldflags.
package buildinfo

// Version is the fboard-node release version (e.g. v0.3.1). Defaults to "dev".
var Version = "dev"

// BuildTime is the UTC build timestamp. Defaults to "unknown".
var BuildTime = "unknown"
