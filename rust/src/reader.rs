//! The reader: opening, the in-memory indexes and the lookups.

use std::fs::File;
use std::io::Read;
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};
use std::path::Path;
use std::sync::Arc;
use std::time::{Duration, SystemTime};

use crate::error::{Error, Result};
use crate::format::*;
use crate::geo2::{self, Span};
use crate::ranges::Ranges;
use crate::types::*;
use crate::{geo1, Asn, Country};

/// The bytes the sections live in: a memory mapping, or memory the reader
/// owns or was handed.
pub(crate) enum Backing {
    #[cfg(feature = "mmap")]
    Mmap(memmap2::Mmap),
    Bytes(Box<dyn AsRef<[u8]> + Send + Sync>),
}

impl Backing {
    #[inline]
    fn bytes(&self) -> &[u8] {
        match self {
            #[cfg(feature = "mmap")]
            Backing::Mmap(m) => m,
            Backing::Bytes(b) => (**b).as_ref(),
        }
    }
}

/// The string table, copied out of the file at open and checked to be UTF-8
/// once, so that lookups hand out `&str` without validating or allocating.
/// String `i` is `blob[off[i]..off[i + 1]]`.
pub(crate) struct Strings {
    blob: String,
    off: Box<[u32]>,
}

impl Strings {
    /// Builds the table from the strings in order. A string that is not
    /// valid UTF-8 is stored with U+FFFD in place of the invalid sequences
    /// (the Go reader keeps the bytes as they are).
    pub(crate) fn new<'b>(strings: impl ExactSizeIterator<Item = &'b [u8]>) -> Result<Strings> {
        let mut off = Vec::with_capacity(strings.len() + 1);
        let mut blob = String::new();
        off.push(0);
        for s in strings {
            blob.push_str(&String::from_utf8_lossy(s));
            let end = u32::try_from(blob.len()).map_err(|_| {
                Error::TooLarge("the string table is over 4 GiB once made UTF-8".into())
            })?;
            off.push(end);
        }
        Ok(Strings {
            blob,
            off: off.into_boxed_slice(),
        })
    }

    /// String `id`; `""` for an id past the table (never the case for a
    /// validated id).
    #[inline]
    pub(crate) fn get(&self, id: u32) -> &str {
        let i = id as usize;
        match (self.off.get(i), self.off.get(i.wrapping_add(1))) {
            (Some(&a), Some(&b)) => self.blob.get(a as usize..b as usize).unwrap_or(""),
            _ => "",
        }
    }
}

/// Where the lookup tables are in the backing bytes, in the GEO2 layout.
#[derive(Clone, Copy, Debug, Default)]
pub(crate) struct Tables {
    pub records: Span,
    pub v4idx: Span,
    pub v4starts: Span,
    pub v4ids: Span,
    pub v6hi: Span,
    pub v6ids: Span,
    pub v6exc_idx: Span,
    pub v6exc_lo: Span,
    pub xrecs: Span,
}

/// Everything about a validated database except the bytes it is in.
pub(crate) struct Parts {
    pub profile: Profile,
    pub id_width: u8,
    pub lat_scale: u8,
    pub tables: Tables,
    pub strings: Strings,
    pub fields: u32,
    pub xl: XLayout,
    pub langs: Vec<String>,
    pub meta: Metadata,
}

const V4_IDX_BITS: u32 = 20;
const V4_SHIFT: u32 = 32 - V4_IDX_BITS;
const V6_IDX_BITS: u32 = 16;

/// 64 top-16 IPv4 buckets, `w << 6` to `w << 6 | 63` for word `w`: which of
/// them hold a boundary, and the nearest non-empty buckets outside the word,
/// so that the non-empty bucket before or after any bucket is found without
/// scanning an empty run. `prev` and `next` are 0 when there is none
/// (`range4` never uses them then).
#[derive(Clone, Copy, Debug, Default)]
struct OccWord {
    full: u64,
    prev: u16,
    next: u16,
}

impl OccWord {
    /// The map of the 65 536 buckets, `nonempty(g)` telling bucket `g`.
    fn build(nonempty: impl Fn(usize) -> bool) -> Box<[OccWord]> {
        let mut o = vec![OccWord::default(); 1 << 10];
        for g in 0..1usize << 16 {
            if nonempty(g) {
                o[g >> 6].full |= 1 << (g & 63);
            }
        }
        let mut last = 0u16;
        for (w, e) in o.iter_mut().enumerate() {
            e.prev = last;
            if e.full != 0 {
                last = ((w << 6) as u16) | (63 - e.full.leading_zeros()) as u16;
            }
        }
        let mut first = 0u16;
        for (w, e) in o.iter_mut().enumerate().rev() {
            e.next = first;
            if e.full != 0 {
                first = ((w << 6) as u16) | e.full.trailing_zeros() as u16;
            }
        }
        o.into_boxed_slice()
    }
}

/// The shared, immutable state of a reader.
struct Inner {
    backing: Backing,
    t: Tables,
    profile: Profile,
    id_width: u8,
    rec_width: usize,
    lat_div: f64,
    strings: Strings,
    /// `lo4[w]` is the index of the first IPv4 boundary whose absolute
    /// start is `>= w << 12` (a top-20 window); `lo4[1 << 20] == nV4`.
    lo4: Box<[u32]>,
    /// Which top-16 buckets hold an IPv4 boundary, 64 to a word, with the
    /// nearest non-empty buckets outside each word.
    occ4: Box<[OccWord]>,
    /// `v6root[s]` is the index of the first IPv6 boundary whose top 16 bits
    /// are `>= s`; `v6root[1 << 16] == nV6`.
    v6root: Box<[u32]>,
    /// Bit `i` set when IPv6 boundary `i` has a non-zero low half.
    v6exc: Box<[u64]>,
    n_v4: usize,
    n_v6: usize,
    fields: u32,
    xl: XLayout,
    langs: Vec<String>,
    meta: Metadata,
}

/// A read-only handle to an ip2geo database (a `.geoc` file, GEO2 or GEO1).
///
/// A `Reader` is cheap to clone (the database is shared through an
/// [`Arc`]) and is `Send + Sync`: share it between threads freely. Lookups
/// allocate nothing; the strings in their results borrow from the reader.
///
/// ```no_run
/// use std::net::IpAddr;
///
/// let db = ip2geo::Reader::open("city.geoc")?;
/// let ip: IpAddr = "81.2.69.142".parse().unwrap();
/// if let Some(rec) = db.lookup(ip) {
///     println!("{} {} {} {}", rec.country_iso, rec.city, rec.latitude, rec.longitude);
/// }
/// # Ok::<(), ip2geo::Error>(())
/// ```
#[derive(Clone)]
pub struct Reader {
    inner: Arc<Inner>,
    /// Index into `langs` of the language names are returned in.
    lang: usize,
    /// When `lang != 0`: the XRECORDS offset of each name kind in that
    /// language, `None` when the file stores no such name.
    name_off: [Option<usize>; NUM_NAME_KINDS],
}

impl std::fmt::Debug for Reader {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Reader")
            .field("metadata", &self.inner.meta)
            .field("language", &self.language())
            .finish_non_exhaustive()
    }
}

/// Options for opening a database, in the style of [`std::fs::OpenOptions`].
///
/// ```no_run
/// let db = ip2geo::OpenOptions::new()
///     .language("de")
///     .skip_checksum(true)
///     .open("city.geoc")?;
/// # Ok::<(), ip2geo::Error>(())
/// ```
#[derive(Clone, Debug)]
pub struct OpenOptions {
    skip_checksum: bool,
    mmap: bool,
    language: String,
}

impl Default for OpenOptions {
    fn default() -> Self {
        OpenOptions::new()
    }
}

impl OpenOptions {
    /// The defaults: verify checksums, memory-map files, primary language.
    pub fn new() -> OpenOptions {
        OpenOptions {
            skip_checksum: false,
            mmap: true,
            language: String::new(),
        }
    }

    /// Skips the CRC-32C verification of a GEO2 file's sections, which reads
    /// every byte at open. Every other check still runs, so a damaged file
    /// cannot make a lookup fail or panic, but it may answer wrongly. GEO1
    /// files have no checksums.
    pub fn skip_checksum(&mut self, skip: bool) -> &mut Self {
        self.skip_checksum = skip;
        self
    }

    /// Whether [`open`](Self::open) memory-maps a regular file (the default,
    /// with the `mmap` feature) or reads it into memory. Ignored without the
    /// `mmap` feature and by [`from_bytes`](Self::from_bytes).
    pub fn mmap(&mut self, mmap: bool) -> &mut Self {
        self.mmap = mmap;
        self
    }

    /// The language of the names (one of [`Reader::languages`]); `""`, the
    /// default, is the primary one. Opening fails with
    /// [`Error::Language`] when the database does not have it.
    pub fn language(&mut self, lang: impl Into<String>) -> &mut Self {
        self.language = lang.into();
        self
    }

    /// Opens the database file at `path`.
    ///
    /// Files over 2 GiB are refused with [`Error::TooLarge`]. The header is
    /// checked before the rest is read, so a wrong path fails fast. A
    /// regular file is memory-mapped unless [`mmap(false)`](Self::mmap) was
    /// given or mapping fails; a GEO2 file stays mapped for as long as a
    /// clone of the reader lives, a GEO1 file is converted into memory and
    /// unmapped before `open` returns.
    ///
    /// A mapped file must not be modified or truncated while a reader uses
    /// it (replace it by writing a new file and renaming it over the old
    /// one, as the ip2geo builder does).
    pub fn open<P: AsRef<Path>>(&self, path: P) -> Result<Reader> {
        let mut f = File::open(path)?;
        let md = f.metadata()?;
        let size = md.len();
        if size > MAX_FILE_SIZE {
            return Err(Error::TooLarge(format!(
                "the file is {size} bytes, over the {MAX_FILE_SIZE}-byte limit"
            )));
        }
        let mut hdr = [0u8; HEADER_SIZE];
        let mut n = 0;
        while n < hdr.len() {
            match f.read(&mut hdr[n..]) {
                Ok(0) => break,
                Ok(k) => n += k,
                Err(e) if e.kind() == std::io::ErrorKind::Interrupted => {}
                Err(e) => return Err(e.into()),
            }
        }
        check_any_header(&hdr[..n], size, md.is_file())?;

        #[cfg(feature = "mmap")]
        if self.mmap && md.is_file() && size >= n as u64 {
            if let Ok(m) = map(&f) {
                return self.open_backing(Backing::Mmap(m));
            }
            // Mapping is not supported here or failed: read the file instead.
        }

        // The size is a hint: the file may change underfoot and special
        // files report 0. Read to EOF, but never past the limit.
        let cap = usize::try_from(size).unwrap_or(0).max(n);
        let mut b = Vec::with_capacity(cap);
        b.extend_from_slice(&hdr[..n]);
        (&mut f)
            .take(MAX_FILE_SIZE + 1 - n as u64)
            .read_to_end(&mut b)?;
        if b.len() as u64 > MAX_FILE_SIZE {
            return Err(Error::TooLarge(format!(
                "the file is over the {MAX_FILE_SIZE}-byte limit"
            )));
        }
        self.open_backing(Backing::Bytes(Box::new(b)))
    }

    /// Opens a database held in memory: a `Vec<u8>`, a `&'static [u8]`
    /// (for example from `include_bytes!`), an `Arc<[u8]>` or anything else
    /// that derefs to bytes. A GEO2 database is used in place, without a
    /// copy; a GEO1 one is converted into new memory and `bytes` dropped.
    ///
    /// ```
    /// // Not a database: the error says why.
    /// let err = ip2geo::OpenOptions::new().from_bytes(b"GEO3".to_vec()).unwrap_err();
    /// assert!(matches!(err, ip2geo::Error::BadMagic(_)));
    /// ```
    pub fn from_bytes<B>(&self, bytes: B) -> Result<Reader>
    where
        B: AsRef<[u8]> + Send + Sync + 'static,
    {
        let len = bytes.as_ref().len() as u64;
        if len > MAX_FILE_SIZE {
            return Err(Error::TooLarge(format!(
                "the input is {len} bytes, over the {MAX_FILE_SIZE}-byte limit"
            )));
        }
        self.open_backing(Backing::Bytes(Box::new(bytes)))
    }

    fn open_backing(&self, backing: Backing) -> Result<Reader> {
        let b = backing.bytes();
        let (parts, backing) = if b.starts_with(MAGIC2) {
            let g = geo2::parse(b, self.skip_checksum)?;
            (parts_from_geo2(&g, b)?, backing)
        } else if b.starts_with(MAGIC1) {
            let (parts, buf) = geo1::open(b)?;
            // The mapping or input is released here; the tables are in buf.
            (parts, Backing::Bytes(Box::new(buf)))
        } else {
            return Err(bad_magic(b));
        };
        let r = Reader::new(Inner::new(backing, parts));
        r.with_language(&self.language)
    }
}

/// Maps a file read-only.
#[cfg(feature = "mmap")]
#[allow(unsafe_code)]
fn map(f: &File) -> std::io::Result<memmap2::Mmap> {
    // SAFETY: memmap2's map is unsafe because the mapped file could be
    // modified or truncated by another process while mapped, which would
    // change bytes behind a `&[u8]` (or raise SIGBUS on truncation). The
    // reader documents that a mapped database must not be modified in
    // place (OpenOptions::open, crate docs), exactly as the Go reader
    // does; databases are replaced by renaming a new file over the old one.
    unsafe { memmap2::Mmap::map(f) }
}

fn bad_magic(h: &[u8]) -> Error {
    Error::BadMagic(format!(
        "magic {:?}, want \"GEO2\" or \"GEO1\"",
        String::from_utf8_lossy(&h[..h.len().min(4)])
    ))
}

/// Checks the first bytes of a file before the rest is read: the magic, then
/// the GEO1 header, or the GEO2 header and, for a regular file, that its
/// directory fits.
fn check_any_header(h: &[u8], size: u64, regular: bool) -> Result<()> {
    if h.starts_with(MAGIC2) {
        let (_, _, _, sections) = geo2::parse_header(h)?;
        let end = HEADER_SIZE as u64 + u64::from(sections) * DIR_ENTRY_SIZE as u64;
        if regular && end > size {
            return Err(Error::corrupt(format!(
                "directory of {sections} entries needs {end} bytes, the file has {size}"
            )));
        }
        return Ok(());
    }
    if !h.starts_with(MAGIC1) {
        return Err(bad_magic(h));
    }
    geo1::check_header(h)
}

fn unix_time(sec: u64) -> Option<SystemTime> {
    if sec == 0 || sec > i64::MAX as u64 {
        return None;
    }
    SystemTime::UNIX_EPOCH.checked_add(Duration::from_secs(sec))
}

fn parts_from_geo2(g: &geo2::Geo2, b: &[u8]) -> Result<Parts> {
    let idx = g.bytes(b, SEC_STRIDX);
    let blob = g.bytes(b, SEC_STRBLOB);
    let strings = Strings::new((0..g.n_strings).map(|i| {
        let (s, e) = (u32_at(idx, i * 4) as usize, u32_at(idx, i * 4 + 4) as usize);
        blob.get(s..e).unwrap_or(&[])
    }))?;
    let langs_sec = g.bytes(b, SEC_LANGS);
    let langs: Vec<String> = if !langs_sec.is_empty() {
        langs_sec
            .chunks_exact(4)
            .map(|c| strings.get(u32_at(c, 0)).to_string())
            .collect()
    } else {
        let l = strings.get(g.meta.language);
        if l.is_empty() {
            Vec::new()
        } else {
            vec![l.to_string()]
        }
    };
    let profile = Profile::from_u8(g.profile).ok_or_else(|| Error::corrupt("unknown profile"))?;
    let m = &g.meta;
    let meta = Metadata {
        format_version: VERSION2,
        profile,
        lat_scale: g.lat_scale,
        build_time: unix_time(m.build_time),
        source_time: unix_time(m.source_time),
        source_type: strings.get(m.source_type).to_string(),
        language: strings.get(m.language).to_string(),
        fields: FieldSet::from_bits_retain(g.fields),
        description: strings.get(m.description).to_string(),
        builder: strings.get(m.builder).to_string(),
        networks: m.networks,
        distinct_records: u64::from(m.distinct_records),
        records: g.n_records,
        strings: g.n_strings,
        ipv4_ranges: g.n_v4,
        ipv6_ranges: g.n_v6,
    };
    let s = |id: u32| g.sec[id as usize];
    Ok(Parts {
        profile,
        id_width: g.id_width,
        lat_scale: g.lat_scale,
        tables: Tables {
            records: s(SEC_RECORDS),
            v4idx: s(SEC_V4IDX),
            v4starts: s(SEC_V4STARTS),
            v4ids: s(SEC_V4IDS),
            v6hi: s(SEC_V6STARTS),
            v6ids: s(SEC_V6IDS),
            v6exc_idx: s(SEC_V6EXCIDX),
            v6exc_lo: s(SEC_V6EXCLO),
            xrecs: s(SEC_XRECORDS),
        },
        strings,
        fields: g.fields,
        xl: g.xl,
        langs,
        meta,
    })
}

impl Inner {
    /// Binds validated parts to their bytes and builds the indexes.
    fn new(backing: Backing, p: Parts) -> Inner {
        let b = backing.bytes();
        let t = p.tables;
        let n_v4 = t.v4starts.len / 2;
        let n_v6 = t.v6hi.len / 8;

        // lo4: top-16 bucket g holds boundaries V4IDX[g]..V4IDX[g+1]; its 16
        // top-20 windows split them on the top 4 bits of the u16 start.
        let v4idx = t.v4idx.of(b);
        let v4starts = t.v4starts.of(b);
        let mut lo4 = vec![0u32; (1 << V4_IDX_BITS) + 1];
        for g in 0..1usize << 16 {
            let mut i = u32_at(v4idx, g * 4) as usize;
            let end = (u32_at(v4idx, g * 4 + 4) as usize).min(n_v4);
            for k in 0..16usize {
                while i < end && usize::from(u16_at(v4starts, i * 2)) < k << V4_SHIFT {
                    i += 1;
                }
                lo4[g * 16 + k] = i as u32;
            }
        }
        lo4[1 << V4_IDX_BITS] = n_v4 as u32;
        let occ4 = OccWord::build(|g| u32_at(v4idx, g * 4) < u32_at(v4idx, g * 4 + 4));

        // v6root over the top 16 bits of the starts' top halves.
        let v6hi = t.v6hi.of(b);
        let mut v6root = vec![0u32; (1 << V6_IDX_BITS) + 1];
        let mut i = 0usize;
        for (s, e) in v6root.iter_mut().enumerate().take(1 << V6_IDX_BITS) {
            while i < n_v6 && (u64_at(v6hi, i * 8) >> 48) < s as u64 {
                i += 1;
            }
            *e = i as u32;
        }
        v6root[1 << V6_IDX_BITS] = n_v6 as u32;

        let mut v6exc = vec![0u64; n_v6.div_ceil(64)];
        for c in t.v6exc_idx.of(b).chunks_exact(4) {
            let i = u32_at(c, 0) as usize;
            if let Some(w) = v6exc.get_mut(i >> 6) {
                *w |= 1 << (i & 63);
            }
        }

        Inner {
            t,
            profile: p.profile,
            id_width: p.id_width,
            rec_width: record_width(p.profile as u8),
            lat_div: lat_div(p.lat_scale),
            strings: p.strings,
            lo4: lo4.into_boxed_slice(),
            occ4,
            v6root: v6root.into_boxed_slice(),
            v6exc: v6exc.into_boxed_slice(),
            n_v4,
            n_v6,
            fields: p.fields,
            xl: p.xl,
            langs: p.langs,
            meta: p.meta,
            backing,
        }
    }

    #[inline]
    fn bytes(&self) -> &[u8] {
        self.backing.bytes()
    }

    // ---- IPv4 ----

    /// The index of the IPv4 boundary holding `key`, `None` below the first.
    #[inline]
    fn index4(&self, key: u32) -> Option<usize> {
        let w = (key >> V4_SHIFT) as usize;
        let mut l = self.lo4.get(w).copied().unwrap_or(0) as usize;
        let mut h = self.lo4.get(w + 1).copied().unwrap_or(0) as usize;
        let k = key as u16;
        let s = self.t.v4starts.of(self.bytes());
        while l < h {
            let mid = l + (h - l) / 2;
            if u16_at(s, mid * 2) > k {
                h = mid;
            } else {
                l = mid + 1;
            }
        }
        l.checked_sub(1)
    }

    #[inline]
    fn search4(&self, key: u32) -> u32 {
        match self.index4(key) {
            Some(i) => read_id(self.t.v4ids.of(self.bytes()), self.id_width, i),
            None => 0,
        }
    }

    /// The absolute start of IPv4 boundary `i`, whose bucket is `bucket`.
    fn start4_in(&self, i: usize, bucket: u32) -> u32 {
        (bucket << 16) | u32::from(u16_at(self.t.v4starts.of(self.bytes()), i * 2))
    }

    /// The IPv4 boundary range holding `key` and its record id.
    ///
    /// Boundary `i`, the last at or below `key`, is in key's top-16 bucket
    /// `g` unless it is below the bucket's first boundary; then it is the
    /// last boundary of the last non-empty bucket before `g`. Boundary
    /// `i + 1` is in `g` unless it is at or past the bucket's end; then it
    /// is the first boundary of the first non-empty bucket after `g`. Both
    /// neighbouring buckets come from `occ4`, as in the Go reader.
    fn range4(&self, key: u32) -> (u32, u32, u32) {
        let g = key >> 16;
        let i = self.index4(key);
        let lo = self.lo4.get((g as usize) << 4).copied().unwrap_or(0) as usize;
        let hi = self.lo4.get((g as usize + 1) << 4).copied().unwrap_or(0) as usize;
        let o = self
            .occ4
            .get((g >> 6) as usize)
            .copied()
            .unwrap_or_default();
        let b = g & 63;
        let below = o.full & ((1u64 << b) - 1);
        let above = o.full & !(2u64 << b).wrapping_sub(1); // 0 for b == 63
        let prev = if below != 0 {
            (g & !63) | (63 - below.leading_zeros())
        } else {
            u32::from(o.prev)
        };
        let next_bucket = if above != 0 {
            (g & !63) | above.trailing_zeros()
        } else {
            u32::from(o.next)
        };

        let (mut first, mut last, mut id) = (0u32, u32::MAX, 0u32);
        if let Some(i) = i {
            let bucket = if i < lo { prev } else { g };
            first = self.start4_in(i, bucket);
            id = read_id(self.t.v4ids.of(self.bytes()), self.id_width, i);
        }
        let next = i.map_or(0, |i| i + 1);
        if next < self.n_v4 {
            let bucket = if next >= hi { next_bucket } else { g };
            last = self.start4_in(next, bucket).wrapping_sub(1);
        }
        (first, last, id)
    }

    // ---- IPv6 ----

    #[inline]
    fn exc(&self, i: usize) -> bool {
        self.v6exc
            .get(i >> 6)
            .is_some_and(|w| w & (1 << (i & 63)) != 0)
    }

    /// The low half of IPv6 boundary `i`, which must be an exception.
    fn exc_lo(&self, i: usize) -> u64 {
        let b = self.bytes();
        let idx = self.t.v6exc_idx.of(b);
        let (mut l, mut h) = (0usize, idx.len() / 4);
        while l < h {
            let mid = l + (h - l) / 2;
            if (u32_at(idx, mid * 4) as usize) < i {
                l = mid + 1;
            } else {
                h = mid;
            }
        }
        u64_at(self.t.v6exc_lo.of(b), l * 8)
    }

    /// The 128-bit start of IPv6 boundary `i`.
    pub(crate) fn start6(&self, i: usize) -> u128 {
        let hi = u64_at(self.t.v6hi.of(self.bytes()), i * 8);
        let lo = if self.exc(i) { self.exc_lo(i) } else { 0 };
        (u128::from(hi) << 64) | u128::from(lo)
    }

    /// The index of the IPv6 boundary holding `key`, `None` below the first.
    #[inline]
    fn index6(&self, key: u128) -> Option<usize> {
        let (kh, kl) = ((key >> 64) as u64, key as u64);
        let s = (kh >> (64 - V6_IDX_BITS)) as usize;
        let mut l = self.v6root.get(s).copied().unwrap_or(0) as usize;
        let mut h = self.v6root.get(s + 1).copied().unwrap_or(0) as usize;
        let v = self.t.v6hi.of(self.bytes());
        while l < h {
            let mid = l + (h - l) / 2;
            if u64_at(v, mid * 8) > kh {
                h = mid;
            } else {
                l = mid + 1;
            }
        }
        // Starts sharing the key's top half are ordered by their low half,
        // which is 0 except for the exceptions: step back over those above
        // the key.
        let mut i = l.checked_sub(1)?;
        while u64_at(v, i * 8) == kh && self.exc(i) && self.exc_lo(i) > kl {
            i = i.checked_sub(1)?;
        }
        Some(i)
    }

    #[inline]
    fn search6(&self, key: u128) -> u32 {
        match self.index6(key) {
            Some(i) => read_id(self.t.v6ids.of(self.bytes()), self.id_width, i),
            None => 0,
        }
    }

    /// The IPv6 boundary range holding `key` (not in an alias prefix) and
    /// its record id, cut short at the alias prefixes.
    fn range6(&self, key: u128) -> (u128, u128, u32) {
        let i = self.index6(key);
        let (mut first, mut last, mut id) = (0u128, u128::MAX, 0u32);
        if let Some(i) = i {
            first = self.start6(i);
            id = read_id(self.t.v6ids.of(self.bytes()), self.id_width, i);
        }
        let next = i.map_or(0, |i| i + 1);
        if next < self.n_v6 {
            last = self.start6(next).wrapping_sub(1);
        }
        for &(af, al) in &V6_ALIASES {
            if al < key && al >= first {
                first = al + 1;
            }
            if key < af && last >= af {
                last = af - 1;
            }
        }
        (first, last, id)
    }

    // ---- decoding ----

    /// The record id of `key`, 0 when it is not covered.
    #[inline]
    fn find(&self, key: Key) -> u32 {
        match key {
            Key::V4(k) => self.search4(k),
            Key::V6(k) => self.search6(k),
        }
    }

    /// Decodes record `id` (a geo profile) into a record.
    #[inline]
    fn decode(&self, id: u32) -> Record<'_> {
        let mut rec = Record::default();
        let off = id as usize * self.rec_width;
        let all = self.t.records.of(self.bytes());
        let r = all.get(off..off + self.rec_width).unwrap_or(&[]);
        let s = &self.strings;
        rec.country_iso = s.get(u32::from(u16_at(r, REC_COUNTRY_ISO)));
        rec.country_name = s.get(u32::from(u16_at(r, REC_COUNTRY_NAME)));
        rec.continent = s.get(u32::from(u16_at(r, REC_CONTINENT)));
        rec.country_geoname_id = u32_at(r, REC_COUNTRY_GEO);
        if self.profile as u8 >= Profile::Region as u8 {
            rec.region_iso = s.get(u32::from(u16_at(r, REC_REGION_ISO)));
            rec.region_name = s.get(u32::from(u16_at(r, REC_REGION_NAME)));
        }
        if self.profile == Profile::City {
            rec.city = s.get(u24_at(r, REC_CITY_NAME));
            rec.city_geoname_id = u32_at(r, REC_CITY_GEO);
            rec.latitude = f64::from(u32_at(r, REC_LAT) as i32) / self.lat_div;
            rec.longitude = f64::from(u32_at(r, REC_LON) as i32) / self.lat_div;
            rec.accuracy_radius = u16_at(r, REC_ACCURACY);
            rec.time_zone = s.get(u16_at(r, REC_TIME_ZONE).into());
        }
        rec
    }

    /// Decodes ASN record `id`.
    fn asn(&self, id: u32) -> Asn<'_> {
        let off = id as usize * REC_WIDTH_ASN;
        let all = self.t.records.of(self.bytes());
        let r = all.get(off..off + REC_WIDTH_ASN).unwrap_or(&[]);
        Asn {
            number: u32_at(r, REC_ASN_NUMBER),
            organization: self.strings.get(u24_at(r, REC_ASN_ORG)),
        }
    }

    /// The XRECORDS entry of record `id`, empty without XRECORDS.
    fn xentry(&self, id: u32) -> &[u8] {
        let w = self.xl.width;
        let off = id as usize * w;
        self.t
            .xrecs
            .of(self.bytes())
            .get(off..off + w)
            .unwrap_or(&[])
    }
}

/// The IPv6 prefixes looked up as IPv4, ascending, as (first, last):
/// `::/96`, `::ffff:0:0/96`, `2001::/32` (Teredo) and `2002::/16` (6to4).
pub(crate) const V6_ALIASES: [(u128, u128); 4] = [
    (0, 0xFFFF_FFFF),
    (0xFFFF_0000_0000, 0xFFFF_FFFF_FFFF),
    (0x2001_0000 << 96, (0x2001_0000 << 96) | ((1 << 96) - 1)),
    (0x2002 << 112, (0x2002 << 112) | ((1 << 112) - 1)),
];

/// A search key: an IPv4 address, or an IPv6 one that is not an alias of
/// IPv4 space.
#[derive(Clone, Copy)]
enum Key {
    V4(u32),
    V6(u128),
}

/// The IPv4 address an IPv6 address is aliased to: `::ffff:0:0/96`,
/// `::/96`, `2002::/16` (bits 16-47) and `2001::/32` (bits 32-63). NAT64
/// `64:ff9b::/96` is not mapped.
#[inline]
fn key_of(ip: IpAddr) -> Key {
    match ip {
        IpAddr::V4(a) => Key::V4(u32::from(a)),
        IpAddr::V6(a) => {
            if let Some(v4) = a.to_ipv4_mapped() {
                return Key::V4(u32::from(v4));
            }
            let k = u128::from(a);
            let hi = (k >> 64) as u64;
            if hi >> 48 == 0x2002 {
                Key::V4((hi >> 16) as u32)
            } else if hi >> 32 == 0x2001_0000 {
                Key::V4(hi as u32)
            } else if k >> 32 == 0 {
                Key::V4(k as u32)
            } else {
                Key::V6(k)
            }
        }
    }
}

impl Reader {
    fn new(inner: Inner) -> Reader {
        Reader {
            inner: Arc::new(inner),
            lang: 0,
            name_off: [None; NUM_NAME_KINDS],
        }
    }

    /// Opens the database file at `path` with the default
    /// [`OpenOptions`]: checksums verified, memory-mapped, primary language.
    pub fn open<P: AsRef<Path>>(path: P) -> Result<Reader> {
        OpenOptions::new().open(path)
    }

    /// Opens a database held in memory with the default [`OpenOptions`];
    /// see [`OpenOptions::from_bytes`].
    pub fn from_bytes<B>(bytes: B) -> Result<Reader>
    where
        B: AsRef<[u8]> + Send + Sync + 'static,
    {
        OpenOptions::new().from_bytes(bytes)
    }

    /// The profile the database was built with.
    pub fn profile(&self) -> Profile {
        self.inner.profile
    }

    /// The file's header and META fields and its table sizes.
    pub fn metadata(&self) -> &Metadata {
        &self.inner.meta
    }

    /// The optional field sets the database holds.
    pub fn fields(&self) -> FieldSet {
        FieldSet::from_bits_retain(self.inner.fields)
    }

    /// The languages of the names, the primary one first. Empty for a GEO1
    /// database, which records none.
    pub fn languages(&self) -> &[String] {
        &self.inner.langs
    }

    /// The language this reader returns names in; `""` when the database
    /// records none.
    pub fn language(&self) -> &str {
        self.inner.langs.get(self.lang).map_or("", String::as_str)
    }

    /// A reader for the same database whose names are in `lang`, one of
    /// [`languages`](Self::languages) (`""` for the primary one). A name
    /// missing in `lang` is returned in the primary language. It shares
    /// the database and is cheap: make one per language, not per lookup.
    ///
    /// Fails with [`Error::Language`] when the database does not have `lang`.
    pub fn with_language(&self, lang: &str) -> Result<Reader> {
        let langs = &self.inner.langs;
        let j = if lang.is_empty() {
            0
        } else {
            langs.iter().position(|l| l == lang).ok_or_else(|| {
                let have = if langs.is_empty() {
                    "records none".to_string()
                } else {
                    format!("has {}", langs.join(", "))
                };
                Error::Language(format!("language {lang:?}: the database {have}"))
            })?
        };
        let mut r = self.clone();
        r.lang = j;
        for (k, o) in r.name_off.iter_mut().enumerate() {
            *o = if j > 0 {
                self.inner.xl.name_off(j, k)
            } else {
                None
            };
        }
        Ok(r)
    }

    /// Looks up `ip`. `None` when the address is not covered. A covered
    /// address whose record has every field empty is `Some` with an empty
    /// record; in a [`Profile::Asn`] database a covered address is `Some`
    /// with an empty record (use [`asn`](Self::asn)).
    ///
    /// IPv4 addresses in IPv6 form are looked up in the IPv4 table, as
    /// MaxMind databases alias them: `::ffff:0:0/96`, `::/96`, `2002::/16`
    /// (6to4, bits 16-47) and `2001::/32` (Teredo server, bits 32-63).
    /// NAT64 `64:ff9b::/96` is not mapped.
    #[inline]
    pub fn lookup(&self, ip: IpAddr) -> Option<Record<'_>> {
        let inner = &*self.inner;
        let id = inner.find(key_of(ip));
        if id == 0 {
            return None;
        }
        if inner.profile == Profile::Asn {
            return Some(Record::default());
        }
        let mut rec = inner.decode(id);
        if self.lang != 0 {
            let x = inner.xentry(id);
            self.localize(x, NAME_COUNTRY, &mut rec.country_name);
            self.localize(x, NAME_REGION, &mut rec.region_name);
            self.localize(x, NAME_CITY, &mut rec.city);
        }
        Some(rec)
    }

    /// [`lookup`](Self::lookup) with every optional field: the record plus
    /// the field sets the database holds, and the ASN of a
    /// [`Profile::Asn`] database.
    pub fn lookup_details(&self, ip: IpAddr) -> Option<Details<'_>> {
        match self.inner.find(key_of(ip)) {
            0 => None,
            id => Some(self.details(id)),
        }
    }

    /// The autonomous system of `ip`. `None` when it is not covered, and
    /// always for a database that is not [`Profile::Asn`].
    pub fn asn(&self, ip: IpAddr) -> Option<Asn<'_>> {
        if self.inner.profile != Profile::Asn {
            return None;
        }
        match self.inner.find(key_of(ip)) {
            0 => None,
            id => Some(self.inner.asn(id)),
        }
    }

    /// The range of addresses around `ip` that share its answer: the stored
    /// boundary range that holds it, and whether that range is covered (as
    /// [`lookup`](Self::lookup)'s `Some`); an uncovered gap is returned with
    /// `false`. For an IPv4 address, and the IPv6 forms looked up as IPv4,
    /// the range is the IPv4 one, in IPv4 form. An IPv6 range stops short of
    /// those IPv6 forms, so every address in it has the same answer.
    pub fn lookup_range(&self, ip: IpAddr) -> (IpRange, bool) {
        match key_of(ip) {
            Key::V4(k) => {
                let (f, l, id) = self.inner.range4(k);
                (
                    IpRange {
                        first: IpAddr::V4(Ipv4Addr::from(f)),
                        last: IpAddr::V4(Ipv4Addr::from(l)),
                    },
                    id != 0,
                )
            }
            Key::V6(k) => {
                let (f, l, id) = self.inner.range6(k);
                (
                    IpRange {
                        first: IpAddr::V6(Ipv6Addr::from(f)),
                        last: IpAddr::V6(Ipv6Addr::from(l)),
                    },
                    id != 0,
                )
            }
        }
    }

    /// Iterates over the covered ranges in ascending order, the IPv4 ones
    /// (in IPv4 form) before the IPv6 ones, each with its details as
    /// [`lookup_details`](Self::lookup_details) returns them. Gaps are
    /// skipped; adjacent ranges may share a record. The IPv6 ranges leave
    /// out the prefixes looked up as IPv4, so a stored range across one
    /// comes in parts.
    pub fn ranges(&self) -> Ranges<'_> {
        Ranges::new(self)
    }

    // ---- crate internals used by the ranges iterator ----

    pub(crate) fn n_v4(&self) -> usize {
        self.inner.n_v4
    }

    pub(crate) fn n_v6(&self) -> usize {
        self.inner.n_v6
    }

    pub(crate) fn v4idx_at(&self, g: usize) -> u32 {
        u32_at(self.inner.t.v4idx.of(self.inner.bytes()), g * 4)
    }

    pub(crate) fn v4start_low(&self, i: usize) -> u16 {
        u16_at(self.inner.t.v4starts.of(self.inner.bytes()), i * 2)
    }

    pub(crate) fn v4id(&self, i: usize) -> u32 {
        read_id(
            self.inner.t.v4ids.of(self.inner.bytes()),
            self.inner.id_width,
            i,
        )
    }

    pub(crate) fn v6id(&self, i: usize) -> u32 {
        read_id(
            self.inner.t.v6ids.of(self.inner.bytes()),
            self.inner.id_width,
            i,
        )
    }

    pub(crate) fn v6hi_at(&self, i: usize) -> u64 {
        u64_at(self.inner.t.v6hi.of(self.inner.bytes()), i * 8)
    }

    pub(crate) fn n_exc(&self) -> usize {
        self.inner.t.v6exc_idx.len / 4
    }

    pub(crate) fn exc_entry(&self, j: usize) -> (usize, u64) {
        let b = self.inner.bytes();
        (
            u32_at(self.inner.t.v6exc_idx.of(b), j * 4) as usize,
            u64_at(self.inner.t.v6exc_lo.of(b), j * 8),
        )
    }

    /// Sets `*s` to the name of kind `k` in this reader's language from
    /// XRECORDS entry `x`, unless the entry has none (id 0).
    #[inline]
    fn localize<'a>(&'a self, x: &[u8], k: usize, s: &mut &'a str) {
        if let Some(off) = self.name_off[k] {
            let id = u24_at(x, off);
            if id != 0 {
                *s = self.inner.strings.get(id);
            }
        }
    }

    /// Decodes record `id` with every optional field, names in this
    /// reader's language.
    pub(crate) fn details(&self, id: u32) -> Details<'_> {
        let inner = &*self.inner;
        let mut d = Details::default();
        if inner.profile == Profile::Asn {
            d.asn = inner.asn(id);
            return d;
        }
        d.record = inner.decode(id);
        let l = &inner.xl;
        if l.width == 0 {
            return d;
        }
        let x = inner.xentry(id);
        let s = &inner.strings;
        let str24 = |o: usize| s.get(u24_at(x, o));
        let f = inner.fields;
        let block = |set: u32| l.block(set).filter(|_| f & set != 0);
        if let Some(o) = block(FIELD_EU) {
            d.is_in_european_union = u8_at(x, o + X_EU_FLAGS) & X_FLAG_EU != 0;
        }
        if let Some(o) = block(FIELD_CONTINENT) {
            d.continent_name = str24(o + X_CONTINENT_NAME);
            d.continent_geoname_id = u32_at(x, o + X_CONTINENT_GEO);
        }
        if let Some(o) = block(FIELD_REGISTERED) {
            let fl = u8_at(x, o + X_REGISTERED_FLAGS);
            d.registered_country = Country {
                iso: str24(o + X_REGISTERED_ISO),
                name: str24(o + X_REGISTERED_NAME),
                geoname_id: u32_at(x, o + X_REGISTERED_GEO),
                is_in_european_union: fl & X_FLAG_REGISTERED_EU != 0,
            };
            d.represented_country = Country {
                iso: str24(o + X_REPRESENTED_ISO),
                name: str24(o + X_REPRESENTED_NAME),
                geoname_id: u32_at(x, o + X_REPRESENTED_GEO),
                is_in_european_union: fl & X_FLAG_REPRESENTED_EU != 0,
            };
            d.represented_country_type = str24(o + X_REPRESENTED_TYPE);
        }
        if let Some(o) = block(FIELD_SUBDIVISIONS) {
            d.region_geoname_id = u32_at(x, o + X_REGION_GEO);
            d.region2_iso = str24(o + X_REGION2_ISO);
            d.region2_name = str24(o + X_REGION2_NAME);
            d.region2_geoname_id = u32_at(x, o + X_REGION2_GEO);
        }
        if let Some(o) = block(FIELD_POSTAL) {
            d.postal_code = str24(o + X_POSTAL);
        }
        if let Some(o) = block(FIELD_METRO) {
            d.metro_code = u16_at(x, o + X_METRO);
        }
        if let Some(o) = block(FIELD_TRAITS) {
            let fl = u8_at(x, o + X_TRAITS_FLAGS);
            d.is_anycast = fl & X_FLAG_ANYCAST != 0;
            d.is_anonymous_proxy = fl & X_FLAG_ANONYMOUS_PROXY != 0;
            d.is_satellite_provider = fl & X_FLAG_SATELLITE != 0;
        }
        if let Some(o) = block(FIELD_LOCATION) {
            let fl = u8_at(x, o + X_LOC_FLAGS);
            d.has_coordinates = fl & X_FLAG_HAS_COORDINATES != 0;
            d.has_accuracy_radius = fl & X_FLAG_HAS_ACCURACY != 0;
        }
        if self.lang != 0 {
            self.localize(x, NAME_COUNTRY, &mut d.record.country_name);
            self.localize(x, NAME_REGION, &mut d.record.region_name);
            self.localize(x, NAME_CITY, &mut d.record.city);
            self.localize(x, NAME_CONTINENT, &mut d.continent_name);
            self.localize(x, NAME_REGION2, &mut d.region2_name);
            self.localize(x, NAME_REGISTERED, &mut d.registered_country.name);
            self.localize(x, NAME_REPRESENTED, &mut d.represented_country.name);
        }
        d
    }
}
