package main

import (
	"fmt"
	"runtime/debug"
)

// version is set at build time with -ldflags "-X main.version=...". See the
// Makefile. When it is empty the value falls back to the VCS data that the Go
// toolchain stamps into the binary, so a plain "go build" still identifies
// itself.
var version string

// versionString returns the version of this binary, in order of preference:
// the value injected at build time, then the commit the toolchain recorded,
// then a marker that says neither was available.
func versionString() string {
	if version != "" {
		return version
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}

	var revision, modified string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			if setting.Value == "true" {
				modified = "-dirty"
			}
		}
	}

	if revision == "" {
		return "unknown"
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	return fmt.Sprintf("git-%s%s", revision, modified)
}
