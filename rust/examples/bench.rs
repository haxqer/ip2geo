//! A rough lookup timing on a database file, without a benchmark framework:
//!
//! ```text
//! cargo run --release --example bench -- path/to/city.geoc
//! ```
//!
//! It looks up 65 536 pseudo-random addresses (the same xorshift sequence
//! for every run) repeatedly for about a second per case and prints the
//! mean time per call. Shared machines are noisy: read the numbers as an
//! indication, not a measurement.

use std::hint::black_box;
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};
use std::time::{Duration, Instant};

use ip2geo::Reader;

struct Rng(u64);

impl Rng {
    fn next(&mut self) -> u64 {
        self.0 ^= self.0 >> 12;
        self.0 ^= self.0 << 25;
        self.0 ^= self.0 >> 27;
        self.0.wrapping_mul(0x2545_F491_4F6C_DD1D)
    }
}

fn time(name: &str, addrs: &[IpAddr], mut f: impl FnMut(IpAddr) -> bool) {
    let mut found = 0usize;
    for &a in addrs {
        found += usize::from(f(a)); // warm up
    }
    let mut calls = 0usize;
    let start = Instant::now();
    while start.elapsed() < Duration::from_secs(1) {
        for &a in addrs {
            found += usize::from(f(black_box(a)));
        }
        calls += addrs.len();
    }
    let ns = start.elapsed().as_nanos() as f64 / calls as f64;
    black_box(found);
    println!("{name:<28} {ns:>7.1} ns/op  ({calls} calls)");
}

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let path = std::env::args()
        .nth(1)
        .ok_or("usage: bench <database.geoc>")?;
    let t = Instant::now();
    let db = Reader::open(&path)?;
    println!("open (mmap, CRC checked)     {:?}", t.elapsed());
    let m = db.metadata();
    println!(
        "{}: {} profile, {} IPv4 and {} IPv6 ranges, {} records",
        path, m.profile, m.ipv4_ranges, m.ipv6_ranges, m.records
    );

    let mut rng = Rng(0x9E37_79B9_7F4A_7C15);
    let v4: Vec<IpAddr> = (0..1 << 16)
        .map(|_| IpAddr::V4(Ipv4Addr::from(rng.next() as u32)))
        .collect();
    // IPv6 keys in 2400:: to 2fff:ffff:..., outside the alias prefixes.
    let v6: Vec<IpAddr> = (0..1 << 16)
        .map(|_| {
            let hi = (rng.next() & 0x0FFF_FFFF_FFFF_FFFF) | 0x2400_0000_0000_0000;
            IpAddr::V6(Ipv6Addr::from(
                (u128::from(hi) << 64) | u128::from(rng.next()),
            ))
        })
        .collect();

    time("lookup IPv4", &v4, |a| db.lookup(a).is_some());
    time("lookup_details IPv4", &v4, |a| {
        db.lookup_details(a).is_some()
    });
    time("lookup_range IPv4", &v4, |a| db.lookup_range(a).1);
    time("lookup IPv6", &v6, |a| db.lookup(a).is_some());
    time("lookup_range IPv6", &v6, |a| db.lookup_range(a).1);
    let t = Instant::now();
    let n = db.ranges().count();
    println!("ranges: {n} in {:?}", t.elapsed());
    Ok(())
}
