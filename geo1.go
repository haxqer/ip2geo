package ip2geo

import (
	"encoding/binary"
	"fmt"
	"math"
	"unsafe"

	"github.com/haxqer/ip2geo/internal/format"
)

// geo1 is a validated GEO1 input: the tables are sub-slices of the input.
type geo1 struct {
	profile  Profile
	latScale uint8
	b        []byte
	strOff   []uint32 // offset in b of each string's bytes; its u16 length precedes it
	rc       uint32   // record count
	recs     []byte   // rc records of RecWidth(profile) u32 fields
	starts4  []byte   // u32 LE per IPv4 start
	ids4     []byte   // u32 LE per IPv4 start
	starts6  []byte   // 16 bytes big-endian per IPv6 start
	ids6     []byte   // u32 LE per IPv6 start
}

// openGEO1 validates a GEO1 input as v0.1.x did and converts it into a
// Reader with the GEO2 in-memory layout. b is not retained.
func openGEO1(b []byte) (*Reader, error) {
	g, err := parseGEO1(b)
	if err != nil {
		return nil, err
	}
	return g.transcode()
}

// parseGEO1 checks the whole input: the header (magic, version, a known
// profile, a lat/lon scale of at most 7, a zero reserved byte), every count
// against the bytes left, a record width equal to RecWidth(profile), at
// least one record, every record id below the record count, strictly
// ascending IPv4 and IPv6 range starts, and no trailing bytes. It allocates
// only the string offsets, after their count is checked against the input.
func parseGEO1(b []byte) (*geo1, error) {
	if err := checkHeader(b); err != nil {
		return nil, err
	}
	g := &geo1{profile: Profile(b[5]), latScale: b[6], b: b}
	c := cursor{b: b, off: headerLen}

	// Strings. Each needs at least its 2-byte length, which bounds the count
	// before anything is allocated.
	ns, err := c.u32("string count")
	if err != nil {
		return nil, err
	}
	if uint64(ns) > c.rem()/2 {
		return nil, fmt.Errorf("ip2geo: %d strings cannot fit in %d bytes: %w", ns, c.rem(), ErrCorrupt)
	}
	g.strOff = make([]uint32, ns)
	for i := range g.strOff {
		l, err := c.u16("string length")
		if err != nil {
			return nil, err
		}
		if _, err := c.take(uint64(l), 1, "string"); err != nil {
			return nil, err
		}
		g.strOff[i] = uint32(c.off - int(l))
	}

	// Records. Record 0 is the not-found sentinel, so there is at least one,
	// and the width must be exactly what decode reads for the profile.
	rc, err := c.u32("record count")
	if err != nil {
		return nil, err
	}
	rw, err := c.u16("record width")
	if err != nil {
		return nil, err
	}
	if rc == 0 {
		return nil, fmt.Errorf("ip2geo: no records, record 0 is required: %w", ErrCorrupt)
	}
	if int(rw) != RecWidth(g.profile) {
		return nil, fmt.Errorf("ip2geo: record width %d, %s profile needs %d: %w", rw, g.profile, RecWidth(g.profile), ErrCorrupt)
	}
	if g.recs, err = c.take(uint64(rc), uint64(rw), "records"); err != nil {
		return nil, err
	}
	g.rc = rc

	// IPv4 ranges: n4 starts, then n4 record ids.
	n4, err := c.u32("IPv4 range count")
	if err != nil {
		return nil, err
	}
	if g.starts4, err = c.take(uint64(n4), 4, "IPv4 starts"); err != nil {
		return nil, err
	}
	if g.ids4, err = c.take(uint64(n4), 4, "IPv4 record ids"); err != nil {
		return nil, err
	}
	le := binary.LittleEndian
	for i := 1; i < int(n4); i++ {
		if le.Uint32(g.starts4[i*4:]) <= le.Uint32(g.starts4[i*4-4:]) {
			return nil, fmt.Errorf("ip2geo: IPv4 start %d is not above start %d: %w", i, i-1, ErrCorrupt)
		}
	}
	if err := checkRecordIDs(g.ids4, rc, "IPv4"); err != nil {
		return nil, err
	}

	// IPv6 ranges: n6 16-byte big-endian starts, then n6 record ids.
	n6, err := c.u32("IPv6 range count")
	if err != nil {
		return nil, err
	}
	if g.starts6, err = c.take(uint64(n6), 16, "IPv6 starts"); err != nil {
		return nil, err
	}
	if g.ids6, err = c.take(uint64(n6), 4, "IPv6 record ids"); err != nil {
		return nil, err
	}
	be := binary.BigEndian
	for i := 1; i < int(n6); i++ {
		p, s := g.starts6[i*16-16:], g.starts6[i*16:]
		if !less128(be.Uint64(p), be.Uint64(p[8:]), be.Uint64(s), be.Uint64(s[8:])) {
			return nil, fmt.Errorf("ip2geo: IPv6 start %d is not above start %d: %w", i, i-1, ErrCorrupt)
		}
	}
	if err := checkRecordIDs(g.ids6, rc, "IPv6"); err != nil {
		return nil, err
	}

	if c.rem() != 0 {
		return nil, fmt.Errorf("ip2geo: %d trailing bytes: %w", c.rem(), ErrCorrupt)
	}
	return g, nil
}

// checkHeader validates the fixed GEO1 header: magic, version, profile,
// lat/lon scale and the reserved byte. Only the first headerLen bytes are
// read.
func checkHeader(h []byte) error {
	if len(h) < len(magic) || string(h[:len(magic)]) != magic {
		return fmt.Errorf("ip2geo: magic %q, want %q: %w", h[:min(len(h), len(magic))], magic, ErrBadMagic)
	}
	if len(h) < headerLen {
		return fmt.Errorf("ip2geo: truncated header, %d of %d bytes: %w", len(h), headerLen, ErrCorrupt)
	}
	if h[4] != version {
		return fmt.Errorf("ip2geo: format version %d, want %d: %w", h[4], version, ErrUnsupportedVersion)
	}
	if Profile(h[5]) > ProfileCity {
		return fmt.Errorf("ip2geo: unknown profile %d: %w", h[5], ErrCorrupt)
	}
	if h[6] > maxLatScale {
		return fmt.Errorf("ip2geo: lat/lon scale %d is over %d: %w", h[6], maxLatScale, ErrCorrupt)
	}
	if h[7] != 0 {
		return fmt.Errorf("ip2geo: reserved header byte is %d, want 0: %w", h[7], ErrCorrupt)
	}
	return nil
}

// cursor walks the input. A size is a u32 count times a width of at most
// 2^16, formed in uint64 so it cannot wrap, and is checked against the bytes
// left before any slice is taken. 32-bit builds are as safe as 64-bit ones.
type cursor struct {
	b   []byte
	off int
}

func (c *cursor) rem() uint64 { return uint64(len(c.b) - c.off) }

// take returns the next count*width bytes.
func (c *cursor) take(count, width uint64, what string) ([]byte, error) {
	n := count * width
	if n > c.rem() {
		return nil, fmt.Errorf("ip2geo: truncated %s, need %d bytes, have %d: %w", what, n, c.rem(), ErrCorrupt)
	}
	s := c.b[c.off : c.off+int(n)]
	c.off += int(n)
	return s, nil
}

func (c *cursor) u32(what string) (uint32, error) {
	s, err := c.take(1, 4, what)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(s), nil
}

func (c *cursor) u16(what string) (uint16, error) {
	s, err := c.take(1, 2, what)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(s), nil
}

// checkRecordIDs checks that every little-endian u32 record id in b is below
// the record count rc.
func checkRecordIDs(b []byte, rc uint32, family string) error {
	for i := 0; i < len(b)/4; i++ {
		if id := binary.LittleEndian.Uint32(b[i*4:]); id >= rc {
			return fmt.Errorf("ip2geo: %s range %d points at record %d of %d: %w", family, i, id, rc, ErrCorrupt)
		}
	}
	return nil
}

// GEO1 record fields, as u32 indexes into a record.
const (
	f1CountryISO = iota
	f1CountryName
	f1Continent
	f1CountryGeo
	f1RegionISO
	f1RegionName
	f1CityName
	f1CityGeo
	f1Lat
	f1Lon
	f1Accuracy
	f1TimeZone
)

// transcode converts the validated GEO1 tables into the GEO2 layout on the
// heap. The strings are renumbered so that every string a u16 field uses
// comes first, then the city names; strings no record uses are dropped. A
// GEO1 string id of 0 or past the string table read as the empty string
// before and become id 0. Record 0 is written as all zero: GEO1 never
// decoded it.
func (g *geo1) transcode() (*Reader, error) {
	le := binary.LittleEndian
	p := g.profile
	rw, w := RecWidth(p), format.RecordWidth(uint8(p))
	rc, ns := int(g.rc), uint32(len(g.strOff))
	field := func(i, f int) uint32 { return le.Uint32(g.recs[i*rw+f*4:]) }

	// newID[o] is the new id of GEO1 string o, 0 until o is first used.
	newID := make([]uint32, ns)
	next := uint32(1)
	assign := func(f int) {
		for i := 1; i < rc; i++ {
			if o := field(i, f); o != 0 && o < ns && newID[o] == 0 {
				newID[o] = next
				next++
			}
		}
	}
	u16Fields := []int{f1CountryISO, f1CountryName, f1Continent}
	if p >= ProfileRegion {
		u16Fields = append(u16Fields, f1RegionISO, f1RegionName)
	}
	if p >= ProfileCity {
		u16Fields = append(u16Fields, f1TimeZone)
	}
	for _, f := range u16Fields {
		assign(f)
	}
	if next > 1<<16 {
		return nil, fmt.Errorf("ip2geo: %d distinct strings in country, region and time zone fields, at most %d fit: %w", next-1, 1<<16-1, ErrTooLarge)
	}
	if p >= ProfileCity {
		assign(f1CityName)
		if next > 1<<24 {
			return nil, fmt.Errorf("ip2geo: %d distinct strings, at most %d fit: %w", next-1, 1<<24-1, ErrTooLarge)
		}
	}
	str := func(o uint32) []byte {
		off := g.strOff[o]
		return g.b[off : off+uint32(le.Uint16(g.b[off-2:]))]
	}
	order := make([]uint32, next) // order[new id] = GEO1 id; order[0] is unused
	size := 0
	for o, id := range newID {
		if id != 0 {
			order[id] = uint32(o)
			size += len(str(uint32(o)))
		}
	}
	blob := make([]byte, 0, size)
	strs := make([]string, next)
	for id := 1; id < len(order); id++ {
		s := str(order[id])
		blob = append(blob, s...)
		if len(s) > 0 {
			strs[id] = unsafe.String(&blob[len(blob)-len(s)], len(s))
		}
	}
	mapStr := func(o uint32) uint32 {
		if o >= ns {
			return 0
		}
		return newID[o]
	}

	recs := make([]byte, rc*w)
	for i := 1; i < rc; i++ {
		d := recs[i*w : (i+1)*w]
		le.PutUint16(d[format.RecCountryISO:], uint16(mapStr(field(i, f1CountryISO))))
		le.PutUint16(d[format.RecCountryName:], uint16(mapStr(field(i, f1CountryName))))
		le.PutUint16(d[format.RecContinent:], uint16(mapStr(field(i, f1Continent))))
		le.PutUint32(d[format.RecCountryGeo:], field(i, f1CountryGeo))
		if p >= ProfileRegion {
			le.PutUint16(d[format.RecRegionISO:], uint16(mapStr(field(i, f1RegionISO))))
			le.PutUint16(d[format.RecRegionName:], uint16(mapStr(field(i, f1RegionName))))
		}
		if p >= ProfileCity {
			format.PutUint24(d[format.RecCityName:], mapStr(field(i, f1CityName)))
			le.PutUint32(d[format.RecCityGeo:], field(i, f1CityGeo))
			le.PutUint32(d[format.RecLat:], field(i, f1Lat))
			le.PutUint32(d[format.RecLon:], field(i, f1Lon))
			le.PutUint16(d[format.RecAccuracy:], uint16(field(i, f1Accuracy)))
			le.PutUint16(d[format.RecTimeZone:], uint16(mapStr(field(i, f1TimeZone))))
		}
	}

	idw := format.IDWidth(uint64(rc))
	r := &Reader{
		profile:  p,
		idWidth:  idw,
		recWidth: w,
		latDiv:   math.Pow10(int(g.latScale)),
		strings:  strs,
		records:  recs,
	}

	// IPv4: V4IDX[b] is the first start at or above b<<16; V4STARTS keeps the
	// low 16 bits.
	n4 := len(g.starts4) / 4
	v4idx := make([]byte, format.V4IdxEntries*4)
	r.v4starts = make([]byte, n4*2)
	r.v4ids = make([]byte, 0, n4*int(idw))
	b := 0
	for i := range n4 {
		s := le.Uint32(g.starts4[i*4:])
		for ; b <= int(s>>16); b++ {
			le.PutUint32(v4idx[b*4:], uint32(i))
		}
		le.PutUint16(r.v4starts[i*2:], uint16(s))
		r.v4ids = format.AppendID(r.v4ids, idw, le.Uint32(g.ids4[i*4:]))
	}
	for ; b < format.V4IdxEntries; b++ {
		le.PutUint32(v4idx[b*4:], uint32(n4))
	}
	r.v4idx = v4idx

	// IPv6: the top halves, and the non-zero low halves as exceptions.
	be := binary.BigEndian
	n6 := len(g.starts6) / 16
	nExc := 0
	for i := range n6 {
		if be.Uint64(g.starts6[i*16+8:]) != 0 {
			nExc++
		}
	}
	r.v6hi = make([]byte, n6*8)
	r.v6ids = make([]byte, 0, n6*int(idw))
	r.v6excIdx = make([]byte, 0, nExc*4)
	r.v6excLo = make([]byte, 0, nExc*8)
	for i := range n6 {
		le.PutUint64(r.v6hi[i*8:], be.Uint64(g.starts6[i*16:]))
		if lo := be.Uint64(g.starts6[i*16+8:]); lo != 0 {
			r.v6excIdx = le.AppendUint32(r.v6excIdx, uint32(i))
			r.v6excLo = le.AppendUint64(r.v6excLo, lo)
		}
		r.v6ids = format.AppendID(r.v6ids, idw, le.Uint32(g.ids6[i*4:]))
	}

	r.meta = Metadata{
		FormatVersion: version,
		Profile:       p,
		LatScale:      int(g.latScale),
		Records:       rc,
		Strings:       int(ns),
		IPv4Ranges:    n4,
		IPv6Ranges:    n6,
	}
	r.buildIndex()
	return r, nil
}
