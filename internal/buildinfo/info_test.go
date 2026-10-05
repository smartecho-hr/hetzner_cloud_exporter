package buildinfo

import (
	"runtime"
	"runtime/debug"
	"testing"
)

// A plain `go build` (no -ldflags) takes the version from the VCS data the
// Go toolchain embeds; values set with -ldflags win.
func TestFillFromBuildInfo(t *testing.T) {
	reset := func() {
		Version, Commit, Date, GoVersion, Platform = "dev", "none", "unknown", "unknown", "unknown"
	}
	t.Cleanup(reset)

	vcs := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "8da46f8c0ffee1234567890"},
		{Key: "vcs.time", Value: "2026-10-02T12:00:00Z"},
		{Key: "vcs.modified", Value: "true"},
	}}

	reset()
	fillFromBuildInfo(vcs, true)
	if Version != "dev-8da46f8c0ffe-dirty" || Commit != "8da46f8c0ffe" || Date != "2026-10-02T12:00:00Z" {
		t.Errorf("from VCS: version %q commit %q date %q", Version, Commit, Date)
	}
	if GoVersion != runtime.Version() || Platform != runtime.GOOS+"/"+runtime.GOARCH {
		t.Errorf("go version %q platform %q", GoVersion, Platform)
	}

	reset()
	fillFromBuildInfo(&debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}, true)
	if Version != "v1.2.3" {
		t.Errorf("go install @v1.2.3: version %q", Version)
	}

	reset()
	Version, Commit = "v0.4.0", "abc1234"
	fillFromBuildInfo(vcs, true)
	if Version != "v0.4.0" || Commit != "abc1234" {
		t.Errorf("-ldflags must win: version %q commit %q", Version, Commit)
	}

	reset()
	fillFromBuildInfo(nil, false)
	if Version != "dev" || GoVersion == "unknown" {
		t.Errorf("without build info: version %q go version %q", Version, GoVersion)
	}
}
