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
	MaxProfile   = 3 // ProfileASN

	// ProfileASN is the header profile of an ASN database (0..2 are the
	// geo profiles country, region and city).
	ProfileASN = 3

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

	// Optional sections of format revision 2.1 (ip2geo v0.3). A file with
	// no extension fields and one language has none of them, so it is
	// byte-for-byte a revision 2.0 file; readers that predate 2.1 ignore
	// them as unknown sections.
	SecXFields  uint32 = 12 // u32 field set bits, u32 reserved (0)
	SecXRecords uint32 = 13 // nRecords × XLayout.Width bytes
	SecLangs    uint32 = 14 // u32[nLangs] string ids, nLangs >= 2
)

// XFieldsSize is the length of the XFIELDS section.
const XFieldsSize = 8

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

// ASN record layout (width 7): Number u32 @0, Organization u24 @4.
const (
	RecASNNumber = 0
	RecASNOrg    = 4

	RecWidthASN = 7
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
	case ProfileASN:
		return RecWidthASN
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

// Extension field sets: the bits of XFIELDS. Each set present adds a fixed
// block to every XRECORDS entry, in bit order. docs/FORMAT.md has the
// layout of each block.
const (
	FieldEU           uint32 = 1 << iota // u8 flags: XFlagEU
	FieldContinent                       // u24 name, u32 geoname id
	FieldRegistered                      // registered and represented country
	FieldSubdivisions                    // first subdivision's geoname id, the second subdivision
	FieldPostal                          // u24 postal code
	FieldMetro                           // u16 metro code
	FieldTraits                          // u8 flags: XFlagAnycast, XFlagAnonymousProxy, XFlagSatellite
	FieldLocation                        // u8 flags: XFlagHasCoordinates, XFlagHasAccuracy

	// KnownFields is every bit this revision defines; a reader rejects a
	// file with any other bit set.
	KnownFields = 1<<8 - 1
)

// numFields is the number of field set bits in KnownFields.
const numFields = 8

// fieldWidth[b] is the block width of field set 1<<b.
var fieldWidth = [numFields]int{1, 7, 24, 14, 3, 2, 1, 1}

// Offsets inside the blocks.
const (
	XEUFlags = 0 // FieldEU: u8

	XContinentName = 0 // FieldContinent: u24
	XContinentGeo  = 3 // u32

	XRegisteredISO   = 0  // FieldRegistered: u24
	XRegisteredName  = 3  // u24
	XRegisteredGeo   = 6  // u32
	XRepresentedISO  = 10 // u24
	XRepresentedName = 13 // u24
	XRepresentedGeo  = 16 // u32
	XRepresentedType = 20 // u24, e.g. "military"
	XRegisteredFlags = 23 // u8: XFlagRegisteredEU, XFlagRepresentedEU

	XRegionGeo   = 0  // FieldSubdivisions: u32, geoname id of the first subdivision
	XRegion2ISO  = 4  // u24, the second subdivision
	XRegion2Name = 7  // u24
	XRegion2Geo  = 10 // u32

	XPostal      = 0 // FieldPostal: u24
	XMetro       = 0 // FieldMetro: u16
	XTraitsFlags = 0 // FieldTraits: u8
	XLocFlags    = 0 // FieldLocation: u8
)

// Flag bits. Bits not listed must be zero.
const (
	XFlagEU = 1 << 0 // FieldEU: country.is_in_european_union

	XFlagRegisteredEU  = 1 << 0 // FieldRegistered: registered_country.is_in_european_union
	XFlagRepresentedEU = 1 << 1 // represented_country.is_in_european_union

	XFlagAnycast        = 1 << 0 // FieldTraits: traits.is_anycast
	XFlagAnonymousProxy = 1 << 1 // traits.is_anonymous_proxy
	XFlagSatellite      = 1 << 2 // traits.is_satellite_provider

	XFlagHasCoordinates = 1 << 0 // FieldLocation: latitude and longitude present
	XFlagHasAccuracy    = 1 << 1 // accuracy_radius present
)

// xFlagMask[b] is the set of defined flag bits of a field set's u8 flags
// byte, for the sets that have one.
var xFlagMask = map[uint32]struct {
	off  int
	mask byte
}{
	FieldEU:         {XEUFlags, XFlagEU},
	FieldRegistered: {XRegisteredFlags, XFlagRegisteredEU | XFlagRepresentedEU},
	FieldTraits:     {XTraitsFlags, XFlagAnycast | XFlagAnonymousProxy | XFlagSatellite},
	FieldLocation:   {XLocFlags, XFlagHasCoordinates | XFlagHasAccuracy},
}

// FlagByte reports where the flags byte of field set f is inside its block
// and which bits are defined; ok is false for a set without one.
func FlagByte(f uint32) (off int, mask byte, ok bool) {
	m, ok := xFlagMask[f]
	return m.off, m.mask, ok
}

// Name kinds: the localized names a file can hold. Each extra language
// stores one u24 string id per kind present, in this order.
const (
	NameCountry     = iota // base record countryName
	NameRegion             // base record regionName (profile >= region)
	NameCity               // base record cityName (profile city)
	NameContinent          // FieldContinent name
	NameRegion2            // FieldSubdivisions second subdivision name
	NameRegistered         // FieldRegistered registered country name
	NameRepresented        // FieldRegistered represented country name
	NumNameKinds
)

// CheckFields reports whether field set bits fields are valid for a header
// profile: only known bits, none for an ASN database, FieldSubdivisions
// from the region profile on, and FieldPostal, FieldMetro and FieldLocation
// for the city profile only.
func CheckFields(profile uint8, fields uint32) bool {
	switch {
	case fields&^KnownFields != 0:
		return false
	case profile == ProfileASN:
		return fields == 0
	case fields&FieldSubdivisions != 0 && profile < 1:
		return false
	case fields&(FieldPostal|FieldMetro|FieldLocation) != 0 && profile < 2:
		return false
	}
	return true
}

// XLayout is the layout of one XRECORDS entry for a profile, a field set
// and a number of languages (the primary one included).
type XLayout struct {
	// Width is the entry width in bytes.
	Width int
	// Field[b] is the offset of field set 1<<b's block, or -1 when the set
	// is absent.
	Field [numFields]int
	// Kinds lists the name kinds each extra language stores, in order.
	Kinds []int
	// LangOff is the offset of the first extra language's names; extra
	// language j (1 for the second language of LANGS) starts at
	// LangOff + (j-1)*3*len(Kinds).
	LangOff int
}

// NewXLayout returns the layout for a header profile, valid field set bits
// (see CheckFields) and nLangs >= 1 languages.
func NewXLayout(profile uint8, fields uint32, nLangs int) XLayout {
	var l XLayout
	for b := range numFields {
		l.Field[b] = -1
		if fields&(1<<b) != 0 {
			l.Field[b] = l.Width
			l.Width += fieldWidth[b]
		}
	}
	l.LangOff = l.Width
	if profile == ProfileASN {
		return l
	}
	l.Kinds = append(l.Kinds, NameCountry)
	if profile >= 1 {
		l.Kinds = append(l.Kinds, NameRegion)
	}
	if profile >= 2 {
		l.Kinds = append(l.Kinds, NameCity)
	}
	if fields&FieldContinent != 0 {
		l.Kinds = append(l.Kinds, NameContinent)
	}
	if fields&FieldSubdivisions != 0 {
		l.Kinds = append(l.Kinds, NameRegion2)
	}
	if fields&FieldRegistered != 0 {
		l.Kinds = append(l.Kinds, NameRegistered, NameRepresented)
	}
	l.Width += (nLangs - 1) * 3 * len(l.Kinds)
	return l
}

// NameOff returns the offset of name kind k for extra language j (1-based
// within the extra languages, i.e. index j of LANGS), or -1 when the
// layout does not store that kind.
func (l *XLayout) NameOff(j, k int) int {
	for i, kind := range l.Kinds {
		if kind == k {
			return l.LangOff + (j-1)*3*len(l.Kinds) + 3*i
		}
	}
	return -1
}
