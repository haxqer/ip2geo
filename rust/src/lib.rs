//! A reader for the ip2geo compact IP-geolocation database format
//! (`.geoc`): GEO2, the format the ip2geo builder writes from v0.2.0 on,
//! including revision 2.1 (extension fields, languages, the ASN profile),
//! and the legacy GEO1, which is converted at open.
//!
//! The format is specified in `docs/FORMAT.md` of
//! <https://github.com/haxqer/ip2geo>; the Go package there is the
//! reference implementation, and this crate passes the repository's
//! language-neutral conformance suite (`testdata/conformance`).
//!
//! # Usage
//!
//! ```no_run
//! use std::net::IpAddr;
//!
//! let db = ip2geo::Reader::open("city.geoc")?;
//! let ip: IpAddr = "81.2.69.142".parse().unwrap();
//! if let Some(rec) = db.lookup(ip) {
//!     println!("{} {} ({}, {})", rec.country_iso, rec.city, rec.latitude, rec.longitude);
//! }
//! if let Some(d) = db.lookup_details(ip) {
//!     println!("postal code {:?}, EU: {}", d.postal_code, d.is_in_european_union);
//! }
//! let (range, found) = db.lookup_range(ip);
//! println!("{}-{} covered: {found}", range.first, range.last);
//!
//! // The same database with names in German, where the file has them.
//! let de = db.with_language("de")?;
//! # Ok::<(), ip2geo::Error>(())
//! ```
//!
//! # Files and memory
//!
//! [`Reader::open`] memory-maps a GEO2 file (with the default `mmap`
//! feature) and searches its sections in place; [`Reader::from_bytes`] does
//! the same over bytes you hand it. Only the string table is copied out, at
//! open, where it is checked to be UTF-8 once, so the `&str` fields of a
//! [`Record`] borrow from the reader and lookups allocate nothing. A GEO1
//! file is converted at open into the GEO2 layout in memory.
//!
//! A mapped file must not be modified or truncated while a reader uses it:
//! replace it by writing a new file and renaming it over the old one.
//!
//! # Validation
//!
//! Opening checks the whole input before returning a [`Reader`]: for GEO2
//! every rule of the "Reader requirements" of FORMAT.md (header, section
//! directory, section lengths, a CRC-32C per section unless
//! [`OpenOptions::skip_checksum`] is set, every string and record id, the
//! order of the range starts, the revision 2.1 sections), for GEO1 the
//! header, every count against the bytes present, the record width, every
//! record id, the order of the starts and that nothing trails the tables.
//! Any input that opens is safe to look up in: a malformed file is an
//! [`Error`], never a panic or an out-of-bounds read.
//!
//! # IPv4 in IPv6 form
//!
//! As MaxMind databases alias them, `::ffff:0:0/96`, `::/96`, 6to4
//! `2002::/16` and Teredo `2001::/32` (the server address, bits 32-63) are
//! looked up in the IPv4 table. NAT64 `64:ff9b::/96` is not mapped.

#![deny(unsafe_code)]
#![warn(missing_docs, rust_2018_idioms)]

mod crc32c;
mod error;
mod format;
mod geo1;
mod geo2;
mod ranges;
mod reader;
mod types;

pub use error::{Error, Result};
pub use ranges::Ranges;
pub use reader::{OpenOptions, Reader};
pub use types::{Asn, Country, Details, FieldSet, IpRange, Metadata, Profile, Record};
