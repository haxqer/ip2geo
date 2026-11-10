//! The iterator over every covered range, [`Reader::ranges`].

use std::iter::FusedIterator;
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};

use crate::reader::V6_ALIASES;
use crate::{Details, IpRange, Reader};

/// The iterator returned by [`Reader::ranges`]: every covered range with its
/// details, the IPv4 ones in ascending order, then the IPv6 ones.
///
/// ```no_run
/// let db = ip2geo::Reader::open("country.geoc")?;
/// for (range, details) in db.ranges().take(3) {
///     println!("{}-{} {}", range.first, range.last, details.record.country_iso);
/// }
/// # Ok::<(), ip2geo::Error>(())
/// ```
#[derive(Debug)]
pub struct Ranges<'a> {
    r: &'a Reader,
    phase: Phase,
    /// The next boundary to visit in the current family.
    i: usize,
    /// IPv4: the top-16 bucket of the last boundary whose start was read.
    g: usize,
    /// IPv6: the next exception.
    j: usize,
    /// The start of boundary `i`, once read.
    first: u128,
    /// IPv6 parts of the current range left to yield, outside the alias
    /// prefixes: `pend[pos..len]`.
    pend: [(u128, u128); 5],
    pend_len: usize,
    pend_pos: usize,
    pend_details: Details<'a>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Phase {
    V4,
    V6,
    Done,
}

impl<'a> Ranges<'a> {
    pub(crate) fn new(r: &'a Reader) -> Ranges<'a> {
        let mut it = Ranges {
            r,
            phase: Phase::V4,
            i: 0,
            g: 0,
            j: 0,
            first: 0,
            pend: [(0, 0); 5],
            pend_len: 0,
            pend_pos: 0,
            pend_details: Details::default(),
        };
        if r.n_v4() > 0 {
            it.first = u128::from(it.start4(0));
        }
        it
    }

    /// The absolute start of IPv4 boundary `i`; `i` never decreases between
    /// calls, so the bucket only moves forward.
    fn start4(&mut self, i: usize) -> u32 {
        while self.g < 0xFFFF && self.r.v4idx_at(self.g + 1) as usize <= i {
            self.g += 1;
        }
        ((self.g as u32) << 16) | u32::from(self.r.v4start_low(i))
    }

    /// The 128-bit start of IPv6 boundary `i`; called once per boundary, in
    /// order, so the exceptions are walked in step.
    fn start6(&mut self, i: usize) -> u128 {
        let hi = u128::from(self.r.v6hi_at(i)) << 64;
        if self.j < self.r.n_exc() {
            let (ei, lo) = self.r.exc_entry(self.j);
            if ei == i {
                self.j += 1;
                return hi | u128::from(lo);
            }
        }
        hi
    }

    fn next_v4(&mut self) -> Option<(IpRange, Details<'a>)> {
        let n = self.r.n_v4();
        while self.i < n {
            let i = self.i;
            let first = self.first as u32;
            let mut last = u32::MAX;
            if i + 1 < n {
                let next = self.start4(i + 1);
                self.first = u128::from(next);
                last = next.wrapping_sub(1);
            }
            self.i += 1;
            let id = self.r.v4id(i);
            if id != 0 {
                let range = IpRange {
                    first: IpAddr::V4(Ipv4Addr::from(first)),
                    last: IpAddr::V4(Ipv4Addr::from(last)),
                };
                return Some((range, self.r.details(id)));
            }
        }
        None
    }

    /// Advances to the next covered IPv6 range and queues its parts outside
    /// the alias prefixes. Returns false when there are no more boundaries.
    fn fill_v6(&mut self) -> bool {
        let n = self.r.n_v6();
        while self.i < n {
            let i = self.i;
            let first = self.first;
            let mut last = u128::MAX;
            if i + 1 < n {
                let next = self.start6(i + 1);
                self.first = next;
                last = next.wrapping_sub(1);
            }
            self.i += 1;
            let id = self.r.v6id(i);
            if id == 0 {
                continue;
            }
            self.split_unaliased(first, last);
            if self.pend_len > 0 {
                self.pend_details = self.r.details(id);
                return true;
            }
        }
        false
    }

    /// Queues the parts of `first..=last` outside the alias prefixes.
    fn split_unaliased(&mut self, mut first: u128, last: u128) {
        self.pend_len = 0;
        self.pend_pos = 0;
        for &(af, al) in &V6_ALIASES {
            if al < first {
                continue; // below what is left
            }
            if last < af {
                break; // above it
            }
            if first < af {
                self.pend[self.pend_len] = (first, af - 1);
                self.pend_len += 1;
            }
            if al >= last {
                return; // the alias covers the rest
            }
            first = al + 1;
        }
        self.pend[self.pend_len] = (first, last);
        self.pend_len += 1;
    }
}

impl<'a> Iterator for Ranges<'a> {
    type Item = (IpRange, Details<'a>);

    fn next(&mut self) -> Option<Self::Item> {
        loop {
            match self.phase {
                Phase::V4 => {
                    if let Some(x) = self.next_v4() {
                        return Some(x);
                    }
                    self.phase = Phase::V6;
                    self.i = 0;
                    if self.r.n_v6() > 0 {
                        self.first = self.start6(0);
                    }
                }
                Phase::V6 => {
                    if self.pend_pos < self.pend_len {
                        let (f, l) = self.pend[self.pend_pos];
                        self.pend_pos += 1;
                        let range = IpRange {
                            first: IpAddr::V6(Ipv6Addr::from(f)),
                            last: IpAddr::V6(Ipv6Addr::from(l)),
                        };
                        return Some((range, self.pend_details));
                    }
                    if !self.fill_v6() {
                        self.phase = Phase::Done;
                    }
                }
                Phase::Done => return None,
            }
        }
    }
}

impl FusedIterator for Ranges<'_> {}
