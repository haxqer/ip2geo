# Releasing

The repository holds three published Go modules, released together with the
same version:

| Module | Directory | Tag | Requires |
| --- | --- | --- | --- |
| `github.com/haxqer/ip2geo` | `/` | `vX.Y.Z` | nothing (standard library only) |
| `github.com/haxqer/ip2geo/mmdb` | `mmdb/` | `mmdb/vX.Y.Z` | root `vX.Y.Z`, maxminddb |
| `github.com/haxqer/ip2geo/cmd/ip2geo` | `cmd/ip2geo/` | `cmd/ip2geo/vX.Y.Z` | root and mmdb `vX.Y.Z` |

`bench/` and `scripts/sizetable/` are modules too, but are never tagged.
`scripts/modules.sh run CMD` runs a command in every module, as CI does.

The root module carries no dependency so that an application importing only
the reader never sees maxminddb. mmdb imports the root's `internal/build`
(the internal rule allows it: the paths share the `github.com/haxqer/ip2geo`
prefix), and `internal/build` has no compatibility promise. Hence the
lockstep versions, and the `mmdb-compat` CI job, which builds the newest
released mmdb against the current root: a user may upgrade the root without
upgrading mmdb.

## Local development

The nested modules build against the checkout through `replace` directives
(`mmdb/go.mod`: root `=> ../`; `cmd/ip2geo/go.mod`: root `=> ../..`, mmdb
`=> ../../mmdb`). No `go.work` is needed, and `go mod tidy` works in every
module. Consumers ignore a dependency's `replace` directives, so mmdb keeps
its replace in released versions. `go install pkg@version` refuses a module
whose `go.mod` has one, so cmd/ip2geo drops its own for the commit tagged
`cmd/ip2geo/vX.Y.Z` (step 2) and gets it back right after (step 3).

## Steps

1. **Release commit.** Add the `## [X.Y.Z] - YYYY-MM-DD` CHANGELOG section
   (the release workflow checks it), then

   ```sh
   scripts/modules.sh require vX.Y.Z   # mmdb and cmd/ip2geo require root (and mmdb) vX.Y.Z
   git commit -am "release: vX.Y.Z"
   git tag vX.Y.Z
   git tag mmdb/vX.Y.Z
   git push origin main
   git push --atomic origin vX.Y.Z mmdb/vX.Y.Z   # root tag first, both at once
   ```

   The `vX.Y.Z` tag starts `.github/workflows/release.yml`. It runs
   `scripts/modules.sh check vX.Y.Z`, requires `mmdb/vX.Y.Z` on the same
   commit, tests every module and runs GoReleaser, which builds
   `cmd/ip2geo` from this commit: its replace directives point at the
   tagged tree, so the binaries contain exactly the tagged sources, and
   `-X .../internal/build.version` names the release in the builder string
   of the files they write.

2. **CLI tag.** Once both tags are pushed:

   ```sh
   scripts/modules.sh pin vX.Y.Z      # drop cmd/ip2geo's replaces, go mod tidy (fetches vX.Y.Z)
   git commit -am "release: cmd/ip2geo vX.Y.Z"
   git tag cmd/ip2geo/vX.Y.Z
   git push origin main cmd/ip2geo/vX.Y.Z
   ```

   Check it from outside the checkout:

   ```sh
   cd "$(mktemp -d)" && GOFLAGS= GOPROXY=direct go install github.com/haxqer/ip2geo/cmd/ip2geo@vX.Y.Z
   ```

3. **Back to the checkout.**

   ```sh
   scripts/modules.sh unpin           # restore cmd/ip2geo's replaces
   git commit -am "cmd/ip2geo: build against the working tree again"
   git push origin main
   ```

The same GoReleaser run pushes the CLI's container image,
`ghcr.io/haxqer/ip2geo` (linux/amd64 + linux/arm64, from the `Dockerfile`):
tags `X.Y.Z` and `X.Y`, plus `latest` when `vX.Y.Z` is the highest stable
`v*` tag. A pre-release tag (`vX.Y.Z-rc.N`) pushes `X.Y.Z-rc.N` only and
never moves `X.Y` or `latest`. Check it after the release:

```sh
docker run --rm -v "$PWD/testdata/conformance:/data:ro" ghcr.io/haxqer/ip2geo:X.Y.Z lookup -db city.geoc 1.0.0.0
# 1.0.0.0: QT / North Testshire / Testville  (12.3456, -65.4321) Testland/Capitalia
```

**First image push only:** GHCR creates the package as private. In the
package's settings (github.com/users/haxqer/packages/container/ip2geo/settings)
change its visibility to public, and check that it is linked to this
repository (the `org.opencontainers.image.source` label does it) and that
the repository has Actions access with the Write role, so later releases
can push.

Never move or delete a pushed tag: the module proxy and checksum database
keep the first contents they see.

## Between the steps

- Until `cmd/ip2geo/vX.Y.Z` exists, `go install .../cmd/ip2geo@vX.Y.Z`
  fails (the root `vX.Y.Z` no longer contains the CLI), while `@latest`
  still installs the previous release. Do step 2 right after step 1.
- **First release with nested modules only:** no `cmd/ip2geo/v*` tag exists
  yet, so from the moment the split reaches `main` until step 2, `@latest`
  resolves the CLI to a pseudo-version of `main`, whose replace directives
  make `go install` refuse it. Merge and release back to back.
- The first `mmdb/v*` and `cmd/ip2geo/v*` versions must require a root
  version without `mmdb/` and `cmd/ip2geo/` in it (the release that
  introduces the split, or later). With an older root, the go command finds
  the packages in two modules and reports an ambiguous import.
