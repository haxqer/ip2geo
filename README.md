# ip2geo

[![CI](https://github.com/haxqer/ip2geo/actions/workflows/ci.yml/badge.svg)](https://github.com/haxqer/ip2geo/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/haxqer/ip2geo.svg)](https://pkg.go.dev/github.com/haxqer/ip2geo)
[![Go Report Card](https://goreportcard.com/badge/github.com/haxqer/ip2geo)](https://goreportcard.com/report/github.com/haxqer/ip2geo)

A fast, **zero-dependency** IP-geolocation reader for Go.

You convert a MaxMind GeoIP2 / GeoLite2 **City** `.mmdb` into a compact `.geoc`
file once, then look up IPv4 **and** IPv6 addresses in it. On random IPv4 a
lookup is a median **14.5× (city profile) to 58× (country profile) faster**
than [`oschwald/geoip2-golang`](https://github.com/oschwald/geoip2-golang)
`City()`. The input is the same `netip.Addr`, but `Lookup` returns
`(Record, bool)` rather than geoip2-golang's `(*City, error)`.

## Speed

5,000,000 random IPv4 lookups, Apple M3, single core, best of 3 per run;
each figure is the median of 5 runs. The speedup column divides every row by
`geoip2-golang` `City()` (median of the 5 per-run ratios):

| reader | ns / lookup | throughput | speedup vs `City()` |
|---|--:|--:|--:|
| `geoip2-golang` `City()` | 1768 | 0.6 M/s | 1× (baseline) |
| `geoip2-golang` `Country()` | 1121 | 0.9 M/s | 1.6× |
| **`ip2geo` `Lookup()` — city profile** | **119** | **8.4 M/s** | **14.5×** (13.2–19.3× over 5 runs) |
| **`ip2geo` `Lookup()` — country profile** | **30.5** | **32.8 M/s** | **58×** |

Same input addresses for every row. The city profile returns country, top
region, city, coordinates, accuracy and time zone; `City()` decodes more
(postal code, every subdivision, registered country, all languages).
Reproduce it with [`bench/`](bench) — see below.

IPv6 lookups inside the source's networks take about 42 ns on the country
profile and about 210 ns on the city profile, where a few very dense /32
prefixes dominate the search.

## Install

```sh
go get github.com/haxqer/ip2geo
```

## Try it now — sample data, no signup

No MaxMind account yet? Grab a ready-made database built from the free,
redistributable [DB-IP IP-to-City Lite](https://db-ip.com/db/download/ip-to-city-lite)
dataset and start looking up addresses immediately:

```sh
# pick one: country (1.7 MB), region (11 MB), or city (32 MB)
curl -LO https://github.com/haxqer/ip2geo/releases/download/data-2026-10/dbip-city-lite.geoc.gz
gunzip dbip-city-lite.geoc.gz

go run github.com/haxqer/ip2geo/cmd/ip2geo@latest lookup -db dbip-city-lite.geoc 8.8.8.8
# 8.8.8.8: US / California / Mountain View  (37.4220, -122.0850)
```

These `.geoc` files load with **zero third-party dependencies** via
`ip2geo.Open`. The `data-2026-10` files are in the v0.1 format and still
load; the monthly releases from `data-2026-11` on are GEO2 files (the city
file uncompressed: about 86 MB instead of 142 MB). Browse every build on the
[data releases](https://github.com/haxqer/ip2geo/releases) page.
IP geolocation by [DB-IP](https://db-ip.com), licensed under CC BY 4.0.

## Quick start (drop-in library)

Point it at the same `.mmdb` you already use with `geoip2-golang`
([free GeoLite2-City from MaxMind](https://dev.maxmind.com/geoip/geolite2-free-geolocation-data)).
`mmdb.Open` builds the fast index in memory — no intermediate file, no CLI —
at a cost of about 2 s at startup (see [below](#convert-an-mmdb--geoc)).

```go
package main

import (
	"fmt"
	"net/netip"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/mmdb"
)

func main() {
	// Build the reader straight from a MaxMind City .mmdb (once, at startup).
	db, err := mmdb.Open("GeoIP2-City.mmdb", mmdb.Options{Profile: ip2geo.ProfileCity})
	if err != nil {
		panic(err)
	}
	defer db.Close()

	rec, ok := db.Lookup(netip.MustParseAddr("81.2.69.142"))
	if !ok {
		fmt.Println("no data")
		return
	}
	fmt.Println(rec.CountryISO)             // GB
	fmt.Println(rec.RegionName)             // England
	fmt.Println(rec.City)                   // Wimbledon
	fmt.Println(rec.Latitude, rec.Longitude) // 51.418 -0.1752
	fmt.Println(rec.TimeZone)               // Europe/London
}
```

That's the whole library. Works the same for IPv6.

## Convert an `.mmdb` → `.geoc`

`mmdb.Open` rebuilds the index in memory on every start: about 2 s and about
350 MiB of peak memory for the city profile (measured on a 123 MiB GeoIP2-City
source). Convert your MaxMind **or DB-IP** `.mmdb` to a compact `.geoc` **once**
with `mmdb.Build` (or the CLI), and `ip2geo.Open` then memory-maps it at every
start — about 42 ms for the city file, 2.6 ms for country — using **only the
standard library** (no `maxminddb`): ideal for servers that restart often,
containers, and CLIs.

The builder writes the GEO2 format only. Files written by v0.1 (GEO1) still
open: they are converted in memory, which takes about 73 ms for a city file.

**Command line** — install once, convert any City `.mmdb`:

```sh
go install github.com/haxqer/ip2geo/cmd/ip2geo@latest

ip2geo build -src GeoIP2-City.mmdb -out city.geoc                        # full city detail
ip2geo build -src GeoIP2-City.mmdb -out country.geoc -profile country    # smaller, country only
ip2geo build -src GeoIP2-City.mmdb -out city.geoc -latscale 2            # keep 2 coordinate decimals
ip2geo build -src GeoIP2-City.mmdb -out city.geoc -description "GeoIP2-City 2026-10-27"
ip2geo build -src GeoIP2-City.mmdb -out city.geoc -fields postal,registered  # optional fields
ip2geo build -src GeoIP2-City.mmdb -out city.geoc -lang en,de,ja         # names in 3 languages
ip2geo build -src GeoLite2-ASN.mmdb -out asn.geoc -profile asn           # ASN database
ip2geo build -src dbip-city-lite-2026-10.csv.gz -out city.geoc           # from a CSV

ip2geo lookup -db city.geoc 81.2.69.142 2001:4860:4860::8888
ip2geo lookup -db city.geoc -lang de 81.2.69.142   # names in another language of the file
ip2geo info   -db city.geoc    # size, format, profile, source, build time, description, counts
```

| flag | meaning | default |
|---|---|---|
| `-src` | input MaxMind/DB-IP `.mmdb`, or a DB-IP / IP2Location CSV (optionally `.gz`) | (required) |
| `-format` | `mmdb` \| `dbip-city` \| `dbip-country` \| `dbip-asn` \| `ip2location` \| `ip2location-asn` | from the `-src` name |
| `-out` | output `.geoc` path | `city.geoc` |
| `-profile` | `city` \| `region` \| `country` \| `asn` | `city` |
| `-fields` | optional field sets, comma-separated: `eu`, `continent`, `registered`, `subdivisions`, `postal`, `metro`, `traits`, `location`, or `all` | none |
| `-lang` | languages of names, comma-separated, primary first; each must be one the source lists | `en` |
| `-latscale` | coordinate precision: lat/lon decimals kept by the city profile, 0..7 (0 means 4) | `4` |
| `-description` | free text stored in the file's metadata, shown by `ip2geo info` | empty |

**From Go** — the same conversion, programmatically:

```go
mmdb.Build("GeoIP2-City.mmdb", "city.geoc", mmdb.Options{Profile: ip2geo.ProfileCity})
```

Optional field sets and extra languages make the file larger (the table below
has the numbers); without them the file is byte-for-byte what v0.2 wrote. CSV
sources go through package `csvdb` the same way (`csvdb.Build`, `csvdb.Open`);
a DB-IP CSV build answers like the same month's `.mmdb` build, apart from a
few places where DB-IP's two files differ (see the package documentation).

**Load the result** anywhere, with zero third-party dependencies:

```go
db, _ := ip2geo.Open("city.geoc") // memory-mapped: about 42 ms for the city file
rec, ok := db.Lookup(ip)
```

## Profiles: trade detail for size

Pick the coarsest profile your app needs — coarser profiles are smaller and
faster.

| profile | fields | file size¹ |
|---|---|--:|
| `country` | country ISO / name / continent | **1.7 MiB** |
| `region` | + top subdivision (state/province) | 13.4 MiB |
| `city` | + city, coordinates, accuracy, time zone | 73.7 MiB |

¹ built from a 123 MiB GeoIP2-City source; v0.1 files were 3.5 / 26 / 116 MiB.
The city profile keeps 4 coordinate decimals (about 11 m) unless `-latscale`
says otherwise.

<!-- size-table:begin -->
Each profile, field set and language option built from one free source,
DB-IP Lite 2026-10 (City, Country and ASN editions, CC BY 4.0), by
`scripts/size-table.sh`; ranges are the IPv4 and IPv6 index boundaries:

| source | `ip2geo build` flags | size | records | IPv4 ranges | IPv6 ranges |
|---|---|--:|--:|--:|--:|
| City Lite | `-profile country` | 4.22 MiB | 252 | 341,345 | 347,185 |
| City Lite | `-profile region` | 29.49 MiB | 3,913 | 2,101,387 | 2,209,952 |
| City Lite | `-profile city` | 81.93 MiB | 560,896 | 3,619,640 | 4,166,177 |
| City Lite | `-profile city -fields eu` | 82.46 MiB | 560,896 | 3,619,640 | 4,166,177 |
| City Lite | `-profile city -fields continent` | 85.67 MiB | 560,896 | 3,619,640 | 4,166,177 |
| City Lite | `-profile city -fields registered` | 94.77 MiB | 560,896 | 3,619,640 | 4,166,177 |
| City Lite | `-profile city -fields subdivisions` | 89.42 MiB | 560,896 | 3,619,640 | 4,166,177 |
| City Lite | `-profile city -fields postal` | 83.53 MiB | 560,896 | 3,619,640 | 4,166,177 |
| City Lite | `-profile city -fields metro` | 83.00 MiB | 560,896 | 3,619,640 | 4,166,177 |
| City Lite | `-profile city -fields traits` | 82.46 MiB | 560,896 | 3,619,640 | 4,166,177 |
| City Lite | `-profile city -fields location` | 82.46 MiB | 560,896 | 3,619,640 | 4,166,177 |
| City Lite | `-profile city -fields all` | 110.28 MiB | 560,896 | 3,619,640 | 4,166,177 |
| City Lite | `-profile city -lang en,de,fr` | 91.56 MiB | 560,896 | 3,619,640 | 4,166,177 |
| City Lite | `-profile city -fields all -lang en,de,fr` | 132.75 MiB | 560,896 | 3,619,640 | 4,166,177 |
| Country Lite | `-profile country` | 4.29 MiB | 252 | 362,122 | 348,697 |
| ASN Lite | `-profile asn` | 6.47 MiB | 82,136 | 483,069 | 128,784 |
<!-- size-table:end -->

The `.geoc` format is specified in [`docs/FORMAT.md`](docs/FORMAT.md).

## API

```go
// build a reader
db, err := mmdb.Open(mmdbPath, opts)                    // from a MaxMind .mmdb, in memory (about 2 s)
db, err := csvdb.Open(csvPath, opts)                    // from a DB-IP or IP2Location CSV, in memory
db, err := ip2geo.Open(geocPath)                        // from a pre-built .geoc, memory-mapped (zero deps)
db, err := ip2geo.Open(geocPath, ip2geo.NoMmap())       // read into the heap instead
db, err := ip2geo.Open(geocPath, ip2geo.SkipChecksum()) // skip the CRC-32C pass at open
db, err := ip2geo.OpenBytes(b)                          // from a .geoc already in memory (copied)
db, err := ip2geo.OpenBytes(b, ip2geo.NoCopy())         // used in place: b must not change afterwards
db, err := ip2geo.Open(geocPath, ip2geo.Language("de")) // names in one of the file's languages

// look up
rec, ok := db.Lookup(ip netip.Addr)   // ok == false means "not found"
rec, ok := db.City(ip)                // alias of Lookup (geoip2-golang style)
rec, ok := db.Country(ip)             // alias of Lookup
d, ok := db.LookupDetails(ip)         // Record plus the optional field sets and the ASN
as, ok := db.ASN(ip)                  // autonomous system number and organization (asn profile)
rng, ok := db.LookupRange(ip)         // the first and last address that share ip's answer
for rng, d := range db.Ranges() { … } // every covered range in address order

de, err := db.WithLanguage("de")      // a view of the same database with German names
db.Languages()                        // the file's languages, primary first
db.Fields()                           // the optional field sets the file holds
db.Profile()                          // country | region | city | asn
db.Metadata()                         // format, source, build time, description, counts (CLI: ip2geo info)
db.Close()                            // a no-op today
```

- **Concurrency.** `Lookup` performs **no allocation** and the `*Reader` is safe
  for concurrent use by multiple goroutines. `Close` is a no-op: it may overlap
  in-flight lookups, and the Reader keeps answering after it.
- **Memory.** A GEO2 file stays mapped; for the city file `Open` adds about
  9.4 MB of heap (indexes and strings). The garbage collector unmaps the file
  once the Reader is unreachable. Never rewrite a mapped file in place: write a
  new one and rename it over the old, as `mmdb.Build` does.
- **Errors.** `ip2geo.Open` and `OpenBytes` validate the whole file, including
  a CRC-32C per section, and return errors wrapping `ip2geo.ErrBadMagic`,
  `ErrUnsupportedVersion`, `ErrCorrupt` or `ErrTooLarge` (over 2 GiB).
  `mmdb.Open` and `mmdb.Build` wrap `mmdb.ErrOptions`, `ErrDatabaseType`,
  `ErrLanguage`, `ErrStringTooLong` or `ErrTooManyStrings`; `csvdb` adds
  `csvdb.ErrSyntax` with the file and line of a malformed row. `Language` and
  `WithLanguage` wrap `ip2geo.ErrLanguage`. Test for them with `errors.Is`.
- **Languages.** A file built with `-lang en,de` stores every name in each
  language. A name the source lacks in the chosen language falls back to the
  primary one (the first of `-lang`).
- **IPv4 in IPv6.** As in MaxMind databases, `::ffff:0:0/96` (IPv4-mapped),
  `::/96` (IPv4-compatible), `2002::/16` (6to4) and `2001::/32` (Teredo) are
  looked up in the IPv4 table. NAT64 `64:ff9b::/96` is not mapped, and the zero
  `netip.Addr` is never found.
- **Found but empty.** A network that is in the source but has none of the
  chosen profile's fields (for example one with only `registered_country`)
  returns `ok == true` and an empty `Record`, as maxminddb's `Result.Found()`
  does. Built with `-fields registered`, it carries its registered country.

The `ip2geo` reader package and the `ip2geo/csvdb` builder import only the
standard library; the `ip2geo/mmdb` builder additionally uses `maxminddb`.

## Reproduce the benchmark

```sh
cd bench
go run . -mmdb /path/to/GeoIP2-City.mmdb -city ../city.geoc -country ../country.geoc
```

The comparison against `geoip2-golang` lives in its own module so that
dependency never touches the `ip2geo` library.

For IPv4 and IPv6 lookups on your own source, `BenchmarkLookup` builds the
readers in memory and reports ns/op and allocs/op:

```sh
IP2GEO_TEST_MMDB=/path/to/City.mmdb go test -run '^$' -bench BenchmarkLookup -benchtime=2s ./mmdb/
```

CI runs it on DB-IP IP-to-City Lite for pull requests and pushes to `main`,
also at the previous release, and fails when a lookup is more than 10% slower
than there (p < 0.05, benchstat).

## License

MIT — see [LICENSE](LICENSE). This project is not affiliated with MaxMind, Inc.
GeoIP2 / GeoLite2 are trademarks of MaxMind, Inc. The sample databases on the
[releases](https://github.com/haxqer/ip2geo/releases) page are derived from the
DB-IP IP-to-City Lite dataset, © DB-IP, licensed under
[CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).
