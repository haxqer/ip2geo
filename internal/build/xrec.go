package build

import (
	"encoding/binary"

	"github.com/haxqer/ip2geo/internal/format"
)

// xTable holds the distinct XRECORDS entries of a build with extension
// field sets or more than one language (docs/FORMAT.md, revision 2.1).
// An entry is kept in its file layout, except that its string ids are the
// provisional ones of the interner; the encoder renumbers them. Entry 0 is
// all zero, the entry of record 0.
type xTable struct {
	fields  uint32
	nLangs  int
	layout  format.XLayout
	strOffs []int // offsets of the u24 string ids in an entry

	ids     map[string]uint32 // entry -> its index in entries
	entries []string
	scratch []byte
}

// newXTable returns the table for a geo profile, valid field set bits and
// nLangs languages, or nil when the build stores no extension entries.
func newXTable(profile uint8, fields uint32, nLangs int) *xTable {
	if profile == format.ProfileASN || (fields == 0 && nLangs < 2) {
		return nil
	}
	t := &xTable{fields: fields, nLangs: nLangs, layout: format.NewXLayout(profile, fields, nLangs)}
	l := &t.layout
	if o := l.Field[bitIndex(format.FieldContinent)]; o >= 0 {
		t.strOffs = append(t.strOffs, o+format.XContinentName)
	}
	if o := l.Field[bitIndex(format.FieldRegistered)]; o >= 0 {
		t.strOffs = append(t.strOffs, o+format.XRegisteredISO, o+format.XRegisteredName,
			o+format.XRepresentedISO, o+format.XRepresentedName, o+format.XRepresentedType)
	}
	if o := l.Field[bitIndex(format.FieldSubdivisions)]; o >= 0 {
		t.strOffs = append(t.strOffs, o+format.XRegion2ISO, o+format.XRegion2Name)
	}
	if o := l.Field[bitIndex(format.FieldPostal)]; o >= 0 {
		t.strOffs = append(t.strOffs, o+format.XPostal)
	}
	for o := l.LangOff; o < l.Width; o += 3 {
		t.strOffs = append(t.strOffs, o)
	}
	t.scratch = make([]byte, l.Width)
	zero := string(t.scratch)
	t.ids = map[string]uint32{zero: 0}
	t.entries = []string{zero}
	return t
}

// bitIndex returns the index b of a field set bit 1<<b.
func bitIndex(f uint32) int {
	b := 0
	for f > 1 {
		f >>= 1
		b++
	}
	return b
}

// add returns the index of r's entry, adding it if it is new. It interns
// the entry's strings, in a fixed order, as 24-bit ones.
func (t *xTable) add(r *Record, strs *interner) uint32 {
	e := t.scratch
	clear(e)
	le := binary.LittleEndian
	l := &t.layout
	put24 := func(off int, s string) { format.PutUint24(e[off:], strs.id(s)) }
	flag := func(off int, bit byte, set bool) {
		if set {
			e[off] |= bit
		}
	}

	if o := l.Field[bitIndex(format.FieldEU)]; o >= 0 {
		flag(o+format.XEUFlags, format.XFlagEU, r.Country.IsInEuropeanUnion)
	}
	if o := l.Field[bitIndex(format.FieldContinent)]; o >= 0 {
		put24(o+format.XContinentName, name(r.Continent.Names, 0))
		le.PutUint32(e[o+format.XContinentGeo:], r.Continent.GeoNameID)
	}
	if o := l.Field[bitIndex(format.FieldRegistered)]; o >= 0 {
		reg, rep := &r.RegisteredCountry, &r.RepresentedCountry
		put24(o+format.XRegisteredISO, reg.ISOCode)
		put24(o+format.XRegisteredName, name(reg.Names, 0))
		le.PutUint32(e[o+format.XRegisteredGeo:], reg.GeoNameID)
		put24(o+format.XRepresentedISO, rep.ISOCode)
		put24(o+format.XRepresentedName, name(rep.Names, 0))
		le.PutUint32(e[o+format.XRepresentedGeo:], rep.GeoNameID)
		put24(o+format.XRepresentedType, r.RepresentedType)
		flag(o+format.XRegisteredFlags, format.XFlagRegisteredEU, reg.IsInEuropeanUnion)
		flag(o+format.XRegisteredFlags, format.XFlagRepresentedEU, rep.IsInEuropeanUnion)
	}
	if o := l.Field[bitIndex(format.FieldSubdivisions)]; o >= 0 {
		s1, s2 := &r.Subdivisions[0], &r.Subdivisions[1]
		le.PutUint32(e[o+format.XRegionGeo:], s1.GeoNameID)
		put24(o+format.XRegion2ISO, s2.ISOCode)
		put24(o+format.XRegion2Name, name(s2.Names, 0))
		le.PutUint32(e[o+format.XRegion2Geo:], s2.GeoNameID)
	}
	if o := l.Field[bitIndex(format.FieldPostal)]; o >= 0 {
		put24(o+format.XPostal, r.PostalCode)
	}
	if o := l.Field[bitIndex(format.FieldMetro)]; o >= 0 {
		le.PutUint16(e[o+format.XMetro:], r.Location.MetroCode)
	}
	if o := l.Field[bitIndex(format.FieldTraits)]; o >= 0 {
		f := o + format.XTraitsFlags
		flag(f, format.XFlagAnycast, r.Traits.IsAnycast)
		flag(f, format.XFlagAnonymousProxy, r.Traits.IsAnonymousProxy)
		flag(f, format.XFlagSatellite, r.Traits.IsSatelliteProvider)
	}
	if o := l.Field[bitIndex(format.FieldLocation)]; o >= 0 {
		f := o + format.XLocFlags
		flag(f, format.XFlagHasCoordinates, r.Location.HasCoordinates)
		flag(f, format.XFlagHasAccuracy, r.Location.HasAccuracyRadius)
	}
	for j := 1; j < t.nLangs; j++ {
		for _, k := range l.Kinds {
			put24(l.NameOff(j, k), name(r.names(k), j))
		}
	}

	if id, ok := t.ids[string(e)]; ok {
		return id
	}
	id := uint32(len(t.entries))
	s := string(e)
	t.ids[s] = id
	t.entries = append(t.entries, s)
	return id
}

// names returns the names of name kind k (format.Name*).
func (r *Record) names(k int) []string {
	switch k {
	case format.NameCountry:
		return r.Country.Names
	case format.NameRegion:
		return r.Subdivisions[0].Names
	case format.NameCity:
		return r.City.Names
	case format.NameContinent:
		return r.Continent.Names
	case format.NameRegion2:
		return r.Subdivisions[1].Names
	case format.NameRegistered:
		return r.RegisteredCountry.Names
	case format.NameRepresented:
		return r.RepresentedCountry.Names
	}
	return nil
}

// appendEntry appends entry i with its string ids renumbered by remap.
func (t *xTable) appendEntry(b []byte, i uint32, remap []uint32) []byte {
	n := len(b)
	b = append(b, t.entries[i]...)
	e := b[n:]
	for _, o := range t.strOffs {
		format.PutUint24(e[o:], remap[format.Uint24(e[o:])])
	}
	return b
}
