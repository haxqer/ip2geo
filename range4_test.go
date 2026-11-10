package ip2geo

import (
	"encoding/binary"
	"math"
	"math/bits"
	"math/rand/v2"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
)

// refRange4 is the IPv4 range holding k and its record id, from the absolute
// starts (refStarts4) by a plain binary search.
func refRange4(r *Reader, starts []uint32, k uint32) (Range, uint32) {
	i := sort.Search(len(starts), func(j int) bool { return starts[j] > k }) - 1
	return refRange4At(r, starts, i)
}

// refRange4At is refRange4 for the key whose last start <= it is start i.
func refRange4At(r *Reader, starts []uint32, i int) (Range, uint32) {
	rg := Range{refAddr4(0), refAddr4(math.MaxUint32)}
	var id uint32
	if i >= 0 {
		rg.First, id = refAddr4(starts[i]), readID(r.v4ids, r.idWidth, i)
	}
	if i+1 < len(starts) {
		rg.Last = refAddr4(starts[i+1] - 1)
	}
	return rg, id
}

// checkOcc4 checks the bucket map against V4IDX: the bit of every bucket,
// and each word's nearest non-empty buckets outside it (0 for none).
func checkOcc4(t *testing.T, r *Reader) {
	t.Helper()
	le := binary.LittleEndian
	var full []int // the non-empty buckets, ascending
	for g := range 1 << 16 {
		if le.Uint32(r.v4idx[g*4:]) != le.Uint32(r.v4idx[g*4+4:]) {
			full = append(full, g)
		}
	}
	for w := range r.occ4 {
		o := r.occ4[w]
		lo := sort.SearchInts(full, w<<6)    // the first in or after word w
		hi := sort.SearchInts(full, w<<6+64) // the first after it
		var want uint64
		for _, g := range full[lo:hi] {
			want |= 1 << (g & 63)
		}
		prev, next := 0, 0
		if lo > 0 {
			prev = full[lo-1]
		}
		if hi < len(full) {
			next = full[hi]
		}
		if o.full != want || int(o.prev) != prev || int(o.next) != next {
			t.Fatalf("occ4[%d] = {%016x %d %d}, want {%016x %d %d}", w, o.full, o.prev, o.next, want, prev, next)
		}
	}
}

// checkRange4 checks range4, and on a sample LookupRange, against a plain
// search over the absolute starts: at every start and the addresses either
// side of it, at the first and last address of every top-16 bucket and the
// addresses either side of them, and at random addresses.
func checkRange4(t *testing.T, r *Reader, random int) {
	t.Helper()
	checkOcc4(t, r)
	starts := refStarts4(r)
	n := len(starts)
	probes := 0
	check := func(k uint32, want Range, id uint32) {
		t.Helper()
		if got, gid := r.range4(k); got != want || gid != id {
			t.Fatalf("range4(%v) = %v, %d; want %v, %d", refAddr4(k), got, gid, want, id)
		}
		if probes++; probes%61 != 0 {
			return
		}
		a := refAddr4(k)
		for _, a := range []netip.Addr{a, netip.AddrFrom16(a.As16())} {
			if got, ok := r.LookupRange(a); got != want || ok != (id != 0) {
				t.Fatalf("LookupRange(%v) = %v, %v; want %v, %v", a, got, ok, want, id != 0)
			}
		}
	}
	// At and either side of every start, the reference index is known.
	for i, s := range starts {
		rg, id := refRange4At(r, starts, i)
		check(s, rg, id)
		if s > 0 {
			rg, id := refRange4At(r, starts, i-1)
			check(s-1, rg, id)
		}
		if s < math.MaxUint32 {
			j := i
			if i+1 < n && starts[i+1] == s+1 {
				j = i + 1
			}
			rg, id := refRange4At(r, starts, j)
			check(s+1, rg, id)
		}
	}
	keys := []uint32{0, 1, math.MaxUint32 - 1, math.MaxUint32}
	for g := range uint32(1 << 16) {
		keys = append(keys, g<<16, g<<16|1, g<<16|0xFFFF, g<<16|0xFFFE)
	}
	rng := rand.New(rand.NewPCG(uint64(n), 4))
	for range random {
		k := rng.Uint32()
		if n > 0 && rng.IntN(2) == 0 {
			k = starts[rng.IntN(n)] ^ rng.Uint32()>>rng.IntN(33) // near a start
		}
		keys = append(keys, k)
	}
	for _, k := range keys {
		rg, id := refRange4(r, starts, k)
		check(k, rg, id)
	}
	t.Logf("%d starts, %d probes", n, probes)
}

// v4DB returns a country-profile database whose IPv4 ranges start at
// starts, every third range without data and the others pointing at record
// i%7+1, with the IPv6 table of cityTestDB.
func v4DB(starts []uint32) *testDB {
	d := cityTestDB().withProfile(ProfileCountry)
	d.recCount = 8
	d.records = make([]byte, int(d.recCount)*int(d.recWidth))
	d.v4starts = starts
	d.v4rec = make([]uint32, len(starts))
	for i := range d.v4rec {
		if i%3 != 0 {
			d.v4rec[i] = uint32(i%7 + 1)
		}
	}
	return d
}

// sparseV4Starts returns sorted, distinct starts in nb random top-16
// buckets, 1 to 4 in each, with long empty runs between them.
func sparseV4Starts(seed uint64, nb int) []uint32 {
	rng := rand.New(rand.NewPCG(seed, 9))
	set := map[uint32]bool{}
	for range nb {
		g := rng.Uint32N(1 << 16)
		for range 1 + rng.IntN(4) {
			switch rng.IntN(4) {
			case 0:
				set[g<<16] = true
			case 1:
				set[g<<16|0xFFFF] = true
			default:
				set[g<<16|rng.Uint32N(1<<16)] = true
			}
		}
	}
	out := make([]uint32, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

func TestRange4(t *testing.T) {
	var everyWord, everyBucket, wordEdges []uint32
	for w := range uint32(1 << 10) {
		everyWord = append(everyWord, (w<<6|w&63)<<16|w) // one bucket per word
	}
	for g := range uint32(1 << 16) {
		everyBucket = append(everyBucket, g<<16|uint32(bits.Reverse16(uint16(g))))
	}
	for _, g := range []uint32{0, 1, 62, 63, 64, 65, 127, 128, 1<<16 - 65, 1<<16 - 64, 1<<16 - 2, 1<<16 - 1} {
		wordEdges = append(wordEdges, g<<16, g<<16|0x8000)
	}
	cases := []struct {
		name   string
		starts []uint32
	}{
		{"empty", nil},
		{"single at 0", []uint32{0}},
		{"single in the middle", []uint32{0x12345678}},
		{"single at a bucket base", []uint32{0x7F000000}},
		{"single at the last address", []uint32{math.MaxUint32}},
		{"first and last buckets", []uint32{0, 0xFFFF, 0x10000, 0xFFFF0000, 0xFFFFFFFE, math.MaxUint32}},
		{"first bucket only", []uint32{0, 1, 0x8000, 0xFFFF}},
		{"last bucket only", []uint32{0xFFFF0000, 0xFFFF8000, math.MaxUint32}},
		{"long empty runs", []uint32{0x01000000, 0x01000005, 0x7F000000, 0x7F00FFFF, 0xFE000001}},
		{"bucket edges", []uint32{0x0000FFFF, 0x00010000, 0x0001FFFF, 0x00020000, 0x003FFFFF, 0x00400000, 0x7FFFFFFF, 0x80000000}},
		{"word edges", wordEdges},
		{"one bucket per word", everyWord},
		{"every bucket", everyBucket},
		{"sparse", sparseV4Starts(1, 300)},
		{"very sparse", sparseV4Starts(2, 5)},
		{"dense run", func() []uint32 {
			var s []uint32
			for k := range uint32(3000) {
				s = append(s, 0x0A000000+k*7)
			}
			return s
		}()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := v4DB(c.starts)
			b, _ := d.encode()
			for _, r := range []*Reader{openGEO2(t, b), openGEO2(t, encodeGEO2(d))} {
				checkRange4(t, r, 20_000)
			}
		})
	}
	t.Run("random", func(t *testing.T) {
		d := randomTestDB(13, ProfileCity, 300, 20_000, 100)
		b, _ := d.encode()
		for _, r := range []*Reader{openGEO2(t, b), openGEO2(t, encodeGEO2(d))} {
			checkRange4(t, r, 100_000)
		}
	})
}

// TestRange4File runs checkRange4 on each database file of the
// IP2GEO_TEST_GEOC list, and skips when it is unset.
func TestRange4File(t *testing.T) {
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
			checkRange4(t, r, 1<<20)
		})
	}
}
