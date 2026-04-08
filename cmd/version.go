package cmd

// Version info injected at build time via ldflags.
// goreleaser sets these automatically on release builds.
// Falls back to "dev" when building locally with `go build`.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)
