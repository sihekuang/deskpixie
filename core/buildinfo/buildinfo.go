// Package buildinfo is the version and commit a build was stamped with (#295), set by the
// Makefile for both the core library and pix:
//
//	-ldflags "-X github.com/sihekuang/deskpixie/core/buildinfo.version=0.19.0
//	          -X github.com/sihekuang/deskpixie/core/buildinfo.commit=abc1234"
//
// A build without them (go test, go run) is "dev", with Go's own record of the commit.
package buildinfo

import "runtime/debug"

var (
	version string
	commit  string
)

// Version is the release version, "dev" when none was stamped.
func Version() string {
	if version == "" {
		return "dev"
	}
	return version
}

// Commit is the short commit the build came from, "-dirty" when it had uncommitted changes;
// "" when unknown.
func Commit() string {
	if commit != "" {
		return commit
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return vcsCommit(info.Settings)
}

// vcsCommit is the short commit in Go's build settings (vcs.revision, vcs.modified).
func vcsCommit(settings []debug.BuildSetting) string {
	rev, dirty := "", false
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if rev != "" && dirty {
		rev += "-dirty"
	}
	return rev
}

// String is the version and its commit: "0.19.0 (abc1234)", or just "0.19.0".
func String() string {
	return Format(Version(), Commit())
}

// Format is how a version and commit read together.
func Format(version, commit string) string {
	if commit == "" {
		return version
	}
	return version + " (" + commit + ")"
}
