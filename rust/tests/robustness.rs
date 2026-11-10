//! Malformed input must give an error, never a panic or an out-of-bounds
//! read: the conformance databases are truncated and have bytes flipped,
//! and every mutant that still opens is looked up in, iterated and checked
//! for basic consistency. Run in the test profile, so arithmetic overflow
//! panics too.
//!
//! `IP2GEO_FUZZ_ITERS` scales the number of random mutants per file
//! (default 400).

use std::fs;
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::path::{Path, PathBuf};

use ip2geo::{Error, OpenOptions, Reader};

fn dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../testdata/conformance")
}

fn databases() -> Vec<(String, Vec<u8>)> {
    let mut v: Vec<_> = fs::read_dir(dir())
        .expect("read testdata/conformance")
        .map(|e| e.expect("dir entry").path())
        .filter(|p| p.extension().is_some_and(|x| x == "geoc"))
        .map(|p| {
            let name = p.file_name().unwrap().to_string_lossy().into_owned();
            (name, fs::read(&p).expect("read database"))
        })
        .collect();
    v.sort();
    assert!(v.len() >= 6, "expected the conformance databases");
    v
}

fn iters() -> usize {
    std::env::var("IP2GEO_FUZZ_ITERS")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(400)
}

/// xorshift64*: deterministic, no dependency.
struct Rng(u64);

impl Rng {
    fn next(&mut self) -> u64 {
        self.0 ^= self.0 >> 12;
        self.0 ^= self.0 << 25;
        self.0 ^= self.0 >> 27;
        self.0.wrapping_mul(0x2545_F491_4F6C_DD1D)
    }

    fn below(&mut self, n: usize) -> usize {
        (self.next() % n.max(1) as u64) as usize
    }
}

/// The sections of a GEO2 file as (offset, length), from its directory, for
/// aiming mutations at small sections too. Empty for anything else.
fn sections(b: &[u8]) -> Vec<(usize, usize)> {
    if !b.starts_with(b"GEO2") || b.len() < 32 {
        return Vec::new();
    }
    let n = u32::from_le_bytes(b[8..12].try_into().unwrap()) as usize;
    let mut v = vec![(0, 32 + 24 * n)];
    for k in 0..n {
        let o = 32 + 24 * k;
        let Some(e) = b.get(o..o + 24) else { break };
        let off = u64::from_le_bytes(e[8..16].try_into().unwrap()) as usize;
        let len = u64::from_le_bytes(e[16..24].try_into().unwrap()) as usize;
        v.push((off, len));
    }
    v
}

/// Probe addresses: edges, the alias prefixes, NAT64 and random ones.
fn probes(rng: &mut Rng) -> Vec<IpAddr> {
    let mut v: Vec<IpAddr> = [
        "0.0.0.0",
        "0.0.0.1",
        "1.0.0.0",
        "127.255.255.255",
        "255.255.255.255",
        "::",
        "::1",
        "::ffff:1.2.3.4",
        "::ffff:255.255.255.255",
        "2001::",
        "2001:db8::1",
        "2002:abcd::",
        "2002:ffff:ffff::",
        "2003::",
        "64:ff9b::1.2.3.4",
        "2000::",
        "3fff:ffff:ffff:ffff:ffff:ffff:ffff:ffff",
        "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff",
    ]
    .iter()
    .map(|s| s.parse().unwrap())
    .collect();
    for _ in 0..64 {
        v.push(IpAddr::V4(Ipv4Addr::from(rng.next() as u32)));
        let hi = rng.next();
        v.push(IpAddr::V6(Ipv6Addr::from(
            (u128::from(hi) << 64) | u128::from(rng.next() & 0xFF),
        )));
    }
    v
}

/// The IPv4 value or the IPv6 value of an address, as u128 with its family.
fn value(ip: IpAddr) -> (bool, u128) {
    match ip {
        IpAddr::V4(a) => (true, u128::from(u32::from(a))),
        IpAddr::V6(a) => (false, u128::from(a)),
    }
}

/// Uses every entry point of a reader that opened, checking that the
/// answers agree with each other.
fn exercise(r: &Reader, probes: &[IpAddr]) {
    let views: Vec<Reader> = std::iter::once(r.clone())
        .chain(
            r.languages()
                .iter()
                .map(|l| r.with_language(l).expect("listed language")),
        )
        .collect();
    let _ = r.metadata();
    for v in &views {
        for &ip in probes {
            let rec = v.lookup(ip);
            let det = v.lookup_details(ip);
            assert_eq!(
                rec.is_some(),
                det.is_some(),
                "{ip}: lookup and details disagree"
            );
            if let (Some(a), Some(d)) = (rec, det) {
                assert_eq!(a, d.record, "{ip}: record and details record differ");
            }
            if let Some(_a) = v.asn(ip) {
                assert!(rec.is_some(), "{ip}: ASN found but lookup not");
            }
            let (range, found) = v.lookup_range(ip);
            assert_eq!(found, rec.is_some(), "{ip}: range found flag");
            let (f4, first) = value(range.first);
            let (l4, last) = value(range.last);
            assert_eq!(f4, l4, "{ip}: range families differ");
            assert!(first <= last, "{ip}: range {first}..{last} is reversed");
            // A range in the query's own family holds the query.
            if f4 == ip.is_ipv4() {
                let (_, k) = value(ip);
                assert!(first <= k && k <= last, "{ip}: not in {range:?}");
            }
        }
        let mut prev: Option<(bool, u128)> = None;
        for (range, d) in v.ranges().take(20_000) {
            let f = value(range.first);
            let l = value(range.last);
            assert!(f.0 == l.0 && f.1 <= l.1, "bad range {range:?}");
            // IPv4 before IPv6, ascending within a family.
            if let Some(p) = prev {
                assert!(p < (!f.0, f.1), "ranges out of order at {range:?}");
            }
            prev = Some((!l.0, l.1));
            // The range's first address answers with its details (an IPv6
            // range never lies inside an alias prefix).
            assert_eq!(v.lookup_details(range.first), Some(d), "{range:?}");
            // LookupRange agrees at both ends.
            assert_eq!(
                v.lookup_range(range.first),
                (range, true),
                "{range:?} first"
            );
            assert_eq!(v.lookup_range(range.last), (range, true), "{range:?} last");
        }
    }
}

/// Opens `b` in every mode; a panic anywhere is reported with `what`.
fn try_open(b: &[u8], what: &str, probes: &[IpAddr], skip_crc: bool) -> Result<(), Error> {
    let res = catch_unwind(AssertUnwindSafe(|| {
        let r = OpenOptions::new()
            .skip_checksum(skip_crc)
            .from_bytes(b.to_vec());
        if let Ok(r) = &r {
            exercise(r, probes);
        }
        r.map(|_| ())
    }));
    match res {
        Ok(r) => r,
        Err(e) => {
            let msg = e
                .downcast_ref::<String>()
                .cloned()
                .or_else(|| e.downcast_ref::<&str>().map(|s| s.to_string()))
                .unwrap_or_default();
            panic!("panic on {what}: {msg}");
        }
    }
}

#[test]
fn originals_open_and_are_consistent() {
    let mut rng = Rng(0x1234_5678_9ABC_DEF1);
    let p = probes(&mut rng);
    for (name, b) in databases() {
        try_open(&b, &name, &p, false).unwrap_or_else(|e| panic!("{name}: {e}"));
    }
}

#[test]
fn truncated_inputs_are_rejected() {
    let mut rng = Rng(0x9E37_79B9_7F4A_7C15);
    let p = probes(&mut rng);
    for (name, b) in databases() {
        // Every length for a small file; for a GEO2 file the first 2 KiB
        // (header and directory), around every section edge, and random
        // lengths.
        let mut lens: Vec<usize> = if b.len() <= 4096 {
            (0..b.len()).collect()
        } else {
            (0..2048).collect()
        };
        for (off, len) in sections(&b) {
            for x in [off, off + len] {
                lens.extend([x.saturating_sub(1), x, x + 1]);
            }
        }
        for _ in 0..200 {
            lens.push(rng.below(b.len()));
        }
        lens.retain(|&n| n < b.len());
        lens.sort_unstable();
        lens.dedup();
        for n in lens {
            for skip in [true, false] {
                let what = format!("{name} truncated to {n} bytes (skip_checksum {skip})");
                match try_open(&b[..n], &what, &p, skip) {
                    Err(Error::Corrupt(_) | Error::BadMagic(_)) => {}
                    Err(e) => panic!("{what}: unexpected error kind: {e}"),
                    Ok(()) => panic!("{what}: opened"),
                }
            }
        }
    }
}

#[test]
fn flipped_bytes_never_panic() {
    let mut rng = Rng(0xD1B5_4A32_D192_ED03);
    let p = probes(&mut rng);
    let n = iters();
    for (name, b) in databases() {
        let secs = sections(&b);
        let (mut opened, mut rejected) = (0usize, 0usize);
        for it in 0..n {
            let mut m = b.clone();
            let flips = 1 + rng.below(4);
            let mut at = Vec::new();
            for _ in 0..flips {
                // Half the flips anywhere, half inside a random section, so
                // the small sections are hit as often as V4IDX.
                let o = if secs.is_empty() || rng.below(2) == 0 {
                    rng.below(m.len())
                } else {
                    let (off, len) = secs[rng.below(secs.len())];
                    (off + rng.below(len.max(1))).min(m.len() - 1)
                };
                m[o] ^= 1 + rng.below(255) as u8;
                at.push(o);
            }
            let what = format!("{name} mutant {it}, flipped at {at:?}");
            match try_open(&m, &what, &p, true) {
                Ok(()) => opened += 1,
                Err(_) => rejected += 1,
            }
        }
        eprintln!("{name}: {n} mutants, {opened} opened, {rejected} rejected");
    }
}

#[test]
fn checksum_catches_flips_inside_sections() {
    let mut rng = Rng(0x5851_F42D_4C95_7F2D);
    let p = probes(&mut rng);
    for (name, b) in databases() {
        let secs = sections(&b);
        if secs.is_empty() {
            continue; // GEO1 has no checksum
        }
        for it in 0..100 {
            // A section other than the header/directory pseudo-section.
            let (off, len) = secs[1 + rng.below(secs.len() - 1)];
            if len == 0 {
                continue;
            }
            let mut m = b.clone();
            let o = off + rng.below(len);
            m[o] ^= 1 + rng.below(255) as u8;
            let what = format!("{name} flip {it} at {o} with checksums");
            match try_open(&m, &what, &p, false) {
                Err(Error::Corrupt(msg)) => assert!(msg.contains("CRC-32C"), "{what}: {msg}"),
                other => panic!("{what}: {other:?}"),
            }
        }
    }
}

#[test]
fn geo1_every_byte() {
    let mut rng = Rng(42);
    let p = probes(&mut rng);
    let (name, b) = databases()
        .into_iter()
        .find(|(_, b)| b.starts_with(b"GEO1"))
        .expect("a GEO1 database");
    for o in 0..b.len() {
        for x in [0x01u8, 0x80, 0xFF, 0x10] {
            let mut m = b.clone();
            m[o] ^= x;
            let _ = try_open(&m, &format!("{name} byte {o} ^ {x:#x}"), &p, false);
        }
    }
}

#[test]
fn header_errors_have_the_go_kinds() {
    let city = fs::read(dir().join("city.geoc")).unwrap();
    let open = |b: Vec<u8>| Reader::from_bytes(b).unwrap_err();

    assert!(matches!(open(Vec::new()), Error::BadMagic(_)));
    assert!(matches!(open(b"GEO3xxxx".to_vec()), Error::BadMagic(_)));
    assert!(matches!(open(b"GEO2".to_vec()), Error::Corrupt(_)));
    let mut v = city.clone();
    v[4] = 3;
    assert!(matches!(open(v), Error::UnsupportedVersion(_)));
    let mut v = city.clone();
    v[5] = 4; // profile
    assert!(matches!(open(v), Error::Corrupt(_)));
    let mut v = city.clone();
    v[6] = 8; // lat scale
    assert!(matches!(open(v), Error::Corrupt(_)));
    let mut v = city.clone();
    v[8..12].copy_from_slice(&u32::MAX.to_le_bytes()); // directory too large
    assert!(matches!(open(v), Error::Corrupt(_)));

    let geo1 = fs::read(dir().join("geo1-city.geoc")).unwrap();
    let mut v = geo1.clone();
    v[4] = 2;
    assert!(matches!(open(v), Error::UnsupportedVersion(_)));
    let mut v = geo1.clone();
    v.push(0); // trailing byte
    assert!(matches!(open(v), Error::Corrupt(_)));
}

#[test]
fn unknown_field_set_bit_is_an_unsupported_version() {
    // Set XFIELDS bit 8 in city.geoc (checksums skipped, as the section's
    // CRC no longer matches).
    let mut b = fs::read(dir().join("city.geoc")).unwrap();
    let (off, _) = sections(&b)
        .into_iter()
        .skip(1)
        .zip(0..)
        .find(|&(_, k)| {
            let o = 32 + 24 * k;
            u32::from_le_bytes(b[o..o + 4].try_into().unwrap()) == 12
        })
        .map(|(s, _)| s)
        .expect("XFIELDS");
    b[off + 1] |= 1;
    let err = OpenOptions::new()
        .skip_checksum(true)
        .from_bytes(b)
        .unwrap_err();
    assert!(matches!(err, Error::UnsupportedVersion(_)), "{err}");
}

#[test]
fn open_errors() {
    let err = Reader::open(dir().join("does-not-exist.geoc")).unwrap_err();
    assert!(matches!(err, Error::Io(_)), "{err}");
    let err = Reader::open(dir().join("README.md")).unwrap_err();
    assert!(matches!(err, Error::BadMagic(_)), "{err}");
}
