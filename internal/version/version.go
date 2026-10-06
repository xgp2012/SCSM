package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// These variables are intended to be overridden at build time via
// -ldflags "-X scnetm/internal/version.Version=...".
var (
	// Version is the panel version. Keep it in sync with releases.
	Version = "0.1.0-dev"
	// Commit is the git revision the binary was built from.
	Commit = "unknown"
	// BuildTime is an RFC3339 timestamp injected at build time.
	BuildTime = "unknown"
)

// Info describes the running panel build.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
	Modified  bool   `json:"modified,omitempty"`
	VCSRev    string `json:"vcs_revision,omitempty"`
}

// Get returns the build information for the running binary.
//
// When the binary was built with the Go toolchain from a VCS checkout (the
// common case for local `go build` and `go run`), the embedded build info is
// preferred for Commit because it is always accurate; an explicit -ldflags
// override still wins.
func Get() Info {
	info := Info{
		Version:   Version,
		Commit:    Commit,
		BuildTime: BuildTime,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}

	if bi, ok := debug.ReadBuildInfo(); ok {
		if Commit == "unknown" || Commit == "" {
			if rev := vcsSetting(bi, "vcs.revision"); rev != "" {
				info.VCSRev = rev
				shorted := rev
				if len(shorted) > 12 {
					shorted = shorted[:12]
				}
				info.Commit = shorted
			}
		}
		if bt := vcsSetting(bi, "vcs.time"); bt != "" && (BuildTime == "unknown" || BuildTime == "") {
			info.BuildTime = bt
		}
		info.Modified = vcsSetting(bi, "vcs.modified") == "true"
	}

	if info.Commit == "" {
		info.Commit = "unknown"
	}
	if info.BuildTime == "" {
		info.BuildTime = "unknown"
	}
	return info
}

func vcsSetting(bi *debug.BuildInfo, key string) string {
	for _, s := range bi.Settings {
		if s.Key == key {
			return s.Value
		}
	}
	return ""
}

// String renders a compact one-line banner, e.g.
// "scnetm 0.1.0-dev (a1b2c3d4e5f6, go1.27.1 linux/amd64)".
func (i Info) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "scnetm %s (%s, %s %s)", i.Version, i.Commit, i.GoVersion, i.Platform)
	if i.Modified {
		b.WriteString(" [dirty]")
	}
	return b.String()
}

// String renders the build banner for the current binary.
func String() string { return Get().String() }

// GoVersionLine renders the toolchain and platform line shown by --version.
func GoVersionLine() string {
	i := Get()
	line := fmt.Sprintf("  built with %s for %s", i.GoVersion, i.Platform)
	if i.BuildTime != "unknown" {
		line += fmt.Sprintf("\n  built at   %s", i.BuildTime)
	}
	return line
}
