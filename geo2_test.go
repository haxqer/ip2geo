package ip2geo

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
	"unsafe"
)

var testMeta = g2meta{
	buildTime: 1761600000, sourceTime: 1588597376, networks: 10176453,
	sourceType: "GeoIP2-City", language: "en", description: "", builder: "ip2geo/test",
	distinct: 886091,
}

func encodeGEO2(d *testDB) []byte { return toGEO2(d, g2opts{meta: testMeta}).encode() }

func openGEO2(tb testing.TB, b []byte, opts ...Option) *Reader {
	tb.Helper()
	r, err := OpenBytes(b, opts...)
	if err != nil {
		tb.Fatalf("OpenBytes: %v", err)
	}
	return r
}

// TestGEO2Lookups runs the GEO1 lookup expectations against the GEO2
// encoding of the same database.
func TestGEO2Lookups(t *testing.T) {
	r := openGEO2(t, encodeGEO2(cityTestDB()))
	if r.Metadata().FormatVersion != 2 {
		t.Fatalf("FormatVersion = %d, want 2", r.Metadata().FormatVersion)
	}
	rec, ok := r.Lookup(netip.MustParseAddr("10.0.0.5"))
	if !ok || rec.CountryISO != "US" || rec.City != "Los Angeles" || rec.RegionISO != "CA" ||
		rec.TimeZone != "America/Los_Angeles" || rec.AccuracyRadius != 20 || rec.CityGeoNameID != 200 {
		t.Errorf("10.0.0.5: %+v, %v", rec, ok)
	}
	if math.Abs(rec.Latitude-34.05) > 1e-6 || math.Abs(rec.Longitude+118.25) > 1e-6 {
		t.Errorf("bad coords: %v %v", rec.Latitude, rec.Longitude)
	}
	for ip, want := range map[string]string{
		"9.255.255.255": "", "10.0.0.0": "US", "10.0.0.255": "US", "10.0.1.0": "", "0.0.0.0": "", "255.255.255.255": "",
		"2001:db8::1": "DE", "2001:db8:ffff:ffff:ffff:ffff:ffff:ffff": "DE", "2001:db9::": "", "2003::1": "",
		"::ffff:10.0.0.5": "US", "::10.0.0.5": "US", "2002:a00:5::1": "US", "2001:0:a00:5::1": "US",
		"2002:b00:5::1": "", "64:ff9b::10.0.0.5": "",
	} {
		rec, ok := r.Lookup(netip.MustParseAddr(ip))
		if ok != (want != "") || rec.CountryISO != want {
			t.Errorf("%s: got %q ok=%v, want %q", ip, rec.CountryISO, ok, want)
		}
	}
	if _, ok := r.Lookup(netip.Addr{}); ok {
		t.Error("the zero Addr was found")
	}
}

// TestGEO2Exceptions looks up around a run of IPv6 starts that share their
// top half, two of them exceptions.
func TestGEO2Exceptions(t *testing.T) {
	d := excTestDB()
	for _, enc := range []struct {
		name string
		b    []byte
	}{{"GEO1", func() []byte { b, _ := d.encode(); return b }()}, {"GEO2", encodeGEO2(d)}} {
		t.Run(enc.name, func(t *testing.T) {
			r := openGEO2(t, enc.b)
			if n := len(r.v6excIdx) / 4; n != 2 {
				t.Fatalf("%d exceptions, want 2", n)
			}
			for ip, want := range map[string]string{
				"2001:db8::":                     "DE",
				"2001:db8::7fff:ffff:ffff:ffff":  "DE",
				"2001:db8::8000:0:0:0":           "US",
				"2001:db8::bfff:ffff:ffff:ffff":  "US",
				"2001:db8::c000:0:0:0":           "",
				"2001:db8::ffff:ffff:ffff:ffff":  "",
				"2001:db8:0:1::":                 "DE",
				"2001:db7:ffff:ffff:ffff:ffff::": "",
			} {
				rec, ok := r.Lookup(netip.MustParseAddr(ip))
				if ok != (want != "") || rec.CountryISO != want {
					t.Errorf("%s: got %q ok=%v, want %q", ip, rec.CountryISO, ok, want)
				}
			}
			checkIndex(t, r)
		})
	}
}

// TestGEO1GEO2Agree opens the GEO1 and the GEO2 encoding of the same
// logical databases and compares both with the reference answer on a few
// thousand addresses: the GEO1 path exercises the reader's conversion, the
// GEO2 one the fixture encoder's.
func TestGEO1GEO2Agree(t *testing.T) {
	cases := []struct {
		name    string
		d       *testDB
		idWidth uint8
	}{
		{"city", cityTestDB(), 1},
		{"exceptions", excTestDB(), 1},
		{"random city, 1-byte ids", randomTestDB(1, ProfileCity, 200, 3000, 3000), 1},
		{"random region, 2-byte ids", randomTestDB(2, ProfileRegion, 3000, 3000, 3000), 2},
		{"random country, 3-byte ids", randomTestDB(3, ProfileCountry, 70000, 2000, 2000), 3},
		{"random city, 2-byte ids at the limit", randomTestDB(4, ProfileCity, 1<<16, 1000, 1000), 2},
		{"random country, 3-byte ids just past 2", randomTestDB(5, ProfileCountry, 1<<16+1, 500, 500), 3},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b1, _ := c.d.encode()
			r1 := openGEO2(t, b1)
			r2 := openGEO2(t, encodeGEO2(c.d))
			for _, r := range []*Reader{r1, r2} {
				if r.idWidth != c.idWidth {
					t.Fatalf("id width %d, want %d", r.idWidth, c.idWidth)
				}
				checkIndex(t, r)
				checkReader(t, r)
			}
			hits := 0
			for _, a := range probeAddrs(c.d, uint64(i), 3000) {
				want, wok := c.d.lookup(a)
				g1, ok1 := r1.Lookup(a)
				g2, ok2 := r2.Lookup(a)
				if ok1 != wok || g1 != want {
					t.Fatalf("GEO1 Lookup(%v) = %+v, %v; want %+v, %v", a, g1, ok1, want, wok)
				}
				if ok2 != wok || g2 != want {
					t.Fatalf("GEO2 Lookup(%v) = %+v, %v; want %+v, %v", a, g2, ok2, want, wok)
				}
				if wok {
					hits++
				}
			}
			t.Logf("%d records, %d+%d starts, %d exceptions, %d hits", c.d.recCount, len(c.d.v4starts), len(c.d.v6starts), len(r2.v6excIdx)/4, hits)
		})
	}
}

// TestGEO1Renumbering checks that a GEO1 file's strings are renumbered for
// the u16 fields: with more than 65535 strings in all, the country, region
// and time zone strings still get ids below 65536.
func TestGEO1Renumbering(t *testing.T) {
	d := cityTestDB()
	for i := range 70000 {
		d.strs = append(d.strs, "filler"+string(rune('a'+i%26))+string(rune('a'+i/26%26))+string(rune('a'+i/676%26)))
	}
	// Record 2 uses the last string as its time zone and city name.
	last := uint32(len(d.strs) - 1)
	w := int(d.recWidth)
	binary.LittleEndian.PutUint32(d.records[2*w+11*4:], last)
	binary.LittleEndian.PutUint32(d.records[2*w+6*4:], last)
	b, _ := d.encode()
	r := openGEO2(t, b)
	rec, ok := r.Lookup(netip.MustParseAddr("2001:db8::1"))
	if !ok || rec.TimeZone != d.strs[last] || rec.City != d.strs[last] || rec.CountryISO != "DE" {
		t.Errorf("Lookup = %+v, %v", rec, ok)
	}
	if got := len(r.strings); got > 20 {
		t.Errorf("%d strings kept, want only the used ones", got)
	}
	if m := r.Metadata(); m.Strings != len(d.strs) || m.FormatVersion != 1 {
		t.Errorf("Metadata = %+v", m)
	}
}

// TestGEO1TooManyStrings: a GEO1 file whose u16 fields use more than 65535
// distinct strings cannot be converted.
func TestGEO1TooManyStrings(t *testing.T) {
	d := cityTestDB().withProfile(ProfileCountry)
	d.strs = []string{""}
	d.recCount = 1 << 15
	d.records = make([]byte, int(d.recCount)*int(d.recWidth))
	for i := 1; i < int(d.recCount); i++ {
		for f := range 3 {
			id := uint32(len(d.strs))
			d.strs = append(d.strs, string(binary.LittleEndian.AppendUint32(nil, id)))
			binary.LittleEndian.PutUint32(d.records[i*int(d.recWidth)+f*4:], id)
		}
	}
	b, _ := d.encode()
	if r, err := OpenBytes(b); r != nil || !errors.Is(err, ErrTooLarge) {
		t.Fatalf("OpenBytes = %v, %v; want ErrTooLarge", r, err)
	}
}

func TestGEO2Metadata(t *testing.T) {
	d := excTestDB()
	r := openGEO2(t, encodeGEO2(d))
	want := Metadata{
		FormatVersion: 2, Profile: ProfileCity, LatScale: 4,
		BuildTime: time.Unix(1761600000, 0).UTC(), SourceTime: time.Unix(1588597376, 0).UTC(),
		SourceType: "GeoIP2-City", Language: "en", Description: "", Builder: "ip2geo/test",
		Networks: 10176453, DistinctRecords: 886091,
		Records: 3, Strings: len(toGEO2(d, g2opts{meta: testMeta}).sec(sStrIdx).data)/4 - 1,
		IPv4Ranges: 3, IPv6Ranges: 8,
	}
	if got := r.Metadata(); got != want {
		t.Errorf("Metadata =\n%+v\nwant\n%+v", got, want)
	}
	if got := r.Metadata().BuildTime.Location(); got != time.UTC {
		t.Errorf("BuildTime location %v, want UTC", got)
	}

	// Zero times stay zero; a description is returned as is.
	m := testMeta
	m.buildTime, m.sourceTime, m.description = 0, 0, "test build"
	r = openGEO2(t, toGEO2(d, g2opts{meta: m}).encode())
	if got := r.Metadata(); !got.BuildTime.IsZero() || !got.SourceTime.IsZero() || got.Description != "test build" {
		t.Errorf("Metadata = %+v", got)
	}

	// GEO1: what the file has, and zero for the rest.
	b, _ := d.encode()
	r = openGEO2(t, b)
	want = Metadata{FormatVersion: 1, Profile: ProfileCity, LatScale: 4, Records: 3, Strings: len(d.strs), IPv4Ranges: 3, IPv6Ranges: 8}
	if got := r.Metadata(); got != want {
		t.Errorf("GEO1 Metadata =\n%+v\nwant\n%+v", got, want)
	}
}

func TestGEO2Profiles(t *testing.T) {
	for _, p := range []Profile{ProfileCountry, ProfileRegion, ProfileCity} {
		t.Run(p.String(), func(t *testing.T) {
			r := openGEO2(t, encodeGEO2(cityTestDB().withProfile(p)))
			if r.Profile() != p || r.Metadata().Profile != p || r.recWidth != []int{10, 14, 33}[p] {
				t.Fatalf("Profile() = %v, record width %d", r.Profile(), r.recWidth)
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

// g2case is a corruption of the excTestDB fixture: mut edits the fixture
// before it is encoded, raw the encoded bytes.
type g2case struct {
	name string
	mut  func(f *g2file)
	raw  func(b []byte) []byte
	want error
	msg  string // a substring of the error message, naming the rule
}

func u32(v uint32) *uint32 { return &v }

// setU32 and friends overwrite element i of a section.
func setU16(f *g2file, id uint32, i int, v uint16) {
	binary.LittleEndian.PutUint16(f.sec(id).data[i*2:], v)
}
func setU32(f *g2file, id uint32, i int, v uint32) {
	binary.LittleEndian.PutUint32(f.sec(id).data[i*4:], v)
}
func setU64(f *g2file, id uint32, i int, v uint64) {
	binary.LittleEndian.PutUint64(f.sec(id).data[i*8:], v)
}
func getU32(f *g2file, id uint32, i int) uint32 {
	return binary.LittleEndian.Uint32(f.sec(id).data[i*4:])
}
func getU64(f *g2file, id uint32, i int) uint64 {
	return binary.LittleEndian.Uint64(f.sec(id).data[i*8:])
}

func swapEntries(i, j int) func(*g2file) {
	return func(f *g2file) { f.dir = func(e []g2entry) { e[i], e[j] = e[j], e[i] } }
}

func editEntry(i int, edit func(e *g2entry)) func(*g2file) {
	return func(f *g2file) { f.dir = func(e []g2entry) { edit(&e[i]) } }
}

func resize(id uint32, delta int) func(*g2file) {
	return func(f *g2file) {
		s := f.sec(id)
		if delta < 0 {
			s.data = s.data[:len(s.data)+delta]
		} else {
			s.data = append(s.data, make([]byte, delta)...)
		}
	}
}

// g2corruptions lists a corruption for every reader requirement of
// docs/FORMAT.md. The fixture has 3 records (1-byte ids), 3 IPv4 starts
// (0.0.0.0, then 10.0.0.0 and 10.0.1.0 in bucket 0x0a00), 8 IPv6 starts with
// exceptions at 2 and 3, and the sections in id order, exceptions last.
func g2corruptions() []g2case {
	cases := []g2case{
		// Rule 1: the header.
		{name: "magic", mut: func(f *g2file) { f.magic = "GEO3" }, want: ErrBadMagic},
		{name: "magic case", mut: func(f *g2file) { f.magic = "geo2" }, want: ErrBadMagic},
		{name: "version 1", mut: func(f *g2file) { f.version = 1 }, want: ErrUnsupportedVersion},
		{name: "version 3", mut: func(f *g2file) { f.version = 3 }, want: ErrUnsupportedVersion},
		{name: "profile 3", mut: func(f *g2file) { f.profile = 3 }, msg: "profile"},
		{name: "lat scale 8", mut: func(f *g2file) { f.latScale = 8 }, msg: "lat/lon scale"},
		{name: "id width 0", mut: func(f *g2file) { f.idWidth = 0 }, msg: "id width"},
		{name: "id width 5", mut: func(f *g2file) { f.idWidth = 5 }, msg: "id width"},
		{name: "reserved first", mut: func(f *g2file) { f.reserved[0] = 1 }, msg: "reserved"},
		{name: "reserved last", mut: func(f *g2file) { f.reserved[19] = 0x80 }, msg: "reserved"},
		{name: "truncated header", raw: func(b []byte) []byte { return b[:31] }, msg: "truncated"},

		// Rule 2: the directory.
		{name: "section count 2^32-1", mut: func(f *g2file) { f.count = u32(math.MaxUint32) }, msg: "directory of"},
		{name: "section count past the end", mut: func(f *g2file) { f.count = u32(1 << 20) }, msg: "directory of"},
		{name: "section count one more", mut: func(f *g2file) { f.count = u32(12) }, msg: "inside the header or directory"},
		{name: "section count 0", mut: func(f *g2file) { f.count = u32(0) }, msg: "missing"},
		{name: "out of order", mut: swapEntries(2, 3), msg: "before the end of the previous"},
		{name: "out of order, last two", mut: swapEntries(9, 10), msg: "before the end of the previous"},
		{name: "overlapping", mut: editEntry(4, func(e *g2entry) { e.off -= 8 }), msg: "before the end of the previous"},
		{name: "overlapping, same offset", mut: func(f *g2file) {
			f.dir = func(e []g2entry) { e[4].off = e[3].off }
		}, msg: "before the end of the previous"},
		{name: "inside the directory", mut: editEntry(0, func(e *g2entry) { e.off = 32 }), msg: "inside the header or directory"},
		{name: "at offset 0", mut: editEntry(0, func(e *g2entry) { e.off = 0 }), msg: "inside the header or directory"},
		{name: "unaligned", mut: editEntry(5, func(e *g2entry) { e.off += 4 }), msg: "multiple of 8"},
		{name: "unaligned by 1", mut: editEntry(10, func(e *g2entry) { e.off++ }), msg: "multiple of 8"},
		{name: "past the end", mut: editEntry(10, func(e *g2entry) { e.n += 8 }), msg: "past the"},
		{name: "offset past the end", mut: editEntry(10, func(e *g2entry) { e.off += 1 << 20 }), msg: "past the"},
		{name: "length wraps", mut: editEntry(10, func(e *g2entry) { e.n = math.MaxUint64 - e.off + 9 }), msg: "past the"},
		{name: "offset and length 2^63", mut: editEntry(10, func(e *g2entry) { e.off, e.n = 1<<63, 1<<63 }), msg: "past the"},

		// Rule 3: required sections and lengths.
		{name: "duplicate exception index", mut: func(f *g2file) {
			f.secs = append(f.secs, g2sec{sV6ExcIdx, f.sec(sV6ExcIdx).data})
		}, msg: "listed twice"},
		{name: "exception index without low halves", mut: func(f *g2file) { f.without(sV6ExcLo) }, msg: "V6EXCLO is 0 bytes"},
		{name: "low halves without exception index", mut: func(f *g2file) { f.without(sV6ExcIdx) }, msg: "V6EXCLO is 16 bytes"},
		{name: "META short", mut: resize(sMeta, -1), msg: "META is 63"},
		{name: "META long", mut: resize(sMeta, 8), msg: "META is 72"},
		{name: "STRIDX empty", mut: func(f *g2file) { f.sec(sStrIdx).data = nil }, msg: "STRIDX is 0"},
		{name: "STRIDX odd", mut: resize(sStrIdx, 2), msg: "multiple of 4"},
		{name: "RECORDS one byte over", mut: resize(sRecords, 1), msg: "RECORDS is 100"},
		{name: "RECORDS one byte short", mut: resize(sRecords, -1), msg: "RECORDS is 98"},
		{name: "V4IDX short", mut: resize(sV4Idx, -4), msg: "V4IDX is"},
		{name: "V4IDX long", mut: resize(sV4Idx, 4), msg: "V4IDX is"},
		{name: "V4STARTS odd", mut: resize(sV4Starts, 1), msg: "V4STARTS is 7"},
		{name: "V4STARTS one more", mut: resize(sV4Starts, 2), msg: "V4IDS is 3"},
		{name: "V4IDS short", mut: resize(sV4IDs, -1), msg: "V4IDS is 2"},
		{name: "V6STARTS not a multiple of 8", mut: resize(sV6Starts, 4), msg: "V6STARTS is 68"},
		{name: "V6IDS long", mut: resize(sV6IDs, 1), msg: "V6IDS is 9"},
		{name: "V6EXCIDX not a multiple of 4", mut: resize(sV6ExcIdx, 2), msg: "V6EXCIDX is 10"},
		{name: "V6EXCLO short", mut: resize(sV6ExcLo, -8), msg: "V6EXCLO is 8"},

		// Rule 4: the checksums, through the data (the CRC is computed
		// before the edit) and through the directory.
		{name: "data byte flipped", raw: func(b []byte) []byte {
			e := g2dir(b)[sRecords-1]
			b[e.off+40] ^= 0x10
			return b
		}, msg: "CRC-32C"},

		// Rule 5: strings.
		{name: "STRIDX[0] not 0", mut: func(f *g2file) { setU32(f, sStrIdx, 0, 1) }, msg: "STRIDX[0]"},
		{name: "STRIDX decreasing", mut: func(f *g2file) { setU32(f, sStrIdx, 3, getU32(f, sStrIdx, 4)+1) }, msg: "is below STRIDX"},
		{name: "STRIDX end short", mut: func(f *g2file) {
			n := len(f.sec(sStrIdx).data)/4 - 1
			setU32(f, sStrIdx, n, getU32(f, sStrIdx, n)-1)
		}, msg: "STRIDX ends at"},
		{name: "STRBLOB longer", mut: resize(sStrBlob, 1), msg: "STRIDX ends at"},
		{name: "string 0 not empty", mut: func(f *g2file) { setU32(f, sStrIdx, 1, 1) }, msg: "string 0"},
		{name: "META source type out of range", mut: func(f *g2file) {
			binary.LittleEndian.PutUint32(f.sec(sMeta).data[24:], uint32(len(f.sec(sStrIdx).data)/4-1))
		}, msg: "META source type"},
		{name: "META builder 2^32-1", mut: func(f *g2file) {
			binary.LittleEndian.PutUint32(f.sec(sMeta).data[36:], math.MaxUint32)
		}, msg: "META builder"},
		{name: "META reserved", mut: func(f *g2file) { f.sec(sMeta).data[63] = 1 }, msg: "META: reserved"},
		{name: "record country ISO out of range", mut: func(f *g2file) {
			binary.LittleEndian.PutUint16(f.sec(sRecords).data[33+0:], uint16(len(f.sec(sStrIdx).data)/4-1))
		}, msg: "record 1: string id"},
		{name: "record region name out of range", mut: func(f *g2file) {
			binary.LittleEndian.PutUint16(f.sec(sRecords).data[66+12:], 0xFFFF)
		}, msg: "record 2: string id"},
		{name: "record time zone out of range", mut: func(f *g2file) {
			binary.LittleEndian.PutUint16(f.sec(sRecords).data[66+31:], 0x8000)
		}, msg: "record 2: string id"},
		{name: "record city name out of range", mut: func(f *g2file) {
			f.sec(sRecords).data[33+16] = 1 // u24 id >= 65536
		}, msg: "city name is string"},

		// Rule 6: records and ids.
		{name: "no records", mut: func(f *g2file) { f.sec(sRecords).data = nil }, msg: "no records"},
		{name: "record 0 country name", mut: func(f *g2file) { f.sec(sRecords).data[2] = 1 }, msg: "record 0"},
		{name: "record 0 last byte", mut: func(f *g2file) { f.sec(sRecords).data[32] = 1 }, msg: "record 0"},
		{name: "IPv4 id = record count", mut: func(f *g2file) { f.sec(sV4IDs).data[1] = 3 }, msg: "IPv4 range 1 points at record 3"},
		{name: "IPv6 id 255", mut: func(f *g2file) { f.sec(sV6IDs).data[7] = 0xFF }, msg: "IPv6 range 7 points at record 255"},

		// Rule 7: IPv4.
		{name: "V4IDX sentinel above", mut: func(f *g2file) { setU32(f, sV4Idx, 1<<16, 4) }, msg: "V4IDX[65536]"},
		{name: "V4IDX sentinel below", mut: func(f *g2file) { setU32(f, sV4Idx, 1<<16, 2) }, msg: "V4IDX[65536]"},
		{name: "V4IDX[0] not 0", mut: func(f *g2file) { setU32(f, sV4Idx, 0, 1) }, msg: "V4IDX[0]"},
		{name: "V4IDX decreasing", mut: func(f *g2file) { setU32(f, sV4Idx, 100, 2) }, msg: "is below V4IDX"},
		{name: "V4IDX past the start count", mut: func(f *g2file) { setU32(f, sV4Idx, 5, 1000) }, msg: "above the IPv4 start count"},
		{name: "IPv4 starts swapped in a bucket", mut: func(f *g2file) {
			setU16(f, sV4Starts, 1, 0x0100)
			setU16(f, sV4Starts, 2, 0x0000)
		}, msg: "IPv4 start 2 is not above start 1"},
		{name: "IPv4 duplicate start", mut: func(f *g2file) { setU16(f, sV4Starts, 2, 0) }, msg: "IPv4 start 2 is not above start 1"},

		// Rule 8: IPv6.
		{name: "V6STARTS decreasing", mut: func(f *g2file) {
			h5, h6 := getU64(f, sV6Starts, 5), getU64(f, sV6Starts, 6)
			setU64(f, sV6Starts, 5, h6)
			setU64(f, sV6Starts, 6, h5)
		}, msg: "V6STARTS[6] is below"},
		{name: "IPv6 duplicate start", mut: func(f *g2file) { setU64(f, sV6Starts, 5, getU64(f, sV6Starts, 4)) }, msg: "IPv6 start 5 is not above"},
		{name: "IPv6 run out of order", mut: func(f *g2file) {
			l0, l1 := getU64(f, sV6ExcLo, 0), getU64(f, sV6ExcLo, 1)
			setU64(f, sV6ExcLo, 0, l1)
			setU64(f, sV6ExcLo, 1, l0)
		}, msg: "IPv6 start 3 is not above"},
		{name: "IPv6 run ends on a zero low half", mut: func(f *g2file) { setU32(f, sV6ExcIdx, 0, 1) }, msg: "IPv6 start 2 is not above"},
		{name: "exceptions out of order", mut: func(f *g2file) {
			setU32(f, sV6ExcIdx, 0, 3)
			setU32(f, sV6ExcIdx, 1, 2)
		}, msg: "exception 1 (start 2) is not above"},
		{name: "exception listed twice", mut: func(f *g2file) { setU32(f, sV6ExcIdx, 1, 2) }, msg: "exception 1 (start 2) is not above"},
		{name: "exception index = start count", mut: func(f *g2file) { setU32(f, sV6ExcIdx, 1, 8) }, msg: "exception 1 is start 8 of 8"},
		{name: "exception index 2^32-1", mut: func(f *g2file) { setU32(f, sV6ExcIdx, 0, math.MaxUint32) }, msg: "exception 0 is start"},
		{name: "exception with zero low half", mut: func(f *g2file) { setU64(f, sV6ExcLo, 1, 0) }, msg: "zero low half"},
	}

	names := []string{1: "META", "STRIDX", "STRBLOB", "RECORDS", "V4IDX", "V4STARTS", "V4IDS", "V6STARTS", "V6IDS", "V6EXCIDX", "V6EXCLO"}
	for id := uint32(sMeta); id <= sV6IDs; id++ {
		cases = append(cases, g2case{name: "missing " + names[id], mut: func(f *g2file) { f.without(id) }, msg: "missing"})
	}
	for id := uint32(sMeta); id <= sV6ExcLo; id++ {
		cases = append(cases,
			g2case{name: "duplicate " + names[id], mut: func(f *g2file) {
				f.secs = append(f.secs, g2sec{id, f.sec(id).data})
			}, msg: "listed twice"},
			g2case{name: "CRC of " + names[id], mut: editEntry(int(id-1), func(e *g2entry) { e.crc ^= 1 << (id % 32) }), msg: "CRC-32C"},
		)
	}
	return cases
}

func TestGEO2Rejects(t *testing.T) {
	base := func() *g2file { return toGEO2(excTestDB(), g2opts{meta: testMeta}) }
	if _, err := OpenBytes(base().encode()); err != nil {
		t.Fatalf("the base fixture does not open: %v", err)
	}
	for _, c := range g2corruptions() {
		t.Run(c.name, func(t *testing.T) {
			f := base()
			if c.mut != nil {
				c.mut(f)
			}
			b := f.encode()
			if c.raw != nil {
				b = c.raw(b)
			}
			want := c.want
			if want == nil {
				want = ErrCorrupt
			}
			for _, opts := range [][]Option{nil, {NoCopy()}} {
				r, err := OpenBytes(b, opts...)
				if r != nil || !errors.Is(err, want) {
					t.Fatalf("OpenBytes = %v, %v; want nil, %v", r, err, want)
				}
				if !strings.HasPrefix(err.Error(), "ip2geo: ") || !strings.Contains(err.Error(), c.msg) {
					t.Errorf("error %q, want the ip2geo: prefix and %q", err, c.msg)
				}
			}
		})
	}
}

// TestGEO2RejectsIDWidth: the id width must be the smallest for the record
// count, even when the id sections are consistent with a wider one.
func TestGEO2RejectsIDWidth(t *testing.T) {
	cases := []struct {
		d     *testDB
		width byte
	}{
		{excTestDB(), 2}, {excTestDB(), 3}, {excTestDB(), 4},
		{randomTestDB(9, ProfileCountry, 256, 100, 100), 2}, // ids up to 255 fit one byte
		{randomTestDB(9, ProfileCountry, 257, 100, 100), 3},
	}
	for _, c := range cases {
		b := toGEO2(c.d, g2opts{idWidth: c.width}).encode()
		if r, err := OpenBytes(b); r != nil || !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "record id width") {
			t.Errorf("%d records, width %d: OpenBytes = %v, %v; want ErrCorrupt", c.d.recCount, c.width, r, err)
		}
	}
	if _, err := OpenBytes(toGEO2(randomTestDB(9, ProfileCountry, 257, 100, 100), g2opts{}).encode()); err != nil {
		t.Errorf("257 records, minimal width: %v", err)
	}
}

// TestGEO2RejectsIDAboveCount: with 2-byte ids, an id between the record
// count and 65535 is refused.
func TestGEO2RejectsIDAboveCount(t *testing.T) {
	f := toGEO2(randomTestDB(10, ProfileCountry, 300, 100, 100), g2opts{})
	binary.LittleEndian.PutUint16(f.sec(sV6IDs).data[2*5:], 300)
	if r, err := OpenBytes(f.encode()); r != nil || !errors.Is(err, ErrCorrupt) {
		t.Fatalf("OpenBytes = %v, %v; want ErrCorrupt", r, err)
	}
}

// TestGEO2Truncated cuts the fixture at every byte of the header and the
// directory and around every section boundary.
func TestGEO2Truncated(t *testing.T) {
	b := encodeGEO2(excTestDB())
	cuts := map[int]bool{}
	for n := 0; n < 32+24*11+8; n++ {
		cuts[n] = true
	}
	for _, e := range g2dir(b) {
		for _, n := range []int{int(e.off), int(e.off) + 1, int(e.off+e.n) - 1, int(e.off + e.n), align8(int(e.off+e.n)) - 1} {
			if n >= 0 && n < len(b) {
				cuts[n] = true
			}
		}
	}
	cuts[len(b)-1] = true
	for n := range cuts {
		r, err := OpenBytes(b[:n])
		want := ErrCorrupt
		if n < 4 {
			want = ErrBadMagic
		}
		if r != nil || !errors.Is(err, want) {
			t.Errorf("len %d of %d: OpenBytes = %v, %v; want nil, %v", n, len(b), r, err, want)
		}
	}
}

// TestGEO2Accepts: what the spec allows besides the canonical layout.
func TestGEO2Accepts(t *testing.T) {
	noExc := cityTestDB()
	cases := []struct {
		name string
		f    *g2file
		raw  func([]byte) []byte
	}{
		{name: "no exception sections", f: toGEO2(noExc, g2opts{})},
		{name: "empty exception sections", f: toGEO2(noExc, g2opts{emptyExc: true})},
		{name: "unknown section last", f: func() *g2file {
			f := toGEO2(excTestDB(), g2opts{meta: testMeta})
			f.secs = append(f.secs, g2sec{99, []byte("a later minor revision")})
			return f
		}()},
		{name: "unknown sections first and in the middle", f: func() *g2file {
			f := toGEO2(excTestDB(), g2opts{meta: testMeta})
			f.secs = append([]g2sec{{0, []byte{1}}, {12, nil}}, f.secs...)
			f.secs = append(f.secs[:6], append([]g2sec{{1 << 31, make([]byte, 100)}}, f.secs[6:]...)...)
			return f
		}()},
		{name: "unknown section listed twice", f: func() *g2file {
			f := toGEO2(excTestDB(), g2opts{})
			f.secs = append(f.secs, g2sec{42, []byte{1}}, g2sec{42, []byte{2}})
			return f
		}()},
		{name: "sections out of id order", f: func() *g2file {
			f := toGEO2(excTestDB(), g2opts{})
			f.secs[0], f.secs[8] = f.secs[8], f.secs[0]
			return f
		}()},
		{name: "empty sections sharing an offset", f: toGEO2(func() *testDB {
			d := cityTestDB()
			d.v4starts, d.v4rec, d.v6starts, d.v6rec = nil, nil, nil, nil
			return d
		}(), g2opts{emptyExc: true})},
		{name: "bytes after the last section", f: toGEO2(excTestDB(), g2opts{}), raw: func(b []byte) []byte {
			return append(b, make([]byte, 13)...)
		}},
		{name: "gaps between sections", f: func() *g2file {
			f := toGEO2(excTestDB(), g2opts{})
			f.gap = 64
			return f
		}()},
		{name: "lat scale 7, region profile", f: func() *g2file {
			d := cityTestDB().withProfile(ProfileRegion)
			d.latScale = 7
			return toGEO2(d, g2opts{})
		}()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := c.f.encode()
			if c.raw != nil {
				b = c.raw(b)
			}
			r, err := OpenBytes(b)
			if err != nil {
				t.Fatal(err)
			}
			checkReader(t, r)
			checkIndex(t, r)
		})
	}
}

// TestGEO2ByteFlips corrupts one structural byte at a time: the header, the
// directory, and the sections except their free-form bytes (string text,
// META numbers, and record fields that are not string ids), which are left
// to the fuzzers. With checksums verified no flip in a section opens.
// Without them some open; none may panic, and a Reader that opens must
// answer lookups as its own tables say.
func TestGEO2ByteFlips(t *testing.T) {
	orig := encodeGEO2(excTestDB())
	const dirEnd = 32 + 24*11
	var at []int
	for i := range dirEnd {
		at = append(at, i)
	}
	for _, e := range g2dir(orig) {
		for k := range int(e.n) {
			switch {
			case e.id == sStrBlob,
				e.id == sMeta && (k < 24 || k >= 40),
				e.id == sRecords && k >= 33 && !isStringField(k%33),
				e.id == sV4Idx && !slices.Contains([]int{0, 1, 4, 0xa00 * 4, 0xa01 * 4, 65536 * 4, 65536*4 + 3}, k):
				continue
			}
			at = append(at, int(e.off)+k)
		}
	}
	tried, opened := 0, 0
	for _, i := range at {
		for _, v := range []byte{0x00, 0xFF, orig[i] ^ 0x01, orig[i] ^ 0x80} {
			if v == orig[i] {
				continue
			}
			b := append([]byte(nil), orig...)
			b[i] = v
			for _, opts := range [][]Option{nil, {SkipChecksum()}} {
				tried++
				r, err := OpenBytes(b, opts...)
				if err != nil {
					if !isSentinel(err) {
						t.Fatalf("byte %d = %#x: error %v wraps no sentinel", i, v, err)
					}
					continue
				}
				if opts == nil && i >= dirEnd {
					t.Fatalf("section byte %d = %#x opened with checksums verified", i, v)
				}
				opened++
				checkReader(t, r)
				checkIndex(t, r)
			}
		}
	}
	t.Logf("%d of %d single-byte corruptions opened", opened, tried)
}

// isStringField reports whether byte k of a packed city record belongs to a
// string id.
func isStringField(k int) bool {
	return k < 6 || k >= 10 && k < 17 || k >= 31
}

func TestGEO2SkipChecksum(t *testing.T) {
	f := toGEO2(excTestDB(), g2opts{})
	f.dir = func(e []g2entry) {
		for i := range e {
			e[i].crc = ^e[i].crc
		}
	}
	b := f.encode()
	if r, err := OpenBytes(b); r != nil || !errors.Is(err, ErrCorrupt) {
		t.Fatalf("OpenBytes = %v, %v; want ErrCorrupt", r, err)
	}
	r := openGEO2(t, b, SkipChecksum())
	if rec, ok := r.Lookup(netip.MustParseAddr("2001:db8::8000:0:0:1")); !ok || rec.City != "Los Angeles" {
		t.Errorf("Lookup = %+v, %v", rec, ok)
	}
	// SkipChecksum does not skip anything else.
	f = toGEO2(excTestDB(), g2opts{})
	setU64(f, sV6ExcLo, 0, 0)
	if r, err := OpenBytes(f.encode(), SkipChecksum()); r != nil || !errors.Is(err, ErrCorrupt) {
		t.Fatalf("OpenBytes(SkipChecksum) = %v, %v; want ErrCorrupt", r, err)
	}
}

// within reports whether p points into b.
func within(p unsafe.Pointer, b []byte) bool {
	start := uintptr(unsafe.Pointer(unsafe.SliceData(b)))
	return uintptr(p) >= start && uintptr(p) < start+uintptr(len(b))
}

// stringsWithin reports whether any non-empty string of r points into b.
func stringsWithin(r *Reader, b []byte) bool {
	for _, s := range r.strings {
		if s != "" && within(unsafe.Pointer(unsafe.StringData(s)), b) {
			return true
		}
	}
	return false
}

func TestOpenBytesNoCopy(t *testing.T) {
	b := encodeGEO2(excTestDB())
	ip := netip.MustParseAddr("10.0.0.5")

	// Without NoCopy the Reader keeps a copy: editing b changes nothing.
	r := openGEO2(t, b)
	want, _ := r.Lookup(ip)
	if within(unsafe.Pointer(&r.records[0]), b) || stringsWithin(r, b) {
		t.Fatal("the Reader refers to the caller's slice without NoCopy")
	}
	r2 := openGEO2(t, b)
	clear(b)
	if got, ok := r2.Lookup(ip); !ok || got != want {
		t.Errorf("Lookup after clearing the input = %+v, %v; want %+v", got, ok, want)
	}

	// With NoCopy the sections are the caller's, the strings are not.
	b = encodeGEO2(excTestDB())
	r = openGEO2(t, b, NoCopy())
	for _, s := range [][]byte{r.records, r.v4idx, r.v4starts, r.v4ids, r.v6hi, r.v6ids, r.v6excIdx, r.v6excLo} {
		if !within(unsafe.Pointer(unsafe.SliceData(s)), b) {
			t.Fatal("a section is not in the caller's slice with NoCopy")
		}
	}
	if stringsWithin(r, b) || within(unsafe.Pointer(unsafe.StringData(r.Metadata().Builder)), b) {
		t.Fatal("a string refers to the caller's slice")
	}
	rec, _ := r.Lookup(ip)
	e := g2dir(b)[sStrBlob-1]
	clear(b[e.off : e.off+e.n]) // the copy of the strings is unaffected
	if got, _ := r.Lookup(ip); got != rec || got.City != "Los Angeles" {
		t.Errorf("Lookup after clearing STRBLOB = %+v, want %+v", got, rec)
	}

	// GEO1 is converted either way.
	g1, _ := excTestDB().encode()
	r = openGEO2(t, g1, NoCopy())
	if within(unsafe.Pointer(&r.records[0]), g1) || stringsWithin(r, g1) {
		t.Error("a GEO1 Reader refers to the input")
	}
}

// writeMapped writes b to a temporary file that tests open mapped. Windows
// cannot remove a mapped file, so there the test's dropped Readers are
// collected and unmapped before the temporary directory is removed.
func writeMapped(t *testing.T, b []byte) string {
	t.Helper()
	p := writeFile(t, b)
	if runtime.GOOS == "windows" {
		t.Cleanup(func() { drainCleanups() })
	}
	return p
}

func TestLookupAllocsGEO2(t *testing.T) {
	b := encodeGEO2(excTestDB())
	p := writeMapped(t, b)
	mapped, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]*Reader{"OpenBytes": openGEO2(t, b), "NoCopy": openGEO2(t, b, NoCopy()), "Open": mapped} {
		for _, c := range []struct {
			ip string
			ok bool
		}{
			{"10.0.0.5", true}, {"2001:db8::1", true}, {"2001:db8::8000:0:0:1", true}, {"2002:a00:5::1", true},
			{"::ffff:10.0.0.5", true}, {"192.0.2.1", false}, {"2001:db8::c000:0:0:1", false}, {"2001:db9::1", false},
		} {
			a := netip.MustParseAddr(c.ip)
			if _, ok := r.Lookup(a); ok != c.ok {
				t.Errorf("%s: %s ok=%v, want %v", name, c.ip, ok, c.ok)
			}
			if n := testing.AllocsPerRun(1000, func() { r.Lookup(a) }); n != 0 {
				t.Errorf("%s: %s allocates %v times per Lookup", name, c.ip, n)
			}
		}
	}
}

func TestOpenGEO2(t *testing.T) {
	d := excTestDB()
	b := encodeGEO2(d)
	p := writeMapped(t, b)
	for _, c := range []struct {
		name   string
		opts   []Option
		mapped bool
	}{
		{"default", nil, mmapSupported},
		{"NoMmap", []Option{NoMmap()}, false},
		{"SkipChecksum", []Option{SkipChecksum()}, mmapSupported},
		{"NoCopy is ignored", []Option{NoCopy()}, mmapSupported},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, err := Open(p, c.opts...)
			if err != nil {
				t.Fatal(err)
			}
			if r.mapped != c.mapped {
				t.Errorf("mapped = %v, want %v", r.mapped, c.mapped)
			}
			for _, a := range probeAddrs(d, 3, 500) {
				want, wok := d.lookup(a)
				if got, ok := r.Lookup(a); ok != wok || got != want {
					t.Fatalf("Lookup(%v) = %+v, %v; want %+v, %v", a, got, ok, want, wok)
				}
			}
			if r.Metadata().Builder != "ip2geo/test" {
				t.Errorf("Metadata = %+v", r.Metadata())
			}
			if err := r.Close(); err != nil {
				t.Error(err)
			}
		})
	}

	// A GEO1 file is converted and never stays mapped.
	g1, _ := d.encode()
	r, err := Open(writeFile(t, g1))
	if err != nil {
		t.Fatal(err)
	}
	if r.mapped {
		t.Error("a GEO1 Reader is mapped")
	}

	// Checksums are verified on a mapped file unless skipped.
	bad := bytes.Clone(b)
	e := g2dir(bad)[sV6Starts-1]
	bad[e.off] ^= 1
	pb := writeMapped(t, bad)
	for _, opts := range [][]Option{nil, {NoMmap()}} {
		if r, err := Open(pb, opts...); r != nil || !errors.Is(err, ErrCorrupt) {
			t.Errorf("Open(bad CRC, %d options) = %v, %v; want ErrCorrupt", len(opts), r, err)
		}
		if _, err := Open(pb, append(opts, SkipChecksum())...); err != nil {
			t.Errorf("Open(bad CRC, SkipChecksum): %v", err)
		}
	}

	for _, c := range []struct {
		name string
		in   []byte
		want error
	}{
		{"short GEO2 header", b[:20], ErrCorrupt},
		{"GEO2 version 3", append([]byte("GEO2\x03"), b[5:]...), ErrUnsupportedVersion},
		{"truncated GEO2", b[:len(b)-8], ErrCorrupt},
		{"GEO2 directory only", b[:32+24*11], ErrCorrupt},
	} {
		for _, opts := range [][]Option{nil, {NoMmap()}} {
			if r, err := Open(writeMapped(t, c.in), opts...); r != nil || !errors.Is(err, c.want) {
				t.Errorf("%s: Open = %v, %v; want nil, %v", c.name, r, err, c.want)
			}
		}
	}
}

// TestOpenGEO2ChecksHeaderFirst: a large file with a bad GEO2 header, or a
// directory longer than the file, is refused without reading the rest.
func TestOpenGEO2ChecksHeaderFirst(t *testing.T) {
	hdr := func(version, profile byte, count uint32) string {
		b := []byte{'G', 'E', 'O', '2', version, profile, 4, 1}
		b = binary.LittleEndian.AppendUint32(b, count)
		return string(append(b, make([]byte, 20)...))
	}
	for h, want := range map[string]error{
		hdr(3, 2, 11):               ErrUnsupportedVersion,
		hdr(2, 9, 11):               ErrCorrupt,
		hdr(2, 2, math.MaxUint32):   ErrCorrupt,
		hdr(2, 2, (64<<20)/24+1000): ErrCorrupt, // the directory is longer than the file
	} {
		for _, opts := range [][]Option{nil, {NoMmap()}} {
			p := sparseFile(t, h, 64<<20)
			before := totalAlloc()
			r, err := Open(p, opts...)
			alloc := totalAlloc() - before
			if r != nil || !errors.Is(err, want) {
				t.Errorf("header %q: Open = %v, %v; want nil, %v", h, r, err, want)
			}
			if alloc >= 1<<20 {
				t.Errorf("header %q: allocated %d bytes, want < 1 MiB", h, alloc)
			}
		}
	}
}

// TestMappingReleased drops a mapped Reader and checks that the garbage
// collector unmaps the file.
func TestMappingReleased(t *testing.T) {
	if !mmapSupported {
		t.Skip("no memory mapping on " + runtime.GOOS)
	}
	p := writeMapped(t, encodeGEO2(excTestDB()))
	before := drainCleanups()
	func() {
		r, err := Open(p)
		if err != nil {
			t.Fatal(err)
		}
		if !r.mapped {
			t.Fatal("the Reader is not mapped")
		}
		if _, ok := r.Lookup(netip.MustParseAddr("10.0.0.5")); !ok {
			t.Fatal("Lookup failed")
		}
	}()
	runtime.GC()
	runtime.GC()
	// Cleanups run on their own goroutine after the collection.
	for deadline := time.Now().Add(5 * time.Second); unmapped.Load() == before; {
		if time.Now().After(deadline) {
			t.Fatal("the mapping was not released after the Reader became unreachable")
		}
		runtime.GC()
		time.Sleep(time.Millisecond)
	}

	// A failed open releases its mapping at once, and so does a GEO1 one:
	// neither leaves a cleanup behind.
	bad := encodeGEO2(excTestDB())
	bad[len(bad)-1] ^= 1
	if _, err := Open(writeMapped(t, bad)); err == nil {
		t.Fatal("a corrupt file opened")
	}
	g1, _ := excTestDB().encode()
	if _, err := Open(writeFile(t, g1)); err != nil {
		t.Fatal(err)
	}
	if n := drainCleanups(); n != before+1 {
		t.Errorf("%d mappings released by cleanups, want 1", n-before)
	}
}

// drainCleanups collects until no more mappings are released, so that the
// mapped Readers earlier tests dropped do not count, and returns the count.
func drainCleanups() int64 {
	n, stable := unmapped.Load(), 0
	for stable < 5 {
		runtime.GC()
		time.Sleep(2 * time.Millisecond)
		if m := unmapped.Load(); m != n {
			n, stable = m, 0
		} else {
			stable++
		}
	}
	return n
}

// TestMappedLookupsUnderGC looks up in mapped Readers that become
// unreachable during the lookups, while the collector runs.
func TestMappedLookupsUnderGC(t *testing.T) {
	if !mmapSupported {
		t.Skip("no memory mapping on " + runtime.GOOS)
	}
	p := writeMapped(t, encodeGEO2(excTestDB()))
	a := netip.MustParseAddr("2001:db8::8000:0:0:1")
	stop, done := make(chan struct{}), make(chan int)
	go func() {
		n := 0
		for ; ; n++ {
			select {
			case <-stop:
				done <- n
				return
			default:
				runtime.GC()
			}
		}
	}()
	defer func() {
		close(stop)
		t.Logf("%d collections", <-done)
	}()
	for range 100 {
		r, err := Open(p)
		if err != nil {
			t.Fatal(err)
		}
		if rec, ok := r.Lookup(a); !ok || rec.City != "Los Angeles" {
			t.Fatalf("Lookup = %+v, %v", rec, ok)
		}
	}
}

func TestGEO2ReaderFile(t *testing.T) {
	// The mapping is read-only: the file can be read by others, and the
	// Reader keeps answering after the path is removed.
	if !mmapSupported || runtime.GOOS == "windows" {
		t.Skip("needs a mapping that survives removing the file")
	}
	p := filepath.Join(t.TempDir(), "db.geoc")
	if err := os.WriteFile(p, encodeGEO2(excTestDB()), 0o400); err != nil {
		t.Fatal(err)
	}
	r, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if rec, ok := r.Lookup(netip.MustParseAddr("10.0.0.5")); !ok || rec.City != "Los Angeles" {
		t.Errorf("Lookup = %+v, %v", rec, ok)
	}
}
