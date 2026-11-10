//! CRC-32C (Castagnoli), the per-section checksum of a GEO2 file: the same
//! function as Go's `crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli))`.
//!
//! A portable slice-by-8 implementation, so the crate needs no dependency
//! and no `unsafe` for it; it checks a few hundred MB/s to a few GB/s,
//! which keeps the checksum pass of an 80 MB city database well under
//! 100 ms.

/// The reflected Castagnoli polynomial.
const POLY: u32 = 0x82F6_3B78;

/// `TABLES[k][b]` is the CRC of byte `b` followed by `k` zero bytes.
static TABLES: [[u32; 256]; 8] = make_tables();

const fn make_tables() -> [[u32; 256]; 8] {
    let mut t = [[0u32; 256]; 8];
    let mut i = 0;
    while i < 256 {
        let mut c = i as u32;
        let mut k = 0;
        while k < 8 {
            c = if c & 1 != 0 { (c >> 1) ^ POLY } else { c >> 1 };
            k += 1;
        }
        t[0][i] = c;
        i += 1;
    }
    let mut i = 0;
    while i < 256 {
        let mut k = 1;
        while k < 8 {
            let prev = t[k - 1][i];
            t[k][i] = (prev >> 8) ^ t[0][(prev & 0xFF) as usize];
            k += 1;
        }
        i += 1;
    }
    t
}

/// Returns the CRC-32C of `data`.
pub(crate) fn checksum(data: &[u8]) -> u32 {
    let mut crc = !0u32;
    let mut chunks = data.chunks_exact(8);
    for c in &mut chunks {
        let lo = u32::from_le_bytes([c[0], c[1], c[2], c[3]]) ^ crc;
        crc = TABLES[7][(lo & 0xFF) as usize]
            ^ TABLES[6][((lo >> 8) & 0xFF) as usize]
            ^ TABLES[5][((lo >> 16) & 0xFF) as usize]
            ^ TABLES[4][(lo >> 24) as usize]
            ^ TABLES[3][c[4] as usize]
            ^ TABLES[2][c[5] as usize]
            ^ TABLES[1][c[6] as usize]
            ^ TABLES[0][c[7] as usize];
    }
    for &b in chunks.remainder() {
        crc = (crc >> 8) ^ TABLES[0][((crc ^ u32::from(b)) & 0xFF) as usize];
    }
    !crc
}

#[cfg(test)]
mod tests {
    use super::checksum;

    #[test]
    fn known_values() {
        // RFC 3720, B.4 and the usual check value.
        assert_eq!(checksum(b""), 0);
        assert_eq!(checksum(b"123456789"), 0xE306_9283);
        assert_eq!(checksum(&[0u8; 32]), 0x8A91_36AA);
        assert_eq!(checksum(&[0xFFu8; 32]), 0x62A8_AB43);
        let inc: Vec<u8> = (0..32).collect();
        assert_eq!(checksum(&inc), 0x46DD_794E);
    }

    #[test]
    fn matches_bytewise() {
        fn slow(data: &[u8]) -> u32 {
            let mut crc = !0u32;
            for &b in data {
                crc ^= u32::from(b);
                for _ in 0..8 {
                    crc = if crc & 1 != 0 {
                        (crc >> 1) ^ super::POLY
                    } else {
                        crc >> 1
                    };
                }
            }
            !crc
        }
        let data: Vec<u8> = (0..1000u32).map(|i| (i * 7 + i / 13) as u8).collect();
        for n in [0, 1, 7, 8, 9, 15, 16, 17, 100, 999, 1000] {
            assert_eq!(checksum(&data[..n]), slow(&data[..n]), "length {n}");
        }
    }
}
