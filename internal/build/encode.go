package build

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/internal/format"
)

// section is one section of a GEO2 file: its id, its length in bytes, and
// a function that appends exactly that many bytes of content to b.
type section struct {
	id     uint32
	length uint64
	fill   func(b []byte) []byte
}

// encode returns the GEO2 file (docs/FORMAT.md) for br, built with opts,
// in one buffer of exactly the file's size. The sections follow the
// directory in id order, each at the next multiple of 8, and the file ends
// with the last section. The string ids of br are final (see
// interner.finish) and fit the record fields that hold them.
func encode(opts *Options, br *buildResult) ([]byte, error) {
	recW := format.RecordWidth(uint8(opts.Profile))
	idW := format.IDWidth(uint64(br.recs.n))
	var blob uint64
	for _, s := range br.strs {
		blob += uint64(len(s))
	}
	nV4, nV6 := uint64(br.v4b.n), uint64(br.v6b.n)
	nExc := uint64(countExceptions(&br.v6b))

	secs := []section{
		{format.SecMeta, format.MetaSize, br.meta.AppendTo},
		{format.SecStrIdx, 4 * uint64(len(br.strs)+1), func(b []byte) []byte { return appendStrIdx(b, br.strs) }},
		{format.SecStrBlob, blob, func(b []byte) []byte { return appendStrBlob(b, br.strs) }},
		{format.SecRecords, uint64(br.recs.n) * uint64(recW), func(b []byte) []byte {
			return appendRecords(b, opts.Profile, recW, &br.recs)
		}},
		{format.SecV4Idx, 4 * format.V4IdxEntries, func(b []byte) []byte { return appendV4Idx(b, &br.v4b) }},
		{format.SecV4Starts, 2 * nV4, func(b []byte) []byte { return appendV4Starts(b, &br.v4b) }},
		{format.SecV4IDs, nV4 * uint64(idW), func(b []byte) []byte { return appendV4IDs(b, idW, &br.v4b) }},
		{format.SecV6Starts, 8 * nV6, func(b []byte) []byte { return appendV6Starts(b, &br.v6b) }},
		{format.SecV6IDs, nV6 * uint64(idW), func(b []byte) []byte { return appendV6IDs(b, idW, &br.v6b) }},
	}
	if nExc > 0 {
		secs = append(secs,
			section{format.SecV6ExcIdx, 4 * nExc, func(b []byte) []byte { return appendExcIdx(b, &br.v6b) }},
			section{format.SecV6ExcLo, 8 * nExc, func(b []byte) []byte { return appendExcLo(b, &br.v6b) }})
	}
	if x := br.x; x != nil {
		secs = append(secs,
			section{format.SecXFields, format.XFieldsSize, func(b []byte) []byte {
				b = binary.LittleEndian.AppendUint32(b, x.fields)
				return binary.LittleEndian.AppendUint32(b, 0)
			}},
			section{format.SecXRecords, uint64(br.recs.n) * uint64(x.layout.Width), func(b []byte) []byte {
				return appendXRecords(b, x, &br.recs, br.remap)
			}})
	}
	if len(br.langs) > 0 {
		secs = append(secs, section{format.SecLangs, 4 * uint64(len(br.langs)), func(b []byte) []byte {
			for _, id := range br.langs {
				b = binary.LittleEndian.AppendUint32(b, id)
			}
			return b
		}})
	}

	// Lay the sections out after the directory.
	dirEnd := uint64(format.HeaderSize + len(secs)*format.DirEntrySize)
	dir := make([]format.DirEntry, len(secs))
	end := dirEnd
	for i, s := range secs {
		dir[i] = format.DirEntry{ID: s.id, Offset: format.Align8(end), Length: s.length}
		end = dir[i].Offset + s.length
	}
	if end > math.MaxInt {
		return nil, fmt.Errorf("encode: the database would be %d bytes, more than this platform can address", end)
	}

	// make zeroes the buffer, so the reserved bytes and the padding between
	// sections need no writes.
	buf := make([]byte, end)
	hdr := format.Header{Profile: uint8(opts.Profile), LatScale: opts.LatScale, IDWidth: idW, SectionCount: uint32(len(secs))}
	if n := len(hdr.AppendTo(buf[:0:format.HeaderSize])); n != format.HeaderSize {
		return nil, fmt.Errorf("encode: header: wrote %d bytes, want %d", n, format.HeaderSize)
	}
	for i, s := range secs {
		e := &dir[i]
		// A full slice expression caps the section, so a fill that writes
		// too much reallocates and fails the length check instead of
		// overwriting the next section.
		b := buf[e.Offset : e.Offset+e.Length : e.Offset+e.Length]
		if n := uint64(len(s.fill(b[:0]))); n != e.Length {
			return nil, fmt.Errorf("encode: section %d: wrote %d bytes, computed %d", s.id, n, e.Length)
		}
		e.CRC = format.Checksum(b)
	}
	d := buf[format.HeaderSize:format.HeaderSize:dirEnd]
	for _, e := range dir {
		d = e.AppendTo(d)
	}
	if uint64(format.HeaderSize+len(d)) != dirEnd {
		return nil, fmt.Errorf("encode: directory: wrote %d bytes, want %d", len(d), dirEnd-format.HeaderSize)
	}
	return buf, nil
}

// v6Halves returns the top and bottom 64 bits of an IPv6 boundary start.
func v6Halves(b *boundary6) (hi, lo uint64) {
	return binary.BigEndian.Uint64(b.start[:8]), binary.BigEndian.Uint64(b.start[8:])
}

// countExceptions returns the number of IPv6 boundaries whose start has
// non-zero low 64 bits.
func countExceptions(v6 *blockList[boundary6]) int {
	n := 0
	for _, blk := range v6.blocks {
		for i := range blk {
			if _, lo := v6Halves(&blk[i]); lo != 0 {
				n++
			}
		}
	}
	return n
}

func appendStrIdx(b []byte, strs []string) []byte {
	off := uint32(0)
	b = binary.LittleEndian.AppendUint32(b, off)
	for _, s := range strs {
		off += uint32(len(s)) // checkStrings bounds the total by maxStrBlob
		b = binary.LittleEndian.AppendUint32(b, off)
	}
	return b
}

func appendStrBlob(b []byte, strs []string) []byte {
	for _, s := range strs {
		b = append(b, s...)
	}
	return b
}

func appendRecords(b []byte, p ip2geo.Profile, width int, recs *blockList[recKey]) []byte {
	var rec [format.RecWidthCity]byte
	for _, blk := range recs.blocks {
		for i := range blk {
			packRecord(&rec, p, &blk[i])
			b = append(b, rec[:width]...)
		}
	}
	return b
}

// appendXRecords appends the XRECORDS entry of every record, in record
// order, with final string ids.
func appendXRecords(b []byte, x *xTable, recs *blockList[recKey], remap []uint32) []byte {
	for _, blk := range recs.blocks {
		for i := range blk {
			b = x.appendEntry(b, blk[i].ext, remap)
		}
	}
	return b
}

// packRecord packs k into rec at the format.Rec* offsets. Only the first
// format.RecordWidth(p) bytes are meaningful. The string ids are final: the
// ones in 16-bit fields are below maxU16Strings and city names below
// maxStrings (checkStrings).
func packRecord(rec *[format.RecWidthCity]byte, p ip2geo.Profile, k *recKey) {
	le := binary.LittleEndian
	if p == ip2geo.ProfileASN {
		le.PutUint32(rec[format.RecASNNumber:], k.countryGeo)
		format.PutUint24(rec[format.RecASNOrg:], k.cityName)
		return
	}
	le.PutUint16(rec[format.RecCountryISO:], uint16(k.countryISO))
	le.PutUint16(rec[format.RecCountryName:], uint16(k.countryName))
	le.PutUint16(rec[format.RecContinent:], uint16(k.continent))
	le.PutUint32(rec[format.RecCountryGeo:], k.countryGeo)
	if p >= ip2geo.ProfileRegion {
		le.PutUint16(rec[format.RecRegionISO:], uint16(k.regionISO))
		le.PutUint16(rec[format.RecRegionName:], uint16(k.regionName))
	}
	if p >= ip2geo.ProfileCity {
		format.PutUint24(rec[format.RecCityName:], k.cityName)
		le.PutUint32(rec[format.RecCityGeo:], k.cityGeo)
		le.PutUint32(rec[format.RecLat:], uint32(k.lat))
		le.PutUint32(rec[format.RecLon:], uint32(k.lon))
		le.PutUint16(rec[format.RecAccuracy:], k.acc)
		le.PutUint16(rec[format.RecTimeZone:], uint16(k.tz))
	}
}

// appendV4Idx appends V4IDX: entry g is the index of the first boundary
// whose start is at or above g<<16, and the last entry is the boundary
// count. No boundary is added at bucket starts.
func appendV4Idx(b []byte, v4 *blockList[boundary4]) []byte {
	var g, i uint32
	for _, blk := range v4.blocks {
		for _, bd := range blk {
			for ; g <= bd.start>>16; g++ {
				b = binary.LittleEndian.AppendUint32(b, i)
			}
			i++
		}
	}
	for ; g < format.V4IdxEntries; g++ {
		b = binary.LittleEndian.AppendUint32(b, i)
	}
	return b
}

func appendV4Starts(b []byte, v4 *blockList[boundary4]) []byte {
	for _, blk := range v4.blocks {
		for _, bd := range blk {
			b = binary.LittleEndian.AppendUint16(b, uint16(bd.start))
		}
	}
	return b
}

func appendV4IDs(b []byte, width uint8, v4 *blockList[boundary4]) []byte {
	for _, blk := range v4.blocks {
		for _, bd := range blk {
			b = format.AppendID(b, width, bd.rec)
		}
	}
	return b
}

func appendV6Starts(b []byte, v6 *blockList[boundary6]) []byte {
	for _, blk := range v6.blocks {
		for i := range blk {
			hi, _ := v6Halves(&blk[i])
			b = binary.LittleEndian.AppendUint64(b, hi)
		}
	}
	return b
}

func appendV6IDs(b []byte, width uint8, v6 *blockList[boundary6]) []byte {
	for _, blk := range v6.blocks {
		for i := range blk {
			b = format.AppendID(b, width, blk[i].rec)
		}
	}
	return b
}

// appendExcIdx appends the index of every IPv6 boundary whose start has
// non-zero low 64 bits, in ascending order.
func appendExcIdx(b []byte, v6 *blockList[boundary6]) []byte {
	var n uint32
	for _, blk := range v6.blocks {
		for i := range blk {
			if _, lo := v6Halves(&blk[i]); lo != 0 {
				b = binary.LittleEndian.AppendUint32(b, n)
			}
			n++
		}
	}
	return b
}

// appendExcLo appends the non-zero low 64 bits of those starts, in the
// same order.
func appendExcLo(b []byte, v6 *blockList[boundary6]) []byte {
	for _, blk := range v6.blocks {
		for i := range blk {
			if _, lo := v6Halves(&blk[i]); lo != 0 {
				b = binary.LittleEndian.AppendUint64(b, lo)
			}
		}
	}
	return b
}
