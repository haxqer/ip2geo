package ip2geo

import (
	"bytes"
	"encoding/binary"
	"math"
	"net/netip"
	"testing"
)

// encodeCityDB hand-builds a minimal city-profile database for hermetic tests.
func encodeCityDB() []byte {
	strs := []string{"", "US", "United States", "NA", "CA", "California", "Los Angeles", "America/Los_Angeles"}
	// record 0 = empty; record 1 = LA
	rec1 := make([]uint32, 12)
	rec1[0] = 1 // countryISO US
	rec1[1] = 2 // countryName
	rec1[2] = 3 // continent NA
	rec1[3] = 100
	rec1[4] = 4 // regionISO CA
	rec1[5] = 5 // regionName California
	rec1[6] = 6 // city LA
	rec1[7] = 200
	rec1[8] = uint32(int32(math.Round(34.05 * 1e4)))
	rec1[9] = uint32(int32(math.Round(-118.25 * 1e4)))
	rec1[10] = 20
	rec1[11] = 7 // tz

	var buf bytes.Buffer
	w32 := func(v uint32) { binary.Write(&buf, binary.LittleEndian, v) }
	w16 := func(v uint16) { binary.Write(&buf, binary.LittleEndian, v) }

	buf.WriteString(magic)
	buf.Write([]byte{version, byte(ProfileCity), 4, 0})

	// strings
	w32(uint32(len(strs)))
	for _, s := range strs {
		w16(uint16(len(s)))
		buf.WriteString(s)
	}

	// records
	rw := RecWidth(ProfileCity)
	w32(2)
	w16(uint16(rw))
	buf.Write(make([]byte, rw)) // record 0 = empty
	for _, v := range rec1 {
		w32(v)
	}

	// v4: 10.0.0.0/24 -> rec1, else empty
	v4starts := []uint32{0, 0x0A000000, 0x0A000100}
	v4rec := []uint32{0, 1, 0}
	w32(uint32(len(v4starts)))
	for _, s := range v4starts {
		w32(s)
	}
	for _, r := range v4rec {
		w32(r)
	}

	// v6: >= 2001:db8:: -> rec1
	v6 := [][16]byte{{}, mustAddr16("2001:db8::")}
	v6rec := []uint32{0, 1}
	w32(uint32(len(v6)))
	for _, s := range v6 {
		buf.Write(s[:])
	}
	for _, r := range v6rec {
		w32(r)
	}
	return buf.Bytes()
}

func mustAddr16(s string) [16]byte { return netip.MustParseAddr(s).As16() }

func openTest(t *testing.T) *Reader {
	t.Helper()
	r, err := OpenBytes(encodeCityDB())
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
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
	if _, ok := r.Lookup(netip.MustParseAddr("2001:db7::ffff")); ok {
		t.Error("2001:db7:: should have no data")
	}
	rec, ok := r.Lookup(netip.MustParseAddr("2001:db8::1"))
	if !ok || rec.CountryISO != "US" {
		t.Errorf("2001:db8::1 -> %+v ok=%v", rec, ok)
	}
}

func TestBadMagic(t *testing.T) {
	if _, err := OpenBytes([]byte("nope____")); err == nil {
		t.Error("expected error on bad magic")
	}
}
