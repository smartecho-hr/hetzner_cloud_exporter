// Package buildinfo holds version information set at build time via -ldflags.
// Without -ldflags (a plain `go build` or `go install`), it falls back to what
// the Go toolchain embeds: the module version, the VCS revision and time, and
// the Go version and platform of the build.
package buildinfo

import (
	"fmt"
	"io"
	"runtime"
	"runtime/debug"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcmetrics"
)

// Build information, set with -ldflags "-X github.com/smartecho-hr/hetzner_cloud_exporter/internal/buildinfo.Version=..."
// (see the Dockerfile); unset values are filled from the Go build info in init.
var (
	Name      = "hetzner_cloud_exporter"
	Version   = "dev"
	Branch    = "unknown"
	Commit    = "none"
	Date      = "unknown"
	BuildUser = "unknown"
	GoVersion = "unknown"
	Platform  = "unknown"
)

func init() {
	info, ok := debug.ReadBuildInfo()
	fillFromBuildInfo(info, ok)
}

// fillFromBuildInfo fills in what -ldflags didn't set.
func fillFromBuildInfo(info *debug.BuildInfo, ok bool) {
	if GoVersion == "unknown" {
		GoVersion = runtime.Version()
	}
	if Platform == "unknown" {
		Platform = runtime.GOOS + "/" + runtime.GOARCH
	}
	if !ok {
		return
	}

	settings := make(map[string]string)
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	revision := settings["vcs.revision"]
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if Commit == "none" && revision != "" {
		Commit = revision
	}
	if Date == "unknown" && settings["vcs.time"] != "" {
		Date = settings["vcs.time"]
	}
	if Version == "dev" {
		switch {
		case info.Main.Version != "" && info.Main.Version != "(devel)":
			// go install module@version, or since Go 1.24 a plain go build in
			// a git checkout (a pseudo-version like v0.4.1-0.2026...-abcdef).
			Version = info.Main.Version
		case revision != "":
			Version = "dev-" + revision // older Go versions
			if settings["vcs.modified"] == "true" {
				Version += "-dirty"
			}
		}
	}
}

// Register registers the hetzner_cloud_exporter_build_info metric.
func Register(reg prometheus.Registerer) {
	buildInfo := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: hcmetrics.Namespace,
			Subsystem: "exporter",
			Name:      "build_info",
			Help:      "Build information for hetzner_cloud_exporter, always 1",
		},
		[]string{"version", "revision", "branch", "date", "goversion", "goos", "goarch"},
	)
	buildInfo.WithLabelValues(Version, Commit, Branch, Date, GoVersion, runtime.GOOS, runtime.GOARCH).Set(1)
	reg.MustRegister(buildInfo)
}

// Map returns the build information, as served on /version.
func Map() map[string]string {
	return map[string]string{
		"name":       Name,
		"version":    Version,
		"branch":     Branch,
		"commit":     Commit,
		"build_user": BuildUser,
		"build_date": Date,
		"go_version": GoVersion,
		"platform":   Platform,
	}
}

// Print writes the build information for --version.
func Print(w io.Writer) {
	fmt.Fprintf(w, "%s, version %s (branch: %s, commit: %s)\n", Name, Version, Branch, Commit)
	fmt.Fprintf(w, "Build user: %s\n", BuildUser)
	fmt.Fprintf(w, "Build date: %s\n", Date)
	fmt.Fprintf(w, "Go version: %s\n", GoVersion)
	fmt.Fprintf(w, "Platform: %s\n", Platform)
}
