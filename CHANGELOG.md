# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-10-28

### Added
- Fast, zero-dependency reader for a compact IP-geolocation database. A
  city-profile lookup is a median 14.5× faster (13.2–19.3× across runs) than
  `oschwald/geoip2-golang` `City()`, and a country-profile lookup 58.4× faster
  (both ratios against `City()`).
- `mmdb.Open` — build the reader in memory directly from a MaxMind
  GeoIP2/GeoLite2 City `.mmdb` (drop-in, no intermediate file).
- `mmdb.Build` and `ip2geo.Open`/`OpenBytes` — pre-build a compact `.geoc` file
  and load it with zero third-party dependencies.
- `country`, `region`, and `city` precision profiles.
- IPv4 and IPv6 support.
- `ip2geo` command-line tool for building and querying a database.
- Sentinel errors for `errors.Is`: `ip2geo.ErrBadMagic`,
  `ip2geo.ErrUnsupportedVersion`, `ip2geo.ErrCorrupt`, `ip2geo.ErrTooLarge`,
  and `mmdb.ErrDatabaseType`, `mmdb.ErrLanguage`, `mmdb.ErrOptions`,
  `mmdb.ErrStringTooLong`.
- Fuzz targets `FuzzOpenBytes` and `FuzzLookup`, seeded with known crashers in
  `testdata/fuzz/`, run weekly for 10 minutes each.
- `BenchmarkLookup` in `mmdb`: country- and city-profile lookups of random
  IPv4, IPv4 hits and IPv6 addresses inside the source's networks, reporting
  ns/op and allocs/op; it runs when `IP2GEO_TEST_MMDB` names a City `.mmdb`,
  and in CI on DB-IP IP-to-City Lite.
- CI on Go 1.25 (the `go.mod` minimum) and the latest stable release: gofmt,
  `go vet`, golangci-lint, govulncheck, `go test -race`, a `GOARCH=386` run of
  the reader tests, and integration tests against DB-IP IP-to-City Lite.
- Release workflow: every `v*` tag builds the `ip2geo` CLI with GoReleaser for
  linux, darwin and windows on amd64 and arm64, with archives and
  `checksums.txt` attached to the GitHub Release.
- Monthly data workflow: on the 3rd of each month, builds country, region and
  city databases from that month's DB-IP IP-to-City Lite and publishes them as
  the `data-YYYY-MM` release with `ATTRIBUTION.txt` and `checksums.txt`.

### Changed
- `ip2geo.Open` reads and validates the 8-byte header before reading the rest
  of the file, and refuses files over 2 GiB (`ErrTooLarge`).
- `ip2geo.OpenBytes` also refuses inputs over 2 GiB with `ErrTooLarge`.
- `Lookup` of the zero `netip.Addr` is always not found.
- `Reader.Close` is now a no-op: it may overlap in-flight lookups, and a closed
  Reader keeps answering.
- Lookups in `::/96` (IPv4-compatible), `2002::/16` (6to4) and `2001::/32`
  (Teredo) resolve through the IPv4 table, as maxminddb's aliases do.
- Networks present in the source but with no fields in the chosen profile now
  return `ok=true` with an empty `Record` instead of not-found.
- The builder rejects non-City source databases such as ASN or ISP (a Country
  database is accepted only with the country profile; `ErrDatabaseType`), a
  language the source does not list (`ErrLanguage`), and a `LatScale` above 7
  or an unknown profile (`ErrOptions`), instead of writing an empty or wrong
  file.
- The integration tests compare found-ness and every field of each profile
  with maxminddb for random IPv4, random IPv6, IPv6 inside the source's
  networks and the alias prefixes, on MaxMind GeoIP2-City and DB-IP
  IP-to-City Lite sources; before, they compared only the country ISO code of
  random IPv4 addresses.

### Fixed
- The reader validates every header field, count, width and record id and the
  ordering of the range starts, and returns `ErrBadMagic`,
  `ErrUnsupportedVersion` or `ErrCorrupt` (wrapped with `%w`) instead of
  panicking or misreading hostile input.
- Allocation is bounded on hostile input: counts are checked against the bytes
  that remain before anything is allocated, and size arithmetic cannot wrap on
  32-bit platforms.
- `Close` no longer races in-flight lookups.
- Strings of 64 KiB or more fail the build with `ErrStringTooLong` instead of
  being truncated.
- `mmdb.Build` writes to a temporary file and renames it into place, so a
  failed build never leaves a partial output file.
- The CLI validates `-latscale` instead of truncating it to 8 bits.

[Unreleased]: https://github.com/haxqer/ip2geo/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/haxqer/ip2geo/releases/tag/v0.1.0
