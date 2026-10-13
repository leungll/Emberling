package main

import (
	"runtime/debug"

	"github.com/leungll/Emberling/backend/internal/api"
)

// Build labels injected at link time, for example
// -ldflags "-X main.buildVersion=v0.4.0 -X main.buildCommit=2f6efb2". They identify the
// binary only; nothing secret is ever injected here.
var (
	buildVersion string
	buildCommit  string
)

// currentBuildInfo reports the injected labels, falling back to what the Go toolchain
// embedded in the binary.
func currentBuildInfo() api.BuildInfo {
	info, ok := debug.ReadBuildInfo()
	return buildInfoOf(buildVersion, buildCommit, info, ok)
}

// buildInfoOf prefers the injected labels. Without them it uses the main module version
// (unless it is the "(devel)" placeholder of a local build) and the VCS revision, marked
// "-dirty" when the working tree had local changes. A field left empty is reported as
// unknown by the API.
func buildInfoOf(version, commit string, info *debug.BuildInfo, ok bool) api.BuildInfo {
	out := api.BuildInfo{Version: version, Commit: commit}
	if !ok || info == nil {
		return out
	}
	if out.Version == "" && info.Main.Version != "(devel)" {
		out.Version = info.Main.Version
	}
	if out.Commit == "" {
		var revision, modified string
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				revision = s.Value
			case "vcs.modified":
				modified = s.Value
			}
		}
		if revision != "" && modified == "true" {
			revision += "-dirty"
		}
		out.Commit = revision
	}
	return out
}
