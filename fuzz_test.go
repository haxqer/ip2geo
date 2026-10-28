package ip2geo

import (
	"bytes"
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

// fuzzDBs are databases that open: every profile, empty tables, and many
// ranges straddling jump-table buckets.
func fuzzDBs() []*testDB {
	out := []*testDB{
		cityTestDB(),
		cityTestDB().withProfile(ProfileCountry),
		cityTestDB().withProfile(ProfileRegion),
		excTestDB(),
	}

	d := cityTestDB()
	d.v4starts, d.v4rec, d.v6starts, d.v6rec = nil, nil, nil, nil
	out = append(out, d)

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
	out = append(out, d)

	// Many starts in one top-16 bucket, so that it gets a level-2 table:
	// starts on multiples of 2^40 (some of them slot edges), starts just
	// below others, and pairs that share their top 64 bits. Then a last
	// range in the last bucket.
	d = cityTestDB()
	d.v6starts, d.v6rec = [][16]byte{{}}, []uint32{0}
	for i := uint64(0); i < 100; i++ {
		hi, lo := 0x2a02<<48|i<<40, uint64(0)
		switch i % 4 {
		case 1:
			hi--
		case 3:
			hi, lo = 0x2a02<<48|(i-1)<<40, 1<<62
		}
		var s [16]byte
		binary.BigEndian.PutUint64(s[:8], hi)
		binary.BigEndian.PutUint64(s[8:], lo)
		d.v6starts = append(d.v6starts, s)
		d.v6rec = append(d.v6rec, uint32(1+i%2))
	}
	d.v6starts = append(d.v6starts, a16("ffff::"))
	d.v6rec = append(d.v6rec, recLA)
	return append(out, d)
}

// fuzzSeeds are the GEO1 and GEO2 encodings of fuzzDBs, and the smallest
// valid database of each format: one empty string, record 0, no ranges.
func fuzzSeeds() [][]byte {
	var out [][]byte
	for _, d := range fuzzDBs() {
		b, _ := d.encode()
		out = append(out, b, toGEO2(d, g2opts{}).encode())
	}
	out = append(out, []byte("GEO1\x01\x00\x00\x00"+"\x01\x00\x00\x00\x00\x00"+"\x01\x00\x00\x00\x10\x00"+
		string(make([]byte, 16))+"\x00\x00\x00\x00"+"\x00\x00\x00\x00"))
	d := cityTestDB().withProfile(ProfileCountry)
	d.strs, d.recCount, d.records = nil, 1, d.records[:RecWidth(ProfileCountry)]
	d.v4starts, d.v4rec, d.v6starts, d.v6rec = nil, nil, nil, nil
	out = append(out, toGEO2(d, g2opts{}).encode())

	// Revision 2.1: every field set and three languages, the sets each
	// smaller profile allows with two, languages only, an empty field set,
	// and an ASN database.
	for _, x := range []struct {
		d *testDB
		e *g2ext
	}{
		{cityTestDB(), laExt()},
		{excTestDB(), laExt()},
		{cityTestDB().withProfile(ProfileCountry), &g2ext{fields: xEU | xContinent | xRegistered | xTraits, langs: []string{"en", "de"}, recs: laExt().recs}},
		{cityTestDB().withProfile(ProfileRegion), &g2ext{fields: xSubdivisions | xEU, langs: []string{"en", "fr"}, recs: laExt().recs}},
		{cityTestDB(), &g2ext{langs: []string{"en", "de", "fr"}, recs: laExt().recs}},
		{cityTestDB(), &g2ext{}},
		{asnTestDB(), nil},
	} {
		out = append(out, toGEO2(x.d, g2opts{meta: testMeta, ext: x.e}).encode())
	}
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
// over the starts, around every start and bucket edge. The reference reads
// the absolute IPv4 starts and the 128-bit IPv6 starts back from the
// sections as docs/FORMAT.md defines them.
func checkIndex(t *testing.T, r *Reader) {
	const maxStarts = 256
	decode := r.decode
	if r.alt { // an ASN database or another language
		decode = r.decodeAlt
	}
	starts4 := refStarts4(r)
	ref4 := func(k uint32) uint32 {
		i := sort.Search(len(starts4), func(i int) bool { return starts4[i] > k })
		if i == 0 {
			return 0
		}
		return readID(r.v4ids, r.idWidth, i-1)
	}
	keys4 := []uint32{0, 1, 1<<v4Shift - 1, 1 << v4Shift, 0x7FFFFFFF, 0x80000000, 0xFFFFFFFF}
	for i, s := range starts4 {
		if i == maxStarts {
			break
		}
		g := s &^ (1<<v4Shift - 1)
		keys4 = append(keys4, s-1, s, s+1, g, g-1, g|(1<<v4Shift-1), s&^0xFFFF, s&^0xFFFF-1)
	}
	for _, k := range keys4 {
		want := ref4(k)
		if got := r.search4(k); got != want {
			t.Fatalf("search4(%08x) = %d, want %d", k, got, want)
		}
		var a [4]byte
		binary.BigEndian.PutUint32(a[:], k)
		rec, ok := r.Lookup(netip.AddrFrom4(a))
		if ok != (want != 0) || (ok && rec != decode(want)) {
			t.Fatalf("Lookup(%v) = %+v, %v; want record %d", netip.AddrFrom4(a), rec, ok, want)
		}
	}

	starts6 := refStarts6(r)
	n6 := len(starts6)
	ref6 := func(k [16]byte) uint32 {
		i := sort.Search(n6, func(i int) bool { return bytes.Compare(starts6[i][:], k[:]) > 0 })
		if i == 0 {
			return 0
		}
		return readID(r.v6ids, r.idWidth, i-1)
	}
	var zero, ones [16]byte
	for i := range ones {
		ones[i] = 0xFF
	}
	keys6 := [][16]byte{zero, ones}
	for i := 0; i < n6 && i < maxStarts; i++ {
		s := starts6[i]
		keys6 = append(keys6, add16(s, -1), s, add16(s, 1), fillFrom(s, 64, 0), fillFrom(s, 64, 0xFF))
		// The edges of the start's top-16 bucket and of its level-2 slot.
		k := int(r.v6dir[binary.BigEndian.Uint16(s[:2])] & v6KMask)
		for _, bit := range []int{v6IdxBits, v6IdxBits + k} {
			lo, hi := fillFrom(s, bit, 0), fillFrom(s, bit, 0xFF)
			keys6 = append(keys6, lo, add16(lo, -1), hi, add16(hi, 1))
		}
	}
	for _, k := range keys6 {
		got := r.search6(binary.BigEndian.Uint64(k[:8]), binary.BigEndian.Uint64(k[8:]))
		if want := ref6(k); got != want {
			t.Fatalf("search6(%v) = %d, want %d", netip.AddrFrom16(k), got, want)
		}
	}
}

// refStarts4 returns the absolute IPv4 starts: start i is (b<<16) | its u16
// value, b being the bucket with V4IDX[b] <= i < V4IDX[b+1].
func refStarts4(r *Reader) []uint32 {
	le := binary.LittleEndian
	out := make([]uint32, len(r.v4starts)/2)
	for b := range 1 << 16 {
		for i := le.Uint32(r.v4idx[b*4:]); i < le.Uint32(r.v4idx[b*4+4:]); i++ {
			out[i] = uint32(b)<<16 | uint32(le.Uint16(r.v4starts[i*2:]))
		}
	}
	return out
}

// refStarts6 returns the 128-bit IPv6 starts, big-endian: the top half from
// V6STARTS, the low half from the exception table or 0.
func refStarts6(r *Reader) [][16]byte {
	le := binary.LittleEndian
	out := make([][16]byte, len(r.v6hi)/8)
	for i := range out {
		binary.BigEndian.PutUint64(out[i][:8], le.Uint64(r.v6hi[i*8:]))
	}
	for j := 0; j < len(r.v6excIdx)/4; j++ {
		binary.BigEndian.PutUint64(out[le.Uint32(r.v6excIdx[j*4:])][8:], le.Uint64(r.v6excLo[j*8:]))
	}
	return out
}

// readID reads id i of a little-endian id array of the given width.
func readID(ids []byte, width uint8, i int) uint32 {
	var v uint32
	for k := int(width) - 1; k >= 0; k-- {
		v = v<<8 | uint32(ids[i*int(width)+k])
	}
	return v
}

// fillFrom sets every bit of k from bit on (0 is the most significant) to
// the matching bit of fill, 0x00 or 0xFF.
func fillFrom(k [16]byte, bit int, fill byte) [16]byte {
	for i := bit; i < 128; i++ {
		m := byte(0x80) >> (i % 8)
		k[i/8] = k[i/8]&^m | fill&m
	}
	return k
}

// FuzzOpenBytes: OpenBytes never panics, fails only with a sentinel error,
// and a Reader it returns answers lookups without panicking. Each input is
// also opened with SkipChecksum, so that mutated GEO2 inputs reach the
// checks behind the CRCs.
func FuzzOpenBytes(f *testing.F) {
	for _, b := range fuzzSeeds() {
		f.Add(b)
	}
	for _, s := range []string{"", "GEO1", "GEO1\x01\x00\x00\x00", "GEO1\x01\x00\x00\x00\xff\xff\xff\xff", crashRecWidth, crashRecID,
		"GEO2", "GEO2\x02\x02\x04\x01\x0b\x00\x00\x00" + string(make([]byte, 20))} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, opts := range [][]Option{nil, {SkipChecksum(), NoCopy()}} {
			r, err := OpenBytes(b, opts...)
			if err != nil {
				if r != nil || !isSentinel(err) {
					t.Fatalf("OpenBytes = %v, %v; want nil and a sentinel error", r, err)
				}
				continue
			}
			checkReader(t, r)
			checkDetails(t, r)
		}
	})
}

// FuzzLookup starts from databases that open and, for every mutation that
// still opens (GEO2 checksums aside), checks the jump-table searches against
// a plain binary search.
func FuzzLookup(f *testing.F) {
	for _, b := range fuzzSeeds() {
		if _, err := OpenBytes(b); err != nil {
			f.Fatalf("seed does not open: %v", err)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := OpenBytes(b, SkipChecksum())
		if err != nil {
			return
		}
		checkReader(t, r)
		checkIndex(t, r)
		checkDetails(t, r)
	})
}
