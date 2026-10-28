package mmdb_test

import (
	"bytes"
	"errors"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/mmdb"
	maxminddb "github.com/oschwald/maxminddb-golang/v2"
)

// names returns a MaxMind "names" map.
func names(en, de string) map[string]any { return map[string]any{"en": en, "de": de} }

// cityData returns a GeoIP2-City style record.
func cityData(continent, iso string, geo uint32, country, countryDE, region, regionName string,
	cityGeo uint32, city string, lat, lon float64, acc uint16, tz string) map[string]any {
	return map[string]any{
		"continent":          map[string]any{"code": continent, "geoname_id": uint32(6255148), "names": names(continent, continent)},
		"country":            map[string]any{"iso_code": iso, "geoname_id": geo, "names": names(country, countryDE)},
		"registered_country": map[string]any{"iso_code": iso, "geoname_id": geo, "names": names(country, countryDE)},
		"subdivisions": []any{
			map[string]any{"iso_code": region, "geoname_id": uint32(1), "names": names(regionName, regionName)},
			map[string]any{"iso_code": "XX", "geoname_id": uint32(2), "names": names("Second level", "Zweite Ebene")},
		},
		"city":     map[string]any{"geoname_id": cityGeo, "names": names(city, city+" (de)")},
		"location": map[string]any{"latitude": lat, "longitude": lon, "accuracy_radius": acc, "time_zone": tz},
		"postal":   map[string]any{"code": "SW19"},
	}
}

// registeredOnly is a network that has data but none of the fields any
// profile keeps (audit finding 2.2: e.g. 23.167.144.0/20 in GeoIP2-City).
func registeredOnly(iso string, geo uint32) map[string]any {
	return map[string]any{
		"registered_country": map[string]any{"iso_code": iso, "geoname_id": geo},
		"traits":             map[string]any{"is_anonymous_proxy": true},
	}
}

func cityFixture() mmdbFixture {
	return mmdbFixture{
		dbType:    "GeoIP2-City",
		languages: []string{"de", "en"},
		aliasV4:   true,
		networks: []mmdbNet{
			{"1.0.0.0/24", cityData("EU", "GB", 2635167, "United Kingdom", "Vereinigtes Königreich", "ENG", "England",
				2634032, "Wimbledon", 51.4248, -0.2093, 20, "Europe/London")},
			{"1.0.1.0/24", cityData("EU", "GB", 2635167, "United Kingdom", "Vereinigtes Königreich", "ENG", "England",
				2643743, "London", 51.5085, -0.1257, 50, "Europe/London")},
			{"1.0.2.0/24", registeredOnly("US", 6252001)},
			// 1.0.3.0/24 is a gap.
			{"1.0.4.0/22", cityData("EU", "DE", 2921044, "Germany", "Deutschland", "BY", "Bavaria",
				2867714, "Munich", 48.1374, 11.5755, 10, "Europe/Berlin")},
			{"2001:db8::/32", cityData("AS", "JP", 1861060, "Japan", "Japan", "13", "Tokyo",
				1850147, "Tokyo", 35.6895, 139.6917, 100, "Asia/Tokyo")},
			{"2001:dc0::/32", registeredOnly("JP", 1861060)},
		},
	}
}

var (
	recWimbledon = ip2geo.Record{CountryISO: "GB", CountryName: "United Kingdom", Continent: "EU", CountryGeoNameID: 2635167,
		RegionISO: "ENG", RegionName: "England", City: "Wimbledon", CityGeoNameID: 2634032,
		Latitude: 51.4248, Longitude: -0.2093, AccuracyRadius: 20, TimeZone: "Europe/London"}
	recLondon = ip2geo.Record{CountryISO: "GB", CountryName: "United Kingdom", Continent: "EU", CountryGeoNameID: 2635167,
		RegionISO: "ENG", RegionName: "England", City: "London", CityGeoNameID: 2643743,
		Latitude: 51.5085, Longitude: -0.1257, AccuracyRadius: 50, TimeZone: "Europe/London"}
	recMunich = ip2geo.Record{CountryISO: "DE", CountryName: "Germany", Continent: "EU", CountryGeoNameID: 2921044,
		RegionISO: "BY", RegionName: "Bavaria", City: "Munich", CityGeoNameID: 2867714,
		Latitude: 48.1374, Longitude: 11.5755, AccuracyRadius: 10, TimeZone: "Europe/Berlin"}
	recTokyo = ip2geo.Record{CountryISO: "JP", CountryName: "Japan", Continent: "AS", CountryGeoNameID: 1861060,
		RegionISO: "13", RegionName: "Tokyo", City: "Tokyo", CityGeoNameID: 1850147,
		Latitude: 35.6895, Longitude: 139.6917, AccuracyRadius: 100, TimeZone: "Asia/Tokyo"}
)

// lookupCase is an address and the full (city profile) record expected for it.
type lookupCase struct {
	ip    string
	found bool
	want  ip2geo.Record
}

var cityLookups = []lookupCase{
	{"0.255.255.255", false, ip2geo.Record{}},
	{"1.0.0.0", true, recWimbledon},
	{"1.0.0.255", true, recWimbledon},
	{"::ffff:1.0.0.1", true, recWimbledon},
	{"1.0.1.200", true, recLondon},
	{"1.0.2.0", true, ip2geo.Record{}}, // found, but no kept field
	{"1.0.2.255", true, ip2geo.Record{}},
	{"1.0.3.1", false, ip2geo.Record{}}, // gap
	{"1.0.4.0", true, recMunich},
	{"1.0.7.255", true, recMunich},
	{"1.0.8.0", false, ip2geo.Record{}},
	{"255.255.255.255", false, ip2geo.Record{}},
	{"2001:db7:ffff::", false, ip2geo.Record{}},
	{"2001:db8::1", true, recTokyo},
	{"2001:db8:ffff:ffff:ffff:ffff:ffff:ffff", true, recTokyo},
	{"2001:db9::", false, ip2geo.Record{}},
	{"2001:dc0::1", true, ip2geo.Record{}}, // found, but no kept field
	{"2001:dc1::", false, ip2geo.Record{}},
	{"ffff::1", false, ip2geo.Record{}},
}

// project zeroes the fields a profile does not keep.
func project(r ip2geo.Record, p ip2geo.Profile) ip2geo.Record {
	if p < ip2geo.ProfileCity {
		r.City, r.CityGeoNameID, r.Latitude, r.Longitude, r.AccuracyRadius, r.TimeZone = "", 0, 0, 0, 0, ""
	}
	if p < ip2geo.ProfileRegion {
		r.RegionISO, r.RegionName = "", ""
	}
	return r
}

func checkLookups(t *testing.T, db *ip2geo.Reader, p ip2geo.Profile, cases []lookupCase) {
	t.Helper()
	for _, c := range cases {
		rec, ok := db.Lookup(netip.MustParseAddr(c.ip))
		if ok != c.found {
			t.Errorf("%s: ok = %v, want %v (record %+v)", c.ip, ok, c.found, rec)
			continue
		}
		if want := project(c.want, p); rec != want {
			t.Errorf("%s:\n got %+v\nwant %+v", c.ip, rec, want)
		}
	}
}

var profiles = []ip2geo.Profile{ip2geo.ProfileCountry, ip2geo.ProfileRegion, ip2geo.ProfileCity}

// TestFixtureReadableByMaxminddb checks the hand-encoded fixture against the
// real maxminddb reader, so the builder tests below rest on a valid file.
func TestFixtureReadableByMaxminddb(t *testing.T) {
	t.Parallel()
	src := writeMMDB(t, cityFixture())
	db, err := maxminddb.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if db.Metadata.DatabaseType != "GeoIP2-City" || strings.Join(db.Metadata.Languages, ",") != "de,en" {
		t.Errorf("metadata = %+v", db.Metadata)
	}

	var got []string
	for res := range db.Networks() {
		var v map[string]any
		if err := res.Decode(&v); err != nil {
			t.Fatalf("%s: %v", res.Prefix(), err)
		}
		got = append(got, res.Prefix().String())
	}
	want := "1.0.0.0/24 1.0.1.0/24 1.0.2.0/24 1.0.4.0/22 2001:db8::/32 2001:dc0::/32"
	if strings.Join(got, " ") != want {
		t.Errorf("Networks() = %v, want %s (the ::ffff:0:0/96 alias must be skipped)", got, want)
	}

	for _, c := range []struct {
		ip    string
		found bool
		iso   string
	}{
		{"1.0.0.1", true, "GB"}, {"::ffff:1.0.0.1", true, "GB"}, {"1.0.2.1", true, ""},
		{"1.0.3.1", false, ""}, {"2001:db8::1", true, "JP"}, {"2001:db9::1", false, ""},
	} {
		res := db.Lookup(netip.MustParseAddr(c.ip))
		var v struct {
			Country struct {
				ISOCode string `maxminddb:"iso_code"`
			} `maxminddb:"country"`
		}
		if err := res.Decode(&v); err != nil {
			t.Fatalf("%s: %v", c.ip, err)
		}
		if res.Found() != c.found || v.Country.ISOCode != c.iso {
			t.Errorf("%s: found=%v iso=%q, want %v %q", c.ip, res.Found(), v.Country.ISOCode, c.found, c.iso)
		}
	}
}

// TestBuildRoundTrip builds each profile with Build and with Open and checks
// every lookup and the stats.
func TestBuildRoundTrip(t *testing.T) {
	t.Parallel()
	src := writeMMDB(t, cityFixture())
	// Every network of the fixture has its own data record: DistinctRecords 6.
	wantStats := map[ip2geo.Profile]mmdb.Stats{
		// records: gap + {GB, empty, DE, JP}; v4: GB, empty, gap, DE, gap.
		ip2geo.ProfileCountry: {Profile: ip2geo.ProfileCountry, Networks: 6, DistinctRecords: 6, Records: 5, V4Ranges: 5, V6Ranges: 4},
		ip2geo.ProfileRegion:  {Profile: ip2geo.ProfileRegion, Networks: 6, DistinctRecords: 6, Records: 5, V4Ranges: 5, V6Ranges: 4},
		// records: gap + {Wimbledon, London, empty, Munich, Tokyo}.
		ip2geo.ProfileCity: {Profile: ip2geo.ProfileCity, Networks: 6, DistinctRecords: 6, Records: 6, V4Ranges: 6, V6Ranges: 4},
	}
	for _, p := range profiles {
		t.Run(p.String(), func(t *testing.T) {
			t.Parallel()
			out := filepath.Join(t.TempDir(), "out.geoc")
			st, err := mmdb.Build(src, out, mmdb.Options{Profile: p})
			if err != nil {
				t.Fatal(err)
			}
			fi, err := os.Stat(out)
			if err != nil {
				t.Fatal(err)
			}
			want := wantStats[p]
			want.Strings, want.Bytes = st.Strings, fi.Size()
			if *st != want {
				t.Errorf("stats = %+v, want %+v", *st, want)
			}

			db, err := ip2geo.Open(out)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if db.Profile() != p {
				t.Errorf("Profile() = %v, want %v", db.Profile(), p)
			}
			checkLookups(t, db, p, cityLookups)

			mem, err := mmdb.Open(src, mmdb.Options{Profile: p})
			if err != nil {
				t.Fatal(err)
			}
			defer mem.Close()
			checkLookups(t, mem, p, cityLookups)
		})
	}
}

// TestDistinctRecordsCountsOffsets: networks that share a data record are
// decoded once, so Stats.DistinctRecords counts data offsets, not networks
// and not builder records.
func TestDistinctRecordsCountsOffsets(t *testing.T) {
	t.Parallel()
	london := func(postal string) map[string]any {
		d := cityData("EU", "GB", 2635167, "United Kingdom", "Vereinigtes Königreich", "ENG", "England",
			2643743, "London", 51.5085, -0.1257, 50, "Europe/London")
		d["postal"] = map[string]any{"code": postal}
		return d
	}
	src := writeMMDB(t, mmdbFixture{
		dbType:    "GeoIP2-City",
		languages: []string{"de", "en"},
		aliasV4:   true,
		networks: []mmdbNet{
			// Equal data is stored once: three networks, one data record.
			{"1.0.0.0/24", london("SW1")},
			{"1.0.1.0/24", london("SW1")},
			{"2001:db8::/32", london("SW1")},
			// A second data record that differs only in a field no
			// profile keeps: it becomes the same builder record.
			{"1.0.4.0/24", london("EC1")},
			{"1.0.5.0/24", registeredOnly("US", 6252001)},
		},
	})

	// The fixture really shares offsets: 5 networks, 3 data records.
	mdb, err := maxminddb.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	offsets := map[uintptr]bool{}
	networks := 0
	for res := range mdb.Networks() {
		networks++
		offsets[res.Offset()] = true
	}
	mdb.Close()
	if networks != 5 || len(offsets) != 3 {
		t.Fatalf("fixture: %d networks, %d distinct offsets; want 5 and 3", networks, len(offsets))
	}

	for _, p := range profiles {
		st, err := mmdb.Build(src, filepath.Join(t.TempDir(), "out.geoc"), mmdb.Options{Profile: p})
		if err != nil {
			t.Fatal(err)
		}
		// records: gap + London + found-but-empty.
		if st.Networks != 5 || st.DistinctRecords != 3 || st.Records != 3 {
			t.Errorf("%v: networks=%d distinct=%d records=%d, want 5, 3, 3", p, st.Networks, st.DistinctRecords, st.Records)
		}
		if !strings.Contains(st.String(), " distinct=3 ") {
			t.Errorf("%v: Stats.String() = %q, want distinct=3", p, st)
		}
		db, err := mmdb.Open(src, mmdb.Options{Profile: p})
		if err != nil {
			t.Fatal(err)
		}
		checkLookups(t, db, p, []lookupCase{
			{"1.0.0.1", true, recLondon}, {"1.0.1.255", true, recLondon}, {"2001:db8::1", true, recLondon},
			{"1.0.4.1", true, recLondon}, {"1.0.5.1", true, ip2geo.Record{}}, {"1.0.2.1", false, ip2geo.Record{}},
		})
		db.Close()
	}
}

// TestNamesInOneLanguage: names are decoded in Options.Lang only, and only
// for the first subdivision. The stored values must equal what a full decode
// of the source gives, for each language, including names missing in it.
func TestNamesInOneLanguage(t *testing.T) {
	t.Parallel()
	langs := []string{"de", "en", "fr", "ja", "zh-CN"}
	multi := func(name string) map[string]any {
		return map[string]any{"de": name + " (de)", "en": name, "ja": name + " (ja)", "zh-CN": name + " (zh)"}
	}
	paris := map[string]any{
		"continent": map[string]any{"code": "EU", "names": multi("Europe")},
		"country":   map[string]any{"iso_code": "FR", "geoname_id": uint32(3017382), "names": multi("France")},
		// Only en and fr: de, ja and zh-CN are missing.
		"city": map[string]any{"geoname_id": uint32(2988507), "names": map[string]any{"en": "Paris", "fr": "Paris (fr)"}},
		"subdivisions": []any{
			map[string]any{"iso_code": "IDF", "geoname_id": uint32(3012874), "names": multi("Ile-de-France")},
			map[string]any{"iso_code": "75", "geoname_id": uint32(2968815), "names": multi("Paris department")},
		},
		"location": map[string]any{"latitude": 48.8534, "longitude": 2.3488, "accuracy_radius": uint16(5), "time_zone": "Europe/Paris"},
	}
	noRegion := map[string]any{
		"country":      map[string]any{"iso_code": "MC", "geoname_id": uint32(2993457), "names": map[string]any{"fr": "Monaco (fr)"}},
		"subdivisions": []any{},
	}
	src := writeMMDB(t, mmdbFixture{
		dbType:    "GeoIP2-City",
		languages: langs,
		networks: []mmdbNet{
			{"1.0.0.0/24", paris},
			{"1.0.1.0/24", noRegion},
			{"2001:db8::/32", paris},
		},
	})
	mdb, err := maxminddb.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	for _, lang := range langs {
		db, err := mmdb.Open(src, mmdb.Options{Profile: ip2geo.ProfileCity, Lang: lang})
		if err != nil {
			t.Fatal(err)
		}
		for _, ip := range []string{"1.0.0.1", "1.0.1.1", "2001:db8::1"} {
			addr := netip.MustParseAddr(ip)
			var full struct {
				Country struct {
					Names map[string]string `maxminddb:"names"`
				} `maxminddb:"country"`
				City struct {
					Names map[string]string `maxminddb:"names"`
				} `maxminddb:"city"`
				Subdivisions []struct {
					ISOCode string            `maxminddb:"iso_code"`
					Names   map[string]string `maxminddb:"names"`
				} `maxminddb:"subdivisions"`
			}
			if err := mdb.Lookup(addr).Decode(&full); err != nil {
				t.Fatal(err)
			}
			var regionISO, regionName string
			if len(full.Subdivisions) > 0 {
				regionISO, regionName = full.Subdivisions[0].ISOCode, full.Subdivisions[0].Names[lang]
			}
			rec, ok := db.Lookup(addr)
			if !ok || rec.CountryName != full.Country.Names[lang] || rec.City != full.City.Names[lang] ||
				rec.RegionISO != regionISO || rec.RegionName != regionName {
				t.Errorf("Lang %s, %s: got %q/%q/%q/%q ok=%v, want %q/%q/%q/%q", lang, ip,
					rec.CountryName, rec.City, rec.RegionISO, rec.RegionName, ok,
					full.Country.Names[lang], full.City.Names[lang], regionISO, regionName)
			}
		}
		db.Close()
	}
}

// TestFoundButEmptyNetwork: a network with data but none of the kept fields
// is found with an empty record; a real gap is not found (audit 2.2 / 8.6).
func TestFoundButEmptyNetwork(t *testing.T) {
	t.Parallel()
	src := writeMMDB(t, cityFixture())
	for _, p := range profiles {
		db, err := mmdb.Open(src, mmdb.Options{Profile: p})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			ip    string
			found bool
		}{
			{"1.0.2.77", true}, {"::ffff:1.0.2.77", true}, {"2001:dc0::77", true},
			{"1.0.3.77", false}, {"::ffff:1.0.3.77", false}, {"2001:db9::77", false},
		} {
			rec, ok := db.Lookup(netip.MustParseAddr(c.ip))
			if ok != c.found || rec != (ip2geo.Record{}) {
				t.Errorf("%v %s: got %+v ok=%v, want empty record ok=%v", p, c.ip, rec, ok, c.found)
			}
		}
		db.Close()
	}
}

func TestLanguage(t *testing.T) {
	t.Parallel()
	src := writeMMDB(t, cityFixture())

	db, err := mmdb.Open(src, mmdb.Options{Profile: ip2geo.ProfileCity, Lang: "de"})
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := db.Lookup(netip.MustParseAddr("1.0.0.1"))
	if !ok || rec.CountryName != "Vereinigtes Königreich" || rec.City != "Wimbledon (de)" {
		t.Errorf("Lang de: %+v ok=%v", rec, ok)
	}
	db.Close()

	_, err = mmdb.Open(src, mmdb.Options{Lang: "xx"})
	if !errors.Is(err, mmdb.ErrLanguage) {
		t.Fatalf("Lang xx: err = %v, want ErrLanguage", err)
	}
	if !strings.Contains(err.Error(), `"xx"`) || !strings.Contains(err.Error(), "de, en") {
		t.Errorf("Lang xx: error %q should name the language and list the available ones", err)
	}

	for _, langs := range [][]string{{"xx"}, {"en", "xx"}, {"de", "en", "xx"}} {
		_, err = mmdb.Open(src, mmdb.Options{Profile: ip2geo.ProfileCity, Languages: langs})
		if !errors.Is(err, mmdb.ErrLanguage) || !strings.Contains(err.Error(), `"xx"`) {
			t.Errorf("Languages %q: err = %v, want ErrLanguage naming \"xx\"", langs, err)
		}
	}

	// Without a languages list in the metadata there is nothing to check.
	fx := cityFixture()
	fx.languages = nil
	db, err = mmdb.Open(writeMMDB(t, fx), mmdb.Options{Lang: "xx"})
	if err != nil {
		t.Fatalf("no languages metadata, Lang xx: %v", err)
	}
	rec, ok = db.Lookup(netip.MustParseAddr("1.0.0.1"))
	if !ok || rec.CountryISO != "GB" || rec.CountryName != "" {
		t.Errorf("no languages metadata, Lang xx: %+v ok=%v", rec, ok)
	}
	db.Close()
}

func TestDatabaseType(t *testing.T) {
	t.Parallel()
	asn := writeMMDB(t, mmdbFixture{
		dbType: "GeoLite2-ASN",
		networks: []mmdbNet{{"8.0.0.0/8", map[string]any{
			"autonomous_system_number": uint32(15169), "autonomous_system_organization": "Google LLC"}}},
	})
	tests := []struct {
		dbType  string
		profile ip2geo.Profile
		wantErr string // substring of the error; "" means success
	}{
		{"GeoIP2-City", ip2geo.ProfileCity, ""},
		{"GeoLite2-City", ip2geo.ProfileRegion, ""},
		{"DBIP-City-Lite", ip2geo.ProfileCity, ""},
		{"GeoIP2-Enterprise", ip2geo.ProfileCity, ""},
		{"GeoIP2-Country", ip2geo.ProfileCountry, ""},
		{"DBIP-Country-Lite", ip2geo.ProfileCountry, ""},
		{"GeoLite2-Country", ip2geo.ProfileRegion, "profile region needs a City database"},
		{"GeoIP2-Country", ip2geo.ProfileCity, "profile city needs a City database"},
		{"GeoLite2-ASN", ip2geo.ProfileCountry, `database type "GeoLite2-ASN"`},
		{"GeoIP2-ISP", ip2geo.ProfileCountry, `database type "GeoIP2-ISP"`},
		{"GeoIP2-Anonymous-IP", ip2geo.ProfileCountry, `database type "GeoIP2-Anonymous-IP"`},
		{"", ip2geo.ProfileCountry, `database type ""`},
		{"GeoLite2-ASN", ip2geo.ProfileASN, ""},
		{"DBIP-ASN-Lite (compat=GeoLite2-ASN)", ip2geo.ProfileASN, ""},
		{"DBIP-ASN-Lite (compat=GeoLite2-ASN)", ip2geo.ProfileCity, "an ASN database; build it with profile asn"},
		{"GeoIP2-City", ip2geo.ProfileASN, "profile asn needs an ASN database"},
		{"GeoIP2-ISP", ip2geo.ProfileASN, "profile asn needs an ASN database"},
	}
	for _, tt := range tests {
		t.Run(tt.dbType+"/"+tt.profile.String(), func(t *testing.T) {
			t.Parallel()
			fx := cityFixture()
			fx.dbType = tt.dbType
			out := filepath.Join(t.TempDir(), "out.geoc")
			_, err := mmdb.Build(writeMMDB(t, fx), out, mmdb.Options{Profile: tt.profile})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, mmdb.ErrDatabaseType) || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want ErrDatabaseType containing %q", err, tt.wantErr)
			}
			if _, err := os.Stat(out); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("output exists after a failed build (stat err %v)", err)
			}
		})
	}

	t.Run("ASN data", func(t *testing.T) {
		t.Parallel()
		if _, err := mmdb.Open(asn, mmdb.Options{}); !errors.Is(err, mmdb.ErrDatabaseType) {
			t.Fatalf("Open ASN: err = %v, want ErrDatabaseType", err)
		}
		out := filepath.Join(t.TempDir(), "asn.geoc")
		if _, err := mmdb.Build(asn, out, mmdb.Options{}); !errors.Is(err, mmdb.ErrDatabaseType) {
			t.Fatalf("Build ASN: err = %v, want ErrDatabaseType", err)
		}
	})
}

// TestOptionsValidation: invalid options fail with ErrOptions before the
// source is opened and before any output is created.
func TestOptionsValidation(t *testing.T) {
	t.Parallel()
	src := writeMMDB(t, cityFixture())
	missing := filepath.Join(t.TempDir(), "missing.mmdb")
	tests := []struct {
		name string
		opts mmdb.Options
	}{
		{"LatScale 8", mmdb.Options{Profile: ip2geo.ProfileCity, LatScale: 8}},
		{"LatScale 255", mmdb.Options{Profile: ip2geo.ProfileCity, LatScale: 255}},
		{"LatScale 8 country", mmdb.Options{LatScale: 8}},
		{"Profile 4", mmdb.Options{Profile: ip2geo.Profile(4)}},
		{"Profile 255", mmdb.Options{Profile: ip2geo.Profile(255)}},
		{"BuildTime before 1970", mmdb.Options{BuildTime: time.Date(1969, 12, 31, 23, 59, 59, 0, time.UTC)}},
		{"Description not UTF-8", mmdb.Options{Description: "caf\xe9"}},
		{"Description too long", mmdb.Options{Description: strings.Repeat("d", 1<<16)}},
		{"Lang and Languages", mmdb.Options{Lang: "en", Languages: []string{"en"}}},
		{"Languages empty language", mmdb.Options{Languages: []string{"en", ""}}},
		{"Languages repeated", mmdb.Options{Languages: []string{"en", "de", "en"}}},
		{"Fields subdivisions country", mmdb.Options{Fields: ip2geo.FieldSubdivisions}},
		{"Fields postal region", mmdb.Options{Profile: ip2geo.ProfileRegion, Fields: ip2geo.FieldPostal}},
		{"Fields metro region", mmdb.Options{Profile: ip2geo.ProfileRegion, Fields: ip2geo.FieldEU | ip2geo.FieldMetro}},
		{"Fields location country", mmdb.Options{Fields: ip2geo.FieldLocation}},
		{"Fields unknown bit", mmdb.Options{Profile: ip2geo.ProfileCity, Fields: 1 << 8}},
		{"Fields unknown bit and all", mmdb.Options{Profile: ip2geo.ProfileCity, Fields: ip2geo.FieldsAll | 1<<8}},
		{"Fields asn", mmdb.Options{Profile: ip2geo.ProfileASN, Fields: ip2geo.FieldEU}},
		{"Languages asn", mmdb.Options{Profile: ip2geo.ProfileASN, Languages: []string{"en", "de"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, s := range []string{src, missing} {
				dir := t.TempDir()
				out := filepath.Join(dir, "out.geoc")
				if _, err := mmdb.Build(s, out, tt.opts); !errors.Is(err, mmdb.ErrOptions) {
					t.Errorf("Build(%s): err = %v, want ErrOptions", filepath.Base(s), err)
				}
				if ents, _ := os.ReadDir(dir); len(ents) != 0 {
					t.Errorf("Build(%s) created %v", filepath.Base(s), ents)
				}
				if _, err := mmdb.Open(s, tt.opts); !errors.Is(err, mmdb.ErrOptions) {
					t.Errorf("Open(%s): err = %v, want ErrOptions", filepath.Base(s), err)
				}
			}
		})
	}
}

func TestLatScale(t *testing.T) {
	t.Parallel()
	src := writeMMDB(t, cityFixture())
	for _, tt := range []struct {
		scale    uint8
		lat, lon float64
	}{
		{0, 51.4248, -0.2093}, // 0 means DefaultLatScale (4)
		{1, 51.4, -0.2},
		{4, 51.4248, -0.2093},
		{mmdb.MaxLatScale, 51.4248, -0.2093},
	} {
		db, err := mmdb.Open(src, mmdb.Options{Profile: ip2geo.ProfileCity, LatScale: tt.scale})
		if err != nil {
			t.Fatalf("LatScale %d: %v", tt.scale, err)
		}
		rec, _ := db.Lookup(netip.MustParseAddr("1.0.0.1"))
		if rec.Latitude != tt.lat || rec.Longitude != tt.lon {
			t.Errorf("LatScale %d: (%v, %v), want (%v, %v)", tt.scale, rec.Latitude, rec.Longitude, tt.lat, tt.lon)
		}
		db.Close()
	}
}

func TestStringTooLong(t *testing.T) {
	t.Parallel()
	withName := func(n int) string {
		fx := cityFixture()
		fx.networks = fx.networks[:1]
		fx.networks[0].data["country"].(map[string]any)["names"] = map[string]any{"en": strings.Repeat("x", n)}
		return writeMMDB(t, fx)
	}

	src := withName(1 << 16)
	if _, err := mmdb.Open(src, mmdb.Options{}); !errors.Is(err, mmdb.ErrStringTooLong) {
		t.Fatalf("Open: err = %v, want ErrStringTooLong", err)
	}
	out := filepath.Join(t.TempDir(), "out.geoc")
	if _, err := mmdb.Build(src, out, mmdb.Options{}); !errors.Is(err, mmdb.ErrStringTooLong) {
		t.Fatalf("Build: err = %v, want ErrStringTooLong", err)
	}

	db, err := mmdb.Open(withName(1<<16-1), mmdb.Options{})
	if err != nil {
		t.Fatalf("65535-byte name: %v", err)
	}
	defer db.Close()
	if rec, _ := db.Lookup(netip.MustParseAddr("1.0.0.1")); len(rec.CountryName) != 1<<16-1 {
		t.Errorf("65535-byte name: got %d bytes", len(rec.CountryName))
	}
}

func TestMissingSource(t *testing.T) {
	t.Parallel()
	src := filepath.Join(t.TempDir(), "missing.mmdb")
	_, err := mmdb.Open(src, mmdb.Options{})
	if !errors.Is(err, fs.ErrNotExist) || !strings.HasPrefix(err.Error(), "mmdb: open ") {
		t.Fatalf("err = %v, want a wrapped fs.ErrNotExist starting with \"mmdb: open \"", err)
	}
}

// tempFiles returns the leftover Build temporary files in dir.
func tempFiles(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, ".geoc-*"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestBuildFailureKeepsOutput: a failed rebuild leaves the previous file
// byte-identical and no temporary file behind.
func TestBuildFailureKeepsOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out := filepath.Join(dir, "city.geoc")
	good := writeMMDB(t, cityFixture())
	if _, err := mmdb.Build(good, out, mmdb.Options{Profile: ip2geo.ProfileCity}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}

	asn := mmdbFixture{dbType: "GeoLite2-ASN", networks: []mmdbNet{{"8.0.0.0/8", map[string]any{"autonomous_system_number": uint32(1)}}}}
	long := cityFixture()
	long.networks[0].data["city"].(map[string]any)["names"] = map[string]any{"en": strings.Repeat("y", 70000)}
	for _, tt := range []struct {
		name string
		src  string
		opts mmdb.Options
		want error
	}{
		{"database type", writeMMDB(t, asn), mmdb.Options{Profile: ip2geo.ProfileCity}, mmdb.ErrDatabaseType},
		{"language", good, mmdb.Options{Profile: ip2geo.ProfileCity, Lang: "xx"}, mmdb.ErrLanguage},
		{"latscale", good, mmdb.Options{Profile: ip2geo.ProfileCity, LatScale: 8}, mmdb.ErrOptions},
		{"profile", good, mmdb.Options{Profile: 4}, mmdb.ErrOptions},
		{"string too long", writeMMDB(t, long), mmdb.Options{Profile: ip2geo.ProfileCity}, mmdb.ErrStringTooLong},
		{"missing source", filepath.Join(dir, "missing.mmdb"), mmdb.Options{}, fs.ErrNotExist},
	} {
		if _, err := mmdb.Build(tt.src, out, tt.opts); !errors.Is(err, tt.want) {
			t.Errorf("%s: err = %v, want %v", tt.name, err, tt.want)
		}
		after, err := os.ReadFile(out)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if !bytes.Equal(after, before) {
			t.Errorf("%s: output changed by a failed build", tt.name)
		}
		if m := tempFiles(t, dir); len(m) != 0 {
			t.Errorf("%s: temporary files left: %v", tt.name, m)
		}
	}
}

// TestBuildWriteFailureCleansUp makes the final rename fail (outPath is a
// directory) after the whole file was written: the temporary file must go.
func TestBuildWriteFailureCleansUp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out := filepath.Join(dir, "city.geoc")
	if err := os.MkdirAll(filepath.Join(out, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := mmdb.Build(writeMMDB(t, cityFixture()), out, mmdb.Options{})
	if err == nil || !strings.HasPrefix(err.Error(), "mmdb: replace ") {
		t.Fatalf("err = %v, want a replace error", err)
	}
	if m := tempFiles(t, dir); len(m) != 0 {
		t.Errorf("temporary files left: %v", m)
	}
	if _, err := os.Stat(filepath.Join(out, "keep")); err != nil {
		t.Errorf("directory at outPath was disturbed: %v", err)
	}
}

// TestBuildReplacesOutput: a successful build replaces an existing file,
// keeps its permission bits, and leaves no temporary file.
func TestBuildReplacesOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := writeMMDB(t, cityFixture())
	// A fixed build time: the two builds may fall in different seconds.
	opts := mmdb.Options{BuildTime: fixedBuildTime}

	fresh := filepath.Join(dir, "fresh.geoc")
	if _, err := mmdb.Build(src, fresh, opts); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "old.geoc")
	if err := os.WriteFile(old, []byte("previous contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(old, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mmdb.Build(src, old, opts); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(fresh)
	b, _ := os.ReadFile(old)
	if !bytes.Equal(a, b) {
		t.Error("replaced file differs from a fresh build of the same input")
	}
	if m := tempFiles(t, dir); len(m) != 0 {
		t.Errorf("temporary files left: %v", m)
	}
	if runtime.GOOS == "windows" {
		return // no Unix permission bits
	}
	for path, want := range map[string]fs.FileMode{fresh: 0o644, old: 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s: mode %v, want %v", filepath.Base(path), got, want)
		}
	}
}
