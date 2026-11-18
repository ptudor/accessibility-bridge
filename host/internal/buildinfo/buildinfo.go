// Package buildinfo exposes the version stamped into the binary by the
// Makefile (-ldflags -X). Without it, Version is "dev" plus the VCS revision
// the Go toolchain recorded, when there is one.
package buildinfo

import "runtime/debug"

// Version is the human-readable version string, set at link time.
var Version = "dev"

// String returns Version, extended with the VCS revision for dev builds.
func String() string {
	if Version != "dev" {
		return Version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Version
	}
	revision, modified := "", ""
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) > 12 {
				revision = s.Value[:12]
			} else {
				revision = s.Value
			}
		case "vcs.modified":
			if s.Value == "true" {
				modified = "-dirty"
			}
		}
	}
	if revision == "" {
		return Version
	}
	return Version + "-" + revision + modified
}
