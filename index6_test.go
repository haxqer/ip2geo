package ip2geo

import (
	"encoding/binary"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
)

// v6Ref holds the IPv6 starts of a Reader as two halves, read back from the
// sections as docs/FORMAT.md defines them, for a plain binary search.
type v6Ref struct{ hi, lo []uint64 }

func newV6Ref(r *Reader) v6Ref {
	starts := refStarts6(r)
	f := v6Ref{make([]uint64, len(starts)), make([]uint64, len(starts))}
	for i, s := range starts {
		f.hi[i], f.lo[i] = binary.BigEndian.Uint64(s[:8]), binary.BigEndian.Uint64(s[8:])
	}
	return f
}

// index returns the last start <= hi:lo, -1 when there is none.
func (f v6Ref) index(hi, lo uint64) int {
	l, h := 0, len(f.hi)
	for l < h {
		m := int(uint(l+h) >> 1)
		if f.hi[m] > hi || f.hi[m] == hi && f.lo[m] > lo {
			h = m
		} else {
			l = m + 1
		}
	}
	return l - 1
}

// checkV6Index checks index6 and search6 against a plain binary search over
// the starts of r: at every start, the addresses either side of it and the
// first and last address with its top half; at the first and last address
// of every leaf slot of the IPv6 index and the addresses either side of
// them; and at random addresses that keep a random number of top bits of a
// random start. A sample of the probes that Lookup does not map to IPv4 also
// goes through Lookup and LookupRange.
func checkV6Index(t *testing.T, r *Reader, random int) {
	t.Helper()
	f := newV6Ref(r)
	n := len(f.hi)
	probes := 0
	check := func(hi, lo uint64) {
		t.Helper()
		want := f.index(hi, lo)
		if got := r.index6(hi, lo); got != want {
			t.Fatalf("index6(%v) = %d, want %d", addr6(hi, lo), got, want)
		}
		var id uint32
		if want >= 0 {
			id = readID(r.v6ids, r.idWidth, want)
		}
		if got := r.search6(hi, lo); got != id {
			t.Fatalf("search6(%v) = %d, want %d (start %d)", addr6(hi, lo), got, id, want)
		}
		if probes++; probes%61 != 0 {
			return
		}
		a := addr6(hi, lo)
		if k := a.As16(); a.Is4In6() {
			return
		} else if _, isV4 := embeddedV4(&k); isV4 {
			return
		}
		_, ok := r.Lookup(a)
		rg, rok := r.LookupRange(a)
		if ok != (id != 0) || rok != ok || rg.First.Compare(a) > 0 || rg.Last.Compare(a) < 0 {
			t.Fatalf("%v: Lookup ok=%v, LookupRange %v ok=%v; want ok=%v and a range around it", a, ok, rg, rok, id != 0)
		}
	}

	for i := range n {
		hi, lo := f.hi[i], f.lo[i]
		check(hi, lo)
		if hi|lo != 0 {
			check(dec128(hi, lo))
		}
		if hi&lo != math.MaxUint64 {
			check(inc128(hi, lo))
		}
		check(hi, 0)
		check(hi, math.MaxUint64)
	}

	// The leaf slots, as the prefix of pos+k bits that each covers. The
	// tables reached must fill v6tab, which the build sized beforehand.
	leaves, entries := 0, 0
	var walk func(o int, prefix uint64, pos, k uint)
	walk = func(o int, prefix uint64, pos, k uint) {
		if k == 0 || pos+k > 64 {
			t.Fatalf("table at %d takes bits %d to %d", o, pos, pos+k)
		}
		entries += 1<<k + 1
		for s := range uint64(1) << k {
			p := prefix | s<<(64-pos-k)
			if e := r.v6tab[o+int(s)]; e&v6SlotChild != 0 {
				walk(int(e&v6ChildOffMask), p, pos+k, uint(e>>v6ChildKShift&0xF)+1)
				continue
			}
			leaves++
			last := p | (1<<(64-pos-k) - 1)
			check(p, 0)
			check(last, math.MaxUint64)
			if p != 0 {
				check(p-1, math.MaxUint64)
			}
			if last != math.MaxUint64 {
				check(last+1, 0)
			}
		}
	}
	walk(0, 0, 0, v6IdxBits)
	if entries != len(r.v6tab) {
		t.Fatalf("the tables hold %d entries, v6tab %d", entries, len(r.v6tab))
	}

	rng := rand.New(rand.NewPCG(uint64(n), 7))
	for range random {
		var hi, lo uint64
		if n > 0 {
			i := rng.IntN(n)
			hi, lo = f.hi[i], f.lo[i]
		}
		rh, rl := rng.Uint64(), rng.Uint64()
		switch b := rng.IntN(129); { // keep the top b bits
		case b == 0:
			hi, lo = rh, rl
		case b < 64:
			m := uint64(math.MaxUint64) >> b
			hi, lo = hi&^m|rh&m, rl
		case b < 128:
			m := uint64(math.MaxUint64) >> (b - 64)
			lo = lo&^m | rl&m
		}
		check(hi, lo)
	}
	t.Logf("%d starts, %d leaf slots, %d probes", n, leaves, probes)
}

// crowdedV6Starts returns starts that stress the IPv6 index past what the
// city databases do: a run sharing one top half (exceptions, which no table
// can split), runs that share top halves differing only in their last 3
// bits (the tables go down to bit 64, the last one cut to the bits left),
// and a dense block over several levels with
// exceptions in its leaves; the last start is the last address.
func crowdedV6Starts() [][16]byte {
	var out [][16]byte
	for j := range uint64(3 * v6SplitStarts) {
		out = append(out, key16(0x2a03<<48|1, j))
	}
	for j := range uint64(8) {
		for l := range uint64(v6SplitStarts / 2) {
			out = append(out, key16(0x2a04<<48|j, l))
		}
	}
	for j := range uint64(5000) {
		out = append(out, key16(0x2a05<<48|j<<20, 0))
		if j%7 == 0 {
			out = append(out, key16(0x2a05<<48|j<<20, 1<<63))
		}
	}
	return append(out, key16(math.MaxUint64, math.MaxUint64))
}

func TestV6IndexCrowded(t *testing.T) {
	starts := crowdedV6Starts()
	r := openDB(t, v6DB(starts))
	// The run sharing a top half stays one leaf.
	if p := v6PathBits(r, 0x2a03<<48|1); p[len(p)-1] == 64 {
		t.Errorf("the run sharing 2a03::1/64 was split down to %v", p)
	}
	if p := v6PathBits(r, 0x2a04<<48|1); p[len(p)-1] != 64 {
		t.Errorf("the run in 2a04::/64 has leaf bits %v, want down to 64", p)
	}
	if p := v6PathBits(r, 0x2a05<<48|1234<<20); len(p) < 3 {
		t.Errorf("the dense block in 2a05::/16 has table bits %v, want 3 levels or more", p)
	}
	checkV6Index(t, r, 100_000)
}

func TestV6IndexExhaustive(t *testing.T) {
	cases := []struct {
		name string
		d    *testDB
	}{
		{"exceptions", excTestDB()},
		{"slot edges", v6DB(slotEdgeStarts(0x2a02, 9))},
		{"random city", randomTestDB(11, ProfileCity, 300, 1000, 20_000)},
		{"random country", randomTestDB(12, ProfileCountry, 300, 1000, 50_000)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, _ := c.d.encode()
			for _, r := range []*Reader{openGEO2(t, b), openGEO2(t, encodeGEO2(c.d))} {
				checkV6Index(t, r, 100_000)
			}
		})
	}
}

// TestV6IndexFile runs checkV6Index on each database file of the
// IP2GEO_TEST_GEOC list, and skips when it is unset.
func TestV6IndexFile(t *testing.T) {
	list := os.Getenv("IP2GEO_TEST_GEOC")
	if list == "" {
		t.Skip("IP2GEO_TEST_GEOC is not set")
	}
	for _, path := range filepath.SplitList(list) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			r, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			checkV6Index(t, r, 1<<20)
		})
	}
}
