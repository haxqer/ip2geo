# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Fast, zero-dependency reader for a compact IP-geolocation database, resolving
  an address roughly 18x (city) to 56x (country) faster than
  `oschwald/geoip2-golang`.
- `mmdb.Open` — build the reader in memory directly from a MaxMind
  GeoIP2/GeoLite2 City `.mmdb` (drop-in, no intermediate file).
- `mmdb.Build` and `ip2geo.Open`/`OpenBytes` — pre-build a compact `.geoc` file
  and load it with zero third-party dependencies.
- `country`, `region`, and `city` precision profiles.
- IPv4 and IPv6 support.
- `ip2geo` command-line tool for building and querying a database.
