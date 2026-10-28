package ip2geo

import (
	"encoding/binary"
	"errors"
	"math"
	"net/netip"
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
