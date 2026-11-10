package build

import (
	"runtime/debug"
	"testing"
)

func TestBuilderVersion(t *testing.T) {
	dir := func(path string) *debug.Module { return &debug.Module{Path: path, Version: "(devel)"} }
	tests := []struct {
		name   string
		bi     *debug.BuildInfo
		linked string
		want   string
	}{
		{"no build info", nil, "", ""},
		{"linked wins", &debug.BuildInfo{Main: debug.Module{Path: modulePath, Version: "v0.3.4"}}, "v0.4.0", "v0.4.0"},
		{"linked without build info", nil, "v0.4.0", "v0.4.0"},
		{"main module, tagged", &debug.BuildInfo{Main: debug.Module{Path: modulePath, Version: "v0.3.4"}}, "", "v0.3.4"},
		{"main module, go run", &debug.BuildInfo{Main: debug.Module{Path: modulePath, Version: "(devel)"}}, "", "(devel)"},
		{"main module, no version", &debug.BuildInfo{Main: debug.Module{Path: modulePath}}, "", "(devel)"},
		{"not in the binary", &debug.BuildInfo{Main: debug.Module{Path: "example.com/app", Version: "v1.0.0"}}, "", ""},
		{"dependency (go install @v)", &debug.BuildInfo{
			Main: debug.Module{Path: modulePath + "/cmd/ip2geo", Version: "v0.4.0"},
			Deps: []*debug.Module{{Path: modulePath, Version: "v0.4.0", Sum: "h1:x"}},
		}, "", "v0.4.0"},
		{"dependency of an application", &debug.BuildInfo{
			Main: debug.Module{Path: "example.com/app", Version: "(devel)"},
			Deps: []*debug.Module{{Path: modulePath, Version: "v0.4.0", Sum: "h1:x"}},
		}, "", "v0.4.0"},
		{"replaced by another version", &debug.BuildInfo{
			Main: debug.Module{Path: "example.com/app", Version: "(devel)"},
			Deps: []*debug.Module{{Path: modulePath, Version: "v0.4.0",
				Replace: &debug.Module{Path: "example.com/fork", Version: "v0.4.1", Sum: "h1:y"}}},
		}, "", "v0.4.1"},
		{"replaced by a directory, outside this repository", &debug.BuildInfo{
			Main: debug.Module{Path: "example.com/app", Version: "v1.2.3"},
			Deps: []*debug.Module{{Path: modulePath, Version: "v0.4.0", Replace: dir("../ip2geo")}},
		}, "", "(devel)"},
		{"CLI from a checkout", &debug.BuildInfo{
			Main: debug.Module{Path: modulePath + "/cmd/ip2geo", Version: "v0.4.1-0.20261201000000-0123456789ab"},
			Deps: []*debug.Module{{Path: modulePath, Version: "v0.4.0", Replace: dir("../..")}},
		}, "", "v0.4.1-0.20261201000000-0123456789ab"},
		{"CLI from a checkout, go run", &debug.BuildInfo{
			Main: debug.Module{Path: modulePath + "/cmd/ip2geo", Version: "(devel)"},
			Deps: []*debug.Module{{Path: modulePath, Version: "v0.4.0", Replace: dir("../..")}},
		}, "", "(devel)"},
		{"lookalike module path", &debug.BuildInfo{
			Main: debug.Module{Path: modulePath + "x/cmd", Version: "v9.9.9"},
			Deps: []*debug.Module{{Path: modulePath, Version: "v0.4.0", Replace: dir("../..")}},
		}, "", "(devel)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := builderVersion(tt.bi, tt.linked); got != tt.want {
				t.Errorf("builderVersion = %q, want %q", got, tt.want)
			}
		})
	}
}
