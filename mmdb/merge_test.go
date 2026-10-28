package mmdb

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
	"math/rand/v2"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"testing"
)

// net4 returns the range of an IPv4 CIDR.
func net4(cidr string, rec uint32) range4 {
	p := netip.MustParsePrefix(cidr)
	a := p.Addr().As4()
	start := binary.BigEndian.Uint32(a[:])
	return range4{start: start, last: start | uint32(uint64(1)<<(32-p.Bits())-1), rec: rec}
}

// net6 returns the range of an IPv6 CIDR.
func net6(cidr string, rec uint32) range6 {
	p := netip.MustParsePrefix(cidr)
	s := p.Addr().As16()
	return range6{start: s, last: lastV6(s, p.Bits()), rec: rec}
}

// mergeV4 feeds in to a merger4 in the given order.
func mergeV4(in []range4) blockList[boundary4] {
	var m merger4
	for _, r := range in {
		m.add(r)
	}
	return m.finish()
}

// mergeV6 feeds in to a merger6 in the given order.
func mergeV6(in []range6) blockList[boundary6] {
	var m merger6
	for _, r := range in {
		m.add(r)
	}
	return m.finish()
}

// refMergeV4 is the v0.1.0 merge, which sorted every network and then merged
// them in one pass. merger4 must give the same boundaries in any input order.
func refMergeV4(in []range4) blockList[boundary4] {
	in = slices.Clone(in)
	sort.Slice(in, func(i, j int) bool { return in[i].start < in[j].start })
	var out []boundary4
	var expected uint64
	var lastRec uint32
	for _, e := range in {
		if uint64(e.start) > expected && lastRec != 0 {
			out = append(out, boundary4{start: uint32(expected), rec: 0})
			lastRec = 0
		}
		if e.rec != lastRec {
			out = append(out, boundary4{start: e.start, rec: e.rec})
			lastRec = e.rec
		}
		expected = uint64(e.last) + 1
	}
	if lastRec != 0 && expected <= math.MaxUint32 {
		out = append(out, boundary4{start: uint32(expected), rec: 0})
	}
	return toBlocks(out)
}

// refMergeV6 is the v0.1.0 IPv6 merge.
func refMergeV6(in []range6) blockList[boundary6] {
	in = slices.Clone(in)
	sort.Slice(in, func(i, j int) bool { return cmp16b(in[i].start, in[j].start) < 0 })
	var out []boundary6
	var expected [16]byte
	haveExpected := false
	var lastRec uint32
	for _, e := range in {
		if lastRec != 0 {
			if haveExpected && cmp16b(e.start, expected) > 0 {
				out = append(out, boundary6{start: expected, rec: 0})
				lastRec = 0
			} else if !haveExpected && e.start != expected {
				out = append(out, boundary6{start: expected, rec: 0})
				lastRec = 0
			}
		}
		if e.rec != lastRec {
			out = append(out, boundary6{start: e.start, rec: e.rec})
			lastRec = e.rec
		}
		next, overflow := inc16(e.last)
		expected = next
		haveExpected = !overflow
	}
	if lastRec != 0 && haveExpected {
		out = append(out, boundary6{start: expected, rec: 0})
	}
	return toBlocks(out)
}

func toBlocks[T any](s []T) blockList[T] {
	var l blockList[T]
	for _, v := range s {
		l.push(v)
	}
	return l
}

// elems returns the elements of l, checking its block layout.
func elems[T any](l blockList[T]) []T {
	var s []T
	for i, blk := range l.blocks {
		if i < len(l.blocks)-1 && len(blk) != blockLen {
			panic(fmt.Sprintf("block %d of %d holds %d elements", i, len(l.blocks), len(blk)))
		}
		s = append(s, blk...)
	}
	if len(s) != l.n {
		panic(fmt.Sprintf("blocks hold %d elements, n = %d", len(s), l.n))
	}
	return s
}

func fmtV4(bs blockList[boundary4]) string {
	var parts []string
	for _, b := range elems(bs) {
		var a [4]byte
		binary.BigEndian.PutUint32(a[:], b.start)
		parts = append(parts, fmt.Sprintf("%s=%d", netip.AddrFrom4(a), b.rec))
	}
	return strings.Join(parts, " ")
}

func fmtV6(bs blockList[boundary6]) string {
	var parts []string
	for _, b := range elems(bs) {
		parts = append(parts, fmt.Sprintf("%s=%d", netip.AddrFrom16(b.start), b.rec))
	}
	return strings.Join(parts, " ")
}

// TestMergeV4 checks that runs of equal records merge and that gaps, and only
// gaps, get record 0, for sorted input and for input out of order.
func TestMergeV4(t *testing.T) {
	tests := []struct {
		name string
		in   []range4
		want string
	}{
		{"empty", nil, ""},
		{"single", []range4{net4("10.0.0.0/8", 1)}, "10.0.0.0=1 11.0.0.0=0"},
		{"adjacent same record merges",
			[]range4{net4("10.0.0.0/24", 2), net4("10.0.1.0/24", 2)}, "10.0.0.0=2 10.0.2.0=0"},
		{"adjacent different records",
			[]range4{net4("10.0.0.0/24", 1), net4("10.0.1.0/24", 2)}, "10.0.0.0=1 10.0.1.0=2 10.0.2.0=0"},
		{"gap between same record",
			[]range4{net4("10.0.0.0/24", 1), net4("10.0.2.0/24", 1)}, "10.0.0.0=1 10.0.1.0=0 10.0.2.0=1 10.0.3.0=0"},
		{"unsorted input",
			[]range4{net4("10.0.2.0/24", 3), net4("10.0.0.0/24", 1)}, "10.0.0.0=1 10.0.1.0=0 10.0.2.0=3 10.0.3.0=0"},
		{"out of order after a merged run",
			[]range4{net4("10.0.0.0/24", 1), net4("10.0.1.0/24", 1), net4("10.0.3.0/24", 2), net4("10.0.2.0/24", 1)},
			"10.0.0.0=1 10.0.3.0=2 10.0.4.0=0"},
		{"out of order after a gap",
			[]range4{net4("10.0.0.0/24", 1), net4("10.0.4.0/24", 1), net4("10.0.2.0/24", 2)},
			"10.0.0.0=1 10.0.1.0=0 10.0.2.0=2 10.0.3.0=0 10.0.4.0=1 10.0.5.0=0"},
		{"repeated start", []range4{net4("10.0.0.0/24", 1), net4("10.0.0.0/24", 1)}, "10.0.0.0=1 10.0.1.0=0"},
		{"starts at zero, ends at the top",
			[]range4{net4("0.0.0.0/1", 1), net4("128.0.0.0/1", 2)}, "0.0.0.0=1 128.0.0.0=2"},
		{"ends at the top after a gap",
			[]range4{net4("1.0.0.0/8", 1), net4("255.0.0.0/8", 1)}, "1.0.0.0=1 2.0.0.0=0 255.0.0.0=1"},
		{"out of order after the top",
			[]range4{net4("255.0.0.0/8", 1), net4("1.0.0.0/8", 1)}, "1.0.0.0=1 2.0.0.0=0 255.0.0.0=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fmtV4(mergeV4(tt.in)); got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestMergeV6(t *testing.T) {
	tests := []struct {
		name string
		in   []range6
		want string
	}{
		{"empty", nil, ""},
		{"single", []range6{net6("2001:db8::/32", 1)}, "2001:db8::=1 2001:db9::=0"},
		{"adjacent same record merges",
			[]range6{net6("2001:db8::/33", 4), net6("2001:db8:8000::/33", 4)}, "2001:db8::=4 2001:db9::=0"},
		{"gap between",
			[]range6{net6("2001:db8::/32", 1), net6("2001:dc0::/32", 2)}, "2001:db8::=1 2001:db9::=0 2001:dc0::=2 2001:dc1::=0"},
		{"unsorted input",
			[]range6{net6("2001:dc0::/32", 2), net6("2001:db8::/32", 1)}, "2001:db8::=1 2001:db9::=0 2001:dc0::=2 2001:dc1::=0"},
		{"out of order after a merged run",
			[]range6{net6("2001:db8::/33", 4), net6("2001:db8:8000::/33", 4), net6("2001:dc0::/32", 5), net6("2001:db9::/32", 4)},
			"2001:db8::=4 2001:dba::=0 2001:dc0::=5 2001:dc1::=0"},
		{"starts at ::", []range6{net6("::/1", 1), net6("8000::/1", 2)}, "::=1 8000::=2"},
		{"ends at the top",
			[]range6{net6("ff00::/8", 1)}, "ff00::=1"},
		{"out of order after the top",
			[]range6{net6("ff00::/8", 1), net6("2001:db8::/32", 1)}, "2001:db8::=1 2001:db9::=0 ff00::=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fmtV6(mergeV6(tt.in)); got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

// orders returns the input orders the merge is checked with: ascending, as
// generated (the streaming path), and out-of-order ones (the sort fallback).
// sorted must hold at least two ranges.
func orders[T any](rng *rand.Rand, sorted []T) map[string][]T {
	n := len(sorted)
	reversed := slices.Clone(sorted)
	slices.Reverse(reversed)
	shuffled := slices.Clone(sorted)
	rng.Shuffle(n, func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	swapped := slices.Clone(sorted) // one swap in the second half: fall back mid-stream
	i := (n-1)/2 + rng.IntN(n/2)
	swapped[i], swapped[i+1] = swapped[i+1], swapped[i]
	lastFirst := append([]T{}, sorted[n-1:]...) // the highest range first
	lastFirst = append(lastFirst, sorted[:n-1]...)
	return map[string][]T{
		"sorted": sorted, "reversed": reversed, "shuffled": shuffled, "swapped": swapped, "lastFirst": lastFirst,
	}
}

// TestMergeAnyOrderV4 checks merger4 against the v0.1.0 merge on random
// disjoint ranges, in ascending order and out of order.
func TestMergeAnyOrderV4(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for iter := range 300 {
		var in []range4
		addr := uint64(0) // next free address; 1<<32 after the top
		if iter%3 != 0 {
			addr = uint64(rng.IntN(1 << 20))
		}
		for n := 2 + rng.IntN(60); len(in) < n; {
			if rng.IntN(2) == 0 {
				addr += uint64(1) << rng.IntN(24) // gap
			}
			size := uint64(1) << rng.IntN(25)
			if iter%4 == 0 && rng.IntN(20) == 0 && addr < 1<<32 {
				size = 1<<32 - addr // ends at the top
			}
			if addr+size > 1<<32 {
				break
			}
			in = append(in, range4{start: uint32(addr), last: uint32(addr + size - 1), rec: 1 + uint32(rng.IntN(3))})
			addr += size
		}
		if len(in) < 2 {
			continue
		}
		want := fmtV4(refMergeV4(in))
		for name, order := range orders(rng, in) {
			var m merger4
			for _, r := range order {
				m.add(r)
			}
			ascending := slices.IsSortedFunc(order, func(a, b range4) int { return cmp.Compare(a.start, b.start) })
			if fellBack := m.unsorted != nil; fellBack == ascending {
				t.Errorf("iteration %d, %s: fell back to sorting = %v", iter, name, fellBack)
			}
			if got := fmtV4(m.finish()); got != want {
				t.Fatalf("iteration %d, %s:\n got %s\nwant %s\ninput %v", iter, name, got, want, order)
			}
		}
	}
}

// add128 returns a + 2^k and whether it overflowed 128 bits.
func add128(a [16]byte, k int) ([16]byte, bool) {
	hi := binary.BigEndian.Uint64(a[:8])
	lo := binary.BigEndian.Uint64(a[8:])
	var carry uint64
	if k < 64 {
		lo, carry = bits.Add64(lo, 1<<k, 0)
		hi, carry = bits.Add64(hi, 0, carry)
	} else {
		hi, carry = bits.Add64(hi, 1<<(k-64), 0)
	}
	binary.BigEndian.PutUint64(a[:8], hi)
	binary.BigEndian.PutUint64(a[8:], lo)
	return a, carry != 0
}

// TestMergeAnyOrderV6 is TestMergeAnyOrderV4 for IPv6, with ranges whose
// sizes carry across bytes and halves, and some that end at the top.
func TestMergeAnyOrderV6(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for iter := range 300 {
		var in []range6
		var addr [16]byte
		if iter%3 != 0 {
			addr[rng.IntN(16)] = byte(rng.IntN(256))
		}
		for n := 2 + rng.IntN(60); len(in) < n; {
			var over bool
			if rng.IntN(2) == 0 {
				if addr, over = add128(addr, rng.IntN(100)); over { // gap
					break
				}
			}
			rec := 1 + uint32(rng.IntN(3))
			if iter%4 == 0 && rng.IntN(10) == 0 {
				in = append(in, range6{start: addr, last: dec16([16]byte{}), rec: rec}) // ends at the top
				break
			}
			end, over := add128(addr, rng.IntN(100)) // one past the range
			if over {
				break
			}
			in = append(in, range6{start: addr, last: dec16(end), rec: rec})
			addr = end
		}
		if len(in) < 2 {
			continue
		}
		want := fmtV6(refMergeV6(in))
		for name, order := range orders(rng, in) {
			var m merger6
			for _, r := range order {
				m.add(r)
			}
			ascending := slices.IsSortedFunc(order, func(a, b range6) int { return cmp16b(a.start, b.start) })
			if fellBack := m.unsorted != nil; fellBack == ascending {
				t.Errorf("iteration %d, %s: fell back to sorting = %v", iter, name, fellBack)
			}
			if got := fmtV6(m.finish()); got != want {
				t.Fatalf("iteration %d, %s:\n got %s\nwant %s", iter, name, got, want)
			}
		}
	}
}

// TestLastV6 checks lastV6 against setting the host bits one by one.
func TestLastV6(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	for b := 0; b <= 128; b++ {
		for range 20 {
			var start [16]byte
			for i := range start {
				start[i] = byte(rng.IntN(256))
			}
			want := start
			for i := b; i < 128; i++ {
				want[i/8] |= 1 << (7 - uint(i%8))
			}
			if got := lastV6(start, b); got != want {
				t.Fatalf("lastV6(%x, %d) = %x, want %x", start, b, got, want)
			}
		}
	}
}

func TestInc16Dec16(t *testing.T) {
	var zero, ones [16]byte
	for i := range ones {
		ones[i] = 0xff
	}
	if got, over := inc16(ones); got != zero || !over {
		t.Errorf("inc16(all ones) = %x, %v", got, over)
	}
	if got := dec16(zero); got != ones {
		t.Errorf("dec16(::) = %x", got)
	}
	rng := rand.New(rand.NewPCG(7, 8))
	for range 1000 {
		var a [16]byte
		for i := range a {
			a[i] = byte(rng.IntN(4) * 85) // 0x00, 0x55, 0xaa, 0xff: many carries
		}
		if next, _ := inc16(a); dec16(next) != a {
			t.Fatalf("dec16(inc16(%x)) = %x", a, dec16(next))
		}
	}
}

func TestBlockList(t *testing.T) {
	var l blockList[uint32]
	const n = 2*blockLen + 3
	for i := range n {
		l.push(uint32(i))
	}
	if l.n != n || len(l.blocks) != 3 {
		t.Fatalf("n = %d, %d blocks; want %d, 3", l.n, len(l.blocks), n)
	}
	for i := range n {
		if got := l.at(i); got != uint32(i) {
			t.Fatalf("at(%d) = %d", i, got)
		}
	}
	if s := elems(l); len(s) != n || s[n-1] != n-1 {
		t.Fatalf("elems: len %d, last %d", len(s), s[len(s)-1])
	}
}

// TestMergeManyBlocks runs the merge, and the sort fallback, over more
// boundaries than fit in one block.
func TestMergeManyBlocks(t *testing.T) {
	const n = 3*blockLen + 7
	in4 := make([]range4, n)
	in6 := make([]range6, n)
	for i := range n {
		start := uint32(i) * 4 // a 1-address gap after every other range
		last := start + 2 + uint32(i%2)
		in4[i] = range4{start: start, last: last, rec: 1 + uint32(i%3)}
		var s, l [16]byte
		binary.BigEndian.PutUint32(s[12:], start)
		binary.BigEndian.PutUint32(l[12:], last)
		s[0], l[0] = 0x20, 0x20
		in6[i] = range6{start: s, last: l, rec: 1 + uint32(i%3)}
	}
	want4, want6 := fmtV4(refMergeV4(in4)), fmtV6(refMergeV6(in6))
	if got := fmtV4(mergeV4(in4)); got != want4 {
		t.Error("IPv4, ascending: boundaries differ from the v0.1.0 merge")
	}
	if got := fmtV6(mergeV6(in6)); got != want6 {
		t.Error("IPv6, ascending: boundaries differ from the v0.1.0 merge")
	}
	// One swap near the end: the fallback recovers 3+ blocks of boundaries.
	in4[n-3], in4[n-2] = in4[n-2], in4[n-3]
	in6[n-3], in6[n-2] = in6[n-2], in6[n-3]
	if got := fmtV4(mergeV4(in4)); got != want4 {
		t.Error("IPv4, out of order: boundaries differ from the v0.1.0 merge")
	}
	if got := fmtV6(mergeV6(in6)); got != want6 {
		t.Error("IPv6, out of order: boundaries differ from the v0.1.0 merge")
	}
}
