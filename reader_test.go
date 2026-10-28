package ip2geo

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net/netip"
	"sort"
	"testing"
)

// testDB is the logical content of a database. encode lays it out in the
// GEO1 format; tests corrupt either the struct or the encoded bytes.
type testDB struct {
	version, profile, latScale, reserved byte

	strs     []string
	recCount uint32 // written as is, so it may disagree with records
	recWidth uint16
	records  []byte

	v4starts []uint32
	v4rec    []uint32
	v6starts [][16]byte
	v6rec    []uint32
	trailing []byte
}

// layout holds the offsets of the count fields in an encoded testDB.
type layout struct{ strCount, recCount, v4Count, v6Count int }

func (d *testDB) encode() ([]byte, layout) {
	le := binary.LittleEndian
	var l layout
	b := append([]byte(magic), d.version, d.profile, d.latScale, d.reserved)
	l.strCount = len(b)
	b = le.AppendUint32(b, uint32(len(d.strs)))
	for _, s := range d.strs {
		b = le.AppendUint16(b, uint16(len(s)))
		b = append(b, s...)
	}
	l.recCount = len(b)
	b = le.AppendUint32(b, d.recCount)
	b = le.AppendUint16(b, d.recWidth)
	b = append(b, d.records...)
	l.v4Count = len(b)
	b = le.AppendUint32(b, uint32(len(d.v4starts)))
	for _, s := range d.v4starts {
		b = le.AppendUint32(b, s)
	}
	for _, id := range d.v4rec {
		b = le.AppendUint32(b, id)
	}
	l.v6Count = len(b)
	b = le.AppendUint32(b, uint32(len(d.v6starts)))
	for _, s := range d.v6starts {
		b = append(b, s[:]...)
	}
	for _, id := range d.v6rec {
		b = le.AppendUint32(b, id)
	}
	return append(b, d.trailing...), l
}

// withProfile narrows the city records to profile p. Record fields are laid
// out so that each profile's record is a prefix of the city record.
func (d *testDB) withProfile(p Profile) *testDB {
	w := RecWidth(p)
	var recs []byte
	for i := 0; i < int(d.recCount); i++ {
		recs = append(recs, d.records[i*int(d.recWidth):i*int(d.recWidth)+w]...)
	}
	d.profile, d.recWidth, d.records = byte(p), uint16(w), recs
	return d
}

// Record ids in the test database.
const (
	recNone   = 0 // the not-found sentinel
	recLA     = 1 // IPv4 10.0.0.0/24
	recBerlin = 2 // IPv6 2001:db8::/32 and 2002::/16
)

func coord(deg float64) uint32 { return uint32(int32(math.Round(deg * 1e4))) }

// cityTestDB hand-builds a minimal city-profile database for hermetic tests.
func cityTestDB() *testDB {
	strs := []string{"", "US", "United States", "NA", "CA", "California", "Los Angeles", "America/Los_Angeles",
		"DE", "Germany", "EU", "BE", "Berlin", "Europe/Berlin"}
	la := []uint32{1, 2, 3, 100, 4, 5, 6, 200, coord(34.05), coord(-118.25), 20, 7}
	berlin := []uint32{8, 9, 10, 300, 11, 12, 12, 400, coord(52.52), coord(13.405), 50, 13}
	recs := make([]byte, RecWidth(ProfileCity)) // record 0 = empty
	for _, rec := range [][]uint32{la, berlin} {
		for _, v := range rec {
			recs = binary.LittleEndian.AppendUint32(recs, v)
		}
	}
	return &testDB{
		version: version, profile: byte(ProfileCity), latScale: 4,
		strs:     strs,
		recCount: 3, recWidth: uint16(RecWidth(ProfileCity)), records: recs,
		// 10.0.0.0/24 -> LA, the rest of IPv4 -> not found.
		v4starts: []uint32{0, 0x0A000000, 0x0A000100},
		v4rec:    []uint32{recNone, recLA, recNone},
		// 2001:db8::/32 -> Berlin. 2002::/16 -> Berlin as well, so a 6to4
		// address that resolves to LA proves the IPv4 table answered.
		v6starts: [][16]byte{{}, a16("2001:db8::"), a16("2001:db9::"), a16("2002::"), a16("2003::")},
		v6rec:    []uint32{recNone, recBerlin, recNone, recBerlin, recNone},
	}
}

func encodeCityDB() []byte {
	b, _ := cityTestDB().encode()
	return b
}

func a16(s string) [16]byte { return netip.MustParseAddr(s).As16() }

func openTest(tb testing.TB) *Reader {
	tb.Helper()
	r, err := OpenBytes(encodeCityDB())
	if err != nil {
		tb.Fatalf("OpenBytes: %v", err)
	}
	return r
}

func TestLookupV4(t *testing.T) {
	r := openTest(t)
	rec, ok := r.Lookup(netip.MustParseAddr("10.0.0.5"))
	if !ok {
		t.Fatal("expected data for 10.0.0.5")
	}
	if rec.CountryISO != "US" || rec.City != "Los Angeles" || rec.RegionISO != "CA" {
		t.Errorf("unexpected record: %+v", rec)
	}
	if math.Abs(rec.Latitude-34.05) > 1e-6 || math.Abs(rec.Longitude+118.25) > 1e-6 {
		t.Errorf("bad coords: %v %v", rec.Latitude, rec.Longitude)
	}
	if rec.TimeZone != "America/Los_Angeles" {
		t.Errorf("bad tz: %q", rec.TimeZone)
	}
}

func TestV4Boundaries(t *testing.T) {
	r := openTest(t)
	cases := []struct {
		ip string
		ok bool
	}{
		{"9.255.255.255", false}, // just below
		{"10.0.0.0", true},       // first address
		{"10.0.0.255", true},     // last address
		{"10.0.1.0", false},      // just past the /24
		{"192.0.2.1", false},     // unrelated
		{"0.0.0.0", false},
		{"255.255.255.255", false},
	}
	for _, c := range cases {
		_, ok := r.Lookup(netip.MustParseAddr(c.ip))
		if ok != c.ok {
			t.Errorf("%s: got ok=%v want %v", c.ip, ok, c.ok)
		}
	}
}

func TestLookupV6(t *testing.T) {
	r := openTest(t)
	for _, s := range []string{"2001:db7::ffff", "2001:db9::", "2003::1", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff"} {
		if _, ok := r.Lookup(netip.MustParseAddr(s)); ok {
			t.Errorf("%s should have no data", s)
		}
	}
	for _, s := range []string{"2001:db8::", "2001:db8::1", "2001:db8:ffff:ffff:ffff:ffff:ffff:ffff", "2001:db8::1%eth0"} {
		rec, ok := r.Lookup(netip.MustParseAddr(s))
		if !ok || rec.CountryISO != "DE" || rec.City != "Berlin" {
			t.Errorf("%s -> %+v ok=%v, want the Berlin record", s, rec, ok)
		}
	}
}

func TestLookupZeroAddr(t *testing.T) {
	r := openTest(t)
	if rec, ok := r.Lookup(netip.Addr{}); ok {
		t.Errorf("Lookup(netip.Addr{}) = %+v, true; want not found", rec)
	}
}

// TestLookupIPv4Aliases checks parity with MaxMind's aliasing of IPv6
// prefixes to the IPv4 tree.
func TestLookupIPv4Aliases(t *testing.T) {
	r := openTest(t)
	cases := []struct {
		ip   string
		want string // CountryISO, "" for not found
	}{
		{"10.0.0.5", "US"},
		{"::ffff:10.0.0.5", "US"},     // IPv4-mapped
		{"::10.0.0.5", "US"},          // IPv4-compatible, ::/96
		{"2002:0a00:0005::1", "US"},   // 6to4: bytes 2-5
		{"2002:a00:5:ffff::", "US"},   // 6to4, rest of the /48
		{"2001:0:0a00:0005::1", "US"}, // Teredo: bytes 4-7
		{"2001:0:a00:5:ffff:ffff:ffff:ffff", "US"},
		{"::11.0.0.5", ""},          // aliased, but 11.0.0.5 has no data
		{"2002:0b00:0005::1", ""},   // the IPv6 table's 2002::/16 entry is not consulted
		{"2001:0:0b00:0005::1", ""}, //
		{"::", ""},                  // 0.0.0.0
		{"::1", ""},                 // 0.0.0.1
		{"64:ff9b::10.0.0.5", ""},   // NAT64 is not mapped
		{"::1:10.0.0.5", ""},        // outside ::/96
		{"::ffff:0:10.0.0.5", ""},   // SIIT ::ffff:0:0:0/96, not mapped
		{"2001:1:a00:5::1", ""},     // outside 2001::/32
		{"2003:a00:5::1", ""},       // outside 2002::/16
		{"2001:db8::1", "DE"},       // ordinary IPv6 still uses the IPv6 table
	}
	for _, c := range cases {
		rec, ok := r.Lookup(netip.MustParseAddr(c.ip))
		if ok != (c.want != "") || rec.CountryISO != c.want {
			t.Errorf("%s: got %q ok=%v, want %q", c.ip, rec.CountryISO, ok, c.want)
		}
	}
}

func TestEmbeddedV4(t *testing.T) {
	cases := []struct {
		ip   string
		want string // "" when the key is not an IPv4 alias
	}{
		{"::", "0.0.0.0"},
		{"::1.2.3.4", "1.2.3.4"},
		{"::255.255.255.255", "255.255.255.255"},
		{"2002:102:304::", "1.2.3.4"},
		{"2002:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "255.255.255.255"},
		{"2001:0:102:304::", "1.2.3.4"},
		{"2001:0:ffff:ffff:ffff:ffff:ffff:ffff", "255.255.255.255"},
		{"::ffff:1.2.3.4", ""}, // left to netip's Is4In6
		{"::1:0:0", ""},
		{"64:ff9b::1.2.3.4", ""},
		{"2001:1::", ""},
		{"2001:db8::", ""},
		{"2003::", ""},
		{"2001::1:0:0", "0.0.0.0"},
	}
	for _, c := range cases {
		k := netip.MustParseAddr(c.ip).As16()
		v4, ok := embeddedV4(&k)
		if c.want == "" {
			if ok {
				t.Errorf("%s: got alias %08x, want none", c.ip, v4)
			}
			continue
		}
		w := netip.MustParseAddr(c.want).As4()
		if !ok || v4 != binary.BigEndian.Uint32(w[:]) {
			t.Errorf("%s: got %08x ok=%v, want %s", c.ip, v4, ok, c.want)
		}
	}
}

func TestProfiles(t *testing.T) {
	for _, p := range []Profile{ProfileCountry, ProfileRegion, ProfileCity} {
		t.Run(p.String(), func(t *testing.T) {
			b, _ := cityTestDB().withProfile(p).encode()
			r, err := OpenBytes(b)
			if err != nil {
				t.Fatal(err)
			}
			if r.Profile() != p {
				t.Errorf("Profile() = %v", r.Profile())
			}
			rec, ok := r.Lookup(netip.MustParseAddr("10.0.0.5"))
			if !ok || rec.CountryISO != "US" || rec.CountryName != "United States" || rec.Continent != "NA" || rec.CountryGeoNameID != 100 {
				t.Errorf("country fields: %+v ok=%v", rec, ok)
			}
			if (rec.RegionISO == "CA") != (p >= ProfileRegion) {
				t.Errorf("region fields: %+v", rec)
			}
			if (rec.City == "Los Angeles" && rec.AccuracyRadius == 20) != (p >= ProfileCity) {
				t.Errorf("city fields: %+v", rec)
			}
		})
	}
}

func TestLatScale(t *testing.T) {
	d := cityTestDB()
	d.latScale = maxLatScale
	b, _ := d.encode()
	r, err := OpenBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := r.Lookup(netip.MustParseAddr("10.0.0.5"))
	if math.Abs(rec.Latitude-34.05e-3) > 1e-9 {
		t.Errorf("Latitude = %v with scale 7, want 0.003405", rec.Latitude)
	}
}

func TestBadMagic(t *testing.T) {
	if _, err := OpenBytes([]byte("nope____")); !errors.Is(err, ErrBadMagic) {
		t.Errorf("got %v, want ErrBadMagic", err)
	}
}

func TestLookupAllocs(t *testing.T) {
	r := openTest(t)
	cases := []struct {
		name, ip string
		ok       bool
	}{
		{"v4 hit", "10.0.0.5", true},
		{"v6 hit", "2001:db8::1", true},
		{"alias hit", "2002:a00:5::1", true},
		{"mapped hit", "::ffff:10.0.0.5", true},
		{"v4 miss", "192.0.2.1", false},
		{"v6 miss", "2001:db9::1", false},
	}
	for _, c := range cases {
		a := netip.MustParseAddr(c.ip)
		if _, ok := r.Lookup(a); ok != c.ok {
			t.Errorf("%s: %s ok=%v, want %v", c.name, c.ip, ok, c.ok)
		}
		if n := testing.AllocsPerRun(1000, func() { r.Lookup(a) }); n != 0 {
			t.Errorf("%s: %s allocates %v times per Lookup", c.name, c.ip, n)
		}
	}
}

var sinkRec Record

func BenchmarkLookup(b *testing.B) {
	r := openTest(b)
	for _, c := range []struct{ name, ip string }{
		{"v4", "10.0.0.5"}, {"v6", "2001:db8::1"}, {"alias", "2002:a00:5::1"}, {"miss", "192.0.2.1"},
	} {
		a := netip.MustParseAddr(c.ip)
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				sinkRec, _ = r.Lookup(a)
			}
		})
	}
}

// key16 makes a 16-byte key from its big-endian halves.
func key16(hi, lo uint64) (k [16]byte) {
	binary.BigEndian.PutUint64(k[:8], hi)
	binary.BigEndian.PutUint64(k[8:], lo)
	return k
}

// v6DB returns a country-profile database whose IPv6 ranges start at
// starts, range i pointing at record i+1, and with the IPv4 table of
// cityTestDB.
func v6DB(starts [][16]byte) *testDB {
	d := cityTestDB().withProfile(ProfileCountry)
	d.recCount = uint32(len(starts) + 1)
	d.records = make([]byte, int(d.recCount)*int(d.recWidth))
	d.v6starts = starts
	d.v6rec = make([]uint32, len(starts))
	for i := range d.v6rec {
		d.v6rec[i] = uint32(i + 1)
	}
	return d
}

func openDB(t testing.TB, d *testDB) *Reader {
	t.Helper()
	b, _ := d.encode()
	r, err := OpenBytes(b)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	return r
}

// l2Bits returns the level-2 bits of the top-16 bucket holding prefix p.
func l2Bits(r *Reader, p string) int {
	a := netip.MustParsePrefix(p).Addr().As16()
	return int(r.v6dir[binary.BigEndian.Uint16(a[:2])] & v6KMask)
}

// checkV6 checks search6 against the definition of a range, around every
// start of d: start i and the address before start i+1 belong to range i.
// Every probe in extra is checked against a plain binary search over d, and
// a sample of starts through Lookup as well.
func checkV6(t *testing.T, d *testDB, r *Reader, extra ...[16]byte) {
	t.Helper()
	search := func(k [16]byte) uint32 {
		return r.search6(binary.BigEndian.Uint64(k[:8]), binary.BigEndian.Uint64(k[8:]))
	}
	ref := func(k [16]byte) uint32 {
		j := sort.Search(len(d.v6starts), func(i int) bool { return bytes.Compare(d.v6starts[i][:], k[:]) > 0 })
		if j == 0 {
			return 0
		}
		return d.v6rec[j-1]
	}
	for i, s := range d.v6starts {
		if got := search(s); got != d.v6rec[i] {
			t.Fatalf("search6(%v), start %d: got record %d, want %d", netip.AddrFrom16(s), i, got, d.v6rec[i])
		}
		if s == ([16]byte{}) {
			continue
		}
		var below uint32 // record of the range holding s-1
		if i > 0 {
			below = d.v6rec[i-1]
		}
		if got := search(add16(s, -1)); got != below {
			t.Fatalf("search6(%v), before start %d: got record %d, want %d", netip.AddrFrom16(add16(s, -1)), i, got, below)
		}
		if i%61 == 0 {
			extra = append(extra, s, add16(s, 1))
		}
	}
	var ones [16]byte
	for i := range ones {
		ones[i] = 0xFF
	}
	for _, k := range append(extra, [16]byte{}, ones) {
		want := ref(k)
		if got := search(k); got != want {
			t.Fatalf("search6(%v) = %d, want %d", netip.AddrFrom16(k), got, want)
		}
		a := netip.AddrFrom16(k)
		if _, isV4 := embeddedV4(&k); isV4 || a.Is4In6() {
			continue // Lookup answers from the IPv4 table
		}
		if rec, ok := r.Lookup(a); ok != (want != 0) || rec != r.decode(want) {
			t.Fatalf("Lookup(%v) = %+v, %v; want record %d", a, rec, ok, want)
		}
	}
}

// slotEdgeStarts returns 8 starts in each of the 2^k level-2 slots of the
// top-16 bucket top: on the slot's first address, six inside, and on its
// last address, a one-address range. 8 * 2^k starts give the bucket k bits.
func slotEdgeStarts(top uint64, k int) [][16]byte {
	var out [][16]byte
	shift := 48 - k
	for s := uint64(0); s < 1<<k; s++ {
		first := top<<48 | s<<shift
		last := first | (1<<shift - 1)
		out = append(out, key16(first, 0))
		for j := uint64(1); j <= 6; j++ {
			out = append(out, key16(first|j<<(shift-4), j))
		}
		out = append(out, key16(last, math.MaxUint64))
	}
	return out
}

func TestV6IndexSlotEdges(t *testing.T) {
	for _, k := range []int{0, 1, 4, 9} {
		t.Run(fmt.Sprintf("k=%d", k), func(t *testing.T) {
			starts := slotEdgeStarts(0x2a02, k)
			d := v6DB(starts)
			r := openDB(t, d)
			if got := l2Bits(r, "2a02::/16"); got != k {
				t.Fatalf("2a02::/16 has %d level-2 bits, want %d", got, k)
			}
			// Each slot's edges, the bucket's edges and the addresses
			// just outside them.
			var extra [][16]byte
			for _, s := range starts {
				extra = append(extra, add16(s, 1), fillFrom(s, 16+k, 0), fillFrom(s, 16+k, 0xFF))
			}
			lo, hi := key16(0x2a02<<48, 0), key16(0x2a02<<48|(1<<48-1), math.MaxUint64)
			extra = append(extra, lo, add16(lo, -1), hi, add16(hi, 1))
			checkV6(t, d, r, extra...)
			if got := r.search6(0x2a01<<48|(1<<48-1), math.MaxUint64); got != 0 {
				t.Errorf("2a01:ffff:...: got record %d, want none", got)
			}
			if got, want := r.search6(0x2a03<<48, 0), uint32(len(starts)); got != want {
				t.Errorf("2a03::: got record %d, want %d, the last range", got, want)
			}
		})
	}
}

// deepV6Starts returns over 8 * 2^16 starts, all inside 2a02::/16, so that
// the bucket gets the most level-2 bits, v6MaxL2Bits: one on the first and
// one on the last address of each 2a02:s::/32 slot, and a dense cluster in
// 2a02:abcd::/32 of pairs that share their top 64 bits.
func deepV6Starts() [][16]byte {
	const top = 0x2a02 << 48
	var out [][16]byte
	for s := uint64(0); s < 1<<16; s++ {
		out = append(out, key16(top|s<<32, 0))
		if s == 0xabcd {
			for j := uint64(1); j <= 200_000; j++ {
				out = append(out, key16(top|s<<32|j<<8, 0), key16(top|s<<32|j<<8, 1<<63))
			}
		}
		out = append(out, key16(top|s<<32|0xFFFFFFFF, math.MaxUint64))
	}
	return out
}

func TestV6IndexDeepBucket(t *testing.T) {
	starts := deepV6Starts()
	d := v6DB(starts)
	r := openDB(t, d)
	if got := l2Bits(r, "2a02::/16"); got != v6MaxL2Bits {
		t.Fatalf("2a02::/16 has %d level-2 bits for %d starts, want %d", got, len(starts), v6MaxL2Bits)
	}
	for _, p := range []string{"::/16", "2a01::/16", "2a03::/16", "ffff::/16"} {
		if got := l2Bits(r, p); got != 0 {
			t.Errorf("empty bucket %s has %d level-2 bits, want 0", p, got)
		}
	}
	rng := rand.New(rand.NewPCG(1, 2))
	var extra [][16]byte
	for range 20_000 {
		extra = append(extra, key16(0x2a02<<48|rng.Uint64()>>16, rng.Uint64()))
		extra = append(extra, key16(0x2a02_abcd<<32|rng.Uint64()>>38, rng.Uint64())) // the cluster
	}
	checkV6(t, d, r, extra...)
}

func TestV6IndexBelowFirstStart(t *testing.T) {
	// The first start is deep inside a bucket with a level-2 table, so the
	// keys below it fall in an earlier slot of that bucket or in earlier,
	// empty buckets: none of them is covered.
	var starts [][16]byte
	for j := uint64(0); j < 100; j++ {
		starts = append(starts, key16(0x2a02_1234<<32|j<<16, 0))
	}
	d := v6DB(starts)
	r := openDB(t, d)
	if l2Bits(r, "2a02::/16") == 0 {
		t.Fatal("2a02::/16 has no level-2 table")
	}
	for _, s := range []string{"::", "::1", "1::", "2a01:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "2a02::",
		"2a02:1000::", "2a02:1233:ffff:ffff:ffff:ffff:ffff:ffff"} {
		k := a16(s)
		if got := r.search6(binary.BigEndian.Uint64(k[:8]), binary.BigEndian.Uint64(k[8:])); got != 0 {
			t.Errorf("search6(%s) = %d, want 0 (below the first start)", s, got)
		}
		if _, ok := r.Lookup(netip.MustParseAddr(s)); ok && s != "::" && s != "::1" {
			t.Errorf("Lookup(%s) found data below the first start", s)
		}
	}
	checkV6(t, d, r)
}

func TestV6IndexLastAddress(t *testing.T) {
	ones := a16("ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff")
	for _, withLast := range []bool{false, true} {
		t.Run(fmt.Sprintf("start at the last address=%v", withLast), func(t *testing.T) {
			starts := [][16]byte{{}}
			starts = append(starts, slotEdgeStarts(0xffff, 3)[1:]...)
			if withLast {
				starts[len(starts)-1] = ones // replaces the slot's last-address start
			} else {
				starts = starts[:len(starts)-1]
			}
			d := v6DB(starts)
			r := openDB(t, d)
			if got := l2Bits(r, "ffff::/16"); got == 0 {
				t.Fatal("ffff::/16 has no level-2 table")
			}
			want := uint32(len(starts))
			if got := r.search6(math.MaxUint64, math.MaxUint64); got != want {
				t.Errorf("search6(ffff:...:ffff) = %d, want %d, the last range", got, want)
			}
			checkV6(t, d, r, add16(ones, -1), fillFrom(ones, 19, 0))
		})
	}
}

func TestV6IndexEmptyBuckets(t *testing.T) {
	// Populated buckets 2001::/16 and 2a02::/16 with level-2 tables, and
	// 0db8::/16 with one start, around runs of empty buckets: a key in an
	// empty bucket belongs to the last range of the populated bucket before it.
	starts := [][16]byte{a16("db8::")}
	starts = append(starts, slotEdgeStarts(0x2001, 2)...)
	starts = append(starts, slotEdgeStarts(0x2a02, 3)...)
	d := v6DB(starts)
	r := openDB(t, d)
	last2001 := uint32(1 + 8<<2)
	cases := []struct {
		ip   string
		want uint32
	}{
		{"::", 0},
		{"db7:ffff:ffff:ffff:ffff:ffff:ffff:ffff", 0},
		{"db8::", 1},
		{"db9::", 1},
		{"2000:ffff:ffff:ffff:ffff:ffff:ffff:ffff", 1},
		{"2001:ffff:ffff:ffff:ffff:ffff:ffff:ffff", last2001},
		{"2002::", last2001},
		{"2400::1", last2001},
		{"2a01:ffff:ffff:ffff:ffff:ffff:ffff:ffff", last2001},
		{"2a02::", last2001 + 1},
		{"2a03::", uint32(len(starts))},
		{"ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", uint32(len(starts))},
	}
	for _, c := range cases {
		k := a16(c.ip)
		if got := r.search6(binary.BigEndian.Uint64(k[:8]), binary.BigEndian.Uint64(k[8:])); got != c.want {
			t.Errorf("search6(%s) = %d, want %d", c.ip, got, c.want)
		}
	}
	for _, p := range []string{"::/16", "db8::/16", "2002::/16", "2400::/16", "2a01::/16", "ffff::/16"} {
		if got := l2Bits(r, p); got != 0 {
			t.Errorf("%s has %d level-2 bits, want 0", p, got)
		}
	}
	if l2Bits(r, "2001::/16") != 2 || l2Bits(r, "2a02::/16") != 3 {
		t.Errorf("level-2 bits: 2001::/16 %d, 2a02::/16 %d; want 2 and 3", l2Bits(r, "2001::/16"), l2Bits(r, "2a02::/16"))
	}
	checkV6(t, d, r)
}

func TestV6L2Bits(t *testing.T) {
	cases := []struct{ count, want int }{
		{0, 0}, {1, 0}, {8, 0}, {9, 1}, {16, 1}, {17, 2}, {64, 3}, {65, 4},
		{8 << 15, 15}, {8<<15 + 1, 16}, {8 << 16, 16}, {8<<16 + 1, 16}, {1 << 30, 16},
	}
	for _, c := range cases {
		if got := v6L2Bits(c.count); got != c.want {
			t.Errorf("v6L2Bits(%d) = %d, want %d", c.count, got, c.want)
		}
	}
}

// BenchmarkLookupV6Index looks up addresses spread over the ranges of the
// deep-bucket database, one per range in turn; most of them fall in the
// dense 2a02:abcd::/32 slot.
func BenchmarkLookupV6Index(b *testing.B) {
	starts := deepV6Starts()
	r := openDB(b, v6DB(starts))
	addrs := make([]netip.Addr, 1<<16)
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range addrs {
		addrs[i] = netip.AddrFrom16(add16(starts[rng.IntN(len(starts))], 1))
	}
	b.ReportAllocs()
	n := 0
	for i := 0; b.Loop(); i++ {
		if _, ok := r.Lookup(addrs[i&(len(addrs)-1)]); ok {
			n++
		}
	}
	sinkN = n
}

var sinkN int
