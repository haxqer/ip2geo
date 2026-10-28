package ip2geo

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"math/rand/v2"
	"net/netip"
	"slices"
	"sort"
)

// This file is a second, independent implementation of the GEO2 layout of
// docs/FORMAT.md, used to build test fixtures. It deliberately shares no
// code with the reader or with the mmdb builder, and spells out the spec's
// numbers instead of using internal/format.

// GEO2 section ids, from the spec's table.
const (
	sMeta     = 1
	sStrIdx   = 2
	sStrBlob  = 3
	sRecords  = 4
	sV4Idx    = 5
	sV4Starts = 6
	sV4IDs    = 7
	sV6Starts = 8
	sV6IDs    = 9
	sV6ExcIdx = 10
	sV6ExcLo  = 11
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// g2sec is one section of a GEO2 fixture.
type g2sec struct {
	id   uint32
	data []byte
}

// g2entry is a directory entry.
type g2entry struct {
	id, crc uint32
	off, n  uint64
}

// g2file is a GEO2 file before layout: the header fields and the sections
// in file order. encode lays it out; tests corrupt the fields, the sections,
// the directory (through dir) or the encoded bytes.
type g2file struct {
	magic                               string
	version, profile, latScale, idWidth byte
	reserved                            [20]byte
	secs                                []g2sec
	count                               *uint32         // header section count, len(secs) when nil
	dir                                 func([]g2entry) // edits the directory before it is written
	gap                                 int             // zero bytes before each section, a multiple of 8
}

func (f *g2file) sec(id uint32) *g2sec {
	for i := range f.secs {
		if f.secs[i].id == id {
			return &f.secs[i]
		}
	}
	panic(fmt.Sprintf("fixture has no section %d", id))
}

// without returns f without section id.
func (f *g2file) without(id uint32) *g2file {
	f.secs = slices.DeleteFunc(f.secs, func(s g2sec) bool { return s.id == id })
	return f
}

func align8(n int) int { return (n + 7) &^ 7 }

// encode lays out the header, the directory and the sections, each section
// at the next multiple of 8 and zero-padded, with its CRC-32C.
func (f *g2file) encode() []byte {
	le := binary.LittleEndian
	n := len(f.secs)
	off := align8(32 + 24*n)
	ents := make([]g2entry, n)
	for i, s := range f.secs {
		off += f.gap
		ents[i] = g2entry{s.id, crc32.Checksum(s.data, castagnoli), uint64(off), uint64(len(s.data))}
		off = align8(off + len(s.data))
	}
	out := make([]byte, off)
	copy(out, f.magic)
	out[4], out[5], out[6], out[7] = f.version, f.profile, f.latScale, f.idWidth
	count := uint32(n)
	if f.count != nil {
		count = *f.count
	}
	le.PutUint32(out[8:], count)
	copy(out[12:32], f.reserved[:])
	for i, s := range f.secs {
		copy(out[ents[i].off:], s.data)
	}
	if f.dir != nil {
		f.dir(ents)
	}
	for i, e := range ents {
		p := out[32+24*i:]
		le.PutUint32(p, e.id)
		le.PutUint32(p[4:], e.crc)
		le.PutUint64(p[8:], e.off)
		le.PutUint64(p[16:], e.n)
	}
	return out
}

// g2dir decodes the directory of an encoded fixture.
func g2dir(b []byte) []g2entry {
	le := binary.LittleEndian
	out := make([]g2entry, le.Uint32(b[8:]))
	for i := range out {
		p := b[32+24*i:]
		out[i] = g2entry{le.Uint32(p), le.Uint32(p[4:]), le.Uint64(p[8:]), le.Uint64(p[16:])}
	}
	return out
}

// g2meta is the content of the META section, strings as text.
type g2meta struct {
	buildTime, sourceTime, networks            uint64
	sourceType, language, description, builder string
	distinct                                   uint32
}

type g2opts struct {
	meta     g2meta
	idWidth  byte // 0: the minimal width
	emptyExc bool // write empty exception sections when there are no exceptions
}

// geo1Str is GEO1's reading of a string id: 0 and ids past the table are "".
func (d *testDB) geo1Str(id uint32) string {
	if id == 0 || int(id) >= len(d.strs) {
		return ""
	}
	return d.strs[id]
}

// geo1Field returns u32 field f of GEO1 record i.
func (d *testDB) geo1Field(i, f int) uint32 {
	return binary.LittleEndian.Uint32(d.records[i*int(d.recWidth)+f*4:])
}

// toGEO2 encodes the logical database of a GEO1 test database as GEO2. The
// strings are numbered "" first, then the strings of u16 fields in sorted
// order, then the other city names, then the META strings not seen yet;
// equal strings share an id.
func toGEO2(d *testDB, o g2opts) *g2file {
	le := binary.LittleEndian
	p := Profile(d.profile)
	nrec := int(d.recCount)
	u16Fields := []int{0, 1, 2} // country ISO, country name, continent
	if p >= ProfileRegion {
		u16Fields = append(u16Fields, 4, 5) // region ISO, region name
	}
	if p >= ProfileCity {
		u16Fields = append(u16Fields, 11) // time zone
	}
	ids := map[string]uint32{"": 0}
	strs := []string{""}
	addSorted := func(set map[string]bool) {
		var keys []string
		for k := range set {
			if _, ok := ids[k]; !ok {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			ids[k] = uint32(len(strs))
			strs = append(strs, k)
		}
	}
	set := map[string]bool{}
	for i := 1; i < nrec; i++ {
		for _, f := range u16Fields {
			set[d.geo1Str(d.geo1Field(i, f))] = true
		}
	}
	addSorted(set)
	if len(strs) > 1<<16 {
		panic("too many u16 strings for a fixture")
	}
	set = map[string]bool{}
	if p >= ProfileCity {
		for i := 1; i < nrec; i++ {
			set[d.geo1Str(d.geo1Field(i, 6))] = true
		}
	}
	addSorted(set)
	m := o.meta
	addSorted(map[string]bool{m.sourceType: true, m.language: true, m.description: true, m.builder: true})

	var strIdx, strBlob []byte
	strIdx = le.AppendUint32(strIdx, 0)
	for _, s := range strs {
		strBlob = append(strBlob, s...)
		strIdx = le.AppendUint32(strIdx, uint32(len(strBlob)))
	}

	width := map[Profile]int{ProfileCountry: 10, ProfileRegion: 14, ProfileCity: 33}[p]
	recs := make([]byte, nrec*width)
	for i := 1; i < nrec; i++ {
		r := recs[i*width:]
		sid := func(f int) uint32 { return ids[d.geo1Str(d.geo1Field(i, f))] }
		le.PutUint16(r[0:], uint16(sid(0)))
		le.PutUint16(r[2:], uint16(sid(1)))
		le.PutUint16(r[4:], uint16(sid(2)))
		le.PutUint32(r[6:], d.geo1Field(i, 3))
		if p >= ProfileRegion {
			le.PutUint16(r[10:], uint16(sid(4)))
			le.PutUint16(r[12:], uint16(sid(5)))
		}
		if p >= ProfileCity {
			c := sid(6)
			r[14], r[15], r[16] = byte(c), byte(c>>8), byte(c>>16)
			le.PutUint32(r[17:], d.geo1Field(i, 7))
			le.PutUint32(r[21:], d.geo1Field(i, 8))
			le.PutUint32(r[25:], d.geo1Field(i, 9))
			le.PutUint16(r[29:], uint16(d.geo1Field(i, 10)))
			le.PutUint16(r[31:], uint16(sid(11)))
		}
	}

	idw := o.idWidth
	if idw == 0 {
		switch m := nrec - 1; {
		case m < 1<<8:
			idw = 1
		case m < 1<<16:
			idw = 2
		case m < 1<<24:
			idw = 3
		default:
			idw = 4
		}
	}
	appendID := func(b []byte, id uint32) []byte {
		for k := range int(idw) {
			b = append(b, byte(id>>(8*k)))
		}
		return b
	}

	var v4idx, v4s, v4ids []byte
	for b := 0; b <= 1<<16; b++ {
		i := sort.Search(len(d.v4starts), func(i int) bool { return uint64(d.v4starts[i]) >= uint64(b)<<16 })
		v4idx = le.AppendUint32(v4idx, uint32(i))
	}
	for i, s := range d.v4starts {
		v4s = le.AppendUint16(v4s, uint16(s&0xFFFF))
		v4ids = appendID(v4ids, d.v4rec[i])
	}

	var v6hi, v6ids, excIdx, excLo []byte
	for i, s := range d.v6starts {
		v6hi = le.AppendUint64(v6hi, binary.BigEndian.Uint64(s[:8]))
		if lo := binary.BigEndian.Uint64(s[8:]); lo != 0 {
			excIdx = le.AppendUint32(excIdx, uint32(i))
			excLo = le.AppendUint64(excLo, lo)
		}
		v6ids = appendID(v6ids, d.v6rec[i])
	}

	var meta []byte
	meta = le.AppendUint64(meta, m.buildTime)
	meta = le.AppendUint64(meta, m.sourceTime)
	meta = le.AppendUint64(meta, m.networks)
	for _, s := range []string{m.sourceType, m.language, m.description, m.builder} {
		meta = le.AppendUint32(meta, ids[s])
	}
	meta = le.AppendUint32(meta, m.distinct)
	meta = append(meta, make([]byte, 20)...)

	f := &g2file{magic: "GEO2", version: 2, profile: d.profile, latScale: d.latScale, idWidth: idw}
	f.secs = []g2sec{
		{sMeta, meta}, {sStrIdx, strIdx}, {sStrBlob, strBlob}, {sRecords, recs},
		{sV4Idx, v4idx}, {sV4Starts, v4s}, {sV4IDs, v4ids},
		{sV6Starts, v6hi}, {sV6IDs, v6ids},
	}
	if len(excIdx) > 0 || o.emptyExc {
		f.secs = append(f.secs, g2sec{sV6ExcIdx, excIdx}, g2sec{sV6ExcLo, excLo})
	}
	return f
}

// lookup is the reference answer for a, from the logical database: the
// aliases of docs/FORMAT.md, a linear search for the range, and the GEO1
// reading of the record.
func (d *testDB) lookup(a netip.Addr) (Record, bool) {
	var id uint32
	find4 := func(k uint32) uint32 {
		id := uint32(0)
		for i, s := range d.v4starts {
			if s <= k {
				id = d.v4rec[i]
			}
		}
		return id
	}
	switch {
	case a.Is4() || a.Is4In6():
		b := a.As4()
		id = find4(binary.BigEndian.Uint32(b[:]))
	case a.IsValid():
		k := a.As16()
		switch {
		case k[0] == 0x20 && k[1] == 0x02:
			id = find4(binary.BigEndian.Uint32(k[2:6]))
		case k[0] == 0x20 && k[1] == 0x01 && k[2] == 0 && k[3] == 0:
			id = find4(binary.BigEndian.Uint32(k[4:8]))
		case bytes.Equal(k[:12], make([]byte, 12)):
			id = find4(binary.BigEndian.Uint32(k[12:]))
		default:
			for i, s := range d.v6starts {
				if bytes.Compare(s[:], k[:]) <= 0 {
					id = d.v6rec[i]
				}
			}
		}
	}
	if id == 0 {
		return Record{}, false
	}
	p := Profile(d.profile)
	div := math.Pow10(int(d.latScale))
	f := func(k int) uint32 { return d.geo1Field(int(id), k) }
	rec := Record{
		CountryISO:       d.geo1Str(f(0)),
		CountryName:      d.geo1Str(f(1)),
		Continent:        d.geo1Str(f(2)),
		CountryGeoNameID: f(3),
	}
	if p >= ProfileRegion {
		rec.RegionISO, rec.RegionName = d.geo1Str(f(4)), d.geo1Str(f(5))
	}
	if p >= ProfileCity {
		rec.City = d.geo1Str(f(6))
		rec.CityGeoNameID = f(7)
		rec.Latitude = float64(int32(f(8))) / div
		rec.Longitude = float64(int32(f(9))) / div
		rec.AccuracyRadius = uint16(f(10))
		rec.TimeZone = d.geo1Str(f(11))
	}
	return rec, true
}

// excTestDB is cityTestDB with a run of IPv6 starts sharing their top half,
// two of them exceptions (non-zero low half):
//
//	2001:db8::/65         Berlin   start 1, hi H, lo 0
//	2001:db8::8000:0:0:0  LA       start 2, hi H, lo 1<<63
//	2001:db8::c000:0:0:0  none     start 3, hi H, lo 3<<62
//	2001:db8:0:1::        Berlin   start 4
func excTestDB() *testDB {
	d := cityTestDB()
	d.v6starts = [][16]byte{{}, a16("2001:db8::"), a16("2001:db8::8000:0:0:0"), a16("2001:db8::c000:0:0:0"),
		a16("2001:db8:0:1::"), a16("2001:db9::"), a16("2002::"), a16("2003::")}
	d.v6rec = []uint32{recNone, recBerlin, recLA, recNone, recBerlin, recNone, recBerlin, recNone}
	return d
}

// randomTestDB returns a GEO1 test database of profile p with nRec records
// of random content, n4 IPv4 starts and about n6 IPv6 starts. Records use
// string ids past the string table too, which read as "". The IPv4 starts
// include bucket edges; the IPv6 starts cluster in a few prefixes, with
// exceptions and runs sharing a top half.
func randomTestDB(seed uint64, p Profile, nRec, n4, n6 int) *testDB {
	rng := rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15))
	strs := []string{"", "", "US"} // an empty string 1 as well
	for i := 3; i < 300; i++ {
		strs = append(strs, fmt.Sprintf("s%d-%x", i, rng.Uint32()))
	}
	d := &testDB{
		version: version, profile: byte(ProfileCity), latScale: byte(rng.IntN(maxLatScale + 1)),
		strs: strs, recCount: uint32(nRec), recWidth: uint16(RecWidth(ProfileCity)),
	}
	le := binary.LittleEndian
	for i := range nRec {
		for f := range 12 {
			var v uint32
			switch f {
			case 0, 1, 2, 4, 5, 6, 11:
				v = uint32(rng.IntN(len(strs) + 8))
			default:
				v = rng.Uint32()
			}
			if i == 0 && f == 3 {
				v = 0xDEAD // GEO1 never reads record 0
			}
			d.records = le.AppendUint32(d.records, v)
		}
	}
	d.withProfile(p)

	v4 := map[uint32]bool{0: true}
	for len(v4) < n4 {
		s := rng.Uint32()
		switch rng.IntN(6) {
		case 0:
			s &^= 0xFFFF // a top-16 bucket base
		case 1:
			s |= 0xFFFF // the last address of a bucket
		case 2:
			s &^= 1<<v4Shift - 1 // a top-20 bucket base
		case 3:
			s = 0x0A000000 | s&0x3FFFF // dense around 10.0.0.0
		}
		v4[s] = true
	}
	for s := range v4 {
		d.v4starts = append(d.v4starts, s)
	}
	slices.Sort(d.v4starts)
	id := func() uint32 { // a fifth of the ranges have no data
		if rng.IntN(5) == 0 {
			return 0
		}
		return uint32(rng.IntN(nRec))
	}
	for range d.v4starts {
		d.v4rec = append(d.v4rec, id())
	}

	v6 := map[[16]byte]bool{{}: true}
	prefixes := []uint64{0x20010db8 << 32, 0x2a02 << 48, 0x2400_cb00 << 32, 0xfe80 << 48}
	for len(v6) < n6 {
		pre := prefixes[rng.IntN(len(prefixes))]
		hi := pre | rng.Uint64()>>uint(16+rng.IntN(40))
		var lo uint64
		switch rng.IntN(8) {
		case 0:
			lo = rng.Uint64() | 1 // an exception
		case 1: // a run sharing the top half
			for k := range 1 + rng.IntN(4) {
				v6[key16(hi, uint64(k)<<62|uint64(rng.IntN(1000)))] = true
			}
		}
		v6[key16(hi, lo)] = true
	}
	for s := range v6 {
		d.v6starts = append(d.v6starts, s)
	}
	slices.SortFunc(d.v6starts, func(a, b [16]byte) int { return bytes.Compare(a[:], b[:]) })
	for range d.v6starts {
		d.v6rec = append(d.v6rec, id())
	}
	return d
}

// probeAddrs returns addresses to compare readers on: the edges of every
// range (up to a limit), random IPv4 and IPv6 addresses, addresses in the
// IPv6 prefixes the database uses, and the IPv6 forms of IPv4 probes.
func probeAddrs(d *testDB, seed uint64, n int) []netip.Addr {
	rng := rand.New(rand.NewPCG(seed, 7))
	var out []netip.Addr
	v4 := func(k uint32) netip.Addr {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], k)
		return netip.AddrFrom4(b)
	}
	for i, s := range d.v4starts {
		if i > 400 {
			break
		}
		out = append(out, v4(s), v4(s-1), v4(s+1))
	}
	for i, s := range d.v6starts {
		if i > 400 {
			break
		}
		out = append(out, netip.AddrFrom16(s), netip.AddrFrom16(add16(s, -1)), netip.AddrFrom16(add16(s, 1)),
			netip.AddrFrom16(fillFrom(s, 64, 0xFF)))
	}
	for len(out) < n {
		switch rng.IntN(4) {
		case 0:
			out = append(out, v4(rng.Uint32()))
		case 1:
			out = append(out, netip.AddrFrom16(key16(rng.Uint64(), rng.Uint64())))
		case 2:
			if len(d.v6starts) > 0 {
				s := d.v6starts[rng.IntN(len(d.v6starts))]
				out = append(out, netip.AddrFrom16(fillFrom(s, 32+rng.IntN(96), byte(rng.Uint32()))))
			}
		case 3:
			var b [4]byte
			binary.BigEndian.PutUint32(b[:], rng.Uint32())
			if len(d.v4starts) > 0 && rng.IntN(2) == 0 {
				binary.BigEndian.PutUint32(b[:], d.v4starts[rng.IntN(len(d.v4starts))]+uint32(rng.IntN(3))-1)
			}
			out = append(out, v4Forms(b)...)
		}
	}
	return out
}
