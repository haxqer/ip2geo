package build

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"math"
	"slices"
)

// range4 is an IPv4 network, from its first to its last address, whose data
// is record rec.
type range4 struct{ start, last, rec uint32 }

// range6 is range4 for IPv6.
type range6 struct {
	start, last [16]byte
	rec         uint32
}

// boundary4 starts a range of IPv4 addresses that maps to record rec, up to
// the next boundary. rec 0 marks a gap (no data).
type boundary4 struct{ start, rec uint32 }

// boundary6 is boundary4 for IPv6.
type boundary6 struct {
	start [16]byte
	rec   uint32
}

// blockLen is the number of elements in each block of a blockList.
const blockLen = 1 << 15

// blockList is an append-only list kept in blocks of blockLen elements.
// Unlike a slice it grows without copying its elements and has at most one
// partly used block, which matters for the millions of boundaries of a city
// build: a slice grown by append allocates about five times its final size
// on the way, and up to a quarter of it stays unused.
type blockList[T any] struct {
	blocks [][]T
	n      int
}

// push appends v.
func (l *blockList[T]) push(v T) {
	if l.n%blockLen == 0 {
		l.blocks = append(l.blocks, make([]T, 0, blockLen))
	}
	last := len(l.blocks) - 1
	l.blocks[last] = append(l.blocks[last], v)
	l.n++
}

// at returns element i.
func (l *blockList[T]) at(i int) T { return l.blocks[i/blockLen][i%blockLen] }

// merger4 turns IPv4 networks into sorted range boundaries as they arrive:
// adjacent networks with the same record merge into one run, and a run that
// is followed by a gap ends with a rec-0 boundary. Networks must not overlap
// and must not use rec 0, which Builder reserves for gaps.
//
// maxminddb's Networks yields networks in ascending order, so the merge
// normally runs in a single pass and never holds the networks themselves.
// Correctness does not depend on that order: when a network does not start
// above the previous one, add recovers the ranges merged so far from the
// boundaries and from then on collects every network, and finish sorts and
// merges them all. The boundaries are the same either way.
type merger4 struct {
	out      blockList[boundary4]
	expected uint64 // one past the last address of the previous range
	lastRec  uint32 // record of the open run; 0 before the first range and in a gap
	prev     uint32 // start of the previous range, once started
	started  bool
	unsorted []range4 // every range, once the input was out of order
}

// add merges the next network.
func (m *merger4) add(r range4) {
	switch {
	case m.unsorted != nil:
		m.unsorted = append(m.unsorted, r)
	case m.started && r.start <= m.prev:
		m.unsorted = append(m.ranges(), r)
	default:
		m.started, m.prev = true, r.start
		m.step(r)
	}
}

// step merges r, which starts above every range merged before.
func (m *merger4) step(r range4) {
	if uint64(r.start) > m.expected && m.lastRec != 0 {
		m.out.push(boundary4{start: uint32(m.expected), rec: 0})
		m.lastRec = 0
	}
	if r.rec != m.lastRec {
		m.out.push(boundary4{start: r.start, rec: r.rec})
		m.lastRec = r.rec
	}
	m.expected = uint64(r.last) + 1
}

// finish returns the boundaries of every network added. It must be called
// once, after the last add.
func (m *merger4) finish() blockList[boundary4] {
	if rs := m.unsorted; rs != nil {
		slices.SortFunc(rs, func(a, b range4) int { return cmp.Compare(a.start, b.start) })
		*m = merger4{}
		for _, r := range rs {
			m.step(r)
		}
	}
	if m.lastRec != 0 && m.expected <= math.MaxUint32 {
		m.out.push(boundary4{start: uint32(m.expected), rec: 0})
	}
	return m.out
}

// ranges returns the ranges merged so far, one per run: a boundary with a
// record runs up to the next boundary, and the open run (always the last
// boundary) up to the end of the previous range.
func (m *merger4) ranges() []range4 {
	rs := make([]range4, 0, m.out.n)
	for i := range m.out.n {
		b := m.out.at(i)
		if b.rec == 0 {
			continue
		}
		last := uint32(m.expected - 1)
		if i+1 < m.out.n {
			last = m.out.at(i+1).start - 1
		}
		rs = append(rs, range4{start: b.start, last: last, rec: b.rec})
	}
	return rs
}

// merger6 is merger4 for IPv6.
type merger6 struct {
	out blockList[boundary6]
	// expected is one past the last address of the previous range.
	// haveExpected is false before the first range and after a range that
	// ends at the top of the address space, where expected wraps to ::.
	expected     [16]byte
	haveExpected bool
	lastRec      uint32
	prev         [16]byte
	started      bool
	unsorted     []range6
}

// add merges the next network.
func (m *merger6) add(r range6) {
	switch {
	case m.unsorted != nil:
		m.unsorted = append(m.unsorted, r)
	case m.started && cmp16b(r.start, m.prev) <= 0:
		m.unsorted = append(m.ranges(), r)
	default:
		m.started, m.prev = true, r.start
		m.step(r)
	}
}

// step merges r, which starts above every range merged before.
func (m *merger6) step(r range6) {
	if m.lastRec != 0 {
		gap := r.start != m.expected
		if m.haveExpected {
			gap = cmp16b(r.start, m.expected) > 0
		}
		if gap {
			m.out.push(boundary6{start: m.expected, rec: 0})
			m.lastRec = 0
		}
	}
	if r.rec != m.lastRec {
		m.out.push(boundary6{start: r.start, rec: r.rec})
		m.lastRec = r.rec
	}
	next, overflow := inc16(r.last)
	m.expected, m.haveExpected = next, !overflow
}

// finish returns the boundaries of every network added. It must be called
// once, after the last add.
func (m *merger6) finish() blockList[boundary6] {
	if rs := m.unsorted; rs != nil {
		slices.SortFunc(rs, func(a, b range6) int { return cmp16b(a.start, b.start) })
		*m = merger6{}
		for _, r := range rs {
			m.step(r)
		}
	}
	if m.lastRec != 0 && m.haveExpected {
		m.out.push(boundary6{start: m.expected, rec: 0})
	}
	return m.out
}

// ranges is merger4.ranges for IPv6. When the open run ends at the top of
// the address space, expected has wrapped to :: and dec16 wraps it back.
func (m *merger6) ranges() []range6 {
	rs := make([]range6, 0, m.out.n)
	for i := range m.out.n {
		b := m.out.at(i)
		if b.rec == 0 {
			continue
		}
		last := dec16(m.expected)
		if i+1 < m.out.n {
			last = dec16(m.out.at(i + 1).start)
		}
		rs = append(rs, range6{start: b.start, last: last, rec: b.rec})
	}
	return rs
}

func cmp16b(a, b [16]byte) int { return bytes.Compare(a[:], b[:]) }

// inc16 returns a+1 and whether it wrapped around to ::.
func inc16(a [16]byte) (res [16]byte, overflow bool) {
	res = a
	for i := 15; i >= 0; i-- {
		res[i]++
		if res[i] != 0 {
			return res, false
		}
	}
	return res, true
}

// dec16 returns a-1, wrapping :: around to the all-ones address.
func dec16(a [16]byte) [16]byte {
	for i := 15; i >= 0; i-- {
		a[i]--
		if a[i] != 0xff {
			return a
		}
	}
	return a
}

// lastV6 returns the last address of the network start/bits: start with
// every host bit set.
func lastV6(start [16]byte, bits int) [16]byte {
	hi := binary.BigEndian.Uint64(start[:8])
	lo := binary.BigEndian.Uint64(start[8:])
	const ones = ^uint64(0)
	if bits < 64 {
		hi |= ones >> bits
		lo = ones
	} else {
		lo |= ones >> (bits - 64) // bits 128 shifts by 64, which gives 0
	}
	var last [16]byte
	binary.BigEndian.PutUint64(last[:8], hi)
	binary.BigEndian.PutUint64(last[8:], lo)
	return last
}
