# ip2geo for Rust

A Rust reader for the ip2geo compact IP-geolocation database format
(`.geoc`), the files the Go `ip2geo` builder writes. It reads:

- **GEO2** (format version 2), including revision 2.1: extension field sets,
  names in several languages, and the ASN profile;
- **GEO1** (the v0.1.x format), converted at open into the GEO2 layout, as
  the Go reader does.

## Status

The crate passes the repository's language-neutral conformance suite,
[`testdata/conformance`](../testdata/conformance) (every database, query,
language and range, coordinates compared bit for bit), and validates its
input the way the Go reader does: a truncated or corrupt file is an error,
never a panic or an out-of-bounds read. It is not published on crates.io;
use it as a git or path dependency:

```toml
[dependencies]
ip2geo = { git = "https://github.com/haxqer/ip2geo" }
```

The minimum supported Rust version is 1.81 (`rust-version` in
`Cargo.toml`); CI tests it and the latest stable release.

## Usage

```rust
use std::net::IpAddr;

let db = ip2geo::Reader::open("city.geoc")?;
let ip: IpAddr = "81.2.69.142".parse().unwrap();

if let Some(rec) = db.lookup(ip) {
    println!("{} {} {} ({}, {})",
        rec.country_iso, rec.region_name, rec.city, rec.latitude, rec.longitude);
}

// Every optional field the database holds (see db.fields()).
if let Some(d) = db.lookup_details(ip) {
    println!("EU: {}, postal code: {}", d.is_in_european_union, d.postal_code);
}

// The range of addresses that share the answer.
let (range, found) = db.lookup_range(ip);
println!("{}-{} covered: {found}", range.first, range.last);

// The names in another language of the file; missing names fall back to
// the primary language.
let de = db.with_language("de")?;

// An ASN database.
let asn_db = ip2geo::Reader::open("asn.geoc")?;
if let Some(a) = asn_db.asn(ip) {
    println!("AS{} {}", a.number, a.organization);
}

// Every covered range with its details.
for (range, d) in db.ranges().take(10) {
    println!("{}-{} {}", range.first, range.last, d.record.country_iso);
}
# Ok::<(), ip2geo::Error>(())
```

`OpenOptions` selects the language at open, skips the CRC-32C check
(`skip_checksum`) or reads the file into memory instead of mapping it
(`mmap(false)`). `Reader::from_bytes` opens a database already in memory
(a `Vec<u8>`, a `&'static [u8]` from `include_bytes!`, an `Arc<[u8]>`, ...).

| Go | Rust |
|---|---|
| `ip2geo.Open(path, opts...)` | `Reader::open(path)`, `OpenOptions::new()...open(path)` |
| `ip2geo.OpenBytes(b, opts...)` | `Reader::from_bytes(b)`, `OpenOptions::new()...from_bytes(b)` |
| `Language`, `SkipChecksum`, `NoMmap` | `OpenOptions::language`, `skip_checksum`, `mmap(false)` |
| `Reader.Lookup` / `LookupDetails` / `ASN` | `lookup` / `lookup_details` / `asn`, returning `Option` |
| `Reader.LookupRange` | `lookup_range`, returning `(IpRange, bool)` |
| `Reader.Ranges` | `ranges()`, an `Iterator<Item = (IpRange, Details)>` |
| `Reader.WithLanguage` | `with_language` |
| `Reader.Metadata` / `Languages` / `Fields` / `Profile` | `metadata` / `languages` / `fields` / `profile` |
| `ErrBadMagic`, `ErrCorrupt`, ... | `Error::BadMagic`, `Error::Corrupt`, ... |

### Strings and memory

A GEO2 file is memory-mapped (the `mmap` feature, on by default, uses
`memmap2`) and its lookup tables are searched in place. The string table is
copied out once at open, like the Go reader does, and checked to be UTF-8
there, so a `Record<'_>` holds `&str`s that borrow from the `Reader`:
lookups allocate nothing and validate nothing. A `Reader` is
`Clone + Send + Sync` and shares the database through an `Arc`. A string
that is not valid UTF-8 (the format says strings are UTF-8 but no reader
rejects one that is not) is returned with U+FFFD in place of the invalid
bytes; the Go reader returns the bytes as they are.

A mapped file must not be modified or truncated while a reader uses it;
replace it by writing a new file and renaming it over the old one, as the
ip2geo builder does. `mmap(false)` reads the file into memory instead.

## Relation to the Go reader

The Go package at the repository root is the reference implementation, and
[`docs/FORMAT.md`](../docs/FORMAT.md) the specification. This crate follows
the Go reader's validation rules, error kinds and answers, which the
conformance suite pins down; the in-memory indexes differ (a top-20 IPv4
table as in Go, a flat top-16 IPv6 table instead of Go's split tree) but
give the same answers.

## Development

```sh
cargo fmt --check
cargo clippy --all-targets -- -D warnings
cargo test
# a rough timing of lookups on a file
cargo run --release --example bench -- ../testdata/conformance/city.geoc
```

`tests/conformance.rs` runs the suite and fails on any file in
`testdata/conformance` it does not understand. `tests/robustness.rs`
truncates and flips bytes of the suite's databases and checks that nothing
panics; `IP2GEO_FUZZ_ITERS` sets the number of mutants per file (400 by
default).

## License

MIT, as the rest of the repository: see [LICENSE](../LICENSE).
