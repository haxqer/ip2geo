// Package format holds the byte-level constants and helpers of the GEO2 file
// format shared by the reader (package ip2geo) and the builder (package mmdb).
// The layout is specified in docs/FORMAT.md; this package is the single source
// of truth for the numbers in that document.
package format

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
)

// File-level constants.
const (
	Magic        = "GEO2"
	Version      = 2
	HeaderSize   = 32
	DirEntrySize = 24
	MetaSize     = 64
	MaxLatScale  = 7
	MaxProfile   = 2

	// V4IdxEntries is the number of u32 entries in the V4IDX section:
	// one per top-16-bit bucket plus the final sentinel equal to nV4.
	V4IdxEntries = 1<<16 + 1
)

// Section ids. Every id except the two exception sections is required.
const (
	SecMeta     uint32 = 1
	SecStrIdx   uint32 = 2
	SecStrBlob  uint32 = 3
	SecRecords  uint32 = 4
	SecV4Idx    uint32 = 5
	SecV4Starts uint32 = 6
	SecV4IDs    uint32 = 7
	SecV6Starts uint32 = 8
	SecV6IDs    uint32 = 9
	SecV6ExcIdx uint32 = 10
	SecV6ExcLo  uint32 = 11
)

// Packed record layout (little-endian, unaligned). Offsets are in bytes.
// country (width 10): ISO u16 @0, Name u16 @2, Continent u16 @4, GeoName u32 @6.
// region (width 14): country fields, then RegionISO u16 @10, RegionName u16 @12.
// city (width 33): region fields, then CityName u24 @14, CityGeoName u32 @17,
// Lat i32 @21, Lon i32 @25, Accuracy u16 @29, TimeZone u16 @31.
const (
	RecCountryISO  = 0
	RecCountryName = 2
	RecContinent   = 4
	RecCountryGeo  = 6
	RecRegionISO   = 10
	RecRegionName  = 12
	RecCityName    = 14
	RecCityGeo     = 17
	RecLat         = 21
	RecLon         = 25
	RecAccuracy    = 29
	RecTimeZone    = 31

	RecWidthCountry = 10
	RecWidthRegion  = 14
	RecWidthCity    = 33
)

// RecordWidth returns the packed record width for a profile (0..2), or 0
// for an unknown profile.
func RecordWidth(profile uint8) int {
	switch profile {
	case 0:
		return RecWidthCountry
	case 1:
		return RecWidthRegion
	case 2:
		return RecWidthCity
	}
	return 0
}

// IDWidth returns the smallest number of bytes (1..4) that can hold every
// record id below nRecords.
func IDWidth(nRecords uint64) uint8 {
	switch max := nRecords - 1; {
	case nRecords == 0 || max < 1<<8:
		return 1
	case max < 1<<16:
		return 2
	case max < 1<<24:
		return 3
	default:
		return 4
	}
}

// Align8 rounds n up to a multiple of 8.
func Align8(n uint64) uint64 { return (n + 7) &^ 7 }

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// Checksum is the CRC-32C of b, the per-section checksum of the directory.
func Checksum(b []byte) uint32 { return crc32.Checksum(b, crcTable) }

// Header is the fixed 32-byte file header.
type Header struct {
	Profile      uint8
	LatScale     uint8
	IDWidth      uint8
	SectionCount uint32
}

// Errors returned by the parse helpers; the reader wraps them in its own
// sentinel errors.
var (
	ErrMagic   = errors.New("not a GEO2 file")
	ErrVersion = errors.New("unsupported GEO2 version")
	ErrHeader  = errors.New("invalid GEO2 header")
	ErrMeta    = errors.New("invalid META section")
)

// AppendTo appends the encoded header to b.
func (h Header) AppendTo(b []byte) []byte {
	b = append(b, Magic...)
	b = append(b, Version, h.Profile, h.LatScale, h.IDWidth)
	b = binary.LittleEndian.AppendUint32(b, h.SectionCount)
	var zero [HeaderSize - 12]byte
	return append(b, zero[:]...)
}

// ParseHeader decodes and validates the first HeaderSize bytes of a file.
// It checks the magic and version first so callers can distinguish "not
// GEO2" from "broken GEO2".
func ParseHeader(b []byte) (Header, error) {
	if len(b) < 4 || string(b[:4]) != Magic {
		return Header{}, ErrMagic
	}
	if len(b) < HeaderSize {
		return Header{}, ErrHeader
	}
	if b[4] != Version {
		return Header{}, ErrVersion
	}
	h := Header{
		Profile:      b[5],
		LatScale:     b[6],
		IDWidth:      b[7],
		SectionCount: binary.LittleEndian.Uint32(b[8:]),
	}
	if h.Profile > MaxProfile || h.LatScale > MaxLatScale || h.IDWidth < 1 || h.IDWidth > 4 {
		return Header{}, ErrHeader
	}
	for _, c := range b[12:HeaderSize] {
		if c != 0 {
			return Header{}, ErrHeader
		}
	}
	return h, nil
}

// DirEntry is one 24-byte directory entry.
type DirEntry struct {
	ID     uint32
	CRC    uint32
	Offset uint64
	Length uint64
}

// AppendTo appends the encoded entry to b.
func (e DirEntry) AppendTo(b []byte) []byte {
	b = binary.LittleEndian.AppendUint32(b, e.ID)
	b = binary.LittleEndian.AppendUint32(b, e.CRC)
	b = binary.LittleEndian.AppendUint64(b, e.Offset)
	return binary.LittleEndian.AppendUint64(b, e.Length)
}

// ParseDirEntry decodes one entry; b must hold at least DirEntrySize bytes.
func ParseDirEntry(b []byte) DirEntry {
	return DirEntry{
		ID:     binary.LittleEndian.Uint32(b[0:]),
		CRC:    binary.LittleEndian.Uint32(b[4:]),
		Offset: binary.LittleEndian.Uint64(b[8:]),
		Length: binary.LittleEndian.Uint64(b[16:]),
	}
}

// Meta is the fixed 64-byte META section. The string fields are ids into the
// strings table.
type Meta struct {
	BuildTime       uint64 // Unix seconds, UTC
	SourceTime      uint64 // source build epoch, 0 if unknown
	Networks        uint64
	SourceType      uint32
	Language        uint32
	Description     uint32
	Builder         uint32
	DistinctRecords uint32
}

// AppendTo appends the encoded META block to b.
func (m Meta) AppendTo(b []byte) []byte {
	b = binary.LittleEndian.AppendUint64(b, m.BuildTime)
	b = binary.LittleEndian.AppendUint64(b, m.SourceTime)
	b = binary.LittleEndian.AppendUint64(b, m.Networks)
	b = binary.LittleEndian.AppendUint32(b, m.SourceType)
	b = binary.LittleEndian.AppendUint32(b, m.Language)
	b = binary.LittleEndian.AppendUint32(b, m.Description)
	b = binary.LittleEndian.AppendUint32(b, m.Builder)
	b = binary.LittleEndian.AppendUint32(b, m.DistinctRecords)
	var zero [MetaSize - 44]byte
	return append(b, zero[:]...)
}

// ParseMeta decodes a META section, which must be exactly MetaSize bytes
// with zero reserved bytes.
func ParseMeta(b []byte) (Meta, error) {
	if len(b) != MetaSize {
		return Meta{}, ErrMeta
	}
	for _, c := range b[44:] {
		if c != 0 {
			return Meta{}, ErrMeta
		}
	}
	return Meta{
		BuildTime:       binary.LittleEndian.Uint64(b[0:]),
		SourceTime:      binary.LittleEndian.Uint64(b[8:]),
		Networks:        binary.LittleEndian.Uint64(b[16:]),
		SourceType:      binary.LittleEndian.Uint32(b[24:]),
		Language:        binary.LittleEndian.Uint32(b[28:]),
		Description:     binary.LittleEndian.Uint32(b[32:]),
		Builder:         binary.LittleEndian.Uint32(b[36:]),
		DistinctRecords: binary.LittleEndian.Uint32(b[40:]),
	}, nil
}

// Uint24 reads a little-endian 24-bit value from b[0:3].
func Uint24(b []byte) uint32 { return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 }

// PutUint24 writes v's low 24 bits to b[0:3], little-endian.
func PutUint24(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
}

// ReadID reads the record id at index i from an id array of the given width.
func ReadID(ids []byte, width uint8, i int) uint32 {
	switch width {
	case 1:
		return uint32(ids[i])
	case 2:
		return uint32(binary.LittleEndian.Uint16(ids[i*2:]))
	case 3:
		return Uint24(ids[i*3:])
	default:
		return binary.LittleEndian.Uint32(ids[i*4:])
	}
}

// AppendID appends id to b using width bytes.
func AppendID(b []byte, width uint8, id uint32) []byte {
	switch width {
	case 1:
		return append(b, byte(id))
	case 2:
		return binary.LittleEndian.AppendUint16(b, uint16(id))
	case 3:
		return append(b, byte(id), byte(id>>8), byte(id>>16))
	default:
		return binary.LittleEndian.AppendUint32(b, id)
	}
}
