# The `.geoc` file format

This document specifies the bytes of an ip2geo database file. Version 2
("GEO2") is the format the `mmdb` builder writes from v0.2.0 on. Version 1
("GEO1", written by v0.1.x) is kept readable and is described at the end.

All integers are little-endian unless stated otherwise. "u8/u16/u24/u32/u64"
are unsigned integers of that many bits, "i32" is two's-complement signed.
Offsets are from the start of the file.

## Design summary

- One flat file, read in place: the reader memory-maps (or retains) the file
  and uses the arrays directly. Only the strings are copied to the heap, so a
  `Record` never points into the mapping.
- Every section is 8-byte aligned, has a CRC-32C, and is located through a
  directory, so a truncated or corrupt file is rejected before any lookup.
- Sizes measured on a 2024 GeoIP2-City source and on DB-IP City Lite 2026-10
  (Apple M3, v0.1.0 GEO1 as the baseline):

  | file | GEO1 | GEO2 (this spec) |
  |---|--:|--:|
  | GeoIP2-City, city profile | 121.3 MB | about 77 MB (−36%) |
  | GeoIP2-City, country profile | 3.7 MB | about 1.8 MB (−51%) |
  | DB-IP City Lite, city profile | 142.0 MB | about 86 MB (−40%) |

  The savings come from four measured changes: IPv6 starts stored as their
  top 64 bits (0.1–0.6% of boundaries need the low half, kept in an exception
  table), bucket-relative 16-bit IPv4 starts, packed records, and record ids
  as narrow as the record count allows.

## Layout

```
offset 0      Header, 32 bytes
offset 32     Directory, sectionCount × 24 bytes
then          Sections, each starting at a multiple of 8, zero-padded
```

### Header (32 bytes)

| offset | type | field | rule |
|--:|---|---|---|
| 0 | 4 bytes | magic | `"GEO2"` |
| 4 | u8 | version | 2 |
| 5 | u8 | profile | 0 country, 1 region, 2 city, 3 ASN (revision 2.1) |
| 6 | u8 | latScale | 0..7; coordinates are `int32(round(deg × 10^latScale))` |
| 7 | u8 | idWidth | bytes per record id in V4IDS and V6IDS: 1, 2, 3 or 4 |
| 8 | u32 | sectionCount | number of directory entries |
| 12 | 20 bytes | reserved | all zero |

### Directory entry (24 bytes each)

| offset | type | field | rule |
|--:|---|---|---|
| 0 | u32 | id | section id (table below) |
| 4 | u32 | crc | CRC-32C (Castagnoli polynomial) of the section's `length` bytes |
| 8 | u64 | offset | multiple of 8; `offset + length <= file size` |
| 16 | u64 | length | bytes, excluding padding |

Sections must appear in ascending `offset` order, must not overlap, and must
start after the directory; gaps between sections and bytes after the last
section are allowed and ignored. Every id in the table below is required
exactly once, except V6EXCIDX/V6EXCLO, which may be absent when there are no
exceptions (equivalent to length 0). A reader ignores the contents of entries
whose id it does not know (such an id may repeat), but still applies the
position and CRC checks to them; this is how later minor revisions may add
sections without a version bump.

### Sections

| id | name | contents |
|--:|---|---|
| 1 | META | fixed 64-byte block, below |
| 2 | STRIDX | `u32[nStrings + 1]` byte offsets into STRBLOB |
| 3 | STRBLOB | UTF-8 bytes of all strings, concatenated |
| 4 | RECORDS | `nRecords × recordWidth(profile)` bytes |
| 5 | V4IDX | `u32[65537]`, the IPv4 top-16-bit index |
| 6 | V4STARTS | `u16[nV4]`, low 16 bits of each IPv4 boundary start |
| 7 | V4IDS | `nV4 × idWidth` bytes, record id per IPv4 boundary |
| 8 | V6STARTS | `u64[nV6]`, top 64 bits of each IPv6 boundary start |
| 9 | V6IDS | `nV6 × idWidth` bytes, record id per IPv6 boundary |
| 10 | V6EXCIDX | `u32[nExc]`, indices into V6STARTS whose low 64 bits are non-zero |
| 11 | V6EXCLO | `u64[nExc]`, those low 64 bits, same order |
| 12 | XFIELDS | revision 2.1, optional: `u32` field set bits, `u32` reserved (0) |
| 13 | XRECORDS | revision 2.1, optional: `nRecords × xWidth` bytes, extension fields per record |
| 14 | LANGS | revision 2.1, optional: `u32[nLangs]` string ids of the name languages |

Counts are derived from section lengths: `nStrings = len(STRIDX)/4 − 1`,
`nRecords = len(RECORDS)/recordWidth`, `nV4 = len(V4STARTS)/2`,
`nV6 = len(V6STARTS)/8`, `nExc = len(V6EXCIDX)/4`. Lengths must divide
exactly, `len(V4IDS) == nV4 × idWidth` and `len(V6IDS) == nV6 × idWidth`,
`len(V6EXCLO) == nExc × 8`, and `len(V4IDX) == 65537 × 4`.

#### META (64 bytes)

| offset | type | field |
|--:|---|---|
| 0 | u64 | buildTime: Unix seconds (UTC) when the file was built |
| 8 | u64 | sourceTime: build epoch of the source database, 0 if unknown |
| 16 | u64 | networks: networks read from the source |
| 24 | u32 | sourceType: string id, e.g. `GeoIP2-City`, `DBIP-City-Lite` |
| 28 | u32 | language: string id, e.g. `en` |
| 32 | u32 | description: string id, free text, may be the empty string |
| 36 | u32 | builder: string id, e.g. `ip2geo/v0.2.0` |
| 40 | u32 | distinctRecords: distinct source data records seen (informational) |
| 44 | 20 bytes | reserved, all zero |

#### Strings

String `i` is `STRBLOB[STRIDX[i] : STRIDX[i+1]]`. `STRIDX[0] == 0`, the
offsets are non-decreasing, and `STRIDX[nStrings] == len(STRBLOB)`. String 0
is the empty string, so `STRIDX[1] == 0`. Every string referenced by a u16 record field must have
an id below 65536; the builder therefore assigns ids to all strings used by
u16 fields before any city name. Any string id in a record or in META must
be `< nStrings`.

#### Records

Record `i` occupies bytes `[i × width, (i+1) × width)` of RECORDS. Record 0
is all zero and is the "no data" sentinel: a boundary whose id is 0 means
the address is not covered. A network that exists in the source but has
none of the profile's fields gets a non-zero id whose record happens to be
all zero, so it is reported as found (parity with maxminddb's `Found()`).

| profile | width | fields, in order |
|---|--:|---|
| country | 10 | u16 countryISO, u16 countryName, u16 continent, u32 countryGeoNameID |
| region | 14 | country fields, then u16 regionISO, u16 regionName |
| city | 33 | region fields, then u24 cityName, u32 cityGeoNameID, i32 latitude, i32 longitude, u16 accuracyRadius, u16 timeZone |
| ASN (2.1) | 7 | u32 number, u24 organization |

`countryISO`, `countryName`, `continent`, `regionISO`, `regionName`,
`cityName` and `timeZone` are string ids. Latitude and longitude are
`int32(round(degrees × 10^latScale))`; a missing coordinate is stored as 0.
Records are not aligned; readers use unaligned little-endian loads.

#### IPv4 boundaries

The IPv4 space is a sorted list of boundary starts; the record of address
`A` is the id of the last boundary whose absolute start is `<= A`. Boundary
`i` has absolute start `(g << 16) | V4STARTS[i]` where `g` is its bucket,
the unique value with `V4IDX[g] <= i < V4IDX[g+1]`.

- `V4IDX[g]` is the index of the first boundary whose absolute start is
  `>= g << 16`; `V4IDX[0] == 0` and `V4IDX[65536] == nV4`. Non-decreasing.
- Absolute starts are strictly ascending (so within a bucket the u16 values
  are strictly ascending).
- No boundary is synthesised at bucket bases. To look up `A` with
  `g = A >> 16`: binary-search `[V4IDX[g], V4IDX[g+1])` for the last `i` with
  `V4STARTS[i] <= A & 0xffff`; if none, `i = V4IDX[g] − 1`, and if that is
  `−1` the address is not covered. The id is `V4IDS[i]`.

A reader may build a finer in-memory index (the Go reader builds a top-20
table at open, as GEO1 did) but the file only stores the top-16 one.

#### IPv6 boundaries

Boundary `i` has start `hi = V6STARTS[i]` (bits 0–63 of the address, most
significant first, as one integer) and `lo = 0`, unless `i` appears in
V6EXCIDX, in which case `lo` is the matching V6EXCLO entry. The 128-bit
starts are strictly ascending; `V6STARTS` is therefore non-decreasing and a
run of equal `hi` values is ordered by `lo`. V6EXCIDX is strictly ascending.

To look up an address with halves `(kh, kl)`: binary-search for the last `i`
with `V6STARTS[i] <= kh`. While `i >= 0`, `V6STARTS[i] == kh` and
`lo(i) > kl`, decrement `i`. If `i < 0` the address is not covered,
otherwise the id is `V6IDS[i]`. Because at most one boundary of a run has
`lo == 0`, the step-back loop is bounded by the run length (a few entries).

#### Record ids

`V4IDS` and `V6IDS` hold `idWidth`-byte little-endian ids. `idWidth` must be
the smallest of 1, 2, 3, 4 that can hold `nRecords − 1`, and every id must be
`< nRecords`. The sections may be padded to a multiple of 8 after `length`
bytes; the padding is not covered by the CRC.

### IPv4 addresses in IPv6 form

The file stores IPv4 data only in the IPv4 tables. Readers map
`::ffff:0:0/96` (IPv4-mapped), `::/96` (IPv4-compatible), `2002::/16` (6to4,
the IPv4 address is bits 16–47) and `2001::/32` (Teredo server, bits 32–63)
to the embedded IPv4 address before searching, which is what MaxMind
databases do through their alias pointers. `64:ff9b::/96` (NAT64) is not
mapped. The zero address is never found.

## Reader requirements

A conforming reader rejects a file, without reading past its end and without
allocating in proportion to any count before that count is checked against
the file size, when any of the following fails:

1. magic, version, profile ∈ {0,1,2}, latScale ≤ 7, idWidth ∈ {1,2,3,4},
   reserved bytes zero;
2. the directory fits in the file; offsets are multiples of 8, sections start
   after the directory, are in ascending order, do not overlap, and end within
   the file (unknown ids included);
3. every required section is present exactly once and has a valid length;
4. every section's CRC-32C matches (a reader may offer an option to skip this);
5. STRIDX is non-decreasing from 0 to `len(STRBLOB)` with string 0 empty;
   META string ids and all record string ids are `< nStrings`, and ids in u16
   fields are implicitly so;
6. `nRecords >= 1`, record 0 is all zero, `idWidth` is minimal for
   `nRecords`, and every id in V4IDS/V6IDS is `< nRecords`;
7. V4IDX is non-decreasing with `V4IDX[0] == 0` and `V4IDX[65536] == nV4`,
   and absolute IPv4 starts are strictly ascending;
8. V6STARTS is non-decreasing, V6EXCIDX strictly ascending with every entry
   `< nV6` and a non-zero V6EXCLO, and the 128-bit starts strictly ascending.

Lookups never allocate and never return memory that aliases the file: the
reader copies STRBLOB to its heap at open and builds its string table over
that copy.

## Revision 2.1: extension fields, languages, ASN (ip2geo v0.3)

Revision 2.1 adds three optional sections and the ASN profile. The version
byte stays 2. A file built with no extension field set and one language
has none of the new sections and is byte-for-byte a revision 2.0 file, so
the default builds keep their v0.2 sizes. A revision 2.0 reader ignores
XFIELDS, XRECORDS and LANGS as unknown sections and reads the base records
correctly; it rejects profile 3.

### Extension records

XFIELDS (8 bytes) is a `u32` of field set bits and a `u32` reserved, zero.
XRECORDS holds one entry of `xWidth` bytes per record, indexed by record
id like RECORDS, so `len(XRECORDS) == nRecords × xWidth`. Entry 0 is all
zero. Two records whose base fields are equal but whose extension fields
differ are distinct records, so RECORDS may hold duplicate entries.

An entry is the blocks of the field sets present, in bit order, followed
by the names of the extra languages (below). All string ids in XRECORDS
are u24, geoname ids u32, metro code u16; flag bits not listed must be 0.

| bit | set | width | block, offsets in bytes |
|--:|---|--:|---|
| 0 | EU | 1 | u8 flags @0: bit 0 `country.is_in_european_union` |
| 1 | continent | 7 | u24 continent name @0, u32 continent geoname id @3 |
| 2 | registered | 24 | registered_country: u24 ISO @0, u24 name @3, u32 geoname id @6; represented_country: u24 ISO @10, u24 name @13, u32 geoname id @16, u24 type @20; u8 flags @23: bit 0 registered EU, bit 1 represented EU |
| 3 | subdivisions | 14 | u32 geoname id of the first subdivision @0; second subdivision: u24 ISO @4, u24 name @7, u32 geoname id @10 |
| 4 | postal | 3 | u24 postal code @0 |
| 5 | metro | 2 | u16 `location.metro_code` @0 |
| 6 | traits | 1 | u8 flags @0: bit 0 `is_anycast`, bit 1 `is_anonymous_proxy`, bit 2 `is_satellite_provider` |
| 7 | location | 1 | u8 flags @0: bit 0 latitude and longitude present, bit 1 accuracy radius present |

Allowed sets by profile: every set except subdivisions, postal, metro and
location for country; every set except postal, metro and location for
region; all for city; none for ASN. The first subdivision's ISO code and
name are the base record's regionISO and regionName. A source with more
than two subdivisions keeps the first two.

### Languages

LANGS lists `nLangs >= 2` string ids, each a distinct non-empty string.
`LANGS[0]` is the primary language and equals META.language; the base
record and the extension blocks hold names in it. A file with one language
has no LANGS section.

For each extra language `j = 1 .. nLangs−1`, in order, an XRECORDS entry
ends with one u24 string id per name kind the file stores, in this order:
country name, region name (profile ≥ region), city name (city),
continent name (continent set), second subdivision name (subdivisions
set), registered country name and represented country name (registered
set). The block of language `j` starts at
`xWidthBlocks + (j−1) × 3 × kinds`, where `xWidthBlocks` is the sum of the
set widths. Id 0 (the empty string) means the source has no name in that
language; a reader then returns the primary language's name (fallback).

XFIELDS and XRECORDS are both present or both absent. They are present
when the field set bits are non-zero or LANGS is present (the bits may be
0 when only languages are stored).

### ASN records

Profile 3 records are 7 bytes: u32 autonomous system number, u24
organization string id. Record 0 is all zero (not covered). An ASN file has
latScale 0 and no XFIELDS, XRECORDS or LANGS.

### Reader requirements (revision 2.1)

In addition to rules 1–8, a revision 2.1 reader rejects a file when:

9. XFIELDS is not 8 bytes, its reserved word is non-zero, or it has a bit
   outside 0–7 (reported as an unsupported version: a newer field set);
   or its bits are not allowed for the profile;
10. exactly one of XFIELDS and XRECORDS is present, or LANGS is present
    without them, or any of the three appears in an ASN file, or one of
    them appears more than once;
11. `len(XRECORDS) != nRecords × xWidth`, entry 0 is not all zero, a string
    id is `>= nStrings`, or an undefined flag bit is set;
12. LANGS has fewer than 2 entries, an id `>= nStrings`, an empty or
    repeated language, or `LANGS[0] != META.language`;
13. for an ASN file: `nRecords` and ids as in rule 6, and every
    organization id `< nStrings`.

## Metadata exposed by the Go reader

`Reader.Metadata()` returns the header and META fields (format version,
profile, latScale, build time, source time, source type, language,
description, builder, networks, record/string/boundary counts). For a GEO1
file the fields the file does not carry are zero.

## GEO1 (legacy, v0.1.x)

A GEO1 file is read by transcoding it at open into the in-memory layout
described above (its strings are renumbered so that non-city strings fit
u16 ids, its records are packed, its IPv4 starts split into bucket + u16,
and its IPv6 starts split into hi/lo with an exception table). Its layout:

```
"GEO1" u8 version=1  u8 profile  u8 latScale  u8 reserved=0
u32 nStrings; nStrings × (u16 length, bytes)
u32 nRecords; u16 recordWidth; nRecords × recordWidth bytes
    record fields are all u32: countryISO, countryName, continent,
    countryGeoNameID [, regionISO, regionName] [, cityName, cityGeoNameID,
    lat, lon, accuracy, timeZone]  (widths 16 / 24 / 48)
u32 nV4; u32 starts[nV4]; u32 ids[nV4]
u32 nV6; 16-byte big-endian starts[nV6]; u32 ids[nV6]
```

GEO1 has no checksum, no metadata and no section directory; its validation
rules are the Phase 1 ones (counts against remaining bytes, record width
equal to the profile's, ids below the record count, strictly ascending
starts, no trailing bytes).
