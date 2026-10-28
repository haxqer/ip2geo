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

IPv6 is slower: about 97 ns per city-profile lookup for addresses weighted by
prefix size, up to ~420–450 ns when every stored range is sampled equally
(the IPv6 index is coarser; improving it is planned).

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
curl -LO https://github.com/haxqer/ip2geo/releases/download/data-2026-07/dbip-city-lite.geoc.gz
gunzip dbip-city-lite.geoc.gz

go run github.com/haxqer/ip2geo/cmd/ip2geo@latest lookup -db dbip-city-lite.geoc 8.8.8.8
# 8.8.8.8: US / California / Mountain View  (37.4220, -122.0850)
```

These `.geoc` files load with **zero third-party dependencies** via
`ip2geo.Open`. Browse every build on the
[data releases](https://github.com/haxqer/ip2geo/releases) page.
IP geolocation by [DB-IP](https://db-ip.com), licensed under CC BY 4.0.

## Quick start (drop-in library)

Point it at the same `.mmdb` you already use with `geoip2-golang`
([free GeoLite2-City from MaxMind](https://dev.maxmind.com/geoip/geolite2-free-geolocation-data)).
`mmdb.Open` builds the fast index in memory — no intermediate file, no CLI —
but the build is slow (see [below](#convert-an-mmdb--geoc)); servers should
convert once instead.

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

`mmdb.Open` rebuilds the index in memory on every start: about 27 s and about
2.2 GiB of peak memory for the city profile (measured on a 123 MiB GeoIP2-City
source). Convert your MaxMind **or DB-IP** `.mmdb` to a compact `.geoc` **once**
with `mmdb.Build` (or the CLI), and `ip2geo.Open` then loads it in a fraction of
a second at every start, using **only the standard library** (no `maxminddb`) —
ideal for servers that restart often, containers, and CLIs.

**Command line** — install once, convert any City `.mmdb`:

```sh
go install github.com/haxqer/ip2geo/cmd/ip2geo@latest

ip2geo build -src GeoIP2-City.mmdb -out city.geoc                        # full city detail
ip2geo build -src GeoIP2-City.mmdb -out country.geoc -profile country    # smaller, country only
ip2geo build -src GeoIP2-City.mmdb -out city.geoc -latscale 2            # keep 2 coordinate decimals

ip2geo lookup -db city.geoc 81.2.69.142 2001:4860:4860::8888
```

| flag | meaning | default |
|---|---|---|
| `-src` | input MaxMind/DB-IP City `.mmdb` | (required) |
| `-out` | output `.geoc` path | `city.geoc` |
| `-profile` | `city` \| `region` \| `country` | `city` |
| `-lang` | language of names; must be one the source lists | `en` |
| `-latscale` | coordinate precision: lat/lon decimals kept by the city profile, 0..7 (0 means 4) | `4` |

**From Go** — the same conversion, programmatically:

```go
mmdb.Build("GeoIP2-City.mmdb", "city.geoc", mmdb.Options{Profile: ip2geo.ProfileCity})
```

**Load the result** anywhere, with zero third-party dependencies:

```go
db, _ := ip2geo.Open("city.geoc") // sub-second startup
rec, ok := db.Lookup(ip)
```

## Profiles: trade detail for size

Pick the coarsest profile your app needs — coarser profiles are smaller and
faster.

| profile | fields | file size¹ |
|---|---|--:|
| `country` | country ISO / name / continent | **3.5 MiB** |
| `region` | + top subdivision (state/province) | 26 MiB |
| `city` | + city, coordinates, accuracy, time zone | 116 MiB |

¹ built from a 123 MiB GeoIP2-City source. The city profile keeps 4 coordinate
decimals (about 11 m) unless `-latscale` says otherwise.

## API

```go
// build a reader
db, err := mmdb.Open(mmdbPath, opts)  // from a MaxMind .mmdb, in memory (slow, see above)
db, err := ip2geo.Open(geocPath)      // from a pre-built .geoc (zero deps)
db, err := ip2geo.OpenBytes(b)        // from a .geoc already in memory

// look up
rec, ok := db.Lookup(ip netip.Addr)   // ok == false means "not found"
rec, ok := db.City(ip)                // alias of Lookup (geoip2-golang style)
rec, ok := db.Country(ip)             // alias of Lookup

db.Profile()                          // country | region | city
db.Close()                            // a no-op today
```

- **Concurrency.** `Lookup` performs **no allocation** and the `*Reader` is safe
  for concurrent use by multiple goroutines. `Close` is a no-op: it may overlap
  in-flight lookups, and the Reader keeps answering after it.
- **Errors.** `ip2geo.Open` and `OpenBytes` validate the whole file and return
  errors wrapping `ip2geo.ErrBadMagic`, `ErrUnsupportedVersion`, `ErrCorrupt` or
  `ErrTooLarge` (over 2 GiB). `mmdb.Open` and `mmdb.Build` wrap
  `mmdb.ErrOptions`, `ErrDatabaseType`, `ErrLanguage` or `ErrStringTooLong`.
  Test for them with `errors.Is`.
- **IPv4 in IPv6.** As in MaxMind databases, `::ffff:0:0/96` (IPv4-mapped),
  `::/96` (IPv4-compatible), `2002::/16` (6to4) and `2001::/32` (Teredo) are
  looked up in the IPv4 table. NAT64 `64:ff9b::/96` is not mapped, and the zero
  `netip.Addr` is never found.
- **Found but empty.** A network that is in the source but has none of the
  chosen profile's fields (for example one with only `registered_country`)
  returns `ok == true` and an empty `Record`, as maxminddb's `Result.Found()`
  does.

The `ip2geo` reader package imports only the standard library; the
`ip2geo/mmdb` builder additionally uses `maxminddb`.

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

## License

MIT — see [LICENSE](LICENSE). This project is not affiliated with MaxMind, Inc.
GeoIP2 / GeoLite2 are trademarks of MaxMind, Inc. The sample databases on the
[releases](https://github.com/haxqer/ip2geo/releases) page are derived from the
DB-IP IP-to-City Lite dataset, © DB-IP, licensed under
[CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).
