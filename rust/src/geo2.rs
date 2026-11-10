//! Validation of a GEO2 input: every rule of the "Reader requirements" of
//! docs/FORMAT.md, in the order and with the error kinds of the Go reader
//! (`geo2.go`). Nothing here reads past the input or allocates in
//! proportion to a count before that count is bounded by the input size.

use std::collections::HashSet;

use crate::crc32c;
use crate::error::{Error, Result};
use crate::format::*;

/// Where a section is in the input.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub(crate) struct Span {
    pub off: usize,
    pub len: usize,
}

impl Span {
    /// The section's bytes in `b`, empty if (impossibly, once validated) out
    /// of range.
    #[inline]
    pub(crate) fn of(self, b: &[u8]) -> &[u8] {
        b.get(self.off..self.off.wrapping_add(self.len))
            .unwrap_or(&[])
    }
}

/// The fixed 64-byte META block; string fields are ids.
#[derive(Clone, Copy, Debug, Default)]
pub(crate) struct Meta {
    pub build_time: u64,
    pub source_time: u64,
    pub networks: u64,
    pub source_type: u32,
    pub language: u32,
    pub description: u32,
    pub builder: u32,
    pub distinct_records: u32,
}

/// A validated GEO2 input: where its known sections are, its header and META
/// fields and the counts derived from the section lengths.
#[derive(Debug)]
pub(crate) struct Geo2 {
    pub profile: u8,
    pub lat_scale: u8,
    pub id_width: u8,
    section_count: u32,
    pub sec: [Span; NUM_SECTIONS],
    has: [bool; NUM_SECTIONS],
    pub meta: Meta,
    pub n_strings: usize,
    pub n_records: usize,
    pub n_v4: usize,
    pub n_v6: usize,
    n_exc: usize,
    pub fields: u32,
    pub xl: XLayout,
}

/// Checks the GEO2 header: magic, version, profile, latScale, idWidth and
/// the reserved bytes. Only the first `HEADER_SIZE` bytes are read.
pub(crate) fn parse_header(b: &[u8]) -> Result<(u8, u8, u8, u32)> {
    if b.get(..4) != Some(&MAGIC2[..]) {
        return Err(Error::BadMagic(format!(
            "magic {:?}, want \"GEO2\"",
            String::from_utf8_lossy(&b[..b.len().min(4)])
        )));
    }
    let Some(h) = b.get(..HEADER_SIZE) else {
        return Err(Error::corrupt(format!(
            "truncated GEO2 header, {} of {HEADER_SIZE} bytes",
            b.len()
        )));
    };
    if h[4] != VERSION2 {
        return Err(Error::UnsupportedVersion(format!(
            "GEO2 format version {}, want {VERSION2}",
            h[4]
        )));
    }
    let (profile, lat_scale, id_width) = (h[5], h[6], h[7]);
    if profile > MAX_PROFILE {
        return Err(Error::corrupt(format!("unknown profile {profile}")));
    }
    if lat_scale > MAX_LAT_SCALE {
        return Err(Error::corrupt(format!(
            "lat/lon scale {lat_scale} is over {MAX_LAT_SCALE}"
        )));
    }
    if !(1..=4).contains(&id_width) {
        return Err(Error::corrupt(format!(
            "record id width {id_width} is not 1 to 4"
        )));
    }
    if h[12..].iter().any(|&c| c != 0) {
        return Err(Error::corrupt("reserved GEO2 header bytes are not zero"));
    }
    Ok((profile, lat_scale, id_width, u32_at(h, 8)))
}

/// One directory entry.
struct DirEntry {
    id: u32,
    crc: u32,
    offset: u64,
    length: u64,
}

fn dir_entry(b: &[u8], k: usize) -> DirEntry {
    let o = HEADER_SIZE + k * DIR_ENTRY_SIZE;
    DirEntry {
        id: u32_at(b, o),
        crc: u32_at(b, o + 4),
        offset: u64_at(b, o + 8),
        length: u64_at(b, o + 16),
    }
}

/// Checks every rule of the reader requirements, rules 1 to 13, without
/// reading past `b`. The magic has been matched by the caller or is checked
/// again here.
pub(crate) fn parse(b: &[u8], skip_crc: bool) -> Result<Geo2> {
    let (profile, lat_scale, id_width, section_count) = parse_header(b)?;
    let mut g = Geo2 {
        profile,
        lat_scale,
        id_width,
        section_count,
        sec: [Span::default(); NUM_SECTIONS],
        has: [false; NUM_SECTIONS],
        meta: Meta::default(),
        n_strings: 0,
        n_records: 0,
        n_v4: 0,
        n_v6: 0,
        n_exc: 0,
        fields: 0,
        xl: XLayout::default(),
    };
    g.check_directory(b)?;
    g.check_lengths()?;
    if !skip_crc {
        for k in 0..g.section_count as usize {
            let e = dir_entry(b, k);
            // Positions were checked: offset + length <= b.len().
            let s = Span {
                off: e.offset as usize,
                len: e.length as usize,
            };
            let got = crc32c::checksum(s.of(b));
            if got != e.crc {
                return Err(Error::corrupt(format!(
                    "section {} (directory entry {k}): CRC-32C {got:08x}, directory says {:08x}",
                    section_name(e.id),
                    e.crc
                )));
            }
        }
    }
    g.meta = parse_meta(g.bytes(b, SEC_META))?;
    g.check_strings(b)?;
    g.check_records(b)?;
    g.check_ipv4(b)?;
    g.check_ipv6(b)?;
    g.check_extensions(b)?;
    Ok(g)
}

fn parse_meta(m: &[u8]) -> Result<Meta> {
    if m.len() != META_SIZE || m[44..].iter().any(|&c| c != 0) {
        return Err(Error::corrupt("META: reserved bytes are not zero"));
    }
    Ok(Meta {
        build_time: u64_at(m, 0),
        source_time: u64_at(m, 8),
        networks: u64_at(m, 16),
        source_type: u32_at(m, 24),
        language: u32_at(m, 28),
        description: u32_at(m, 32),
        builder: u32_at(m, 36),
        distinct_records: u32_at(m, 40),
    })
}

impl Geo2 {
    /// Section `id` of `b`, empty when absent.
    pub(crate) fn bytes<'b>(&self, b: &'b [u8], id: u32) -> &'b [u8] {
        self.sec[id as usize].of(b)
    }

    fn len_of(&self, id: u32) -> u64 {
        self.sec[id as usize].len as u64
    }

    /// Rule 2 for every entry, known or not, and rule 3's presence part.
    fn check_directory(&mut self, b: &[u8]) -> Result<()> {
        let size = b.len() as u64;
        let n = u64::from(self.section_count);
        let mut end = HEADER_SIZE as u64 + n * DIR_ENTRY_SIZE as u64;
        if end > size {
            return Err(Error::corrupt(format!(
                "directory of {n} entries needs {end} bytes, the input has {size}"
            )));
        }
        for k in 0..n as usize {
            let e = dir_entry(b, k);
            let name = || section_name(e.id);
            if e.offset % 8 != 0 {
                return Err(Error::corrupt(format!(
                    "section {} (directory entry {k}) starts at {}, not a multiple of 8",
                    name(),
                    e.offset
                )));
            }
            if e.offset < end && k == 0 {
                return Err(Error::corrupt(format!(
                    "section {} (directory entry {k}) starts at {}, inside the header or directory",
                    name(),
                    e.offset
                )));
            }
            if e.offset < end {
                return Err(Error::corrupt(format!(
                    "section {} (directory entry {k}) starts at {}, before the end of the previous section at {end}",
                    name(),
                    e.offset
                )));
            }
            if e.length > size || e.offset > size - e.length {
                return Err(Error::corrupt(format!(
                    "section {} (directory entry {k}) at {}, {} bytes, ends past the {size}-byte input",
                    name(),
                    e.offset,
                    e.length
                )));
            }
            end = e.offset + e.length;
            let id = e.id as usize;
            if id == 0 || id >= NUM_SECTIONS {
                continue; // unknown: a later minor revision's section
            }
            if self.has[id] {
                return Err(Error::corrupt(format!(
                    "section {} is listed twice",
                    name()
                )));
            }
            self.has[id] = true;
            // Both fit in usize: they are bounded by b.len().
            self.sec[id] = Span {
                off: e.offset as usize,
                len: e.length as usize,
            };
        }
        for id in SEC_META..SEC_V6EXCIDX {
            if !self.has[id as usize] {
                return Err(Error::corrupt(format!(
                    "required section {} is missing",
                    section_name(id)
                )));
            }
        }
        Ok(())
    }

    /// Rule 3's lengths, and the counts derived from them.
    fn check_lengths(&mut self) -> Result<()> {
        let n = |id| self.len_of(id);
        let idw = u64::from(self.id_width);
        let rw = record_width(self.profile) as u64;
        let err = |m: String| Err(Error::corrupt(m));
        if n(SEC_META) != META_SIZE as u64 {
            return err(format!("META is {} bytes, want {META_SIZE}", n(SEC_META)));
        }
        if n(SEC_STRIDX) < 4 || n(SEC_STRIDX) % 4 != 0 {
            return err(format!(
                "STRIDX is {} bytes, want a non-zero multiple of 4",
                n(SEC_STRIDX)
            ));
        }
        if n(SEC_RECORDS) % rw != 0 {
            return err(format!(
                "RECORDS is {} bytes, not a multiple of the record width {rw}",
                n(SEC_RECORDS)
            ));
        }
        if n(SEC_V4IDX) != V4IDX_ENTRIES as u64 * 4 {
            return err(format!(
                "V4IDX is {} bytes, want {}",
                n(SEC_V4IDX),
                V4IDX_ENTRIES * 4
            ));
        }
        if n(SEC_V4STARTS) % 2 != 0 {
            return err(format!(
                "V4STARTS is {} bytes, not a multiple of 2",
                n(SEC_V4STARTS)
            ));
        }
        if n(SEC_V4IDS) != n(SEC_V4STARTS) / 2 * idw {
            return err(format!(
                "V4IDS is {} bytes, want {} for {} starts of {idw}-byte ids",
                n(SEC_V4IDS),
                n(SEC_V4STARTS) / 2 * idw,
                n(SEC_V4STARTS) / 2
            ));
        }
        if n(SEC_V6STARTS) % 8 != 0 {
            return err(format!(
                "V6STARTS is {} bytes, not a multiple of 8",
                n(SEC_V6STARTS)
            ));
        }
        if n(SEC_V6IDS) != n(SEC_V6STARTS) / 8 * idw {
            return err(format!(
                "V6IDS is {} bytes, want {} for {} starts of {idw}-byte ids",
                n(SEC_V6IDS),
                n(SEC_V6STARTS) / 8 * idw,
                n(SEC_V6STARTS) / 8
            ));
        }
        if n(SEC_V6EXCIDX) % 4 != 0 {
            return err(format!(
                "V6EXCIDX is {} bytes, not a multiple of 4",
                n(SEC_V6EXCIDX)
            ));
        }
        if n(SEC_V6EXCLO) != n(SEC_V6EXCIDX) / 4 * 8 {
            return err(format!(
                "V6EXCLO is {} bytes, want {} for {} exceptions",
                n(SEC_V6EXCLO),
                n(SEC_V6EXCIDX) / 4 * 8,
                n(SEC_V6EXCIDX) / 4
            ));
        }
        let len = |id: u32| self.sec[id as usize].len;
        self.n_strings = len(SEC_STRIDX) / 4 - 1;
        self.n_records = len(SEC_RECORDS) / rw as usize;
        self.n_v4 = len(SEC_V4STARTS) / 2;
        self.n_v6 = len(SEC_V6STARTS) / 8;
        self.n_exc = len(SEC_V6EXCIDX) / 4;
        Ok(())
    }

    /// The STRIDX half of rule 5, and the META string ids.
    fn check_strings(&self, b: &[u8]) -> Result<()> {
        let idx = self.bytes(b, SEC_STRIDX);
        let v0 = u32_at(idx, 0);
        if v0 != 0 {
            return Err(Error::corrupt(format!("STRIDX[0] is {v0}, want 0")));
        }
        let mut prev = 0u32;
        for i in 1..=self.n_strings {
            let v = u32_at(idx, i * 4);
            if v < prev {
                return Err(Error::corrupt(format!(
                    "STRIDX[{i}] = {v} is below STRIDX[{}] = {prev}",
                    i - 1
                )));
            }
            prev = v;
        }
        let blob = self.sec[SEC_STRBLOB as usize].len;
        if u64::from(prev) != blob as u64 {
            return Err(Error::corrupt(format!(
                "STRIDX ends at {prev}, STRBLOB is {blob} bytes"
            )));
        }
        if self.n_strings > 0 && u32_at(idx, 4) != 0 {
            return Err(Error::corrupt(format!(
                "string 0 is {} bytes, want the empty string",
                u32_at(idx, 4)
            )));
        }
        let m = &self.meta;
        for (name, id) in [
            ("source type", m.source_type),
            ("language", m.language),
            ("description", m.description),
            ("builder", m.builder),
        ] {
            if id as u64 >= self.n_strings as u64 {
                return Err(Error::corrupt(format!(
                    "META {name} is string {id} of {}",
                    self.n_strings
                )));
            }
        }
        Ok(())
    }

    /// The record half of rule 5, rule 6 and rule 13.
    fn check_records(&self, b: &[u8]) -> Result<()> {
        if self.n_records == 0 {
            return Err(Error::corrupt("no records, record 0 is required"));
        }
        let recs = self.bytes(b, SEC_RECORDS);
        let w = record_width(self.profile);
        if recs[..w].iter().any(|&c| c != 0) {
            return Err(Error::corrupt("record 0 is not all zero"));
        }
        let mut u16_offs: Vec<usize> = Vec::new();
        let mut u24: Option<(usize, &str)> = None;
        if self.profile == PROFILE_ASN {
            u24 = Some((REC_ASN_ORG, "organization"));
        } else {
            u16_offs.extend([REC_COUNTRY_ISO, REC_COUNTRY_NAME, REC_CONTINENT]);
            if self.profile >= 1 {
                u16_offs.extend([REC_REGION_ISO, REC_REGION_NAME]);
            }
            if self.profile == 2 {
                u16_offs.push(REC_TIME_ZONE);
                u24 = Some((REC_CITY_NAME, "city name"));
            }
        }
        if self.n_strings > usize::from(u16::MAX) {
            u16_offs.clear(); // every u16 id is below nStrings
        }
        let ns = self.n_strings as u64;
        for (i, r) in recs.chunks_exact(w).enumerate().skip(1) {
            for &o in &u16_offs {
                let id = u16_at(r, o);
                if u64::from(id) >= ns {
                    return Err(Error::corrupt(format!(
                        "record {i}: string id {id} at byte {o} is not below the {ns} strings"
                    )));
                }
            }
            if let Some((o, name)) = u24 {
                let id = u24_at(r, o);
                if u64::from(id) >= ns {
                    return Err(Error::corrupt(format!(
                        "record {i}: {name} is string {id} of {ns}"
                    )));
                }
            }
        }

        let want = id_width(self.n_records as u64);
        if self.id_width != want {
            return Err(Error::corrupt(format!(
                "record id width {} for {} records, want {want}",
                self.id_width, self.n_records
            )));
        }
        let nr = self.n_records as u64;
        for (name, sec, n) in [
            ("IPv4", SEC_V4IDS, self.n_v4),
            ("IPv6", SEC_V6IDS, self.n_v6),
        ] {
            let ids = self.bytes(b, sec);
            for i in 0..n {
                let id = read_id(ids, self.id_width, i);
                if u64::from(id) >= nr {
                    return Err(Error::corrupt(format!(
                        "{name} range {i} points at record {id} of {nr}"
                    )));
                }
            }
        }
        Ok(())
    }

    /// Rules 9 to 12, in the Go reader's order: rule 10, rule 9, the LANGS
    /// count, the XRECORDS length, the LANGS contents and the XRECORDS
    /// entries.
    fn check_extensions(&mut self, b: &[u8]) -> Result<()> {
        let p = self.profile;
        let has_f = self.has[SEC_XFIELDS as usize];
        let has_x = self.has[SEC_XRECORDS as usize];
        let has_l = self.has[SEC_LANGS as usize];
        if p == PROFILE_ASN && (has_f || has_x || has_l) {
            return Err(Error::corrupt(
                "an ASN database has an XFIELDS, XRECORDS or LANGS section",
            ));
        }
        if has_f != has_x {
            return Err(Error::corrupt(
                "XFIELDS and XRECORDS must both be present or both absent",
            ));
        }
        if has_l && !has_f {
            return Err(Error::corrupt(
                "LANGS is present without XFIELDS and XRECORDS",
            ));
        }
        if !has_f {
            self.xl = XLayout::new(p, 0, 1);
            return Ok(());
        }

        let xf = self.bytes(b, SEC_XFIELDS);
        if xf.len() != XFIELDS_SIZE {
            return Err(Error::corrupt(format!(
                "XFIELDS is {} bytes, want {XFIELDS_SIZE}",
                xf.len()
            )));
        }
        let fields = u32_at(xf, 0);
        let reserved = u32_at(xf, 4);
        if reserved != 0 {
            return Err(Error::corrupt(format!(
                "XFIELDS reserved word is {reserved:#x}, want 0"
            )));
        }
        let unknown = fields & !KNOWN_FIELDS;
        if unknown != 0 {
            return Err(Error::UnsupportedVersion(format!(
                "XFIELDS has field set bits {unknown:#x} this reader does not know"
            )));
        }
        if !check_fields(p, fields) {
            return Err(Error::corrupt(format!(
                "field sets {fields:#x} are not allowed for profile {p}"
            )));
        }

        let langs = self.bytes(b, SEC_LANGS);
        let mut n_langs = 1usize;
        if has_l {
            if langs.len() % 4 != 0 {
                return Err(Error::corrupt(format!(
                    "LANGS is {} bytes, not a multiple of 4",
                    langs.len()
                )));
            }
            n_langs = langs.len() / 4;
            if n_langs < 2 {
                return Err(Error::corrupt(format!(
                    "LANGS lists {n_langs} languages, want at least 2"
                )));
            }
        }

        // The entry width in u64: n_langs is only bounded by the input so far.
        let xr = self.bytes(b, SEC_XRECORDS);
        let base = XLayout::new(p, fields, 1);
        let w = (base.width as u64)
            .saturating_add((n_langs as u64 - 1).saturating_mul(3 * base.n_kinds() as u64));
        let want = w.checked_mul(self.n_records as u64);
        if want != Some(xr.len() as u64) {
            return Err(Error::corrupt(format!(
                "XRECORDS is {} bytes, want {w} x {} records",
                xr.len(),
                self.n_records
            )));
        }
        // Now w <= len(XRECORDS), so the layout's arithmetic cannot overflow.
        self.fields = fields;
        self.xl = XLayout::new(p, fields, n_langs);

        if has_l {
            self.check_langs(b, langs)?;
        }
        self.check_xrecords(xr)
    }

    /// The bytes of string `id`, which must be below n_strings.
    fn str_bytes<'b>(&self, b: &'b [u8], id: u32) -> &'b [u8] {
        let idx = self.bytes(b, SEC_STRIDX);
        let i = id as usize;
        let (s, e) = (u32_at(idx, i * 4) as usize, u32_at(idx, i * 4 + 4) as usize);
        self.bytes(b, SEC_STRBLOB).get(s..e).unwrap_or(&[])
    }

    /// Rule 12 for a LANGS section of at least two entries.
    fn check_langs(&self, b: &[u8], langs: &[u8]) -> Result<()> {
        let mut seen: HashSet<&[u8]> = HashSet::with_capacity(langs.len() / 4);
        for (j, c) in langs.chunks_exact(4).enumerate() {
            let id = u32_at(c, 0);
            if id as u64 >= self.n_strings as u64 {
                return Err(Error::corrupt(format!(
                    "LANGS[{j}] is string {id} of {}",
                    self.n_strings
                )));
            }
            let s = self.str_bytes(b, id);
            if s.is_empty() {
                return Err(Error::corrupt(format!("LANGS[{j}] is the empty string")));
            }
            if !seen.insert(s) {
                return Err(Error::corrupt(format!(
                    "LANGS[{j}] {:?} is listed twice",
                    String::from_utf8_lossy(s)
                )));
            }
        }
        let l0 = self.str_bytes(b, u32_at(langs, 0));
        let m = self.str_bytes(b, self.meta.language);
        if l0 != m {
            return Err(Error::corrupt(format!(
                "LANGS[0] {:?} is not the META language {:?}",
                String::from_utf8_lossy(l0),
                String::from_utf8_lossy(m)
            )));
        }
        Ok(())
    }

    /// Rule 11 past the length: entry 0 all zero, every u24 string id below
    /// n_strings, no undefined flag bit.
    fn check_xrecords(&self, xr: &[u8]) -> Result<()> {
        let l = &self.xl;
        let w = l.width;
        if w == 0 {
            return Ok(());
        }
        if xr[..w].iter().any(|&c| c != 0) {
            return Err(Error::corrupt("XRECORDS entry 0 is not all zero"));
        }
        let mut offs: Vec<usize> = Vec::new();
        for (set, rel) in [
            (FIELD_CONTINENT, &[X_CONTINENT_NAME][..]),
            (
                FIELD_REGISTERED,
                &[
                    X_REGISTERED_ISO,
                    X_REGISTERED_NAME,
                    X_REPRESENTED_ISO,
                    X_REPRESENTED_NAME,
                    X_REPRESENTED_TYPE,
                ][..],
            ),
            (FIELD_SUBDIVISIONS, &[X_REGION2_ISO, X_REGION2_NAME][..]),
            (FIELD_POSTAL, &[X_POSTAL][..]),
        ] {
            if let Some(base) = l.block(set).filter(|_| self.fields & set != 0) {
                offs.extend(rel.iter().map(|o| base + o));
            }
        }
        offs.extend((l.lang_off..w).step_by(3));
        let mut flags: Vec<(usize, u8)> = Vec::new();
        for bit in 0..8 {
            let f = 1u32 << bit;
            if self.fields & f == 0 {
                continue;
            }
            if let (Some((off, mask)), Some(base)) = (flag_byte(f), l.block(f)) {
                flags.push((base + off, mask));
            }
        }

        let ns = self.n_strings as u64;
        for (i, e) in xr.chunks_exact(w).enumerate().skip(1) {
            for &o in &offs {
                let id = u24_at(e, o);
                if u64::from(id) >= ns {
                    return Err(Error::corrupt(format!(
                        "XRECORDS entry {i}: string id {id} at byte {o} is not below the {ns} strings"
                    )));
                }
            }
            for &(o, mask) in &flags {
                let c = u8_at(e, o);
                if c & !mask != 0 {
                    return Err(Error::corrupt(format!(
                        "XRECORDS entry {i}: undefined flag bits {:#x} at byte {o}",
                        c & !mask
                    )));
                }
            }
        }
        Ok(())
    }

    /// Rule 7.
    fn check_ipv4(&self, b: &[u8]) -> Result<()> {
        let idx = self.bytes(b, SEC_V4IDX);
        let starts = self.bytes(b, SEC_V4STARTS);
        let last = u32_at(idx, (V4IDX_ENTRIES - 1) * 4);
        if last as u64 != self.n_v4 as u64 {
            return Err(Error::corrupt(format!(
                "V4IDX[65536] is {last}, want the IPv4 start count {}",
                self.n_v4
            )));
        }
        let first = u32_at(idx, 0);
        if first != 0 {
            return Err(Error::corrupt(format!("V4IDX[0] is {first}, want 0")));
        }
        for bk in 0..V4IDX_ENTRIES - 1 {
            let lo = u32_at(idx, bk * 4);
            let hi = u32_at(idx, bk * 4 + 4);
            if hi < lo {
                return Err(Error::corrupt(format!(
                    "V4IDX[{}] = {hi} is below V4IDX[{bk}] = {lo}",
                    bk + 1
                )));
            }
            if hi as u64 > self.n_v4 as u64 {
                return Err(Error::corrupt(format!(
                    "V4IDX[{}] = {hi} is above the IPv4 start count {}",
                    bk + 1,
                    self.n_v4
                )));
            }
            for i in lo as usize + 1..hi as usize {
                if u16_at(starts, i * 2) <= u16_at(starts, i * 2 - 2) {
                    return Err(Error::corrupt(format!(
                        "IPv4 start {i} is not above start {}",
                        i - 1
                    )));
                }
            }
        }
        Ok(())
    }

    /// Rule 8.
    fn check_ipv6(&self, b: &[u8]) -> Result<()> {
        let exc_idx = self.bytes(b, SEC_V6EXCIDX);
        let exc_lo = self.bytes(b, SEC_V6EXCLO);
        for j in 0..self.n_exc {
            let i = u32_at(exc_idx, j * 4);
            if i as u64 >= self.n_v6 as u64 {
                return Err(Error::corrupt(format!(
                    "IPv6 exception {j} is start {i} of {}",
                    self.n_v6
                )));
            }
            if j > 0 && i <= u32_at(exc_idx, j * 4 - 4) {
                return Err(Error::corrupt(format!(
                    "IPv6 exception {j} (start {i}) is not above exception {}",
                    j - 1
                )));
            }
            if u64_at(exc_lo, j * 8) == 0 {
                return Err(Error::corrupt(format!(
                    "IPv6 exception {j} (start {i}) has a zero low half"
                )));
            }
        }
        let hi = self.bytes(b, SEC_V6STARTS);
        let mut j = 0usize;
        let mut prev: Option<(u64, u64)> = None;
        for i in 0..self.n_v6 {
            let h = u64_at(hi, i * 8);
            let mut l = 0u64;
            if j < self.n_exc && u32_at(exc_idx, j * 4) as usize == i {
                l = u64_at(exc_lo, j * 8);
                j += 1;
            }
            if let Some((ph, pl)) = prev {
                if h < ph {
                    return Err(Error::corrupt(format!(
                        "V6STARTS[{i}] is below V6STARTS[{}]",
                        i - 1
                    )));
                }
                if (ph, pl) >= (h, l) {
                    return Err(Error::corrupt(format!(
                        "IPv6 start {i} is not above start {}",
                        i - 1
                    )));
                }
            }
            prev = Some((h, l));
        }
        Ok(())
    }
}
