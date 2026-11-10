package build

import (
	"runtime/debug"
	"strings"
)

// version, when set at link time, is the version BuilderName reports:
//
//	go build -ldflags "-X github.com/haxqer/ip2geo/internal/build.version=v1.2.3"
//
// The release binaries set it. They are built from the cmd/ip2geo module
// against this module's directory (a replace directive), and a module
// replaced by a directory has no version in the build information.
var version string

// builderVersion returns the version of this module that bi describes, or
// "" when bi does not include it. linked is the version set at link time,
// which wins when set.
//
// When this module is replaced by a directory and the main module is
// another module of this repository (cmd/ip2geo, built from a checkout),
// the two come from the same checkout and are released in step, so the
// main module's version, which the go command derives from the checkout's
// tags and commit, stands for this module's as well.
func builderVersion(bi *debug.BuildInfo, linked string) string {
	if linked != "" {
		return linked
	}
	if bi == nil {
		return ""
	}
	mod := &bi.Main
	if mod.Path != modulePath {
		mod = nil
		for _, d := range bi.Deps {
			if d.Path == modulePath {
				mod = d
				break
			}
		}
	}
	if mod == nil {
		// Test binaries built by Go 1.25 and older list no dependencies.
		// A main module of this repository (mmdb, cmd/ip2geo) builds
		// against this module's directory, which has no version.
		if strings.HasPrefix(bi.Main.Path, modulePath+"/") {
			return "(devel)"
		}
		return ""
	}
	v := mod.Version
	if r := mod.Replace; r != nil {
		v = r.Version
		if isDirReplace(r) && strings.HasPrefix(bi.Main.Path, modulePath+"/") && isVersion(bi.Main.Version) {
			v = bi.Main.Version
		}
	}
	if v == "" {
		v = "(devel)"
	}
	return v
}

// isDirReplace reports whether r replaces a module with a directory rather
// than with another module version.
func isDirReplace(r *debug.Module) bool {
	return r.Sum == "" && (r.Version == "" || r.Version == "(devel)")
}

// isVersion reports whether v is a version the go command stamped (a tag or
// a pseudo-version), not "" or "(devel)".
func isVersion(v string) bool {
	return strings.HasPrefix(v, "v")
}
