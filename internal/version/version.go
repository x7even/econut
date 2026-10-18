// Package version reports which build of econut is running.
//
// The value comes from the module data the Go toolchain embeds, so there is no
// build flag to pass and nothing that can drift from the git tag: a binary from
// "go install github.com/x7even/econut/cmd/...@v0.3.2" reports v0.3.2, and a
// "go build" in a working copy reports the pseudo-version the toolchain derives
// from the commit, suffixed "+dirty" when there are uncommitted changes. Both
// are passed through verbatim, so this agrees with "go version -m" rather than
// offering a second opinion.
//
// Only a build with no module data at all - "-buildvcs=false", or a build from
// outside a repository - reaches the fallback, and even then it answers rather
// than returning nothing, because the question this package exists to settle is
// whether a machine is protected by the build you think it is.
package version

import "runtime/debug"

// String returns a short version for this binary. It never returns an empty
// string, because a blank answer to "which build is this?" is worse than an
// honest "unknown".
func String() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	return describe(info)
}

// describe is String's logic, split out so it can be tested against build info
// that this test binary does not have - a release tag, a build with no VCS data.
func describe(info *debug.BuildInfo) string {
	if info == nil {
		return "unknown"
	}
	// A module version is authoritative when there is one, and for a build in a
	// working copy that is a pseudo-version carrying the revision and "+dirty".
	// "(devel)" is what the toolchain writes when it has no module version to
	// report, so it is a marker, not a version.
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}

	var rev string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}

	out := "devel"
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if rev != "" {
		out += "-" + rev
	}
	if dirty {
		out += "+dirty"
	}
	return out
}
