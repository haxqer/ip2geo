# Image of the ip2geo CLI, published as ghcr.io/haxqer/ip2geo by the release
# workflow (.goreleaser.yaml, dockers_v2). It packages the binaries GoReleaser
# has already built and compiles nothing: `docker build .` on a checkout does
# not work. The build context GoReleaser assembles holds this file, LICENSE
# and one static binary per platform at linux/amd64/ip2geo and
# linux/arm64/ip2geo; the smoke-image job in ci.yml lays out the same context
# from a `go build`.
#
# scratch, not a distro or distroless base: the binary is CGO_ENABLED=0 and
# only reads and writes local files (no network, so no CA certificates; times
# are printed in UTC, so no tzdata). Nothing else is in the image, so there is
# no base image to pin, update or scan.
#
# The image holds no database: mount one, for example
#   docker run --rm -v "$PWD:/data" ghcr.io/haxqer/ip2geo lookup -db city.geoc 8.8.8.8
# The OCI labels (version, revision, source, licenses, ...) are set by
# GoReleaser, which knows the release.
FROM scratch

# Set by buildx for each platform it builds, e.g. linux/arm64.
ARG TARGETPLATFORM

COPY $TARGETPLATFORM/ip2geo /usr/local/bin/ip2geo
COPY LICENSE /usr/share/doc/ip2geo/LICENSE

# Unprivileged, by number since scratch has no /etc/passwd (65532 is the
# "nonroot" user of the distroless images). `build` writes its output next to
# -out, so on a Linux host give it your own uid to write into a bind mount:
# docker run --user "$(id -u):$(id -g)" ...
USER 65532:65532

# Relative -db, -src and -out paths resolve in the mounted directory.
WORKDIR /data

# Absolute path: scratch sets no PATH of its own.
ENTRYPOINT ["/usr/local/bin/ip2geo"]
