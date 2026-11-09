package ip2geo

import (
	"encoding/binary"
	"net/netip"
	"runtime"
	"sort"

	"github.com/haxqer/ip2geo/internal/format"
)

// idxBits for the direct-index (DXR) jump tables, chosen for a good
// size/speed balance. v4 uses the top 20 bits, v6 the top 16.
const (
	v4IdxBits = 20
	v4Shift   = 32 - v4IdxBits
	v6IdxBits = 16
)

// The IPv6 index, Reader.v6tab, is a tree of tables over the top halves of
// the starts, built at open. A table splits the addresses of its parent slot
// into 2^k slots on the k bits that follow the parent's; the root,
// v6tab[:1<<v6IdxBits+1], splits all of them on the top 16 bits. A slot that
// holds more than v6SplitStarts starts gets a child table, of
// v6TableBits(count) bits so that its starts spread to about v6SlotStarts
// per slot, unless its starts all share their top half (no split can
// separate them), the bits run out at 64, or v6tab would reach
// v6MaxEntries. Each table has an entry per slot and one more, the end of
// its last slot, and the tables are appended in depth-first order.
//
// A leaf of at most 64 starts spans at most 8 cache lines of V6STARTS. On
// the city databases a lookup then reads about three tables and takes about
// four search steps, against two tables and about eleven steps for a top-16
// bucket split once; a split at 32 or 128 starts measured the same speed,
// with more memory and a slower open below 64 and longer leaves above.
const (
	v6SplitStarts  = 64
	v6SlotStarts   = 8
	v6MaxTableBits = 16
	v6MaxEntries   = 1 << 26
)

// A v6tab entry is one of:
//   - a leaf: the index of the first start of its slot in the low 30 bits,
//     and v6SlotExc when one of the slot's starts is an exception;
//   - a child: v6SlotChild, k-1 in the 4 bits at v6ChildKShift and the
//     offset of the slot's child table in the low 26 bits. The slot's first
//     start is then that of the child's slot 0.
//
// The entry after a leaf's starts its next slot (or ends the table), so a
// leaf slot's starts run from its index to that entry's first start. A
// start index is below 2^28 (a GEO2 start takes at least 9 bytes, a GEO1
// one 20, of an input below maxFileSize = 2^31) and fits the 30 bits.
const (
	v6SlotExc      = 1 << 31
	v6SlotChild    = 1 << 30
	v6SlotIdxMask  = v6SlotChild - 1
	v6ChildKShift  = 26
	v6ChildOffMask = 1<<v6ChildKShift - 1
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
	alt      bool  // Lookup does more than decode; see langs below
	recWidth int   // packed record width, format.RecordWidth(profile)
	latDiv   float64

	// m is the memory mapping the sections are in, or nil when they are on
	// the Go heap. A cleanup on m unmaps the file once m is unreachable:
	// every copy of the Reader, and every Reader WithLanguage made from it,
	// shares m and keeps it reachable, and every method that reads the
	// sections keeps m alive until its last read (runtime.KeepAlive(r.m)).
	// The cleanup is on m rather than on the Reader because a Reader may be
	// copied by value: the copy refers to m but not to the original Reader.
	m *mapping

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
	// v6exc has bit i set when start i is an exception. v6tab is the tree
	// of tables over the top halves described above v6SplitStarts.
	v6hi     []byte
	v6ids    []byte
	v6excIdx []byte
	v6excLo  []byte
	v6exc    []uint64
	v6tab    []uint32

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
	// name. alt (beside profile, in the cache line Lookup reads anyway) is
	// set when Lookup must do more than decode: for an ASN database or a
	// language other than the primary one.
	langs   []string
	lang    int
	nameOff [format.NumNameKinds]int

	meta Metadata
}

// mapping is a memory-mapped file, released by a cleanup (see openMapped)
// once the mapping object is unreachable. It is a separate heap object so
// that its lifetime does not depend on any one Reader value.
type mapping struct {
	b []byte
}

// Profile reports the precision profile the database was built with.
func (r *Reader) Profile() Profile { return r.profile }

// Metadata returns the file's header and META fields and its table sizes.
func (r *Reader) Metadata() Metadata { return r.meta }

// Close does nothing and returns nil. A memory-mapped database is unmapped
// by the garbage collector once the Reader, every copy of it and every
// Reader WithLanguage made from it is unreachable (through
// runtime.AddCleanup), not by Close, so a Lookup that overlaps Close or
// follows it is still safe and keeps answering. Drop every reference to the
// Reader to release its memory.
// Close is safe to call concurrently with Lookup and more than once.
func (r *Reader) Close() error { return nil }

// Lookup resolves an IP address to a Record. ok is false when the address is
// not covered by the database, and for the zero netip.Addr. On a
// ProfileASN database, Lookup reports whether ip is covered with an empty
// Record: use ASN or LookupDetails for its data.
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
	// rec is still the zero Record: decode fills it in place, so the record
	// is written once, straight into the result.
	if id != 0 {
		if r.alt {
			r.decodeAlt(id, &rec)
		} else {
			r.decode(id, &rec)
		}
		ok = true
	}
	// The sections may live in a mapping that a cleanup releases once it is
	// unreachable: keep it alive until the last read from them.
	runtime.KeepAlive(r.m)
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
// 0 when the key is below the first start. Every start before the key's leaf
// slot has a smaller top half than the key and every start after it a
// larger one, so the binary search over the top halves covers the slot
// alone: it finds the last start i whose top half is <= hi. Starts sharing
// the key's top half are ordered by their low half, which is 0 except for
// the exceptions, so when the slot holds an exception the search steps back
// while start i has the key's top half and a low half above lo. A start
// before the slot has a smaller top half, so the step back stays in it.
func (r *Reader) search6(hi, lo uint64) uint32 {
	s := r.v6Leaf(hi)
	e := r.v6tab[s]
	l, h := int(e&v6SlotIdxMask), r.v6First(r.v6tab[s+1])
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

// v6Leaf returns the position in v6tab of the leaf entry of the slot holding
// the top half hi: it walks down from the root, each child table taking the
// k bits after the pos bits its ancestors took. The build keeps pos+k <= 64.
func (r *Reader) v6Leaf(hi uint64) uint {
	t := r.v6tab
	s := uint(hi >> (64 - v6IdxBits))
	pos := uint(v6IdxBits)
	for e := t[s]; e&v6SlotChild != 0; e = t[s] {
		k := uint(e>>v6ChildKShift&0xF) + 1
		s = uint(e&v6ChildOffMask) + uint(hi<<(pos&63)>>((64-k)&63))
		pos += k
	}
	return s
}

// v6First returns the index of the first start of the slot with entry e,
// the entry after a leaf's: e's own index, or that of the first slot of its
// child table when e has one.
func (r *Reader) v6First(e uint32) int {
	for e&v6SlotChild != 0 {
		e = r.v6tab[e&v6ChildOffMask]
	}
	return int(e & v6SlotIdxMask)
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

// decode reads packed record id into rec, which must be zero: it sets the
// fields of r's profile and leaves the others alone. Decoding in place
// rather than returning a Record spares Lookup a copy of the whole struct.
// Every string id in the record was checked against the string table at
// open. The record is read through array pointers, so that its fields need
// no bounds checks of their own.
func (r *Reader) decode(id uint32, rec *Record) {
	le := binary.LittleEndian
	off := int(id) * r.recWidth
	c := (*[format.RecWidthCountry]byte)(r.records[off : off+format.RecWidthCountry])
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
}

// buildIndex builds the in-memory indexes over validated sections: the
// top-20 IPv4 table, the IPv6 tree of tables and the IPv6 exception bitmap,
// and flags the leaf slots that hold an exception.
func (r *Reader) buildIndex() {
	le := binary.LittleEndian
	r.buildIndex4()
	r.buildIndex6()
	n6 := len(r.v6hi) / 8
	exc := make([]uint64, (n6+63)/64)
	for j := 0; j < len(r.v6excIdx); j += 4 {
		i := le.Uint32(r.v6excIdx[j:])
		exc[i>>6] |= 1 << (i & 63)
		r.v6tab[r.v6Leaf(le.Uint64(r.v6hi[i*8:]))] |= v6SlotExc
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

// buildIndex6 builds the IPv6 tree of tables described above v6SplitStarts
// over the top halves of the starts. It counts the entries first, so that
// v6tab is allocated once, at its final size: count and table split the
// same slots in the same order, both through childBits.
func (r *Reader) buildIndex6() {
	n := len(r.v6hi) / 8
	b := v6Builder{v: r.v6hi}
	b.count(0, v6IdxBits, 0, n)
	b.t, b.n = make([]uint32, b.n), 0
	b.table(0, v6IdxBits, 0, n)
	r.v6tab = b.t
}

// v6Builder lays out the tables of the IPv6 index in t.
type v6Builder struct {
	v []byte   // V6STARTS
	t []uint32 // the tables
	n int      // the entries laid out (or counted) so far
}

// hi returns the top half of start i.
func (b *v6Builder) hi(i int) uint64 { return binary.LittleEndian.Uint64(b.v[i*8:]) }

// table lays out at offset n the table that splits starts a:z, which share
// their top pos bits, on the next k bits, followed by the child tables of
// its crowded slots, and returns its offset.
func (b *v6Builder) table(pos, k uint, a, z int) uint32 {
	o := b.n
	b.n += 1<<k + 1
	tab := b.t[o:b.n]
	b.fill(tab, pos, k, a, z)
	b.split(tab, pos, k)
	return uint32(o)
}

// fill sets the 2^k+1 entries of tab, which must be zero, to the first start
// of each slot of starts a:z on the k bits after the pos bits they share, and
// the last one to z.
func (b *v6Builder) fill(tab []uint32, pos, k uint, a, z int) {
	sh := 64 - k
	if z-a > v6SlotStarts<<k {
		// More starts per slot than v6TableBits aims at, as in the root: a
		// binary search for each slot that does not start where the one
		// before it ends.
		i := a
		for s := range uint64(1) << k {
			if i < z && b.hi(i)<<pos>>sh < s {
				i += sort.Search(z-i, func(j int) bool { return b.hi(i+j)<<pos>>sh >= s })
			}
			tab[s] = uint32(i)
		}
		tab[1<<k] = uint32(z)
		return
	}
	// Entry s+1 starts out as the end of slot s when that slot holds a start
	// and 0 when it is empty; then each entry not set takes the one before
	// it. Neither loop branches on the data.
	v := b.v[a*8 : z*8]
	for j := 0; j+8 <= len(v); j += 8 {
		tab[binary.LittleEndian.Uint64(v[j:j+8])<<pos>>sh+1] = uint32(a + j/8 + 1)
	}
	for s, m := 0, uint32(a); s < len(tab); s++ {
		m = max(m, tab[s])
		tab[s] = m
	}
}

// split lays out the child tables of the crowded slots of tab, a table on
// the k bits after pos, and points the slots' entries at them.
func (b *v6Builder) split(tab []uint32, pos, k uint) {
	// Slot s+1 still holds its first start when slot s is split.
	for s := range 1 << k {
		l, h := int(tab[s]), int(tab[s+1])
		if h-l <= v6SplitStarts {
			continue // the common case, checked here before the call
		}
		if ck := b.childBits(pos+k, l, h); ck != 0 {
			c := b.table(pos+k, ck, l, h)
			tab[s] = v6SlotChild | uint32(ck-1)<<v6ChildKShift | c
		}
	}
}

// count adds to n the entries that table(pos, k, a, z) lays out, without
// laying them out: it splits the same slots in the same order. Rather than
// read every start, it reads every q-th one from a, q = v6SplitStarts/2: a
// slot of more than v6SplitStarts starts holds two of these in a row, so
// only where two in a row share a slot does it search for the slot's ends.
func (b *v6Builder) count(pos, k uint, a, z int) {
	const q = v6SplitStarts / 2
	b.n += 1<<k + 1
	sh := 64 - k
	for i := a; i+q < z; {
		s := b.hi(i) << pos >> sh
		if b.hi(i+q)<<pos>>sh != s {
			i += q
			continue
		}
		// The start q before i is in an earlier slot (or before a), else
		// the search would have stopped there.
		l := b.first(pos, sh, max(a, i-q+1), i, s)
		h, step := i+q, q
		for h+step < z && b.hi(h+step)<<pos>>sh == s {
			h += step
			step *= 2
		}
		h = b.first(pos, sh, h+1, min(h+step, z), s+1)
		if ck := b.childBits(pos+k, l, h); ck != 0 {
			b.count(pos+k, ck, l, h)
		}
		i = a + (h-a+q-1)/q*q
	}
}

// first returns the first of starts lo to hi-1 whose slot, bits pos to
// 64-sh, is t or above, or hi when there is none. Its steps do not branch on
// the data.
func (b *v6Builder) first(pos, sh uint, lo, hi int, t uint64) int {
	n := hi - lo
	if n <= 0 {
		return lo
	}
	for n > 1 {
		half := n / 2
		if b.hi(lo+half)<<pos>>sh < t {
			lo += half
		}
		n -= half
	}
	if b.hi(lo)<<pos>>sh < t {
		lo++
	}
	return lo
}

// childBits returns the bits of the child table of the slot of starts l:h,
// which share their top pos bits, or 0 when the slot is a leaf: it holds at
// most v6SplitStarts starts, or its starts share their top half, or pos is
// 64, or the table would take v6tab past v6MaxEntries.
func (b *v6Builder) childBits(pos uint, l, h int) uint {
	if h-l <= v6SplitStarts || pos == 64 || b.hi(l) == b.hi(h-1) {
		return 0
	}
	ck := min(uint(v6TableBits(h-l)), 64-pos)
	if b.n+1<<ck+1 > v6MaxEntries {
		return 0
	}
	return ck
}

// v6TableBits returns the bits of the child table of a slot holding count
// range starts: ceil(log2(count/v6SlotStarts)), clamped to [0, v6MaxTableBits].
func v6TableBits(count int) int {
	k := 0
	for k < v6MaxTableBits && count > v6SlotStarts<<k {
		k++
	}
	return k
}

// less128 reports whether the 128-bit value ahi:alo is below bhi:blo.
func less128(ahi, alo, bhi, blo uint64) bool {
	return ahi < bhi || ahi == bhi && alo < blo
}
