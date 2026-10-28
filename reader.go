package ip2geo

import (
	"encoding/binary"
	"net/netip"
	"runtime"

	"github.com/haxqer/ip2geo/internal/format"
)

// idxBits for the direct-index (DXR) jump tables, chosen for a good
// size/speed balance. v4 uses the top 20 bits, v6 the top 16.
const (
	v4IdxBits = 20
	v4Shift   = 32 - v4IdxBits
	v6IdxBits = 16
)

// The IPv6 index has a second level inside each populated top-16 bucket,
// over the next k bits. k grows with the bucket so that its starts spread to
// about v6SlotStarts per level-2 slot, and is at most v6MaxL2Bits. An entry
// of Reader.v6dir packs the bucket's offset into v6l2 above v6KBits bits of k.
const (
	v6SlotStarts = 8
	v6MaxL2Bits  = 16
	v6KBits      = 5
	v6KMask      = 1<<v6KBits - 1
)

// A v6l2 entry holds the index of the first start of its slot in the low 31
// bits, and v6SlotExc when one of the slot's starts is an exception.
const (
	v6SlotExc     = 1 << 31
	v6SlotIdxMask = v6SlotExc - 1
)

// Reader is a read-only handle to a compact database. It is safe for
// concurrent use by multiple goroutines, Close included. Lookups perform no
// allocation.
//
// The tables are laid out as the sections of a GEO2 file (docs/FORMAT.md),
// read with little-endian loads: for a GEO2 file they are sub-slices of the
// mapped or retained file, for a GEO1 file they are built on the heap at
// open. Every section has been validated, so the searches below index them
// without further checks.
type Reader struct {
	profile  Profile
	idWidth  uint8 // bytes per record id in v4ids and v6ids, 1..4
	recWidth int   // packed record width, format.RecordWidth(profile)
	latDiv   float64

	strings []string // id -> string over a heap copy of STRBLOB; strings[0] == ""
	records []byte   // RECORDS: packed records, record 0 all zero

	// IPv4: V4IDX (u32[65537]), V4STARTS (u16 per boundary), V4IDS, and the
	// top-20 table built at open: lo4[g] is the index of the first boundary
	// whose absolute start is >= g<<v4Shift, and lo4[1<<v4IdxBits] == nV4.
	v4idx    []byte
	v4starts []byte
	v4ids    []byte
	lo4      *[1<<v4IdxBits + 1]int32

	// IPv6: V6STARTS (the top 64 bits of each start), V6IDS, and the
	// exception table V6EXCIDX/V6EXCLO holding the non-zero low halves.
	// v6exc has bit i set when start i is an exception. v6dir[g] describes
	// top-16 bucket g: its level-2 table is v6l2[o:o+1<<k], o = v6dir[g]>>v6KBits
	// and k = v6dir[g]&v6KMask, and v6l2[o+s]&v6SlotIdxMask is the index of the
	// first start in slot s (the k bits after the top 16). The tables are laid
	// end to end and end with nV6, so a slot's starts run from its entry to the
	// next one. An entry also has v6SlotExc set when its slot holds an
	// exception.
	v6hi     []byte
	v6ids    []byte
	v6excIdx []byte
	v6excLo  []byte
	v6exc    []uint64
	v6dir    *[1 << v6IdxBits]uint32
	v6l2     []uint32

	// Revision 2.1: the field sets, XRECORDS (one entry of xl.Width bytes
	// per record) and its layout. A GEO1 database, and a GEO2 one without
	// XRECORDS, has no field sets and Width 0.
	fields FieldSet
	xrecs  []byte
	xl     format.XLayout

	// langs lists the languages of the names, the primary one first: LANGS,
	// or META's language alone, or nil when the file records none. lang is
	// the index of the language Lookup and LookupDetails answer in, and
	// when it is not 0, nameOff[k] is the offset in an XRECORDS entry of
	// that language's u24 name of kind k, or -1 when the file has no such
	// name. alt is set when Lookup must do more than decode: for an ASN
	// database or a language other than the primary one.
	langs   []string
	lang    int
	nameOff [format.NumNameKinds]int
	alt     bool

	meta   Metadata
	mapped bool // the sections are in a memory mapping released by a cleanup

	// owner is the Reader a WithLanguage view was made from: the one whose
	// cleanup releases the mapping, which the view keeps reachable.
	owner *Reader
}

// Profile reports the precision profile the database was built with.
func (r *Reader) Profile() Profile { return r.profile }

// Metadata returns the file's header and META fields and its table sizes.
func (r *Reader) Metadata() Metadata { return r.meta }

// Close does nothing and returns nil. A memory-mapped database is unmapped
// by the garbage collector once the Reader, and every Reader WithLanguage
// made from it, is unreachable (through runtime.AddCleanup), not by Close,
// so a Lookup that overlaps Close or follows it is still safe and keeps
// answering. Drop every reference to the Reader to release its memory.
// Close is safe to call concurrently with Lookup and more than once.
func (r *Reader) Close() error { return nil }

// Lookup resolves an IP address to a Record. ok is false when the address is
// not covered by the database, and for the zero netip.Addr.
//
// IPv4 addresses in IPv6 form are looked up in the IPv4 table, as MaxMind
// databases alias them: ::ffff:0:0/96 (IPv4-mapped), ::/96 (IPv4-compatible),
// 2002::/16 (6to4, bits 16-47) and 2001::/32 (Teredo server, bits 32-63).
// NAT64 64:ff9b::/96 is not mapped.
func (r *Reader) Lookup(ip netip.Addr) (rec Record, ok bool) {
	var id uint32
	if ip.Is4() || ip.Is4In6() {
		a := ip.As4()
		id = r.search4(binary.BigEndian.Uint32(a[:]))
	} else if ip.IsValid() {
		a := ip.As16()
		if v4, isV4 := embeddedV4(&a); isV4 {
			id = r.search4(v4)
		} else {
			id = r.search6(binary.BigEndian.Uint64(a[:8]), binary.BigEndian.Uint64(a[8:]))
		}
	}
	if id != 0 {
		if r.alt {
			rec = r.decodeAlt(id)
		} else {
			rec = r.decode(id)
		}
		ok = true
	}
	// The sections may live in a mapping that a cleanup releases once r is
	// unreachable: keep r alive until the last read from them.
	runtime.KeepAlive(r)
	return rec, ok
}

// City is an alias for Lookup, provided for familiarity with geoip2-golang.
func (r *Reader) City(ip netip.Addr) (Record, bool) { return r.Lookup(ip) }

// Country is an alias for Lookup. The returned Record's country fields are the
// ones of interest; other fields depend on the database profile.
func (r *Reader) Country(ip netip.Addr) (Record, bool) { return r.Lookup(ip) }

// embeddedV4 returns the IPv4 address an IPv6 key is aliased to: bytes 12-15
// of ::/96, bytes 2-5 of 2002::/16 (6to4) and bytes 4-7 of 2001:0::/32
// (Teredo). Lookup handles ::ffff:0:0/96 through netip's Is4In6 instead.
func embeddedV4(k *[16]byte) (uint32, bool) {
	hi := binary.BigEndian.Uint64(k[:8])
	switch {
	case hi>>48 == 0x2002:
		return uint32(hi >> 16), true
	case hi>>32 == 0x2001_0000:
		return uint32(hi), true
	case hi == 0 && binary.BigEndian.Uint32(k[8:12]) == 0:
		return binary.BigEndian.Uint32(k[12:]), true
	}
	return 0, false
}

// The searches load an element as v[i*w:i*w+w] rather than v[i*w:]: with a
// known length the compiler loads straight from &v[0] + i*w, keeping the
// bounds checks off the dependency chain of the binary search.

// search4 returns the record id of the IPv4 key, 0 when it is not covered.
// The boundaries of the key's top-20 window share its top 16 bits, so the
// binary search compares the low 16 bits only. It finds the first boundary
// of the window above the key; the one before it holds the key, and when
// that is the boundary before the window (window start - 1) it is the last
// boundary of an earlier window, as docs/FORMAT.md describes.
func (r *Reader) search4(key uint32) uint32 {
	g := key >> v4Shift
	l, h := int(r.lo4[g]), int(r.lo4[g+1])
	k := uint16(key)
	s := r.v4starts
	for l < h {
		mid := int(uint(l+h) >> 1)
		if binary.LittleEndian.Uint16(s[mid*2:mid*2+2]) > k {
			h = mid
		} else {
			l = mid + 1
		}
	}
	if l == 0 {
		return 0
	}
	return format.ReadID(r.v4ids, r.idWidth, l-1)
}

// search6 returns the record id of the range holding the IPv6 key hi:lo, or
// 0 when the key is below the first start. Every start before the key's
// level-2 slot has a smaller top half than the key and every start after it a
// larger one, so the binary search over the top halves covers the slot
// alone: it finds the last start i whose top half is <= hi. Starts sharing
// the key's top half are ordered by their low half, which is 0 except for
// the exceptions, so when the slot holds an exception the search steps back
// while start i has the key's top half and a low half above lo. A start
// before the slot has a smaller top half, so the step back stays in it.
func (r *Reader) search6(hi, lo uint64) uint32 {
	d := r.v6dir[hi>>(64-v6IdxBits)]
	s := d>>v6KBits + uint32(v6Slot(hi, d&v6KMask))
	e := r.v6l2[s]
	l, h := int(e&v6SlotIdxMask), int(r.v6l2[s+1]&v6SlotIdxMask)
	first := l
	v := r.v6hi
	for l < h {
		mid := int(uint(l+h) >> 1)
		if binary.LittleEndian.Uint64(v[mid*8:mid*8+8]) > hi {
			h = mid
		} else {
			l = mid + 1
		}
	}
	i := l - 1
	if e&v6SlotExc != 0 && i >= first {
		i = r.v6StepBack(i, hi, lo)
	}
	if i < 0 {
		return 0
	}
	return format.ReadID(r.v6ids, r.idWidth, i)
}

// v6StepBack returns the last start at or before i that is <= hi:lo, given
// that every start after i is above it and start i's top half is <= hi.
func (r *Reader) v6StepBack(i int, hi, lo uint64) int {
	for i >= 0 && binary.LittleEndian.Uint64(r.v6hi[i*8:i*8+8]) == hi && r.v6exc[uint(i)>>6]&(1<<(uint(i)&63)) != 0 && r.v6ExcLo(i) > lo {
		i--
	}
	return i
}

// v6ExcLo returns the low half of IPv6 start i, which must be an exception.
func (r *Reader) v6ExcLo(i int) uint64 {
	idx := r.v6excIdx
	l, h := 0, len(idx)/4
	for l < h {
		mid := int(uint(l+h) >> 1)
		if int(binary.LittleEndian.Uint32(idx[mid*4:mid*4+4])) < i {
			l = mid + 1
		} else {
			h = mid
		}
	}
	return binary.LittleEndian.Uint64(r.v6excLo[l*8 : l*8+8])
}

// decode reads packed record id. Every string id in it was checked against
// the string table at open. The record is read through array pointers, so
// that its fields need no bounds checks of their own.
func (r *Reader) decode(id uint32) Record {
	le := binary.LittleEndian
	off := int(id) * r.recWidth
	c := (*[format.RecWidthCountry]byte)(r.records[off : off+format.RecWidthCountry])
	var rec Record
	rec.CountryISO = r.strings[le.Uint16(c[format.RecCountryISO:])]
	rec.CountryName = r.strings[le.Uint16(c[format.RecCountryName:])]
	rec.Continent = r.strings[le.Uint16(c[format.RecContinent:])]
	rec.CountryGeoNameID = le.Uint32(c[format.RecCountryGeo:])
	if r.profile >= ProfileRegion {
		g := (*[format.RecWidthRegion]byte)(r.records[off : off+format.RecWidthRegion])
		rec.RegionISO = r.strings[le.Uint16(g[format.RecRegionISO:])]
		rec.RegionName = r.strings[le.Uint16(g[format.RecRegionName:])]
	}
	if r.profile >= ProfileCity {
		y := (*[format.RecWidthCity]byte)(r.records[off : off+format.RecWidthCity])
		rec.City = r.strings[format.Uint24(y[format.RecCityName:])]
		rec.CityGeoNameID = le.Uint32(y[format.RecCityGeo:])
		rec.Latitude = float64(int32(le.Uint32(y[format.RecLat:]))) / r.latDiv
		rec.Longitude = float64(int32(le.Uint32(y[format.RecLon:]))) / r.latDiv
		rec.AccuracyRadius = le.Uint16(y[format.RecAccuracy:])
		rec.TimeZone = r.strings[le.Uint16(y[format.RecTimeZone:])]
	}
	return rec
}

// buildIndex builds the in-memory indexes over validated sections: the
// top-20 IPv4 table, the two-level IPv6 table and the IPv6 exception bitmap,
// and flags the level-2 slots that hold an exception.
func (r *Reader) buildIndex() {
	le := binary.LittleEndian
	r.buildIndex4()
	r.buildIndex6()
	n6 := len(r.v6hi) / 8
	exc := make([]uint64, (n6+63)/64)
	for j := 0; j < len(r.v6excIdx); j += 4 {
		i := le.Uint32(r.v6excIdx[j:])
		exc[i>>6] |= 1 << (i & 63)
		hi := le.Uint64(r.v6hi[i*8:])
		d := r.v6dir[hi>>(64-v6IdxBits)]
		r.v6l2[d>>v6KBits+uint32(v6Slot(hi, d&v6KMask))] |= v6SlotExc
	}
	r.v6exc = exc
}

// buildIndex4 derives the top-20 table lo4 from V4IDX and the u16 starts:
// top-16 bucket G holds boundaries V4IDX[G]:V4IDX[G+1], and its 16 top-20
// sub-buckets split them on the top 4 bits of their u16 value.
func (r *Reader) buildIndex4() {
	const sub = 1 << (v4IdxBits - 16)
	le := binary.LittleEndian
	s := r.v4starts
	lo4 := new([1<<v4IdxBits + 1]int32)
	for g := range 1 << 16 {
		i, end := int(le.Uint32(r.v4idx[g*4:])), int(le.Uint32(r.v4idx[g*4+4:]))
		for k := range sub {
			for i < end && int(le.Uint16(s[i*2:])) < k<<v4Shift {
				i++
			}
			lo4[g*sub+k] = int32(i)
		}
	}
	lo4[1<<v4IdxBits] = int32(len(s) / 2)
	r.lo4 = lo4
}

// buildIndex6 constructs the two-level IPv6 table described at Reader.v6dir
// over the top halves of the starts.
func (r *Reader) buildIndex6() {
	le := binary.LittleEndian
	v := r.v6hi
	n := len(v) / 8
	top := func(i int) uint64 { return le.Uint64(v[i*8:]) >> (64 - v6IdxBits) }
	dir := new([1 << v6IdxBits]uint32)

	// Size each bucket's level-2 table from the number of starts in it. A
	// bucket of c > v6SlotStarts starts gets fewer than c/4 slots, so there
	// are fewer than 2^16 + n/4 in all: under 2^27 for any input below
	// maxFileSize (a GEO2 start takes at least 9 bytes, a GEO1 one 20, so
	// n < 2^31/9 < 2^28), which leaves v6KBits bits for k in a uint32, and n
	// fits the 31 index bits of a v6l2 entry.
	slots, i := 0, 0
	for g := range dir {
		j := i
		for j < n && top(j) == uint64(g) {
			j++
		}
		k := v6L2Bits(j - i)
		dir[g] = uint32(slots)<<v6KBits | uint32(k)
		slots += 1 << k
		i = j
	}
	l2 := make([]uint32, slots+1)
	i = 0
	for g, d := range dir {
		o, k := int(d>>v6KBits), d&v6KMask
		for s := range 1 << k {
			for i < n && top(i) == uint64(g) && v6Slot(le.Uint64(v[i*8:]), k) < uint64(s) {
				i++
			}
			l2[o+s] = uint32(i)
		}
		for i < n && top(i) == uint64(g) {
			i++
		}
	}
	l2[slots] = uint32(n)
	r.v6dir, r.v6l2 = dir, l2
}

// v6L2Bits returns the level-2 bits for a top-16 bucket holding count range
// starts: ceil(log2(count/v6SlotStarts)), clamped to [0, v6MaxL2Bits].
func v6L2Bits(count int) int {
	k := 0
	for k < v6MaxL2Bits && count > v6SlotStarts<<k {
		k++
	}
	return k
}

// v6Slot returns the k bits that follow the top 16 bits of hi, the level-2
// slot of hi in its bucket. k is at most v6MaxL2Bits; for k == 0 it is 0.
func v6Slot(hi uint64, k uint32) uint64 {
	return hi << v6IdxBits >> 1 >> ((63 - k) & 63)
}

// less128 reports whether the 128-bit value ahi:alo is below bhi:blo.
func less128(ahi, alo, bhi, blo uint64) bool {
	return ahi < bhi || ahi == bhi && alo < blo
}
