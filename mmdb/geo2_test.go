package mmdb_test

// Tests of the GEO2 files mmdb.Build writes, checked with the independent
// parser of geo2parse_test.go rather than with package ip2geo's reader.

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/mmdb"
)

// fixedBuildTime makes test builds reproducible.
var fixedBuildTime = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// buildGEO2 builds src with opts into a temporary file and returns the
// parsed file and its bytes. It fails t if the build fails, if the file
// breaks a rule of the spec or of the builder's layout, or if the stats
// disagree with the file.
func buildGEO2(t *testing.T, src string, opts mmdb.Options) (*geo2File, []byte) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out.geoc")
	st, err := mmdb.Build(src, out, opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	f, err := parseGEO2(b)
	if err != nil {
		t.Fatalf("parse the built file: %v", err)
	}
	got := mmdb.Stats{
		Profile: f.profile, Networks: int(f.meta.networks), DistinctRecords: int(f.meta.distinctRecords),
		Records: len(f.recs), Strings: len(f.strs), V4Ranges: len(f.v4id), V6Ranges: len(f.v6id), Bytes: int64(len(b)),
	}
	if *st != got {
		t.Errorf("Stats = %+v, file says %+v", *st, got)
	}
	if f.profile != opts.Profile {
		t.Errorf("profile %v, want %v", f.profile, opts.Profile)
	}
	return f, b
}

// checkParsedLookups is checkLookups on a parsed file.
func checkParsedLookups(t *testing.T, f *geo2File, cases []lookupCase) {
	t.Helper()
	for _, c := range cases {
		rec, ok := f.lookup(netip.MustParseAddr(c.ip))
		if ok != c.found {
			t.Errorf("%s: ok = %v, want %v (record %+v)", c.ip, ok, c.found, rec)
			continue
		}
		if want := project(c.want, f.profile); rec != want {
			t.Errorf("%s:\n got %+v\nwant %+v", c.ip, rec, want)
		}
	}
}

// sectionIDs returns the directory's section ids in file order.
func sectionIDs(f *geo2File) []uint32 {
	ids := make([]uint32, len(f.dir))
	for i, s := range f.dir {
		ids[i] = s.id
	}
	return ids
}

// countryOnly is a source record with a country ISO code only.
func countryOnly(iso string) map[string]any {
	return map[string]any{"country": map[string]any{"iso_code": iso}}
}

// seqV4 returns the /32 network of the i-th address from 10.0.0.0.
func seqV4(i int) string {
	return fmt.Sprintf("%d.%d.%d.%d/32", 10+i>>24, i>>16&0xff, i>>8&0xff, i&0xff)
}

// TestGEO2Layout checks the file of each profile built from cityFixture:
// header, sections, counts, boundaries, records, and every lookup of
// cityLookups through the spec's lookup algorithm.
func TestGEO2Layout(t *testing.T) {
	t.Parallel()
	src := writeMMDB(t, cityFixture())
	tests := []struct {
		profile  ip2geo.Profile
		latScale uint8
		records  int
		v4, v6   string
	}{
		{ip2geo.ProfileCountry, 0, 5,
			"1.0.0.0=1 1.0.2.0=2 1.0.3.0=0 1.0.4.0=3 1.0.8.0=0",
			"2001:db8::=4 2001:db9::=0 2001:dc0::=2 2001:dc1::=0"},
		{ip2geo.ProfileRegion, 0, 5,
			"1.0.0.0=1 1.0.2.0=2 1.0.3.0=0 1.0.4.0=3 1.0.8.0=0",
			"2001:db8::=4 2001:db9::=0 2001:dc0::=2 2001:dc1::=0"},
		{ip2geo.ProfileCity, mmdb.DefaultLatScale, 6,
			"1.0.0.0=1 1.0.1.0=2 1.0.2.0=3 1.0.3.0=0 1.0.4.0=4 1.0.8.0=0",
			"2001:db8::=5 2001:db9::=0 2001:dc0::=3 2001:dc1::=0"},
	}
	for _, tt := range tests {
		t.Run(tt.profile.String(), func(t *testing.T) {
			t.Parallel()
			f, _ := buildGEO2(t, src, mmdb.Options{Profile: tt.profile, BuildTime: fixedBuildTime})
			if f.latScale != tt.latScale || f.idWidth != 1 || len(f.recs) != tt.records {
				t.Errorf("latScale %d, idWidth %d, %d records; want %d, 1, %d",
					f.latScale, f.idWidth, len(f.recs), tt.latScale, tt.records)
			}
			// No IPv6 start has low bits set: no exception sections.
			if got, want := sectionIDs(f), []uint32{1, 2, 3, 4, 5, 6, 7, 8, 9}; !slices.Equal(got, want) {
				t.Errorf("sections %v, want %v", got, want)
			}
			if got := f.v4Boundaries(); got != tt.v4 {
				t.Errorf("IPv4 boundaries:\n got %s\nwant %s", got, tt.v4)
			}
			if got := f.v6Boundaries(); got != tt.v6 {
				t.Errorf("IPv6 boundaries:\n got %s\nwant %s", got, tt.v6)
			}
			// All IPv4 boundaries are in bucket 1.0.0.0/16.
			n := uint32(len(f.v4id))
			for g, want := range map[int]uint32{0: 0, 0x100: 0, 0x101: n, 0xffff: n, 0x10000: n} {
				if f.v4idx[g] != want {
					t.Errorf("V4IDX[%#x] = %d, want %d", g, f.v4idx[g], want)
				}
			}
			checkParsedLookups(t, f, cityLookups)
		})
	}
}

// TestGEO2StringNumbering checks the exact string table: the strings of
// 16-bit fields first, then the META strings and city names, each group in
// the order first seen, and the record fields that point into it.
func TestGEO2StringNumbering(t *testing.T) {
	t.Parallel()
	src := writeMMDB(t, cityFixture())

	f, _ := buildGEO2(t, src, mmdb.Options{Profile: ip2geo.ProfileCity, BuildTime: fixedBuildTime})
	builder := f.meta.builder
	if !strings.HasPrefix(builder, "ip2geo") {
		t.Errorf("builder %q, want an ip2geo prefix", builder)
	}
	// "Europe/London" is first seen after "Wimbledon" but comes before it,
	// and "Tokyo", a region and a city name, is one 16-bit string.
	want := []string{"", "GB", "United Kingdom", "EU", "ENG", "England", "Europe/London",
		"DE", "Germany", "BY", "Bavaria", "Europe/Berlin", "JP", "Japan", "AS", "13", "Tokyo", "Asia/Tokyo",
		"GeoIP2-City", "en", builder, "Wimbledon", "London", "Munich"}
	if !slices.Equal(f.strs, want) || f.u16Strings != 18 {
		t.Errorf("city strings (first %d of 16-bit fields):\n got %q\nwant %q", f.u16Strings, f.strs, want)
	}
	tokyo := f.recs[f.lookupID(netip.MustParseAddr("2001:db8::1"))]
	if tokyo.regionName != 16 || tokyo.cityName != 16 || tokyo.tz != 17 {
		t.Errorf("Tokyo record: region name %d, city name %d, time zone %d; want 16, 16, 17",
			tokyo.regionName, tokyo.cityName, tokyo.tz)
	}

	f, _ = buildGEO2(t, src, mmdb.Options{Profile: ip2geo.ProfileCountry, Lang: "de", Description: "Grüße", BuildTime: fixedBuildTime})
	want = []string{"", "GB", "Vereinigtes Königreich", "EU", "DE", "Deutschland", "JP", "Japan", "AS",
		"GeoIP2-City", "de", "Grüße", builder}
	if !slices.Equal(f.strs, want) || f.u16Strings != 9 {
		t.Errorf("country strings (first %d of 16-bit fields):\n got %q\nwant %q", f.u16Strings, f.strs, want)
	}
}

// TestGEO2CityNamesAbove16Bits: city names are 24-bit ids after every
// 16-bit string, even a 16-bit string first seen after 70,000 city names.
// 70,001 records also need 3-byte record ids.
func TestGEO2CityNamesAbove16Bits(t *testing.T) {
	t.Parallel()
	const n = 70_000
	fx := mmdbFixture{dbType: "GeoIP2-City", languages: []string{"en"}}
	for i := range n {
		d := map[string]any{
			"country": map[string]any{"iso_code": "GB", "names": map[string]any{"en": "United Kingdom"}},
			"city":    map[string]any{"names": map[string]any{"en": fmt.Sprintf("city%05d", i)}},
		}
		if i == n-1 {
			d["location"] = map[string]any{"time_zone": "Late/Zone", "latitude": -33.8688, "longitude": 151.2093}
		}
		fx.networks = append(fx.networks, mmdbNet{seqV4(i), d})
	}
	f, _ := buildGEO2(t, writeMMDB(t, fx), mmdb.Options{Profile: ip2geo.ProfileCity, BuildTime: fixedBuildTime})

	if f.u16Strings != 4 || f.strs[3] != "Late/Zone" || len(f.strs) != 4+3+n {
		t.Errorf("%d strings, %d of 16-bit fields, string 3 %q; want %d, 4, Late/Zone",
			len(f.strs), f.u16Strings, f.strs[3], 4+3+n)
	}
	if f.idWidth != 3 || len(f.recs) != n+1 {
		t.Errorf("idWidth %d for %d records, want 3 for %d", f.idWidth, len(f.recs), n+1)
	}
	gb := ip2geo.Record{CountryISO: "GB", CountryName: "United Kingdom"}
	city := func(i int) ip2geo.Record { r := gb; r.City = fmt.Sprintf("city%05d", i); return r }
	last := city(n - 1)
	last.TimeZone, last.Latitude, last.Longitude = "Late/Zone", -33.8688, 151.2093
	checkParsedLookups(t, f, []lookupCase{
		{"9.255.255.255", false, ip2geo.Record{}},
		{"10.0.0.0", true, city(0)},
		{"10.0.255.255", true, city(65535)},
		{"10.1.0.64", true, city(65600)},
		{"10.1.17.111", true, last},
		{"10.1.17.112", false, ip2geo.Record{}},
	})
	if id := f.recs[f.lookupID(netip.MustParseAddr("10.1.17.111"))].cityName; id != 4+3+n-1 {
		t.Errorf("last city name id %d, want %d", id, 4+3+n-1)
	}
	// Bucket 10.0/16 is full: 65,536 boundaries, then 10.1/16 holds the
	// rest and the closing gap.
	if f.v4idx[0x0a00] != 0 || f.v4idx[0x0a01] != 1<<16 || f.v4idx[0x0a02] != n+1 {
		t.Errorf("V4IDX[0a00..0a02] = %v, want [0 65536 %d]", f.v4idx[0x0a00:0x0a03], n+1)
	}
}

// TestGEO2TooManyU16Strings: 65,535 distinct non-empty strings in 16-bit
// fields fit (the last one at id 65,535); one more fails with
// ErrTooManyStrings before any output is written.
func TestGEO2TooManyU16Strings(t *testing.T) {
	t.Parallel()
	fixture := func(n int) string {
		fx := mmdbFixture{dbType: "GeoIP2-Country", languages: []string{"en"}}
		for i := range n {
			fx.networks = append(fx.networks, mmdbNet{seqV4(i),
				map[string]any{"country": map[string]any{"names": map[string]any{"en": fmt.Sprintf("c%05d", i)}}}})
		}
		return writeMMDB(t, fx)
	}

	t.Run("65535", func(t *testing.T) {
		t.Parallel()
		f, _ := buildGEO2(t, fixture(1<<16-1), mmdb.Options{BuildTime: fixedBuildTime})
		if f.u16Strings != 1<<16 || f.idWidth != 2 {
			t.Errorf("%d strings of 16-bit fields, idWidth %d; want 65536, 2", f.u16Strings, f.idWidth)
		}
		rec, ok := f.lookup(netip.MustParseAddr("10.0.255.254"))
		if id := f.recs[f.lookupID(netip.MustParseAddr("10.0.255.254"))].countryName; !ok || rec.CountryName != "c65534" || id != 1<<16-1 {
			t.Errorf("last network: %+v ok=%v, name id %d; want c65534 at id 65535", rec, ok, id)
		}
	})

	t.Run("65536", func(t *testing.T) {
		t.Parallel()
		src := fixture(1 << 16)
		dir := t.TempDir()
		out := filepath.Join(dir, "out.geoc")
		_, err := mmdb.Build(src, out, mmdb.Options{})
		if !errors.Is(err, mmdb.ErrTooManyStrings) || !strings.Contains(err.Error(), "65536 distinct strings") {
			t.Fatalf("Build: err = %v, want ErrTooManyStrings naming 65536 strings", err)
		}
		if ents, _ := os.ReadDir(dir); len(ents) != 0 {
			t.Errorf("Build created %v", ents)
		}
		if _, err := mmdb.Open(src, mmdb.Options{}); !errors.Is(err, mmdb.ErrTooManyStrings) {
			t.Errorf("Open: err = %v, want ErrTooManyStrings", err)
		}
	})
}

// TestGEO2V6Exceptions: IPv6 starts with non-zero low 64 bits go to the
// exception table, runs of equal top halves resolve by the step-back rule,
// and a file without IPv4 data has empty IPv4 sections.
func TestGEO2V6Exceptions(t *testing.T) {
	t.Parallel()
	src := writeMMDB(t, mmdbFixture{
		dbType: "GeoIP2-Country",
		networks: []mmdbNet{
			{"2001:db8:0:1::/65", countryOnly("AA")},
			{"2001:db8:0:1:8000::/66", countryOnly("BB")},
			{"2001:db8:0:2::1/128", countryOnly("CC")},
			{"2001:db8:0:3::/64", countryOnly("AA")},
		},
	})
	f, _ := buildGEO2(t, src, mmdb.Options{BuildTime: fixedBuildTime})

	want := "2001:db8:0:1::=1 2001:db8:0:1:8000::=2 2001:db8:0:1:c000::=0 2001:db8:0:2::1=3 2001:db8:0:2::2=0 " +
		"2001:db8:0:3::=1 2001:db8:0:4::=0"
	if got := f.v6Boundaries(); got != want {
		t.Errorf("IPv6 boundaries:\n got %s\nwant %s", got, want)
	}
	if !slices.Equal(f.excIdx, []uint32{1, 2, 3, 4}) ||
		!slices.Equal(f.excLo, []uint64{0x8000 << 48, 0xc000 << 48, 1, 2}) {
		t.Errorf("exceptions %v / %#x, want [1 2 3 4] / [0x8000<<48 0xc000<<48 1 2]", f.excIdx, f.excLo)
	}
	if got := sectionIDs(f); !slices.Equal(got, []uint32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}) {
		t.Errorf("sections %v, want ids 1 to 11", got)
	}
	if len(f.v4id) != 0 || len(f.sec[gsV4Starts]) != 0 || len(f.sec[gsV4IDs]) != 0 || f.v4idx[65536] != 0 {
		t.Errorf("IPv4: %d boundaries, want none", len(f.v4id))
	}

	a, b, c := ip2geo.Record{CountryISO: "AA"}, ip2geo.Record{CountryISO: "BB"}, ip2geo.Record{CountryISO: "CC"}
	checkParsedLookups(t, f, []lookupCase{
		{"2001:db8:0:0:ffff:ffff:ffff:ffff", false, ip2geo.Record{}},
		{"2001:db8:0:1::", true, a},
		{"2001:db8:0:1:7fff:ffff:ffff:ffff", true, a},
		{"2001:db8:0:1:8000::", true, b},
		{"2001:db8:0:1:bfff:ffff:ffff:ffff", true, b},
		{"2001:db8:0:1:c000::", false, ip2geo.Record{}},
		{"2001:db8:0:1:ffff:ffff:ffff:ffff", false, ip2geo.Record{}},
		{"2001:db8:0:2::", false, ip2geo.Record{}},
		{"2001:db8:0:2::1", true, c},
		{"2001:db8:0:2::2", false, ip2geo.Record{}},
		{"2001:db8:0:3::1", true, a},
		{"2001:db8:0:4::", false, ip2geo.Record{}},
		{"1.2.3.4", false, ip2geo.Record{}},
	})
}

// TestGEO2V4Buckets: V4IDX points at the first boundary at or above each
// /16, with no boundary added at bucket starts, for networks that start at
// 0.0.0.0, span buckets and end at the top of the address space; addresses
// in IPv6 form resolve through the IPv4 table.
func TestGEO2V4Buckets(t *testing.T) {
	t.Parallel()
	src := writeMMDB(t, mmdbFixture{
		dbType: "GeoIP2-Country",
		networks: []mmdbNet{
			{"0.0.0.0/8", countryOnly("XX")},
			{"1.0.0.0/15", countryOnly("YY")},
			{"1.2.3.4/32", countryOnly("ZZ")},
			{"255.255.255.0/24", countryOnly("XX")},
		},
	})
	f, _ := buildGEO2(t, src, mmdb.Options{BuildTime: fixedBuildTime})

	if got, want := f.v4Boundaries(), "0.0.0.0=1 1.0.0.0=2 1.2.0.0=0 1.2.3.4=3 1.2.3.5=0 255.255.255.0=1"; got != want {
		t.Errorf("IPv4 boundaries:\n got %s\nwant %s", got, want)
	}
	for g, want := range map[int]uint32{0: 0, 1: 1, 0x100: 1, 0x101: 2, 0x102: 2, 0x103: 5, 0xffff: 5, 0x10000: 6} {
		if f.v4idx[g] != want {
			t.Errorf("V4IDX[%#x] = %d, want %d", g, f.v4idx[g], want)
		}
	}
	if len(f.v6id) != 0 || len(f.dir) != 9 {
		t.Errorf("%d IPv6 boundaries, %d sections; want 0, 9", len(f.v6id), len(f.dir))
	}

	x, y, z := ip2geo.Record{CountryISO: "XX"}, ip2geo.Record{CountryISO: "YY"}, ip2geo.Record{CountryISO: "ZZ"}
	checkParsedLookups(t, f, []lookupCase{
		{"0.0.0.0", true, x},
		{"0.255.255.255", true, x},
		{"1.0.0.0", true, y},
		{"1.1.0.1", true, y}, // a bucket with no boundary of its own
		{"1.1.255.255", true, y},
		{"1.2.0.0", false, ip2geo.Record{}},
		{"1.2.3.3", false, ip2geo.Record{}},
		{"1.2.3.4", true, z},
		{"1.2.3.5", false, ip2geo.Record{}},
		{"128.0.0.0", false, ip2geo.Record{}},
		{"255.255.254.255", false, ip2geo.Record{}},
		{"255.255.255.0", true, x},
		{"255.255.255.255", true, x},
		{"::ffff:1.1.0.1", true, y},
		{"::1.1.0.1", true, y},
		{"2002:101:1::", true, y},                  // 6to4, bits 16-47
		{"2001:0:101:1::", true, y},                // Teredo server, bits 32-63
		{"64:ff9b::101:1", false, ip2geo.Record{}}, // NAT64 is not mapped
		{"2001:db8::1", false, ip2geo.Record{}},
	})
}

// TestGEO2Meta checks every META field against the build's inputs.
func TestGEO2Meta(t *testing.T) {
	t.Parallel()
	src := writeMMDB(t, cityFixture())

	bt := time.Date(2026, 10, 28, 13, 14, 15, 999_999_999, time.FixedZone("UTC+8", 8*3600))
	f, _ := buildGEO2(t, src, mmdb.Options{Profile: ip2geo.ProfileRegion, Lang: "de", Description: "fixture, 2026-10", BuildTime: bt})
	want := geo2Meta{
		buildTime: uint64(time.Date(2026, 10, 28, 5, 14, 15, 0, time.UTC).Unix()), sourceTime: 1700000000,
		networks: 6, sourceType: "GeoIP2-City", language: "de", description: "fixture, 2026-10",
		builder: f.meta.builder, distinctRecords: 6,
	}
	if f.meta != want {
		t.Errorf("META = %+v\n want %+v", f.meta, want)
	}
	if !strings.HasPrefix(f.meta.builder, "ip2geo/") {
		t.Errorf("builder %q, want ip2geo/<version>", f.meta.builder)
	}
	t.Logf("builder %q", f.meta.builder)

	// The zero BuildTime is the time of the build, in whole seconds.
	before := time.Now().Unix()
	f, _ = buildGEO2(t, src, mmdb.Options{})
	after := time.Now().Unix()
	if bt := int64(f.meta.buildTime); bt < before || bt > after {
		t.Errorf("BuildTime %d outside [%d, %d]", bt, before, after)
	}
	if f.meta.language != "en" || f.meta.description != "" {
		t.Errorf("default language %q, description %q; want en and empty", f.meta.language, f.meta.description)
	}
}

// TestGEO2Deterministic: builds with the same BuildTime are byte-identical,
// and a different BuildTime changes only META's build time and its CRC.
func TestGEO2Deterministic(t *testing.T) {
	t.Parallel()
	src := writeMMDB(t, cityFixture())
	for _, p := range profiles {
		t.Run(p.String(), func(t *testing.T) {
			t.Parallel()
			opts := mmdb.Options{Profile: p, BuildTime: fixedBuildTime}
			f, a := buildGEO2(t, src, opts)
			if _, b := buildGEO2(t, src, opts); !bytes.Equal(a, b) {
				t.Fatal("two builds with the same BuildTime differ")
			}

			opts.BuildTime = fixedBuildTime.Add(time.Second)
			_, c := buildGEO2(t, src, opts)
			if len(c) != len(a) {
				t.Fatalf("a later BuildTime changed the size: %d -> %d", len(a), len(c))
			}
			metaOff := int(f.dir[0].off)
			for i := range a {
				inCRC := i >= 32+4 && i < 32+8 // directory entry 0 is META
				inTime := i >= metaOff && i < metaOff+8
				if a[i] != c[i] && !inCRC && !inTime {
					t.Errorf("a later BuildTime changed byte %d", i)
				}
			}
		})
	}
}

// TestGEO2ParserRejects keeps the parser honest: it must notice a changed
// byte in any section, a truncated file, a trailing byte and broken
// padding.
func TestGEO2ParserRejects(t *testing.T) {
	t.Parallel()
	f, b := buildGEO2(t, writeMMDB(t, cityFixture()), mmdb.Options{Profile: ip2geo.ProfileCity, BuildTime: fixedBuildTime})
	mutate := func(name string, m func([]byte) []byte) {
		t.Helper()
		if _, err := parseGEO2(m(bytes.Clone(b))); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
	for _, s := range f.dir {
		if s.length > 0 {
			mutate(geo2SectionNames[s.id]+" byte", func(c []byte) []byte { c[s.off+s.length/2] ^= 1; return c })
		}
	}
	mutate("truncated", func(c []byte) []byte { return c[:len(c)-1] })
	mutate("trailing byte", func(c []byte) []byte { return append(c, 0) })
	mutate("header reserved", func(c []byte) []byte { c[20] = 1; return c })
	mutate("idWidth", func(c []byte) []byte { c[7] = 2; return c })
	padded := false
	for i, s := range f.dir[:len(f.dir)-1] {
		if pad := s.off + s.length; pad < f.dir[i+1].off {
			mutate("padding after "+geo2SectionNames[s.id], func(c []byte) []byte { c[pad] = 1; return c })
			padded = true
		}
	}
	if !padded {
		t.Error("the fixture's file has no padding to check")
	}
}

// TestBuildTimeAndDescriptionLimits: the extreme valid values are stored
// as given (TestOptionsValidation covers the invalid ones).
func TestBuildTimeAndDescriptionLimits(t *testing.T) {
	t.Parallel()
	desc := strings.Repeat("é", (1<<16-1)/2) // 65,534 bytes
	f, _ := buildGEO2(t, writeMMDB(t, cityFixture()), mmdb.Options{BuildTime: time.Unix(0, 0), Description: desc})
	if f.meta.buildTime != 0 || f.meta.description != desc {
		t.Errorf("BuildTime %d, description of %d bytes; want 0 and %d", f.meta.buildTime, len(f.meta.description), len(desc))
	}
}

// envGEOC names .geoc files, separated by the OS path list separator, for
// TestGEO2Files. When it is unset the test skips.
const envGEOC = "IP2GEO_TEST_GEOC"

// TestGEO2Files parses real .geoc files, for example builds of a licensed
// source, with every check of parseGEO2 and logs their layout. With
// IP2GEO_TEST_MMDB naming the source they were built from (with the default
// Lang and LatScale), it also compares lookups through the parser with
// maxminddb on the address sets of TestBuildMatchesSource:
//
//	IP2GEO_TEST_GEOC=country.geoc:city.geoc IP2GEO_TEST_MMDB=GeoIP2-City.mmdb \
//		go test -count=1 -run TestGEO2Files -v ./mmdb/
func TestGEO2Files(t *testing.T) {
	list := os.Getenv(envGEOC)
	if list == "" {
		t.Skipf("set %s to .geoc files to check", envGEOC)
	}
	var sets []addrSet
	var info *sourceInfo
	src := os.Getenv(envMMDB)
	if src != "" {
		info = scanSource(t, src)
		sets = paritySets(info)
	}
	for _, path := range filepath.SplitList(list) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			f, err := parseGEO2(b)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			t.Logf("%s: %d bytes, profile %s, latScale %d, idWidth %d, parsed and checked in %v",
				path, len(b), f.profile, f.latScale, f.idWidth, time.Since(start).Round(time.Millisecond))
			t.Logf("counts: %d strings (%d of 16-bit fields), %d records, %d IPv4 and %d IPv6 boundaries, %d exceptions",
				len(f.strs), f.u16Strings, len(f.recs), len(f.v4id), len(f.v6id), len(f.excIdx))
			t.Logf("META: %+v (build time %s)", f.meta, time.Unix(int64(f.meta.buildTime), 0).UTC().Format(time.RFC3339))
			for _, s := range f.dir {
				t.Logf("  %-9s id %2d  offset %10d  length %10d  crc %08x", geo2SectionNames[s.id], s.id, s.off, s.length, s.crc)
			}
			if info == nil {
				return
			}
			mm := openMaxMind(t, src)
			var addrs, bad int
			for _, set := range sets {
				for _, a := range set.addrs {
					res := mm.Lookup(a)
					var s sourceRecord
					if err := res.Decode(&s); err != nil {
						t.Fatalf("maxminddb lookup %s: %v", a, err)
					}
					want := expected(&s, f.profile, info.lang)
					rec, ok := f.lookup(a)
					addrs++
					if ok == res.Found() && actual(rec) == want {
						continue
					}
					if bad++; bad <= maxReported {
						t.Errorf("%s %s (maxminddb network %s):\n  maxminddb found=%v %+v\n  parsed    ok=%v    %+v",
							set.name, a, res.Prefix(), res.Found(), want, ok, actual(rec))
					}
				}
			}
			t.Logf("%d addresses in %d sets compared with maxminddb, %d mismatches", addrs, len(sets), bad)
			if bad > 0 {
				t.Errorf("%d of %d addresses differ from maxminddb", bad, addrs)
			}
		})
	}
}
