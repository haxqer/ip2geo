package ip2geo

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"time"
	"unsafe"

	"github.com/haxqer/ip2geo/internal/format"
)

// numSections is one more than the largest section id this reader knows.
const numSections = format.SecLangs + 1

var sectionNames = [numSections]string{
	format.SecMeta:     "META",
	format.SecStrIdx:   "STRIDX",
	format.SecStrBlob:  "STRBLOB",
	format.SecRecords:  "RECORDS",
	format.SecV4Idx:    "V4IDX",
	format.SecV4Starts: "V4STARTS",
	format.SecV4IDs:    "V4IDS",
	format.SecV6Starts: "V6STARTS",
	format.SecV6IDs:    "V6IDS",
	format.SecV6ExcIdx: "V6EXCIDX",
	format.SecV6ExcLo:  "V6EXCLO",
	format.SecXFields:  "XFIELDS",
	format.SecXRecords: "XRECORDS",
	format.SecLangs:    "LANGS",
}

// span locates a section in the input.
type span struct{ off, n int }

// geo2 is a validated GEO2 input: where its known sections are, its header
// and META fields, and the counts derived from the section lengths. It
// refers to the input only through offsets, so the sections can be bound
// to the input itself or to a copy of it.
type geo2 struct {
	hdr  format.Header
	sec  [numSections]span // indexed by section id; absent sections are empty
	has  [numSections]bool // whether the directory lists the section
	meta format.Meta

	nStrings, nRecords, nV4, nV6, nExc int

	// Revision 2.1: the XFIELDS bits and the layout of an XRECORDS entry
	// for them and the LANGS count (1 without LANGS).
	fields uint32
	xl     format.XLayout
}

// corrupt returns an ErrCorrupt error with the message msg, formatted.
func corrupt(msg string, args ...any) error {
	return fmt.Errorf("ip2geo: "+msg+": %w", append(args, ErrCorrupt)...)
}

// parseGEO2 checks every rule of the reader requirements in docs/FORMAT.md,
// in their order, without allocating and without reading past b:
//
//  1. the header, through format.ParseHeader;
//  2. the directory and the section positions, in checkDirectory;
//  3. the required sections and their lengths, in checkLengths;
//  4. the CRC-32C of every section (all directory entries, known or not)
//     unless skipCRC is set;
//  5. to 8. the contents, in checkStrings, checkRecords, checkIPv4 and
//     checkIPv6;
//  9. to 12. the revision 2.1 sections XFIELDS, XRECORDS and LANGS, in
//     checkExtensions; rule 13, the ASN records, is part of checkRecords.
//
// Errors wrap ErrUnsupportedVersion for a version other than 2 or an
// unknown field set bit, and ErrCorrupt for everything else; the caller has
// matched the magic.
func parseGEO2(b []byte, skipCRC bool) (*geo2, error) {
	h, err := format.ParseHeader(b)
	if err != nil {
		return nil, headerError(b, err)
	}
	g := &geo2{hdr: h}
	if err := g.checkDirectory(b); err != nil {
		return nil, err
	}
	if err := g.checkLengths(); err != nil {
		return nil, err
	}
	if !skipCRC {
		for k := range int(h.SectionCount) {
			e := format.ParseDirEntry(b[format.HeaderSize+k*format.DirEntrySize:])
			if got := format.Checksum(b[e.Offset : e.Offset+e.Length]); got != e.CRC {
				return nil, corrupt("section %s (directory entry %d): CRC-32C %08x, directory says %08x", secName(e.ID), k, got, e.CRC)
			}
		}
	}
	if g.meta, err = format.ParseMeta(g.bytes(b, format.SecMeta)); err != nil {
		return nil, corrupt("META: reserved bytes are not zero")
	}
	if err := g.checkStrings(b); err != nil {
		return nil, err
	}
	if err := g.checkRecords(b); err != nil {
		return nil, err
	}
	if err := g.checkIPv4(b); err != nil {
		return nil, err
	}
	if err := g.checkIPv6(b); err != nil {
		return nil, err
	}
	if err := g.checkExtensions(b); err != nil {
		return nil, err
	}
	return g, nil
}

// headerError describes why format.ParseHeader refused b.
func headerError(b []byte, err error) error {
	switch {
	case errors.Is(err, format.ErrMagic):
		return fmt.Errorf("ip2geo: magic %q, want %q: %w", b[:min(len(b), 4)], format.Magic, ErrBadMagic)
	case len(b) < format.HeaderSize:
		return corrupt("truncated GEO2 header, %d of %d bytes", len(b), format.HeaderSize)
	case errors.Is(err, format.ErrVersion):
		return fmt.Errorf("ip2geo: GEO2 format version %d, want %d: %w", b[4], format.Version, ErrUnsupportedVersion)
	case b[5] > format.MaxProfile:
		return corrupt("unknown profile %d", b[5])
	case b[6] > format.MaxLatScale:
		return corrupt("lat/lon scale %d is over %d", b[6], format.MaxLatScale)
	case b[7] < 1 || b[7] > 4:
		return corrupt("record id width %d is not 1 to 4", b[7])
	}
	return corrupt("reserved GEO2 header bytes are not zero")
}

func secName(id uint32) string {
	if id < numSections && sectionNames[id] != "" {
		return sectionNames[id]
	}
	return fmt.Sprintf("id %d", id)
}

// bytes returns section id of b, empty when it is absent.
func (g *geo2) bytes(b []byte, id uint32) []byte {
	s := g.sec[id]
	return b[s.off : s.off+s.n : s.off+s.n]
}

// checkDirectory checks rule 2 for every entry, known or not: the directory
// fits in the input, and each section starts on a multiple of 8, after the
// directory and the previous section, and ends within the input. It records
// where the known sections are, refusing a known id listed twice.
func (g *geo2) checkDirectory(b []byte) error {
	size := uint64(len(b))
	n := uint64(g.hdr.SectionCount)
	end := format.HeaderSize + n*format.DirEntrySize
	if end > size {
		return corrupt("directory of %d entries needs %d bytes, the input has %d", n, end, size)
	}
	for k := range int(n) {
		e := format.ParseDirEntry(b[format.HeaderSize+k*format.DirEntrySize:])
		switch {
		case e.Offset%8 != 0:
			return corrupt("section %s (directory entry %d) starts at %d, not a multiple of 8", secName(e.ID), k, e.Offset)
		case e.Offset < end && k == 0:
			return corrupt("section %s (directory entry %d) starts at %d, inside the header or directory", secName(e.ID), k, e.Offset)
		case e.Offset < end:
			return corrupt("section %s (directory entry %d) starts at %d, before the end of the previous section at %d", secName(e.ID), k, e.Offset, end)
		case e.Length > size || e.Offset > size-e.Length:
			return corrupt("section %s (directory entry %d) at %d, %d bytes, ends past the %d-byte input", secName(e.ID), k, e.Offset, e.Length, size)
		}
		end = e.Offset + e.Length
		if e.ID == 0 || e.ID >= numSections {
			continue // unknown: a later minor revision's section
		}
		if g.has[e.ID] {
			return corrupt("section %s is listed twice", secName(e.ID))
		}
		g.has[e.ID] = true
		g.sec[e.ID] = span{int(e.Offset), int(e.Length)}
	}
	for id := format.SecMeta; id < format.SecV6ExcIdx; id++ {
		if !g.has[id] {
			return corrupt("required section %s is missing", secName(id))
		}
	}
	return nil
}

// checkLengths checks rule 3's lengths and derives the counts. All lengths
// are within the input, so the counts are bounded by its size.
func (g *geo2) checkLengths() error {
	n := func(id uint32) uint64 { return uint64(g.sec[id].n) }
	idw := uint64(g.hdr.IDWidth)
	rw := uint64(format.RecordWidth(g.hdr.Profile))
	switch {
	case n(format.SecMeta) != format.MetaSize:
		return corrupt("META is %d bytes, want %d", n(format.SecMeta), format.MetaSize)
	case n(format.SecStrIdx) < 4 || n(format.SecStrIdx)%4 != 0:
		return corrupt("STRIDX is %d bytes, want a non-zero multiple of 4", n(format.SecStrIdx))
	case n(format.SecRecords)%rw != 0:
		return corrupt("RECORDS is %d bytes, not a multiple of the %s record width %d", n(format.SecRecords), Profile(g.hdr.Profile), rw)
	case n(format.SecV4Idx) != format.V4IdxEntries*4:
		return corrupt("V4IDX is %d bytes, want %d", n(format.SecV4Idx), format.V4IdxEntries*4)
	case n(format.SecV4Starts)%2 != 0:
		return corrupt("V4STARTS is %d bytes, not a multiple of 2", n(format.SecV4Starts))
	case n(format.SecV4IDs) != n(format.SecV4Starts)/2*idw:
		return corrupt("V4IDS is %d bytes, want %d for %d starts of %d-byte ids", n(format.SecV4IDs), n(format.SecV4Starts)/2*idw, n(format.SecV4Starts)/2, idw)
	case n(format.SecV6Starts)%8 != 0:
		return corrupt("V6STARTS is %d bytes, not a multiple of 8", n(format.SecV6Starts))
	case n(format.SecV6IDs) != n(format.SecV6Starts)/8*idw:
		return corrupt("V6IDS is %d bytes, want %d for %d starts of %d-byte ids", n(format.SecV6IDs), n(format.SecV6Starts)/8*idw, n(format.SecV6Starts)/8, idw)
	case n(format.SecV6ExcIdx)%4 != 0:
		return corrupt("V6EXCIDX is %d bytes, not a multiple of 4", n(format.SecV6ExcIdx))
	case n(format.SecV6ExcLo) != n(format.SecV6ExcIdx)/4*8:
		return corrupt("V6EXCLO is %d bytes, want %d for %d exceptions", n(format.SecV6ExcLo), n(format.SecV6ExcIdx)/4*8, n(format.SecV6ExcIdx)/4)
	}
	g.nStrings = g.sec[format.SecStrIdx].n/4 - 1
	g.nRecords = g.sec[format.SecRecords].n / int(rw)
	g.nV4 = g.sec[format.SecV4Starts].n / 2
	g.nV6 = g.sec[format.SecV6Starts].n / 8
	g.nExc = g.sec[format.SecV6ExcIdx].n / 4
	return nil
}

// checkStrings checks the STRIDX half of rule 5: the offsets run
// non-decreasing from 0 to len(STRBLOB). String 0 must be the empty string,
// and the META string ids must be below nStrings.
func (g *geo2) checkStrings(b []byte) error {
	le := binary.LittleEndian
	idx := g.bytes(b, format.SecStrIdx)
	if v := le.Uint32(idx); v != 0 {
		return corrupt("STRIDX[0] is %d, want 0", v)
	}
	prev := uint32(0)
	for i := 1; i <= g.nStrings; i++ {
		v := le.Uint32(idx[i*4:])
		if v < prev {
			return corrupt("STRIDX[%d] = %d is below STRIDX[%d] = %d", i, v, i-1, prev)
		}
		prev = v
	}
	if blob := g.sec[format.SecStrBlob].n; uint64(prev) != uint64(blob) {
		return corrupt("STRIDX ends at %d, STRBLOB is %d bytes", prev, blob)
	}
	if g.nStrings > 0 && le.Uint32(idx[4:]) != 0 {
		return corrupt("string 0 is %d bytes, want the empty string", le.Uint32(idx[4:]))
	}
	m := g.meta
	for _, f := range []struct {
		name string
		id   uint32
	}{{"source type", m.SourceType}, {"language", m.Language}, {"description", m.Description}, {"builder", m.Builder}} {
		if uint64(f.id) >= uint64(g.nStrings) {
			return corrupt("META %s is string %d of %d", f.name, f.id, g.nStrings)
		}
	}
	return nil
}

// checkRecords checks the record half of rule 5 and rule 6, and rule 13
// for an ASN database: at least one record, record 0 all zero, every string
// id in a record below nStrings, the minimal id width for nRecords, and
// every id in V4IDS and V6IDS below nRecords.
func (g *geo2) checkRecords(b []byte) error {
	le := binary.LittleEndian
	if g.nRecords == 0 {
		return corrupt("no records, record 0 is required")
	}
	recs := g.bytes(b, format.SecRecords)
	w := format.RecordWidth(g.hdr.Profile)
	for _, c := range recs[:w] {
		if c != 0 {
			return corrupt("record 0 is not all zero")
		}
	}
	// The string fields of the profile: u16 ids, and one u24 id, the city
	// name or the ASN organization.
	var offs []int
	u24, u24Name := -1, ""
	if p := Profile(g.hdr.Profile); p == ProfileASN {
		u24, u24Name = format.RecASNOrg, "organization"
	} else {
		offs = []int{format.RecCountryISO, format.RecCountryName, format.RecContinent}
		if p >= ProfileRegion {
			offs = append(offs, format.RecRegionISO, format.RecRegionName)
		}
		if p == ProfileCity {
			offs = append(offs, format.RecTimeZone)
			u24, u24Name = format.RecCityName, "city name"
		}
	}
	if g.nStrings > math.MaxUint16 {
		offs = offs[:0] // every u16 id is below nStrings
	}
	ns := uint32(g.nStrings)
	for i := 1; i < g.nRecords; i++ {
		r := recs[i*w : (i+1)*w]
		for _, o := range offs {
			if id := uint32(le.Uint16(r[o:])); id >= ns {
				return corrupt("record %d: string id %d at byte %d is not below the %d strings", i, id, o, ns)
			}
		}
		if u24 >= 0 {
			if id := format.Uint24(r[u24:]); id >= ns {
				return corrupt("record %d: %s is string %d of %d", i, u24Name, id, ns)
			}
		}
	}

	if want := format.IDWidth(uint64(g.nRecords)); g.hdr.IDWidth != want {
		return corrupt("record id width %d for %d records, want %d", g.hdr.IDWidth, g.nRecords, want)
	}
	nr := uint32(g.nRecords)
	for _, t := range []struct {
		name string
		id   uint32
		n    int
	}{{"IPv4", format.SecV4IDs, g.nV4}, {"IPv6", format.SecV6IDs, g.nV6}} {
		ids := g.bytes(b, t.id)
		for i := range t.n {
			if id := format.ReadID(ids, g.hdr.IDWidth, i); id >= nr {
				return corrupt("%s range %d points at record %d of %d", t.name, i, id, nr)
			}
		}
	}
	return nil
}

// checkExtensions checks rules 9 to 12, in the order 10, 9, the LANGS
// count, the XRECORDS length, the LANGS contents and the XRECORDS entries,
// so that each count is bounded by its section before it is used. It
// records the field set bits and the XRECORDS layout.
func (g *geo2) checkExtensions(b []byte) error {
	le := binary.LittleEndian
	p := g.hdr.Profile
	hasF, hasX, hasL := g.has[format.SecXFields], g.has[format.SecXRecords], g.has[format.SecLangs]
	switch {
	case p == format.ProfileASN && (hasF || hasX || hasL):
		return corrupt("an ASN database has an XFIELDS, XRECORDS or LANGS section")
	case hasF != hasX:
		return corrupt("XFIELDS and XRECORDS must both be present or both absent")
	case hasL && !hasF:
		return corrupt("LANGS is present without XFIELDS and XRECORDS")
	case !hasF:
		g.xl = format.NewXLayout(p, 0, 1)
		return nil
	}

	xf := g.bytes(b, format.SecXFields)
	if len(xf) != format.XFieldsSize {
		return corrupt("XFIELDS is %d bytes, want %d", len(xf), format.XFieldsSize)
	}
	fields := le.Uint32(xf)
	if v := le.Uint32(xf[4:]); v != 0 {
		return corrupt("XFIELDS reserved word is %#x, want 0", v)
	}
	if v := fields &^ format.KnownFields; v != 0 {
		return fmt.Errorf("ip2geo: XFIELDS has field set bits %#x this reader does not know: %w", v, ErrUnsupportedVersion)
	}
	if !format.CheckFields(p, fields) {
		return corrupt("field sets %#x are not allowed for the %s profile", fields, Profile(p))
	}

	langs := g.bytes(b, format.SecLangs)
	nLangs := 1
	if hasL {
		if len(langs)%4 != 0 {
			return corrupt("LANGS is %d bytes, not a multiple of 4", len(langs))
		}
		if nLangs = len(langs) / 4; nLangs < 2 {
			return corrupt("LANGS lists %d languages, want at least 2", nLangs)
		}
	}

	// The entry width, in uint64: nLangs is only bounded by the input so
	// far. The width is below 2^34 and there are fewer than 2^28 records.
	xr := g.bytes(b, format.SecXRecords)
	base := format.NewXLayout(p, fields, 1)
	w := uint64(base.Width) + uint64(nLangs-1)*3*uint64(len(base.Kinds))
	if want := w * uint64(g.nRecords); uint64(len(xr)) != want {
		return corrupt("XRECORDS is %d bytes, want %d for %d records of %d bytes", len(xr), want, g.nRecords, w)
	}
	// Now w <= len(xr), so the layout's int arithmetic cannot overflow.
	g.fields, g.xl = fields, format.NewXLayout(p, fields, nLangs)

	if hasL {
		if err := g.checkLangs(b, langs); err != nil {
			return err
		}
	}
	return g.checkXRecords(xr)
}

// checkLangs checks rule 12 for a LANGS section of at least two entries:
// every entry is the id of a non-empty string listed once, and the first
// one is the META language.
func (g *geo2) checkLangs(b, langs []byte) error {
	le := binary.LittleEndian
	seen := make(map[string]bool, len(langs)/4)
	for j := range len(langs) / 4 {
		id := le.Uint32(langs[j*4:])
		if uint64(id) >= uint64(g.nStrings) {
			return corrupt("LANGS[%d] is string %d of %d", j, id, g.nStrings)
		}
		s := string(g.str(b, id))
		switch {
		case s == "":
			return corrupt("LANGS[%d] is the empty string", j)
		case seen[s]:
			return corrupt("LANGS[%d] %q is listed twice", j, s)
		}
		seen[s] = true
	}
	if l0, m := g.str(b, le.Uint32(langs)), g.str(b, g.meta.Language); !bytes.Equal(l0, m) {
		return corrupt("LANGS[0] %q is not the META language %q", l0, m)
	}
	return nil
}

// str returns the bytes of string id, which must be below nStrings.
func (g *geo2) str(b []byte, id uint32) []byte {
	le := binary.LittleEndian
	idx := g.bytes(b, format.SecStrIdx)
	return g.bytes(b, format.SecStrBlob)[le.Uint32(idx[id*4:]):le.Uint32(idx[id*4+4:])]
}

// checkXRecords checks rule 11 past the length: entry 0 all zero, and in
// the other entries every u24 string id below nStrings and no flag bit set
// that the field set does not define.
func (g *geo2) checkXRecords(xr []byte) error {
	l := &g.xl
	w := l.Width
	if w == 0 {
		return nil
	}
	for _, c := range xr[:w] {
		if c != 0 {
			return corrupt("XRECORDS entry 0 is not all zero")
		}
	}
	// The u24 string ids: those of the blocks, then every name of the
	// extra languages.
	var offs []int
	for _, f := range []struct {
		set  uint32
		offs []int
	}{
		{format.FieldContinent, []int{format.XContinentName}},
		{format.FieldRegistered, []int{format.XRegisteredISO, format.XRegisteredName, format.XRepresentedISO, format.XRepresentedName, format.XRepresentedType}},
		{format.FieldSubdivisions, []int{format.XRegion2ISO, format.XRegion2Name}},
		{format.FieldPostal, []int{format.XPostal}},
	} {
		if g.fields&f.set != 0 {
			for _, o := range f.offs {
				offs = append(offs, l.Field[bits.TrailingZeros32(f.set)]+o)
			}
		}
	}
	for o := l.LangOff; o < w; o += 3 {
		offs = append(offs, o)
	}
	// The flags bytes, with their defined bits.
	type flagByte struct {
		off  int
		mask byte
	}
	var flags []flagByte
	for b := range bits.Len32(format.KnownFields) {
		if f := uint32(1) << b; g.fields&f != 0 {
			if off, mask, ok := format.FlagByte(f); ok {
				flags = append(flags, flagByte{l.Field[b] + off, mask})
			}
		}
	}

	ns := uint32(g.nStrings)
	for i := 1; i < g.nRecords; i++ {
		e := xr[i*w : (i+1)*w]
		for _, o := range offs {
			if id := format.Uint24(e[o:]); id >= ns {
				return corrupt("XRECORDS entry %d: string id %d at byte %d is not below the %d strings", i, id, o, ns)
			}
		}
		for _, f := range flags {
			if c := e[f.off]; c&^f.mask != 0 {
				return corrupt("XRECORDS entry %d: undefined flag bits %#x at byte %d", i, c&^f.mask, f.off)
			}
		}
	}
	return nil
}

// checkIPv4 checks rule 7: V4IDX starts at 0, is non-decreasing and ends
// with nV4, and within each top-16 bucket the u16 starts are strictly
// ascending, which makes the absolute starts strictly ascending.
func (g *geo2) checkIPv4(b []byte) error {
	le := binary.LittleEndian
	idx := g.bytes(b, format.SecV4Idx)
	starts := g.bytes(b, format.SecV4Starts)
	if v := le.Uint32(idx[(format.V4IdxEntries-1)*4:]); uint64(v) != uint64(g.nV4) {
		return corrupt("V4IDX[65536] is %d, want the IPv4 start count %d", v, g.nV4)
	}
	if v := le.Uint32(idx); v != 0 {
		return corrupt("V4IDX[0] is %d, want 0", v)
	}
	for bk := range format.V4IdxEntries - 1 {
		lo, hi := le.Uint32(idx[bk*4:]), le.Uint32(idx[bk*4+4:])
		if hi < lo {
			return corrupt("V4IDX[%d] = %d is below V4IDX[%d] = %d", bk+1, hi, bk, lo)
		}
		if uint64(hi) > uint64(g.nV4) {
			return corrupt("V4IDX[%d] = %d is above the IPv4 start count %d", bk+1, hi, g.nV4)
		}
		for i := int(lo) + 1; i < int(hi); i++ {
			if le.Uint16(starts[i*2:]) <= le.Uint16(starts[i*2-2:]) {
				return corrupt("IPv4 start %d is not above start %d", i, i-1)
			}
		}
	}
	return nil
}

// checkIPv6 checks rule 8: V6EXCIDX strictly ascending with every entry
// below nV6, every V6EXCLO entry non-zero, V6STARTS non-decreasing, and the
// 128-bit starts strictly ascending.
func (g *geo2) checkIPv6(b []byte) error {
	le := binary.LittleEndian
	excIdx := g.bytes(b, format.SecV6ExcIdx)
	excLo := g.bytes(b, format.SecV6ExcLo)
	for j := range g.nExc {
		i := le.Uint32(excIdx[j*4:])
		switch {
		case uint64(i) >= uint64(g.nV6):
			return corrupt("IPv6 exception %d is start %d of %d", j, i, g.nV6)
		case j > 0 && i <= le.Uint32(excIdx[j*4-4:]):
			return corrupt("IPv6 exception %d (start %d) is not above exception %d", j, i, j-1)
		case le.Uint64(excLo[j*8:]) == 0:
			return corrupt("IPv6 exception %d (start %d) has a zero low half", j, i)
		}
	}
	hi := g.bytes(b, format.SecV6Starts)
	j := 0 // the next exception
	var ph, pl uint64
	for i := range g.nV6 {
		h, l := le.Uint64(hi[i*8:]), uint64(0)
		if j < g.nExc && int(le.Uint32(excIdx[j*4:])) == i {
			l = le.Uint64(excLo[j*8:])
			j++
		}
		if i > 0 && h < ph {
			return corrupt("V6STARTS[%d] is below V6STARTS[%d]", i, i-1)
		}
		if i > 0 && !less128(ph, pl, h, l) {
			return corrupt("IPv6 start %d is not above start %d", i, i-1)
		}
		ph, pl = h, l
	}
	return nil
}

// reader binds the sections of buf, which holds the validated input or a
// copy of it, to a new Reader and builds its indexes. The strings are
// copied to the heap; the other sections stay in buf.
func (g *geo2) reader(buf []byte) *Reader {
	p := Profile(g.hdr.Profile)
	r := &Reader{
		profile:  p,
		idWidth:  g.hdr.IDWidth,
		recWidth: format.RecordWidth(g.hdr.Profile),
		latDiv:   math.Pow10(int(g.hdr.LatScale)),
		strings:  heapStrings(g.bytes(buf, format.SecStrIdx), g.bytes(buf, format.SecStrBlob)),
		records:  g.bytes(buf, format.SecRecords),
		v4idx:    g.bytes(buf, format.SecV4Idx),
		v4starts: g.bytes(buf, format.SecV4Starts),
		v4ids:    g.bytes(buf, format.SecV4IDs),
		v6hi:     g.bytes(buf, format.SecV6Starts),
		v6ids:    g.bytes(buf, format.SecV6IDs),
		v6excIdx: g.bytes(buf, format.SecV6ExcIdx),
		v6excLo:  g.bytes(buf, format.SecV6ExcLo),
		fields:   FieldSet(g.fields),
		xrecs:    g.bytes(buf, format.SecXRecords),
		xl:       g.xl,
		alt:      p == ProfileASN,
	}
	if langs := g.bytes(buf, format.SecLangs); len(langs) > 0 {
		r.langs = make([]string, len(langs)/4)
		for j := range r.langs {
			r.langs[j] = r.strings[binary.LittleEndian.Uint32(langs[j*4:])]
		}
	} else if lang := r.strings[g.meta.Language]; lang != "" {
		r.langs = []string{lang}
	}
	m := g.meta
	r.meta = Metadata{
		FormatVersion:   format.Version,
		Profile:         p,
		LatScale:        int(g.hdr.LatScale),
		BuildTime:       unixTime(m.BuildTime),
		SourceTime:      unixTime(m.SourceTime),
		SourceType:      r.strings[m.SourceType],
		Language:        r.strings[m.Language],
		Fields:          r.fields,
		Description:     r.strings[m.Description],
		Builder:         r.strings[m.Builder],
		Networks:        m.Networks,
		DistinctRecords: uint64(m.DistinctRecords),
		Records:         g.nRecords,
		Strings:         g.nStrings,
		IPv4Ranges:      g.nV4,
		IPv6Ranges:      g.nV6,
	}
	r.buildIndex()
	return r
}

// heapStrings copies blob to the heap and returns the strings STRIDX idx
// delimits in that copy: two allocations, and nothing refers to blob.
func heapStrings(idx, blob []byte) []string {
	le := binary.LittleEndian
	heap := bytes.Clone(blob)
	s := make([]string, len(idx)/4-1)
	for i := range s {
		a, e := le.Uint32(idx[i*4:]), le.Uint32(idx[i*4+4:])
		if e > a {
			s[i] = unsafe.String(&heap[a], int(e-a))
		}
	}
	return s
}

// unixTime converts Unix seconds to a UTC time, 0 to the zero time.
func unixTime(sec uint64) time.Time {
	if sec == 0 || sec > math.MaxInt64 {
		return time.Time{}
	}
	return time.Unix(int64(sec), 0).UTC()
}
