//! GEO1 (v0.1.x) files: validated as the Go reader does (`geo1.go`) and
//! transcoded at open into the GEO2 in-memory layout.

use crate::error::{Error, Result};
use crate::format::*;
use crate::geo2::Span;
use crate::reader::{Parts, Strings, Tables};
use crate::types::{FieldSet, Metadata, Profile};

/// The GEO1 record width (every field a u32) of a profile.
fn rec_width1(p: u8) -> usize {
    match p {
        0 => 16,
        1 => 24,
        2 => 48,
        _ => 0,
    }
}

/// Checks the fixed GEO1 header: magic, version, profile, lat/lon scale and
/// the reserved byte. Only the first 8 bytes are read.
pub(crate) fn check_header(h: &[u8]) -> Result<()> {
    if !h.starts_with(MAGIC1) {
        return Err(Error::BadMagic(format!(
            "magic {:?}, want \"GEO1\"",
            String::from_utf8_lossy(&h[..h.len().min(4)])
        )));
    }
    let Some(h) = h.get(..GEO1_HEADER_SIZE) else {
        return Err(Error::corrupt(format!(
            "truncated header, {} of {GEO1_HEADER_SIZE} bytes",
            h.len()
        )));
    };
    if h[4] != VERSION1 {
        return Err(Error::UnsupportedVersion(format!(
            "format version {}, want {VERSION1}",
            h[4]
        )));
    }
    if h[5] > 2 {
        return Err(Error::corrupt(format!("unknown profile {}", h[5])));
    }
    if h[6] > MAX_LAT_SCALE {
        return Err(Error::corrupt(format!(
            "lat/lon scale {} is over {MAX_LAT_SCALE}",
            h[6]
        )));
    }
    if h[7] != 0 {
        return Err(Error::corrupt(format!(
            "reserved header byte is {}, want 0",
            h[7]
        )));
    }
    Ok(())
}

/// Walks the input. A size is a u32 count times a small width, formed in
/// u64 so it cannot wrap, and checked against the bytes left before any
/// slice is taken.
struct Cursor<'b> {
    b: &'b [u8],
    off: usize,
}

impl<'b> Cursor<'b> {
    fn rem(&self) -> u64 {
        (self.b.len() - self.off) as u64
    }

    fn take(&mut self, count: u64, width: u64, what: &str) -> Result<&'b [u8]> {
        let n = count * width;
        if n > self.rem() {
            return Err(Error::corrupt(format!(
                "truncated {what}, need {n} bytes, have {}",
                self.rem()
            )));
        }
        let s = &self.b[self.off..self.off + n as usize];
        self.off += n as usize;
        Ok(s)
    }

    fn u32(&mut self, what: &str) -> Result<u32> {
        Ok(u32_at(self.take(1, 4, what)?, 0))
    }

    fn u16(&mut self, what: &str) -> Result<u16> {
        Ok(u16_at(self.take(1, 2, what)?, 0))
    }
}

fn check_record_ids(ids: &[u8], rc: u32, family: &str) -> Result<()> {
    for (i, c) in ids.chunks_exact(4).enumerate() {
        let id = u32_at(c, 0);
        if id >= rc {
            return Err(Error::corrupt(format!(
                "{family} range {i} points at record {id} of {rc}"
            )));
        }
    }
    Ok(())
}

// GEO1 record fields, as u32 indexes into a record.
const F1_COUNTRY_ISO: usize = 0;
const F1_COUNTRY_NAME: usize = 1;
const F1_CONTINENT: usize = 2;
const F1_COUNTRY_GEO: usize = 3;
const F1_REGION_ISO: usize = 4;
const F1_REGION_NAME: usize = 5;
const F1_CITY_NAME: usize = 6;
const F1_CITY_GEO: usize = 7;
const F1_LAT: usize = 8;
const F1_LON: usize = 9;
const F1_ACCURACY: usize = 10;
const F1_TIME_ZONE: usize = 11;

/// Validates a GEO1 input and converts it to the GEO2 layout: the parts
/// and the buffer their tables are in.
pub(crate) fn open(b: &[u8]) -> Result<(Parts, Vec<u8>)> {
    check_header(b)?;
    let profile = b[5];
    let lat_scale = b[6];
    let mut c = Cursor {
        b,
        off: GEO1_HEADER_SIZE,
    };

    // Strings: each needs at least its 2-byte length, which bounds the count
    // before anything is allocated.
    let ns = c.u32("string count")?;
    if u64::from(ns) > c.rem() / 2 {
        return Err(Error::corrupt(format!(
            "{ns} strings cannot fit in {} bytes",
            c.rem()
        )));
    }
    let mut strs: Vec<&[u8]> = Vec::with_capacity(ns as usize);
    for _ in 0..ns {
        let l = c.u16("string length")?;
        strs.push(c.take(u64::from(l), 1, "string")?);
    }

    // Records: record 0 is the sentinel, and the width must be the profile's.
    let rc = c.u32("record count")?;
    let rw = c.u16("record width")?;
    if rc == 0 {
        return Err(Error::corrupt("no records, record 0 is required"));
    }
    let rw1 = rec_width1(profile);
    if usize::from(rw) != rw1 {
        return Err(Error::corrupt(format!(
            "record width {rw}, profile {profile} needs {rw1}"
        )));
    }
    let recs = c.take(u64::from(rc), u64::from(rw), "records")?;

    let n4 = c.u32("IPv4 range count")?;
    let starts4 = c.take(u64::from(n4), 4, "IPv4 starts")?;
    let ids4 = c.take(u64::from(n4), 4, "IPv4 record ids")?;
    for i in 1..n4 as usize {
        if u32_at(starts4, i * 4) <= u32_at(starts4, i * 4 - 4) {
            return Err(Error::corrupt(format!(
                "IPv4 start {i} is not above start {}",
                i - 1
            )));
        }
    }
    check_record_ids(ids4, rc, "IPv4")?;

    let n6 = c.u32("IPv6 range count")?;
    let starts6 = c.take(u64::from(n6), 16, "IPv6 starts")?;
    let ids6 = c.take(u64::from(n6), 4, "IPv6 record ids")?;
    let start6 = |i: usize| -> u128 {
        let mut a = [0u8; 16];
        if let Some(s) = starts6.get(i * 16..i * 16 + 16) {
            a.copy_from_slice(s);
        }
        u128::from_be_bytes(a)
    };
    for i in 1..n6 as usize {
        if start6(i - 1) >= start6(i) {
            return Err(Error::corrupt(format!(
                "IPv6 start {i} is not above start {}",
                i - 1
            )));
        }
    }
    check_record_ids(ids6, rc, "IPv6")?;

    if c.rem() != 0 {
        return Err(Error::corrupt(format!("{} trailing bytes", c.rem())));
    }

    transcode(
        profile, lat_scale, &strs, rc, recs, starts4, ids4, n6, &start6, ids6,
    )
}

/// Converts the validated GEO1 tables. Strings are renumbered so that every
/// string a u16 field uses comes first, then the city names; strings no
/// record uses are dropped. A GEO1 string id of 0 or past the string table
/// reads as the empty string and becomes id 0. Record 0 is written as all
/// zero: GEO1 never decoded it.
#[allow(clippy::too_many_arguments)]
fn transcode(
    profile: u8,
    lat_scale: u8,
    strs: &[&[u8]],
    rc: u32,
    recs: &[u8],
    starts4: &[u8],
    ids4: &[u8],
    n6: u32,
    start6: &dyn Fn(usize) -> u128,
    ids6: &[u8],
) -> Result<(Parts, Vec<u8>)> {
    let rw = rec_width1(profile);
    let w = record_width(profile);
    let rc = rc as usize;
    let ns = strs.len() as u32;
    let field = |i: usize, f: usize| u32_at(recs, i * rw + f * 4);

    // new_id[o] is the new id of GEO1 string o, 0 until o is first used.
    let mut new_id = vec![0u32; strs.len()];
    let mut next = 1u32;
    let assign = |f: usize, new_id: &mut [u32], next: &mut u32| {
        for i in 1..rc {
            let o = field(i, f);
            if let Some(slot) = new_id.get_mut(o as usize).filter(|s| o != 0 && **s == 0) {
                *slot = *next;
                *next += 1;
            }
        }
    };
    let mut u16_fields = vec![F1_COUNTRY_ISO, F1_COUNTRY_NAME, F1_CONTINENT];
    if profile >= 1 {
        u16_fields.extend([F1_REGION_ISO, F1_REGION_NAME]);
    }
    if profile >= 2 {
        u16_fields.push(F1_TIME_ZONE);
    }
    for &f in &u16_fields {
        assign(f, &mut new_id, &mut next);
    }
    if next > 1 << 16 {
        return Err(Error::TooLarge(format!(
            "{} distinct strings in country, region and time zone fields, at most 65535 fit",
            next - 1
        )));
    }
    if profile >= 2 {
        assign(F1_CITY_NAME, &mut new_id, &mut next);
        if next > 1 << 24 {
            return Err(Error::TooLarge(format!(
                "{} distinct strings, at most {} fit",
                next - 1,
                (1 << 24) - 1
            )));
        }
    }
    let mut order = vec![0u32; next as usize]; // order[new id] = GEO1 id
    for (o, &id) in new_id.iter().enumerate() {
        if id != 0 {
            order[id as usize] = o as u32;
        }
    }
    let strings = Strings::new(order.iter().enumerate().map(|(id, &o)| {
        if id == 0 {
            &[][..]
        } else {
            strs[o as usize]
        }
    }))?;
    let map_str = |o: u32| -> u32 {
        if o >= ns {
            0
        } else {
            new_id[o as usize]
        }
    };

    // The tables, laid out one after the other in one buffer.
    let mut buf: Vec<u8> = Vec::new();
    let span = |buf: &[u8], start: usize| Span {
        off: start,
        len: buf.len() - start,
    };

    let start = buf.len();
    buf.resize(rc * w, 0);
    for i in 1..rc {
        let d = &mut buf[i * w..(i + 1) * w];
        let put16 = |d: &mut [u8], off: usize, v: u32| {
            d[off..off + 2].copy_from_slice(&(v as u16).to_le_bytes());
        };
        let put32 = |d: &mut [u8], off: usize, v: u32| {
            d[off..off + 4].copy_from_slice(&v.to_le_bytes());
        };
        put16(d, REC_COUNTRY_ISO, map_str(field(i, F1_COUNTRY_ISO)));
        put16(d, REC_COUNTRY_NAME, map_str(field(i, F1_COUNTRY_NAME)));
        put16(d, REC_CONTINENT, map_str(field(i, F1_CONTINENT)));
        put32(d, REC_COUNTRY_GEO, field(i, F1_COUNTRY_GEO));
        if profile >= 1 {
            put16(d, REC_REGION_ISO, map_str(field(i, F1_REGION_ISO)));
            put16(d, REC_REGION_NAME, map_str(field(i, F1_REGION_NAME)));
        }
        if profile >= 2 {
            let city = map_str(field(i, F1_CITY_NAME)).to_le_bytes();
            d[REC_CITY_NAME..REC_CITY_NAME + 3].copy_from_slice(&city[..3]);
            put32(d, REC_CITY_GEO, field(i, F1_CITY_GEO));
            put32(d, REC_LAT, field(i, F1_LAT));
            put32(d, REC_LON, field(i, F1_LON));
            // The u32 accuracy keeps its low 16 bits.
            put16(d, REC_ACCURACY, field(i, F1_ACCURACY));
            put16(d, REC_TIME_ZONE, map_str(field(i, F1_TIME_ZONE)));
        }
    }
    let records = span(&buf, start);
    let idw = id_width(rc as u64);

    // IPv4: V4IDX[g] is the first start at or above g << 16; V4STARTS keeps
    // the low 16 bits.
    let n4 = starts4.len() / 4;
    let start = buf.len();
    let mut g = 0usize;
    for i in 0..n4 {
        let s = u32_at(starts4, i * 4);
        while g <= (s >> 16) as usize {
            buf.extend_from_slice(&(i as u32).to_le_bytes());
            g += 1;
        }
    }
    while g < V4IDX_ENTRIES {
        buf.extend_from_slice(&(n4 as u32).to_le_bytes());
        g += 1;
    }
    let v4idx = span(&buf, start);
    let start = buf.len();
    for i in 0..n4 {
        buf.extend_from_slice(&(u32_at(starts4, i * 4) as u16).to_le_bytes());
    }
    let v4starts = span(&buf, start);
    let start = buf.len();
    for i in 0..n4 {
        push_id(&mut buf, idw, u32_at(ids4, i * 4));
    }
    let v4ids = span(&buf, start);

    // IPv6: the top halves, and the non-zero low halves as exceptions.
    let n6 = n6 as usize;
    let start = buf.len();
    for i in 0..n6 {
        buf.extend_from_slice(&((start6(i) >> 64) as u64).to_le_bytes());
    }
    let v6hi = span(&buf, start);
    let start = buf.len();
    for i in 0..n6 {
        push_id(&mut buf, idw, u32_at(ids6, i * 4));
    }
    let v6ids = span(&buf, start);
    let start = buf.len();
    for i in 0..n6 {
        if start6(i) as u64 != 0 {
            buf.extend_from_slice(&(i as u32).to_le_bytes());
        }
    }
    let v6exc_idx = span(&buf, start);
    let start = buf.len();
    for i in 0..n6 {
        let lo = start6(i) as u64;
        if lo != 0 {
            buf.extend_from_slice(&lo.to_le_bytes());
        }
    }
    let v6exc_lo = span(&buf, start);

    let profile = Profile::from_u8(profile).ok_or_else(|| Error::corrupt("unknown profile"))?;
    let meta = Metadata {
        format_version: VERSION1,
        profile,
        lat_scale,
        build_time: None,
        source_time: None,
        source_type: String::new(),
        language: String::new(),
        fields: FieldSet::NONE,
        description: String::new(),
        builder: String::new(),
        networks: 0,
        distinct_records: 0,
        records: rc,
        strings: ns as usize,
        ipv4_ranges: n4,
        ipv6_ranges: n6,
    };
    let parts = Parts {
        profile,
        id_width: idw,
        lat_scale,
        tables: Tables {
            records,
            v4idx,
            v4starts,
            v4ids,
            v6hi,
            v6ids,
            v6exc_idx,
            v6exc_lo,
            xrecs: Span::default(),
        },
        strings,
        fields: 0,
        xl: XLayout::new(profile as u8, 0, 1),
        langs: Vec::new(),
        meta,
    };
    Ok((parts, buf))
}
