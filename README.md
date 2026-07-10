# ip2geo

[![CI](https://github.com/haxqer/ip2geo/actions/workflows/ci.yml/badge.svg)](https://github.com/haxqer/ip2geo/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/haxqer/ip2geo.svg)](https://pkg.go.dev/github.com/haxqer/ip2geo)
[![Go Report Card](https://goreportcard.com/badge/github.com/haxqer/ip2geo)](https://goreportcard.com/report/github.com/haxqer/ip2geo)

A fast, **zero-dependency** IP-geolocation reader for Go.

You convert a MaxMind GeoIP2 / GeoLite2 **City** `.mmdb` into a compact `.geoc`
file once, then look it up **~18–57× faster** than
[`oschwald/geoip2-golang`](https://github.com/oschwald/geoip2-golang) — with the
same `netip.Addr`-in / struct-out API, IPv4 **and** IPv6.

## Speed

5,000,000 random IPv4 lookups, best of 3, Apple M3, single core:

| reader | ns / lookup | throughput | speedup |
|---|--:|--:|--:|
| `geoip2-golang` `City()` | 1736 | 0.6 M/s | 1× (baseline) |
| `geoip2-golang` `Country()` | 1086 | 0.9 M/s | 1.6× |
| **`ip2geo` `Lookup()` — city profile** | **98** | **10.2 M/s** | **≈18× faster** |
| **`ip2geo` `Lookup()` — country profile** | **31** | **32.3 M/s** | **≈56× faster** |

Same detail level (full country + city + coordinates), same input addresses.
Reproduce it yourself with [`bench/`](bench) — see below.

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
`mmdb.Open` builds the fast index in memory — no intermediate file, no CLI.

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

`mmdb.Open` rebuilds the index in memory on every start (a few seconds). Convert
your MaxMind **or DB-IP** `.mmdb` to a compact `.geoc` **once**, and `ip2geo.Open`
then loads it in a fraction of a second using **only the standard library** (no
`maxminddb`) — ideal for servers that restart often, containers, and CLIs.

**Command line** — install once, convert any City `.mmdb`:

```sh
go install github.com/haxqer/ip2geo/cmd/ip2geo@latest

ip2geo build -src GeoIP2-City.mmdb -out city.geoc                        # full city detail
ip2geo build -src GeoIP2-City.mmdb -out country.geoc -profile country    # smaller, country only
ip2geo build -src GeoIP2-City.mmdb -out city.geoc -latscale 2            # round coords (~1 km), smaller

ip2geo lookup -db city.geoc 81.2.69.142 2001:4860:4860::8888
```

| flag | meaning | default |
|---|---|---|
| `-src` | input MaxMind/DB-IP City `.mmdb` | (required) |
| `-out` | output `.geoc` path | `city.geoc` |
| `-profile` | `city` \| `region` \| `country` | `city` |
| `-lang` | language code for names | `en` |
| `-latscale` | lat/lon decimals kept (city profile) | `4` |

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
| `country` | country ISO / name / continent | **3.5 MB** |
| `region` | + top subdivision (state/province) | 26 MB |
| `city` | + city, coordinates, accuracy, time zone | 116 MB |

¹ built from a GeoIP2-City database (source `.mmdb` is 123 MB). City keeps full
coordinate precision; pass `-latscale 2` to round coordinates (~1 km) and shrink
it further.

## API

```go
// build a reader
db, err := mmdb.Open(mmdbPath, opts)  // from a MaxMind .mmdb, in memory
db, err := ip2geo.Open(geocPath)      // from a pre-built .geoc (zero deps)
db, err := ip2geo.OpenBytes(b)        // from a .geoc already in memory

// look up
rec, ok := db.Lookup(ip netip.Addr)   // ok == false means "not found"
rec, ok := db.City(ip)                // alias of Lookup (geoip2-golang style)
rec, ok := db.Country(ip)             // alias of Lookup

db.Profile()                          // country | region | city
db.Close()
```

`Lookup` performs **no allocation** and the `*Reader` is safe for concurrent use
by multiple goroutines. The `ip2geo` reader package imports only the standard
library; the `ip2geo/mmdb` builder additionally uses `maxminddb`.

## Reproduce the benchmark

```sh
cd bench
go run . -mmdb /path/to/GeoIP2-City.mmdb -city ../city.geoc -country ../country.geoc
```

The comparison against `geoip2-golang` lives in its own module so that
dependency never touches the `ip2geo` library.

## License

MIT — see [LICENSE](LICENSE). This project is not affiliated with MaxMind, Inc.
GeoIP2 / GeoLite2 are trademarks of MaxMind, Inc. The sample databases on the
[releases](https://github.com/haxqer/ip2geo/releases) page are derived from the
DB-IP IP-to-City Lite dataset, © DB-IP, licensed under
[CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).
