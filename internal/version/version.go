// Package version exposes build-time identity for the HollowTone server so a
// deployment can be verified without inspecting the binary on disk.
//
// The values are injected at link time:
//
//	go build -ldflags "\
//	  -X github.com/YLHhhh123/HollowTone-server/internal/version.Version=v1.2.3 \
//	  -X github.com/YLHhhh123/HollowTone-server/internal/version.Commit=abc1234 \
//	  -X github.com/YLHhhh123/HollowTone-server/internal/version.BuildTime=2026-11-01T12:00:00Z"
package version

import (
	"runtime"
	"runtime/debug"
)

// Injected at link time; the defaults mark an unversioned local build.
var (
	// Version is the semantic version of this build.
	Version = "dev"
	// Commit is the git revision the binary was built from.
	Commit = "none"
	// BuildTime is the RFC 3339 timestamp of the build.
	BuildTime = "unknown"
)

// Info is the machine-readable build identity served by GET /version.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"buildTime"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"`
	// VCSModified is true when the working tree had uncommitted changes.
	VCSModified bool `json:"vcsModified"`
}

// Current returns the build identity, filling gaps from the Go build info
// embedded by the toolchain when link-time values were not supplied.
func Current() Info {
	info := Info{
		Version:   Version,
		Commit:    Commit,
		BuildTime: BuildTime,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if info.Commit == "none" && s.Value != "" {
					info.Commit = s.Value
					if len(info.Commit) > 12 {
						info.Commit = info.Commit[:12]
					}
				}
			case "vcs.modified":
				info.VCSModified = s.Value == "true"
			case "vcs.time":
				if info.BuildTime == "unknown" && s.Value != "" {
					info.BuildTime = s.Value
				}
			}
		}
	}
	return info
}
