package ip2geo

import (
	"encoding/binary"
	"iter"
	"math"
	"net/netip"
	"runtime"

	"github.com/haxqer/ip2geo/internal/format"
)

// LookupRange returns the range of addresses around ip that share its
// answer: the stored boundary range that contains ip. ok reports whether
// that range is covered (as Lookup's ok); an uncovered gap is returned with
// ok == false. For an IPv4 address, and for the IPv6 forms Lookup maps to
// IPv4, the range is the IPv4 one, in IPv4 form. The zero netip.Addr
// returns the zero Range and false.
//
// An IPv6 range stops short of those IPv6 forms (::/96, ::ffff:0:0/96,
// 2001::/32 and 2002::/16), so that every address in a returned range has
// the same answer.
func (r *Reader) LookupRange(ip netip.Addr) (Range, bool) {
	var rg Range
	var id uint32
	if ip.Is4() || ip.Is4In6() {
		a := ip.As4()
		rg, id = r.range4(binary.BigEndian.Uint32(a[:]))
	} else if ip.IsValid() {
		a := ip.As16()
		if v4, isV4 := embeddedV4(&a); isV4 {
			rg, id = r.range4(v4)
		} else {
			rg, id = r.range6(binary.BigEndian.Uint64(a[:8]), binary.BigEndian.Uint64(a[8:]))
		}
	}
	runtime.KeepAlive(r)
	return rg, id != 0
}

// Ranges iterates over the covered ranges in ascending order, the IPv4 ones
// (in IPv4 form) before the IPv6 ones, each with its details as
// LookupDetails would return them. Gaps are skipped; adjacent ranges may
// share a record when the source split them. IPv6 aliases of IPv4 space are
// not repeated: the IPv6 ranges leave out the prefixes Lookup maps to IPv4,
// as LookupRange does, so a stored range across one comes in parts.
func (r *Reader) Ranges() iter.Seq2[Range, Details] {
	return func(yield func(Range, Details) bool) {
		_ = r.ranges4(yield) && r.ranges6(yield)
		runtime.KeepAlive(r)
	}
}

// range4 returns the IPv4 boundary range holding key and its record id.
func (r *Reader) range4(key uint32) (Range, uint32) {
	i := r.index4(key)
	first, last, id := uint32(0), uint32(math.MaxUint32), uint32(0)
	if i >= 0 {
		first, id = r.start4(i), format.ReadID(r.v4ids, r.idWidth, i)
	}
	if i+1 < len(r.v4starts)/2 {
		last = r.start4(i+1) - 1
	}
	return Range{addr4(first), addr4(last)}, id
}

// range6 returns the IPv6 boundary range holding hi:lo, which is not in an
// alias prefix, and its record id. The range is cut short at the alias
// prefixes, whose addresses Lookup answers from the IPv4 table.
func (r *Reader) range6(hi, lo uint64) (Range, uint32) {
	i := r.index6(hi, lo)
	var fh, fl uint64
	lh, ll := uint64(math.MaxUint64), uint64(math.MaxUint64)
	var id uint32
	if i >= 0 {
		fh, fl = r.start6(i)
		id = format.ReadID(r.v6ids, r.idWidth, i)
	}
	if i+1 < len(r.v6hi)/8 {
		lh, ll = dec128(r.start6(i + 1))
	}
	for _, a := range &v6Aliases {
		if less128(a.lh, a.ll, hi, lo) && !less128(a.lh, a.ll, fh, fl) {
			fh, fl = inc128(a.lh, a.ll)
		}
		if less128(hi, lo, a.fh, a.fl) && !less128(lh, ll, a.fh, a.fl) {
			lh, ll = dec128(a.fh, a.fl)
		}
	}
	return Range{addr6(fh, fl), addr6(lh, ll)}, id
}

// v6Aliases are the IPv6 prefixes Lookup maps to IPv4, ascending, as their
// first (fh:fl) and last (lh:ll) addresses: ::/96, ::ffff:0:0/96,
// 2001::/32 (Teredo) and 2002::/16 (6to4). See embeddedV4.
var v6Aliases = [...]struct{ fh, fl, lh, ll uint64 }{
	{0, 0, 0, 1<<32 - 1},
	{0, 0xFFFF << 32, 0, 0xFFFF<<32 | (1<<32 - 1)},
	{0x2001_0000 << 32, 0, 0x2001_0000<<32 | (1<<32 - 1), math.MaxUint64},
	{0x2002 << 48, 0, 0x2002<<48 | (1<<48 - 1), math.MaxUint64},
}

// index4 is search4 returning the index of the boundary holding key, -1
// when key is below the first one.
func (r *Reader) index4(key uint32) int {
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
	return l - 1
}

// start4 returns the absolute start of IPv4 boundary i: its u16 value in
// the top-16 bucket g with V4IDX[g] <= i < V4IDX[g+1], the last bucket
// whose first boundary is at or before i.
func (r *Reader) start4(i int) uint32 {
	le := binary.LittleEndian
	// V4IDX[0] == 0 <= i < V4IDX[65536]: find the first g > 0 with
	// V4IDX[g] > i.
	l, h := 1, format.V4IdxEntries-1
	for l < h {
		mid := int(uint(l+h) >> 1)
		if uint64(le.Uint32(r.v4idx[mid*4:])) > uint64(i) {
			h = mid
		} else {
			l = mid + 1
		}
	}
	return uint32(l-1)<<16 | uint32(le.Uint16(r.v4starts[i*2:]))
}

// index6 is search6 returning the index of the boundary holding hi:lo, -1
// when it is below the first one.
func (r *Reader) index6(hi, lo uint64) int {
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
	return i
}

// start6 returns the 128-bit start of IPv6 boundary i.
func (r *Reader) start6(i int) (hi, lo uint64) {
	hi = binary.LittleEndian.Uint64(r.v6hi[i*8:])
	if r.v6exc[uint(i)>>6]&(1<<(uint(i)&63)) != 0 {
		lo = r.v6ExcLo(i)
	}
	return hi, lo
}

// ranges4 yields the covered IPv4 ranges, walking the boundaries and their
// top-16 buckets in step. It reports whether yield asked for more.
func (r *Reader) ranges4(yield func(Range, Details) bool) bool {
	le := binary.LittleEndian
	n := len(r.v4starts) / 2
	g := 0 // the bucket of the boundary start reads
	start := func(i int) uint32 {
		for uint64(le.Uint32(r.v4idx[g*4+4:])) <= uint64(i) {
			g++
		}
		return uint32(g)<<16 | uint32(le.Uint16(r.v4starts[i*2:]))
	}
	var first uint32
	if n > 0 {
		first = start(0)
	}
	for i := range n {
		next, last := uint32(0), uint32(math.MaxUint32)
		if i+1 < n {
			next = start(i + 1)
			last = next - 1
		}
		if id := format.ReadID(r.v4ids, r.idWidth, i); id != 0 {
			var d Details
			r.details(id, &d)
			if !yield(Range{addr4(first), addr4(last)}, d) {
				return false
			}
		}
		first = next
	}
	return true
}

// ranges6 yields the covered IPv6 ranges, walking the boundaries and the
// exception table in step. It reports whether yield asked for more.
func (r *Reader) ranges6(yield func(Range, Details) bool) bool {
	le := binary.LittleEndian
	n := len(r.v6hi) / 8
	nExc := len(r.v6excIdx) / 4
	j := 0 // the next exception
	start := func(i int) (uint64, uint64) {
		hi, lo := le.Uint64(r.v6hi[i*8:]), uint64(0)
		if j < nExc && int(le.Uint32(r.v6excIdx[j*4:])) == i {
			lo = le.Uint64(r.v6excLo[j*8:])
			j++
		}
		return hi, lo
	}
	var fh, fl uint64
	if n > 0 {
		fh, fl = start(0)
	}
	for i := range n {
		var nh, nl uint64
		lh, ll := uint64(math.MaxUint64), uint64(math.MaxUint64)
		if i+1 < n {
			nh, nl = start(i + 1)
			lh, ll = dec128(nh, nl)
		}
		if id := format.ReadID(r.v6ids, r.idWidth, i); id != 0 {
			var d Details
			r.details(id, &d)
			if !yieldUnaliased(yield, fh, fl, lh, ll, d) {
				return false
			}
		}
		fh, fl = nh, nl
	}
	return true
}

// yieldUnaliased yields the parts of the IPv6 range fh:fl to lh:ll outside
// the alias prefixes, which Lookup answers from the IPv4 table. It reports
// whether yield asked for more.
func yieldUnaliased(yield func(Range, Details) bool, fh, fl, lh, ll uint64, d Details) bool {
	for _, a := range &v6Aliases {
		if less128(a.lh, a.ll, fh, fl) {
			continue // below what is left
		}
		if less128(lh, ll, a.fh, a.fl) {
			break // above it
		}
		if less128(fh, fl, a.fh, a.fl) && !yield(Range{addr6(fh, fl), addr6(dec128(a.fh, a.fl))}, d) {
			return false
		}
		if !less128(a.lh, a.ll, lh, ll) {
			return true // the alias covers the rest
		}
		fh, fl = inc128(a.lh, a.ll)
	}
	return yield(Range{addr6(fh, fl), addr6(lh, ll)}, d)
}

func addr4(v uint32) netip.Addr {
	var a [4]byte
	binary.BigEndian.PutUint32(a[:], v)
	return netip.AddrFrom4(a)
}

func addr6(hi, lo uint64) netip.Addr {
	var a [16]byte
	binary.BigEndian.PutUint64(a[:8], hi)
	binary.BigEndian.PutUint64(a[8:], lo)
	return netip.AddrFrom16(a)
}

// dec128 returns hi:lo - 1; hi:lo must not be 0.
func dec128(hi, lo uint64) (uint64, uint64) {
	if lo == 0 {
		hi--
	}
	return hi, lo - 1
}

// inc128 returns hi:lo + 1; hi:lo must not be all ones.
func inc128(hi, lo uint64) (uint64, uint64) {
	if lo == math.MaxUint64 {
		hi++
	}
	return hi, lo + 1
}
