# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- A Rust reader in `rust/` (crate `ip2geo`, not published): reads GEO2,
  revision 2.1 included, and GEO1, validates its input as the Go reader
  does, and passes the conformance suite in `testdata/conformance`. Its
  API follows the Go one (`Reader::open`, `lookup`, `lookup_details`,
  `asn`, `lookup_range`, `ranges`, `with_language`, `metadata`); CI checks
  it with rustfmt, clippy and its tests on stable and on its minimum Rust
  version, 1.81.
- A language-neutral conformance suite in `testdata/conformance`: small
  synthetic databases (every profile, the ASN profile, extension fields and
  languages, a GEO1 file) and, for each, a JSON document of the answers a
  reader must give (`Lookup`, `LookupDetails`, `ASN`, `LookupRange`,
  `Ranges`, metadata) for addresses chosen to hit range edges, the IPv6
  forms of IPv4 addresses and found-but-empty records, so that a reader in
  another language can prove it reads the format as the Go one does. Its
  README specifies the JSON. `go test` checks the Go reader against it and
  that the files match a fresh `go generate ./internal/conformance`.
- A `data-latest` release that always serves the newest monthly sample
  databases at `releases/download/data-latest/<asset>`. After each publish,
  `data.yml` copies the newest `data-YYYY-MM` release's assets to it, byte
  for byte, and moves its tag (`scripts/data-release.sh latest`). It is
  never marked as GitHub's Latest release, which stays on the `v*` code
  releases.
- A retention policy for the monthly data releases: `data.yml` keeps the
  newest 12 `data-YYYY-MM` releases, plus the month that
  `scripts/size-table.month` pins, and deletes older releases together with
  their tags (`scripts/data-release.sh prune`). The count is `DEFAULT_KEEP`
  in the script, or the repository variable `DATA_KEEP`. Only tags that match
  `data-YYYY-MM` exactly are ever deleted, and both new subcommands support
  `--dry-run`.
- `docs/DATA.md`: what the data releases contain, the DB-IP Lite CC BY 4.0
  attribution duties, checksums, how releases are built and verified,
  retention, `data-latest`, and how to build `.geoc` files from a MaxMind
  account in your own private CI. Those files must never be published.
- A container image of the CLI, `ghcr.io/haxqer/ip2geo`, for linux/amd64
  and linux/arm64 in one multi-arch manifest. The release workflow builds it
  with GoReleaser from the same static binaries as the archives, on `scratch`
  with nothing else in it (no database: mount one), running as uid 65532,
  with OCI labels for the version, commit, source and license. A `vX.Y.Z`
  tag publishes `X.Y.Z` and `X.Y`, and `latest` when it is the newest
  stable release; a pre-release publishes `X.Y.Z-rc.N` alone. CI builds the
  image on both platforms and checks a `lookup` against the conformance city
  file.
- Runnable examples, checked by `go test`, for every entry point of the
  reader (`Open`, `OpenBytes`, each `Option`, `Lookup`, `LookupDetails`,
  `ASN`, `LookupRange`, `Ranges`, `Metadata`, `WithLanguage`, `Close`, the
  error values, ...), of `csvdb` and of `mmdb`. They read the conformance
  databases, small synthetic DB-IP-layout CSVs in `csvdb/testdata` and, for
  `mmdb`, a synthetic City `.mmdb` written in code, so they show real output
  on pkg.go.dev.
- `examples/httpserver`: a small HTTP lookup service (`GET /lookup?ip=`,
  `GET /info`) that depends on the root module only and hot-reloads its
  database when the file is replaced (polling) or on `SIGHUP`, swapping an
  `atomic.Pointer[ip2geo.Reader]`. Its test proves under `-race` that no
  request fails across reloads and that every retired Reader is released.

### Changed
- `github.com/haxqer/ip2geo/mmdb` and `github.com/haxqer/ip2geo/cmd/ip2geo`
  are now modules of their own, released with the same version as the root
  module (tags `mmdb/vX.Y.Z` and `cmd/ip2geo/vX.Y.Z` next to `vX.Y.Z`). The
  `github.com/haxqer/ip2geo` module requires nothing any more: an
  application that imports only the reader (or `csvdb`) no longer has
  `maxminddb` in its `go.mod`, `go.sum` or module graph. Import paths are
  unchanged. If you import `ip2geo/mmdb`, run
  `go get github.com/haxqer/ip2geo/mmdb@vX.Y.Z` (it brings the root module
  to the same version) and keep the two on the same version from then on.
  `go install github.com/haxqer/ip2geo/cmd/ip2geo@latest` works as before.
  The file format and the API are unchanged.
- `ExampleReader_fromMMDB` is now `mmdb.ExampleOpen`, and `TestDBIPParity`
  moved from `csvdb` to the `mmdb` module, which has the `.mmdb` reader.

### Fixed
- The builder no longer stores IPv6 data that no lookup can read. A source
  network inside `::/96`, Teredo `2001::/32` or 6to4 `2002::/16`, whose
  addresses the reader looks up as the IPv4 address they embed, was written
  to the IPv6 index and never returned by `Lookup`, `LookupRange` or
  `Ranges`; an mmdb build of such a source now fails with the new
  `mmdb.ErrIPv4Alias`. MaxMind and DB-IP databases have none (they alias
  these blocks to their IPv4 data), so their builds are byte-identical to
  before; CSV builds already left these blocks out. IPv4-mapped
  `::ffff:0:0/96` networks are still built as IPv4 networks. The
  conformance `city.geoc` now covers `2002::/15` instead of storing data
  under `2002:abcd::/32`, and docs/FORMAT.md states the rule.

## [0.3.4] - 2026-11-10

### Changed
- IPv4 `LookupRange` no longer binary-searches V4IDX when a range's ends sit
  in other /16s than the address, which is the usual case on sparse files:
  a 16 KB map of the non-empty /16s, built at open, finds the neighboring
  /16 at once. On DB-IP Lite 2026-10 it is 84% faster on the country file
  (97 → 16 ns, about `Lookup`'s cost) and 52% faster on the city file
  (75 → 36 ns), with similar gains on a GeoIP2 City build (Apple M3), still
  without allocating. `Lookup` and IPv6 are unchanged; open takes one more
  allocation of 16 KB and up to 2% longer. The file format is unchanged.

## [0.3.3] - 2026-11-09

### Changed
- The IPv6 index built at open splits crowded slots again: a slot of more
  than 64 starts gets a child table over the next bits, down to bit 64 if
  need be, so on real data a lookup binary-searches a few dozen starts
  instead of thousands. On DB-IP Lite 2026-10, IPv6 `Lookup` is 29–30%
  faster and `LookupRange` 27–32% faster (Apple M3), still without
  allocating; IPv4 is unchanged. The city file's index takes 3.1 MB more
  heap (6.4 MB; 1.5 MB more for a GeoIP2 City build) and the country
  file's slightly less; building it at open takes about 2 ms longer on a
  city file and allocates nothing beyond the index. The file format is
  unchanged.

## [0.3.2] - 2026-11-09

### Changed
- `Lookup` and `LookupDetails` decode the record in place instead of copying
  it out of a helper: on DB-IP Lite 2026-10, IPv4 lookups are 25–36% faster
  and IPv6 lookups 7–28% faster (Apple M3), still without allocating.

## [0.3.1] - 2026-11-08

### Fixed
- A copy of a memory-mapped `Reader` (`c := *db`) no longer crashes once the
  original `*Reader` is unreachable: the mapping is now released only when
  every copy and every `WithLanguage` view of the Reader is unreachable.
- `ip2geo lookup` escapes every string from the database (country, region,
  city, time zone and the optional fields), so a crafted source cannot write
  terminal control sequences.
- `ip2geo lookup` reports an address that does not parse on stderr and exits
  with status 1; the other addresses are still looked up.
- `ip2geo lookup` prints only what the database's profile keeps: no
  `(0.0000, 0.0000)` coordinates for country and region databases, no
  coordinates when the location field set says the source has none, no
  trailing space without a time zone, and no empty parts in the optional
  field lines. A city record with every field prints as before.
- `ip2geo build -out` defaults to `<profile>.geoc` (for example `asn.geoc`)
  instead of always `city.geoc`.
- A city build fails on a source latitude outside [-90, 90] or longitude
  outside [-180, 180], NaN and infinities included, from `.mmdb` sources as
  already from CSV; before, the stored value depended on the architecture of
  the building machine.
- A build fails on a network `::/n` with n < 96, whose data no IPv4 lookup
  would find.
- `mmdb.Build` and `csvdb.Build` load the built database before replacing the
  output and write nothing if it does not load or is over the 2 GiB limit of
  the format; the atomic write also syncs the directory on Unix.
- Builder errors for a missing language match `errors.Is(err,
  ip2geo.ErrLanguage)`.
- The monthly data workflow checks the built files with the latest released
  CLI before publishing them.

## [0.3.0] - 2026-10-29

### Added
- Format revision 2.1 (docs/FORMAT.md): optional XFIELDS, XRECORDS and LANGS
  sections and an ASN profile. A file built without optional fields and with
  one language has none of them and is byte-for-byte a v0.2 file; v0.2
  readers ignore the new sections, but reject ASN files.
- Optional field sets, `mmdb.Options.Fields` and `ip2geo build -fields`: EU
  membership, continent name and GeoNames id, registered and represented
  country, the first subdivision's GeoNames id and the second subdivision,
  postal code, metro code, the anycast / anonymous proxy / satellite traits,
  and whether the coordinates and accuracy radius are present.
- Several languages per file, `mmdb.Options.Languages` and
  `ip2geo build -lang en,de,ja`: a name missing in a language falls back to
  the primary one. `ip2geo.Language` chooses the language at open,
  `Reader.WithLanguage` returns a view in another language, and
  `Reader.Languages` lists them; `ip2geo.ErrLanguage` reports a language the
  file does not have.
- ASN databases (GeoLite2-ASN, DB-IP ASN Lite): `ip2geo.ProfileASN`,
  `ip2geo build -profile asn` and `Reader.ASN`.
- `Reader.LookupDetails` returns a `Details`: the `Record` plus every optional
  field and the ASN. `Reader.LookupRange` returns the range of addresses that
  share an answer, and `Reader.Ranges` iterates over every covered range.
  None of them allocate except `Ranges`' iterator; `Lookup` is unchanged.
- `Reader.Fields` and `Metadata.Fields` report the file's field sets;
  `ip2geo info` prints the field sets and languages, and `ip2geo lookup`
  prints the optional fields and takes `-lang`.
- Package `csvdb` builds from CSV sources: DB-IP's IP to City, Country and
  ASN Lite CSV files and IP2Location's DB1, DB3, DB5, DB9, DB11 and ASN CSV
  files (IPv4 or IPv6, gzipped or not), with `Open` and `Build` like package
  `mmdb`. Malformed, unsorted or overlapping rows fail with `csvdb.ErrSyntax`
  and the file and line. A DB-IP CSV build answers like the build of the
  same month's .mmdb except where DB-IP's two files differ (see the package
  documentation).
- `ip2geo build -format mmdb|dbip-city|dbip-country|dbip-asn|ip2location|ip2location-asn`;
  the default comes from the `-src` name (`*.mmdb`, `dbip-*-lite*.csv[.gz]`).
- `scripts/size-table.sh` regenerates the README size table (every profile,
  field set and language option, from one DB-IP Lite month); CI checks it.
- Each monthly `data-YYYY-MM` release also carries the DB-IP Lite City,
  Country and ASN `.mmdb` files it was built from.

### Changed
- `mmdb.Options.Lang` is deprecated in favour of `Languages`; setting both is
  an error.
- The source-agnostic part of the builder moved to an internal package shared
  by `mmdb` and `csvdb`; `mmdb`'s API and output are unchanged.

## [0.2.0] - 2026-10-28

### Changed
- IPv6 lookups use a two-level jump table (the top 16 bits, then an adaptive
  number of bits inside each populated bucket) and compare range starts as two
  64-bit halves: 20–55% faster on IPv6 depending on profile and address set,
  unchanged on IPv4, still zero allocations.
- `mmdb.Open` and `mmdb.Build` decode each distinct source record once (keyed
  by its data offset), decode only the fields and the one language the profile
  keeps, and merge ranges as they stream in. A city build takes about 2 s and
  350 MiB of peak memory instead of 27 s and 2.2 GiB; the output is
  byte-identical.
- The builder (`mmdb.Build`, `mmdb.Open` and `ip2geo build`) writes the GEO2
  format only.
- `ip2geo.Open` and `OpenBytes` still read GEO1 files written by v0.1.x: they
  are validated as before and converted to the GEO2 layout in memory at open
  (about 73 ms for a city file).
- On the GEO2 layout, city-profile IPv6 lookups are a further 13% faster on
  addresses inside the source's networks (about 210 ns on GeoIP2-City) and
  30% faster weighted by range; still zero allocations.
- `ErrBadMagic`'s text is now "not an ip2geo database" (was "not a GEO1
  database"); it means the input starts with neither magic.
- `ErrTooLarge` also reports a GEO1 file whose strings cannot be renumbered
  into GEO2's string ids (more than 65,535 distinct strings in country, region
  and time zone fields, or more than 2^24−1 in all).
- `mmdb.Open` hands its freshly built buffer to the reader instead of copying
  it.

### Added
- `mmdb.Stats.DistinctRecords`, also printed as `distinct=` in the CLI's stats
  line: the number of distinct source records decoded.
- GEO2, the `.geoc` format version 2, specified in `docs/FORMAT.md`: an 8-byte
  aligned section directory with a CRC-32C per section, a metadata section,
  packed records with id widths sized to the record count, bucket-relative
  16-bit IPv4 starts, and IPv6 starts stored as their top 64 bits with an
  exception table. Built from a 2024 GeoIP2-City source, the files are
  1,819,048 bytes for country (−50.8% from GEO1's 3,699,834), 14,032,208 for
  region (−48.8% from 27,381,167) and 77,234,992 for city (−36.3% from
  121,270,878); a DB-IP City Lite city file is about 86 MB instead of 142 MB.
- `Reader.Metadata()` and the `Metadata` type: format version, profile,
  coordinate scale, build and source times, source type, language,
  description, builder, network and record counts, string and range counts.
- `ip2geo.Open` memory-maps GEO2 files on Unix and Windows and reads the
  tables in place: about 42 ms for a city file with checksums (about 35 ms
  with `SkipChecksum()`), 2.6 ms for country, and about 9.4 MB of heap for
  the city file. The mapping is released by the garbage collector once the
  Reader is unreachable. Options: `NoMmap()` reads the file into the heap
  instead, `SkipChecksum()` skips the CRC-32C pass, and `NoCopy()` makes
  `OpenBytes` use the caller's slice in place.
- `ip2geo info -db FILE` prints a database's size and metadata, one field per
  line; `ip2geo build -description TEXT` records free text in the metadata.
- `mmdb.Options.BuildTime`, for reproducible builds (byte-identical output
  from the same source and options), and `mmdb.Options.Description`.
- `mmdb.ErrTooManyStrings`, for sources whose strings do not fit GEO2's 16-bit
  and 24-bit string ids.
- CI: a `bench` workflow that runs `BenchmarkLookup` on DB-IP IP-to-City Lite
  at the previous `v*` tag and at the commit, writes the benchstat table to the
  run summary, and fails when a lookup is more than 10% slower with p < 0.05;
  and the root package's tests on `windows-latest` and `macos-latest`.

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

[Unreleased]: https://github.com/haxqer/ip2geo/compare/v0.3.4...HEAD
[0.3.4]: https://github.com/haxqer/ip2geo/compare/v0.3.3...v0.3.4
[0.3.3]: https://github.com/haxqer/ip2geo/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/haxqer/ip2geo/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/haxqer/ip2geo/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/haxqer/ip2geo/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/haxqer/ip2geo/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/haxqer/ip2geo/releases/tag/v0.1.0
