package main

import (
	"fmt"
	"io"
	"runtime/debug"

	"github.com/operatorstack/pitot/adapters"
	"github.com/operatorstack/pitot/schema"
)

// version, commit, and date carry the release identity of the binary. They are
// injected at build time via ldflags (`-X main.version=...`), matching
// GoReleaser's default variable names so its stock build config stamps them
// without extra wiring. In a plain `go build` / `go run` checkout they stay at
// their defaults, and buildCommit() falls back to the VCS revision from
// debug.ReadBuildInfo().
var (
	version = "dev"
	commit  = ""
	date    = ""
)

// releaseVersion returns the injected release version, or "dev" for an
// un-stamped build.
func releaseVersion() string {
	if version == "" {
		return "dev"
	}
	return version
}

// buildCommit returns the injected commit, falling back to the VCS revision
// recorded in the build info, or "unknown".
func buildCommit() string {
	if commit != "" {
		return commit
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && len(setting.Value) >= 12 {
				return setting.Value[:12]
			}
		}
	}
	return "unknown"
}

// runVersion prints the binary's release identity and the wire contracts it
// speaks. After a hydrated install this reports the exact tag and commit the
// artifact was built from.
func runVersion(stdout io.Writer) error {
	fmt.Fprintf(stdout, "pitot %s (%s)\n", releaseVersion(), buildCommit())
	if date != "" {
		fmt.Fprintf(stdout, "  built            : %s\n", date)
	}
	fmt.Fprintf(stdout, "  protocol version : %s\n", schema.Version)
	fmt.Fprintf(stdout, "  adapter version  : %s\n", adapters.AdapterVersion)
	return nil
}
