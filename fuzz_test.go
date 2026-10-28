package ip2geo

import (
	"encoding/binary"
	"net/netip"
	"sort"
	"testing"
)

// fuzzProbes covers both tables, both ends of the address space, the alias
// prefixes and the zero Addr.
var fuzzProbes = func() []netip.Addr {
	out := []netip.Addr{{}}
	for _, s := range []string{
		"0.0.0.0", "10.0.0.5", "8.8.8.8", "255.255.255.255",
		"::", "::1", "::10.0.0.5", "::ffff:10.0.0.5", "2002:a00:5::1", "2001:0:a00:5::1",
		"64:ff9b::a00:5", "2001:db8::1", "fe80::1%eth0", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff",
	} {
		out = append(out, netip.MustParseAddr(s))
	}
	return out
}()

// v4Probes are IPv4 keys whose IPv6 forms must answer like the key itself.
var v4Probes = []uint32{0, 1, 0x0A000005, 0x08080808, 0x7FFFFFFF, 0x80000000, 0xFFFFFFFF}

// fuzzSeeds are databases that open: every profile, empty tables, and many
// ranges straddling jump-table buckets.
func fuzzSeeds() [][]byte {
	var out [][]byte
	add := func(d *testDB) {
		b, _ := d.encode()
		out = append(out, b)
	}
	add(cityTestDB())
	add(cityTestDB().withProfile(ProfileCountry))
	add(cityTestDB().withProfile(ProfileRegion))

	d := cityTestDB()
	d.v4starts, d.v4rec, d.v6starts, d.v6rec = nil, nil, nil, nil
	add(d)

	d = cityTestDB()
	d.v4starts, d.v4rec, d.v6starts, d.v6rec = nil, nil, nil, nil
	for i := uint32(0); i < 64; i++ {
		start := i<<26 | (i&3)<<v4Shift // spread over buckets, some on an edge
		if i&1 == 1 {
			start-- // and some just below one
		}
		d.v4starts = append(d.v4starts, start)
		d.v4rec = append(d.v4rec, i%3)
		var s [16]byte
		s[0], s[1], s[15] = byte(i*4), byte(i), byte(i&1)
		d.v6starts = append(d.v6starts, s)
		d.v6rec = append(d.v6rec, (i+1)%3)
	}
	add(d)

	// Smallest valid database: one empty string, record 0, no ranges.
	out = append(out, []byte("GEO1\x01\x00\x00\x00"+"\x01\x00\x00\x00\x00\x00"+"\x01\x00\x00\x00\x10\x00"+
		string(make([]byte, 16))+"\x00\x00\x00\x00"+"\x00\x00\x00\x00"))
	return out
}

func v4Forms(a [4]byte) []netip.Addr {
	var mapped, compat, sixToFour, teredo [16]byte
	mapped[10], mapped[11] = 0xFF, 0xFF
	copy(mapped[12:], a[:])
	copy(compat[12:], a[:])
	sixToFour[0], sixToFour[1], sixToFour[15] = 0x20, 0x02, 1
	copy(sixToFour[2:], a[:])
	teredo[0], teredo[1], teredo[15] = 0x20, 0x01, 1
	copy(teredo[4:], a[:])
	return []netip.Addr{netip.AddrFrom16(mapped), netip.AddrFrom16(compat), netip.AddrFrom16(sixToFour), netip.AddrFrom16(teredo)}
}

// checkReader looks up the fixed probes, and checks that every IPv6 form of
// an IPv4 address answers like the address itself.
func checkReader(t *testing.T, r *Reader) {
	for _, a := range fuzzProbes {
		r.Lookup(a)
	}
	for _, v := range v4Probes {
		var a4 [4]byte
		binary.BigEndian.PutUint32(a4[:], v)
		want, wantOK := r.Lookup(netip.AddrFrom4(a4))
		for _, a := range v4Forms(a4) {
			if got, ok := r.Lookup(a); ok != wantOK || got != want {
				t.Fatalf("Lookup(%v) = %+v, %v; Lookup(%v) = %+v, %v", a, got, ok, netip.AddrFrom4(a4), want, wantOK)
			}
		}
	}
}

func add16(k [16]byte, delta int) [16]byte {
	for i := 15; i >= 0; i-- {
		old := k[i]
		k[i] += byte(delta)
		if (delta > 0 && k[i] > old) || (delta < 0 && k[i] < old) {
			break
		}
	}
	return k
}

// checkIndex compares the jump-table searches with a plain binary search
// over the starts, around every start and bucket edge.
func checkIndex(t *testing.T, r *Reader) {
	const maxStarts = 256
	ref4 := func(k uint32) uint32 {
		i := sort.Search(len(r.v4starts), func(i int) bool { return r.v4starts[i] > k })
		if i == 0 {
			return 0
		}
		return r.v4rec[i-1]
	}
	keys4 := []uint32{0, 1, 1<<v4Shift - 1, 1 << v4Shift, 0x7FFFFFFF, 0x80000000, 0xFFFFFFFF}
	for i, s := range r.v4starts {
		if i == maxStarts {
			break
		}
		g := s &^ (1<<v4Shift - 1)
		keys4 = append(keys4, s-1, s, s+1, g, g-1, g|(1<<v4Shift-1))
	}
	for _, k := range keys4 {
		want := ref4(k)
		if got := r.search4(k); got != want {
			t.Fatalf("search4(%08x) = %d, want %d", k, got, want)
		}
		var a [4]byte
		binary.BigEndian.PutUint32(a[:], k)
		rec, ok := r.Lookup(netip.AddrFrom4(a))
		if ok != (want != 0) || (ok && rec != r.decode(want)) {
			t.Fatalf("Lookup(%v) = %+v, %v; want record %d", netip.AddrFrom4(a), rec, ok, want)
		}
	}

	n6 := len(r.v6rec)
	start6 := func(i int) (k [16]byte) {
		copy(k[:], r.v6starts[i*16:])
		return k
	}
	ref6 := func(k [16]byte) uint32 {
		i := sort.Search(n6, func(i int) bool { return cmp16(r.v6starts[i*16:], k[:]) > 0 })
		if i == 0 {
			return 0
		}
		return r.v6rec[i-1]
	}
	var zero, ones [16]byte
	for i := range ones {
		ones[i] = 0xFF
	}
	keys6 := [][16]byte{zero, ones}
	for i := 0; i < n6 && i < maxStarts; i++ {
		s := start6(i)
		lo, hi := s, s
		clear(lo[2:])
		for j := 2; j < 16; j++ {
			hi[j] = 0xFF
		}
		keys6 = append(keys6, add16(s, -1), s, add16(s, 1), lo, add16(lo, -1), hi)
	}
	for _, k := range keys6 {
		if got, want := r.search6(k), ref6(k); got != want {
			t.Fatalf("search6(%v) = %d, want %d", netip.AddrFrom16(k), got, want)
		}
	}
}

// FuzzOpenBytes: OpenBytes never panics, fails only with a sentinel error,
// and a Reader it returns answers lookups without panicking.
func FuzzOpenBytes(f *testing.F) {
	for _, b := range fuzzSeeds() {
		f.Add(b)
	}
	for _, s := range []string{"", "GEO1", "GEO1\x01\x00\x00\x00", "GEO1\x01\x00\x00\x00\xff\xff\xff\xff", crashRecWidth, crashRecID} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := OpenBytes(b)
		if err != nil {
			if r != nil || !isSentinel(err) {
				t.Fatalf("OpenBytes = %v, %v; want nil and a sentinel error", r, err)
			}
			return
		}
		checkReader(t, r)
	})
}

// FuzzLookup starts from databases that open and, for every mutation that
// still opens, checks the jump-table searches against a plain binary search.
func FuzzLookup(f *testing.F) {
	for _, b := range fuzzSeeds() {
		if _, err := OpenBytes(b); err != nil {
			f.Fatalf("seed does not open: %v", err)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := OpenBytes(b)
		if err != nil {
			return
		}
		checkReader(t, r)
		checkIndex(t, r)
	})
}
