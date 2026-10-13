package main

import (
	"runtime/debug"
	"testing"

	"github.com/leungll/Emberling/backend/internal/api"
)

func TestBuildInfoOf_InjectedValuesWin(t *testing.T) {
	info := &debug.BuildInfo{
		Main:     debug.Module{Version: "v9.9.9"},
		Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "deadbeef"}},
	}
	got := buildInfoOf("v0.4.0", "2f6efb2", info, true)
	if got != (api.BuildInfo{Version: "v0.4.0", Commit: "2f6efb2"}) {
		t.Errorf("buildInfoOf = %+v", got)
	}
}

func TestBuildInfoOf_FallsBackToEmbeddedModuleAndVCSInfo(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.5.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123abcd"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	got := buildInfoOf("", "", info, true)
	if got != (api.BuildInfo{Version: "v0.5.0", Commit: "0123abcd-dirty"}) {
		t.Errorf("buildInfoOf = %+v", got)
	}
}

func TestBuildInfoOf_NothingKnown_LeavesFieldsEmpty(t *testing.T) {
	devel := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}
	for name, tc := range map[string]struct {
		info *debug.BuildInfo
		ok   bool
	}{
		"no build info":     {nil, false},
		"devel without vcs": {devel, true},
	} {
		if got := buildInfoOf("", "", tc.info, tc.ok); got != (api.BuildInfo{}) {
			t.Errorf("%s: buildInfoOf = %+v, want empty fields for the API to report unknown", name, got)
		}
	}
}
