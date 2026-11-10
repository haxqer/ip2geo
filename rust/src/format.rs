//! Byte-level constants and helpers of the GEO2 format (docs/FORMAT.md),
//! after the Go package `internal/format`.

pub(crate) const MAGIC2: &[u8; 4] = b"GEO2";
pub(crate) const MAGIC1: &[u8; 4] = b"GEO1";
pub(crate) const VERSION2: u8 = 2;
pub(crate) const VERSION1: u8 = 1;
pub(crate) const HEADER_SIZE: usize = 32;
pub(crate) const GEO1_HEADER_SIZE: usize = 8;
pub(crate) const DIR_ENTRY_SIZE: usize = 24;
pub(crate) const META_SIZE: usize = 64;
pub(crate) const MAX_LAT_SCALE: u8 = 7;
pub(crate) const PROFILE_ASN: u8 = 3;
pub(crate) const MAX_PROFILE: u8 = PROFILE_ASN;
/// Entries of V4IDX: one per top-16-bit bucket plus the sentinel `nV4`.
pub(crate) const V4IDX_ENTRIES: usize = (1 << 16) + 1;
/// The largest input accepted, as by the Go reader.
pub(crate) const MAX_FILE_SIZE: u64 = 1 << 31;

// Section ids.
pub(crate) const SEC_META: u32 = 1;
pub(crate) const SEC_STRIDX: u32 = 2;
pub(crate) const SEC_STRBLOB: u32 = 3;
pub(crate) const SEC_RECORDS: u32 = 4;
pub(crate) const SEC_V4IDX: u32 = 5;
pub(crate) const SEC_V4STARTS: u32 = 6;
pub(crate) const SEC_V4IDS: u32 = 7;
pub(crate) const SEC_V6STARTS: u32 = 8;
pub(crate) const SEC_V6IDS: u32 = 9;
pub(crate) const SEC_V6EXCIDX: u32 = 10;
pub(crate) const SEC_V6EXCLO: u32 = 11;
pub(crate) const SEC_XFIELDS: u32 = 12;
pub(crate) const SEC_XRECORDS: u32 = 13;
pub(crate) const SEC_LANGS: u32 = 14;
/// One more than the largest known section id.
pub(crate) const NUM_SECTIONS: usize = 15;

pub(crate) fn section_name(id: u32) -> String {
    const NAMES: [&str; NUM_SECTIONS] = [
        "", "META", "STRIDX", "STRBLOB", "RECORDS", "V4IDX", "V4STARTS", "V4IDS", "V6STARTS",
        "V6IDS", "V6EXCIDX", "V6EXCLO", "XFIELDS", "XRECORDS", "LANGS",
    ];
    match NAMES.get(id as usize) {
        Some(n) if !n.is_empty() => (*n).to_string(),
        _ => format!("id {id}"),
    }
}

pub(crate) const XFIELDS_SIZE: usize = 8;

// Packed record layout (little-endian, unaligned), byte offsets.
pub(crate) const REC_COUNTRY_ISO: usize = 0;
pub(crate) const REC_COUNTRY_NAME: usize = 2;
pub(crate) const REC_CONTINENT: usize = 4;
pub(crate) const REC_COUNTRY_GEO: usize = 6;
pub(crate) const REC_REGION_ISO: usize = 10;
pub(crate) const REC_REGION_NAME: usize = 12;
pub(crate) const REC_CITY_NAME: usize = 14;
pub(crate) const REC_CITY_GEO: usize = 17;
pub(crate) const REC_LAT: usize = 21;
pub(crate) const REC_LON: usize = 25;
pub(crate) const REC_ACCURACY: usize = 29;
pub(crate) const REC_TIME_ZONE: usize = 31;
pub(crate) const REC_ASN_NUMBER: usize = 0;
pub(crate) const REC_ASN_ORG: usize = 4;

pub(crate) const REC_WIDTH_COUNTRY: usize = 10;
pub(crate) const REC_WIDTH_REGION: usize = 14;
pub(crate) const REC_WIDTH_CITY: usize = 33;
pub(crate) const REC_WIDTH_ASN: usize = 7;

/// The packed GEO2 record width of a header profile, 0 for an unknown one.
pub(crate) fn record_width(profile: u8) -> usize {
    match profile {
        0 => REC_WIDTH_COUNTRY,
        1 => REC_WIDTH_REGION,
        2 => REC_WIDTH_CITY,
        PROFILE_ASN => REC_WIDTH_ASN,
        _ => 0,
    }
}

/// The smallest number of bytes (1..=4) that holds every id below `n_records`.
pub(crate) fn id_width(n_records: u64) -> u8 {
    let max = n_records.saturating_sub(1);
    if n_records == 0 || max < 1 << 8 {
        1
    } else if max < 1 << 16 {
        2
    } else if max < 1 << 24 {
        3
    } else {
        4
    }
}

/// `10^scale` for the coordinate scales 0..=7, exact in f64.
pub(crate) fn lat_div(scale: u8) -> f64 {
    const P: [f64; 8] = [1.0, 10.0, 100.0, 1e3, 1e4, 1e5, 1e6, 1e7];
    P.get(usize::from(scale)).copied().unwrap_or(1.0)
}

// Little-endian loads. Out-of-range reads return 0 instead of panicking:
// every read the reader makes was proven in range at open, so this only
// keeps a bug from turning into a panic.

#[inline]
pub(crate) fn u16_at(b: &[u8], off: usize) -> u16 {
    match b.get(off..off.wrapping_add(2)) {
        Some(&[x0, x1]) => u16::from_le_bytes([x0, x1]),
        _ => 0,
    }
}

#[inline]
pub(crate) fn u24_at(b: &[u8], off: usize) -> u32 {
    match b.get(off..off.wrapping_add(3)) {
        Some(&[x0, x1, x2]) => u32::from_le_bytes([x0, x1, x2, 0]),
        _ => 0,
    }
}

#[inline]
pub(crate) fn u32_at(b: &[u8], off: usize) -> u32 {
    match b.get(off..off.wrapping_add(4)) {
        Some(&[x0, x1, x2, x3]) => u32::from_le_bytes([x0, x1, x2, x3]),
        _ => 0,
    }
}

#[inline]
pub(crate) fn u64_at(b: &[u8], off: usize) -> u64 {
    match b.get(off..off.wrapping_add(8)) {
        Some(s) => {
            let mut a = [0u8; 8];
            a.copy_from_slice(s);
            u64::from_le_bytes(a)
        }
        None => 0,
    }
}

#[inline]
pub(crate) fn u8_at(b: &[u8], off: usize) -> u8 {
    b.get(off).copied().unwrap_or(0)
}

/// Record id `i` of an id array of `width` bytes per id.
#[inline]
pub(crate) fn read_id(ids: &[u8], width: u8, i: usize) -> u32 {
    match width {
        1 => u32::from(u8_at(ids, i)),
        2 => u32::from(u16_at(ids, i.wrapping_mul(2))),
        3 => u24_at(ids, i.wrapping_mul(3)),
        _ => u32_at(ids, i.wrapping_mul(4)),
    }
}

/// Appends `id` to `b` in `width` bytes.
pub(crate) fn push_id(b: &mut Vec<u8>, width: u8, id: u32) {
    b.extend_from_slice(&id.to_le_bytes()[..usize::from(width.clamp(1, 4))]);
}

// Extension field sets (XFIELDS bits).
pub(crate) const FIELD_EU: u32 = 1 << 0;
pub(crate) const FIELD_CONTINENT: u32 = 1 << 1;
pub(crate) const FIELD_REGISTERED: u32 = 1 << 2;
pub(crate) const FIELD_SUBDIVISIONS: u32 = 1 << 3;
pub(crate) const FIELD_POSTAL: u32 = 1 << 4;
pub(crate) const FIELD_METRO: u32 = 1 << 5;
pub(crate) const FIELD_TRAITS: u32 = 1 << 6;
pub(crate) const FIELD_LOCATION: u32 = 1 << 7;
pub(crate) const KNOWN_FIELDS: u32 = (1 << 8) - 1;
const NUM_FIELDS: usize = 8;
/// Block width of field set `1 << b`.
const FIELD_WIDTH: [usize; NUM_FIELDS] = [1, 7, 24, 14, 3, 2, 1, 1];

// Offsets inside the blocks.
pub(crate) const X_EU_FLAGS: usize = 0;
pub(crate) const X_CONTINENT_NAME: usize = 0;
pub(crate) const X_CONTINENT_GEO: usize = 3;
pub(crate) const X_REGISTERED_ISO: usize = 0;
pub(crate) const X_REGISTERED_NAME: usize = 3;
pub(crate) const X_REGISTERED_GEO: usize = 6;
pub(crate) const X_REPRESENTED_ISO: usize = 10;
pub(crate) const X_REPRESENTED_NAME: usize = 13;
pub(crate) const X_REPRESENTED_GEO: usize = 16;
pub(crate) const X_REPRESENTED_TYPE: usize = 20;
pub(crate) const X_REGISTERED_FLAGS: usize = 23;
pub(crate) const X_REGION_GEO: usize = 0;
pub(crate) const X_REGION2_ISO: usize = 4;
pub(crate) const X_REGION2_NAME: usize = 7;
pub(crate) const X_REGION2_GEO: usize = 10;
pub(crate) const X_POSTAL: usize = 0;
pub(crate) const X_METRO: usize = 0;
pub(crate) const X_TRAITS_FLAGS: usize = 0;
pub(crate) const X_LOC_FLAGS: usize = 0;

// Flag bits.
pub(crate) const X_FLAG_EU: u8 = 1 << 0;
pub(crate) const X_FLAG_REGISTERED_EU: u8 = 1 << 0;
pub(crate) const X_FLAG_REPRESENTED_EU: u8 = 1 << 1;
pub(crate) const X_FLAG_ANYCAST: u8 = 1 << 0;
pub(crate) const X_FLAG_ANONYMOUS_PROXY: u8 = 1 << 1;
pub(crate) const X_FLAG_SATELLITE: u8 = 1 << 2;
pub(crate) const X_FLAG_HAS_COORDINATES: u8 = 1 << 0;
pub(crate) const X_FLAG_HAS_ACCURACY: u8 = 1 << 1;

/// Where the flags byte of field set `f` is in its block and which bits are
/// defined, for the sets that have one.
pub(crate) fn flag_byte(f: u32) -> Option<(usize, u8)> {
    match f {
        FIELD_EU => Some((X_EU_FLAGS, X_FLAG_EU)),
        FIELD_REGISTERED => Some((
            X_REGISTERED_FLAGS,
            X_FLAG_REGISTERED_EU | X_FLAG_REPRESENTED_EU,
        )),
        FIELD_TRAITS => Some((
            X_TRAITS_FLAGS,
            X_FLAG_ANYCAST | X_FLAG_ANONYMOUS_PROXY | X_FLAG_SATELLITE,
        )),
        FIELD_LOCATION => Some((X_LOC_FLAGS, X_FLAG_HAS_COORDINATES | X_FLAG_HAS_ACCURACY)),
        _ => None,
    }
}

// Name kinds, in the order an extra language stores them.
pub(crate) const NAME_COUNTRY: usize = 0;
pub(crate) const NAME_REGION: usize = 1;
pub(crate) const NAME_CITY: usize = 2;
pub(crate) const NAME_CONTINENT: usize = 3;
pub(crate) const NAME_REGION2: usize = 4;
pub(crate) const NAME_REGISTERED: usize = 5;
pub(crate) const NAME_REPRESENTED: usize = 6;
pub(crate) const NUM_NAME_KINDS: usize = 7;

/// Whether field set bits are valid for a header profile.
pub(crate) fn check_fields(profile: u8, fields: u32) -> bool {
    if fields & !KNOWN_FIELDS != 0 {
        false
    } else if profile == PROFILE_ASN {
        fields == 0
    } else if fields & FIELD_SUBDIVISIONS != 0 && profile < 1 {
        false
    } else {
        !(fields & (FIELD_POSTAL | FIELD_METRO | FIELD_LOCATION) != 0 && profile < 2)
    }
}

/// The layout of one XRECORDS entry for a profile, a field set and a number
/// of languages (the primary one included).
#[derive(Clone, Copy, Debug, Default)]
pub(crate) struct XLayout {
    /// The entry width in bytes.
    pub width: usize,
    /// `field[b]` is the offset of field set `1 << b`'s block, if present.
    pub field: [Option<usize>; NUM_FIELDS],
    /// The name kinds each extra language stores, in order.
    kinds: [usize; NUM_NAME_KINDS],
    n_kinds: usize,
    /// The offset of the first extra language's names.
    pub lang_off: usize,
}

impl XLayout {
    /// The layout for valid field set bits and `n_langs >= 1` languages. The
    /// caller has bounded the resulting width by a section length.
    pub(crate) fn new(profile: u8, fields: u32, n_langs: usize) -> XLayout {
        let mut l = XLayout::default();
        for (b, w) in FIELD_WIDTH.iter().enumerate() {
            if fields & (1 << b) != 0 {
                l.field[b] = Some(l.width);
                l.width += w;
            }
        }
        l.lang_off = l.width;
        if profile == PROFILE_ASN {
            return l;
        }
        let mut push = |k: usize| {
            l.kinds[l.n_kinds] = k;
            l.n_kinds += 1;
        };
        push(NAME_COUNTRY);
        if profile >= 1 {
            push(NAME_REGION);
        }
        if profile >= 2 {
            push(NAME_CITY);
        }
        if fields & FIELD_CONTINENT != 0 {
            push(NAME_CONTINENT);
        }
        if fields & FIELD_SUBDIVISIONS != 0 {
            push(NAME_REGION2);
        }
        if fields & FIELD_REGISTERED != 0 {
            push(NAME_REGISTERED);
            push(NAME_REPRESENTED);
        }
        l.width += n_langs.saturating_sub(1) * 3 * l.n_kinds;
        l
    }

    /// The number of name kinds an extra language stores.
    pub(crate) fn n_kinds(&self) -> usize {
        self.n_kinds
    }

    /// The offset of name kind `k` for extra language `j` (index `j` of
    /// LANGS, so `j >= 1`), or `None` when the layout has no such name.
    pub(crate) fn name_off(&self, j: usize, k: usize) -> Option<usize> {
        let i = self.kinds[..self.n_kinds].iter().position(|&x| x == k)?;
        Some(self.lang_off + j.checked_sub(1)? * 3 * self.n_kinds + 3 * i)
    }

    /// The offset of the block of field set `f` (a single bit), if present.
    pub(crate) fn block(&self, f: u32) -> Option<usize> {
        self.field
            .get(f.trailing_zeros() as usize)
            .copied()
            .flatten()
    }
}
