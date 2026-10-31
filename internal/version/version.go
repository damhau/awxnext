// Package version is awxnext's own version and build. Release builds set
// them with -ldflags:
//
//	-X github.com/damhau/awxnext/internal/version.Version=1.2.0
//	-X github.com/damhau/awxnext/internal/version.Commit=<sha>
//	-X github.com/damhau/awxnext/internal/version.Built=<RFC 3339 time>
//
// Without them, the Go toolchain's VCS stamp of the checkout is used (a
// `go build` in a git clone), and the version is "dev". The AWX version
// awxnext answers to is api.ProductVersion.
package version

import (
	"runtime"
	"runtime/debug"
)

var (
	Version = "dev"
	Commit  = ""
	Built   = ""
)

type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Built   string `json:"built"`
	// Modified: built from a checkout with uncommitted changes.
	Modified bool   `json:"modified"`
	Go       string `json:"go"`
}

func Get() Info {
	i := Info{Version: Version, Commit: Commit, Built: Built, Go: runtime.Version()}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if i.Commit == "" {
					i.Commit = s.Value
				}
			case "vcs.time":
				if i.Built == "" {
					i.Built = s.Value
				}
			case "vcs.modified":
				i.Modified = s.Value == "true" && Commit == ""
			}
		}
	}
	return i
}
