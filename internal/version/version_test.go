package version

import (
	"runtime/debug"
	"strings"
	"testing"
)

func info(mainVersion string, settings ...debug.BuildSetting) *debug.BuildInfo {
	return &debug.BuildInfo{
		Main:     debug.Module{Path: "github.com/x7even/econut", Version: mainVersion},
		Settings: settings,
	}
}

func TestDescribe(t *testing.T) {
	cases := []struct {
		name string
		in   *debug.BuildInfo
		want string
	}{
		{
			name: "a release build reports its tag",
			in:   info("v0.3.1"),
			want: "v0.3.1",
		},
		{
			// What "go build ./cmd/econut" in this working copy actually
			// stamps, verified with "go version -m". It is the common local
			// case, so it must pass through rather than be reduced: it already
			// carries the revision and the dirty marker.
			name: "a dirty local build passes its pseudo-version through",
			in: info("v0.3.1-0.20261018024737-5e400ec0577f+dirty",
				debug.BuildSetting{Key: "vcs.revision", Value: "5e400ec0577f10942c80d4512c52ca9278e6fbed"},
				debug.BuildSetting{Key: "vcs.modified", Value: "true"}),
			want: "v0.3.1-0.20261018024737-5e400ec0577f+dirty",
		},
		{
			name: "a pseudo-version is a version too",
			in:   info("v0.0.0-20261012185746-69dea07cb17e"),
			want: "v0.0.0-20261012185746-69dea07cb17e",
		},
		{
			// Reached only when the toolchain has no module version to report,
			// as with -buildvcs=false or a build outside a repository, but the
			// VCS stamps survive.
			name: "no module version falls back to the revision",
			in: info("(devel)",
				debug.BuildSetting{Key: "vcs.revision", Value: "5e400ec0577f10942c80d4512c52ca9278e6fbed"},
				debug.BuildSetting{Key: "vcs.modified", Value: "false"}),
			want: "devel-5e400ec0577f",
		},
		{
			name: "uncommitted changes are called out in the fallback too",
			in: info("(devel)",
				debug.BuildSetting{Key: "vcs.revision", Value: "5e400ec0577f10942c80d4512c52ca9278e6fbed"},
				debug.BuildSetting{Key: "vcs.modified", Value: "true"}),
			want: "devel-5e400ec0577f+dirty",
		},
		{
			name: "no VCS data at all still answers",
			in:   info("(devel)"),
			want: "devel",
		},
		{
			name: "an empty module version is not a version",
			in:   info(""),
			want: "devel",
		},
		{
			name: "a short revision is not truncated",
			in: info("(devel)",
				debug.BuildSetting{Key: "vcs.revision", Value: "abc123"}),
			want: "devel-abc123",
		},
		{
			name: "nil build info",
			in:   nil,
			want: "unknown",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := describe(c.in); got != c.want {
				t.Errorf("describe() = %q, want %q", got, c.want)
			}
		})
	}
}

// String has to work in this test binary too, which is neither a release nor
// necessarily a VCS build, so assert only what every caller relies on: a
// non-empty single-line answer.
func TestStringIsAlwaysUsable(t *testing.T) {
	got := String()
	if got == "" {
		t.Fatal("String() is empty; callers print it as the answer to --version")
	}
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("String() = %q, want a single line", got)
	}
}
