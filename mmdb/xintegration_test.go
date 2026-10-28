package mmdb_test

// Integration tests of revision 2.1 builds against real sources, through
// the parser of geo2parse_test.go. TestExtMatchesSource runs when
// IP2GEO_TEST_MMDB names a City .mmdb, TestASNMatchesSource when
// IP2GEO_TEST_ASN_MMDB names an ASN one:
//
//	IP2GEO_TEST_MMDB=GeoIP2-City.mmdb IP2GEO_TEST_ASN_MMDB=GeoLite2-ASN.mmdb \
//		go test -count=1 -timeout 30m -v -run 'MatchesSource' ./mmdb/

import (
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/mmdb"
)

// envASN names an ASN .mmdb for TestASNMatchesSource.
const envASN = "IP2GEO_TEST_ASN_MMDB"

type srcNames map[string]string

type srcFullCountry struct {
	ISOCode           string   `maxminddb:"iso_code"`
	GeoNameID         uint32   `maxminddb:"geoname_id"`
	IsInEuropeanUnion bool     `maxminddb:"is_in_european_union"`
	Names             srcNames `maxminddb:"names"`
	Type              string   `maxminddb:"type"`
}

// srcFull is every field of a City record that a build can keep.
type srcFull struct {
	Continent struct {
		Code      string   `maxminddb:"code"`
		GeoNameID uint32   `maxminddb:"geoname_id"`
		Names     srcNames `maxminddb:"names"`
	} `maxminddb:"continent"`
	Country            srcFullCountry `maxminddb:"country"`
	RegisteredCountry  srcFullCountry `maxminddb:"registered_country"`
	RepresentedCountry srcFullCountry `maxminddb:"represented_country"`
	Subdivisions       []struct {
		ISOCode   string   `maxminddb:"iso_code"`
		GeoNameID uint32   `maxminddb:"geoname_id"`
		Names     srcNames `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
	City struct {
		GeoNameID uint32   `maxminddb:"geoname_id"`
		Names     srcNames `maxminddb:"names"`
	} `maxminddb:"city"`
	Location struct {
		Latitude       *float64 `maxminddb:"latitude"`
		Longitude      *float64 `maxminddb:"longitude"`
		AccuracyRadius *uint16  `maxminddb:"accuracy_radius"`
		TimeZone       string   `maxminddb:"time_zone"`
		MetroCode      uint16   `maxminddb:"metro_code"`
	} `maxminddb:"location"`
	Postal struct {
		Code string `maxminddb:"code"`
	} `maxminddb:"postal"`
	Traits struct {
		IsAnycast           bool `maxminddb:"is_anycast"`
		IsAnonymousProxy    bool `maxminddb:"is_anonymous_proxy"`
		IsSatelliteProvider bool `maxminddb:"is_satellite_provider"`
	} `maxminddb:"traits"`
}

// wantFull is what LookupDetails returns for s from a city build with every
// field set and languages langs, names in language j.
func wantFull(s *srcFull, langs []string, j int) ip2geo.Details {
	name := func(n srcNames) string {
		if v := n[langs[j]]; v != "" {
			return v
		}
		return n[langs[0]]
	}
	country := func(c *srcFullCountry) ip2geo.Country {
		return ip2geo.Country{ISO: c.ISOCode, Name: name(c.Names), GeoNameID: c.GeoNameID, IsInEuropeanUnion: c.IsInEuropeanUnion}
	}
	d := ip2geo.Details{
		Record: ip2geo.Record{
			CountryISO: s.Country.ISOCode, CountryName: name(s.Country.Names), Continent: s.Continent.Code,
			CountryGeoNameID: s.Country.GeoNameID, City: name(s.City.Names), CityGeoNameID: s.City.GeoNameID,
			TimeZone: s.Location.TimeZone,
		},
		ContinentName: name(s.Continent.Names), ContinentGeoNameID: s.Continent.GeoNameID,
		IsInEuropeanUnion: s.Country.IsInEuropeanUnion,
		PostalCode:        s.Postal.Code, MetroCode: s.Location.MetroCode,
		HasCoordinates:     s.Location.Latitude != nil && s.Location.Longitude != nil,
		HasAccuracyRadius:  s.Location.AccuracyRadius != nil,
		RegisteredCountry:  country(&s.RegisteredCountry),
		RepresentedCountry: country(&s.RepresentedCountry), RepresentedCountryType: s.RepresentedCountry.Type,
		IsAnycast: s.Traits.IsAnycast, IsAnonymousProxy: s.Traits.IsAnonymousProxy,
		IsSatelliteProvider: s.Traits.IsSatelliteProvider,
	}
	if s.Location.Latitude != nil {
		d.Latitude = float64(scaled(*s.Location.Latitude)) / coordScale
	}
	if s.Location.Longitude != nil {
		d.Longitude = float64(scaled(*s.Location.Longitude)) / coordScale
	}
	if s.Location.AccuracyRadius != nil {
		d.AccuracyRadius = *s.Location.AccuracyRadius
	}
	if len(s.Subdivisions) > 0 {
		r := s.Subdivisions[0]
		d.RegionISO, d.RegionName, d.RegionGeoNameID = r.ISOCode, name(r.Names), r.GeoNameID
	}
	if len(s.Subdivisions) > 1 {
		r := s.Subdivisions[1]
		d.Region2ISO, d.Region2Name, d.Region2GeoNameID = r.ISOCode, name(r.Names), r.GeoNameID
	}
	return d
}

// TestExtMatchesSource builds the source with ProfileCity, FieldsAll and up
// to three of its languages ("en" first), and checks every field of
// LookupDetails, in every language, on random and in-network addresses.
func TestExtMatchesSource(t *testing.T) {
	src := sourceMMDB(t)
	info := scanSource(t, src)
	mm := openMaxMind(t, src)
	langs := []string{"en"}
	for _, l := range mm.Metadata.Languages {
		if len(langs) < 3 && !slices.Contains(langs, l) {
			langs = append(langs, l)
		}
	}
	out := filepath.Join(t.TempDir(), "city-all.geoc")
	st, err := mmdb.Build(src, out, mmdb.Options{
		Profile: ip2geo.ProfileCity, Fields: ip2geo.FieldsAll, Languages: langs, BuildTime: fixedBuildTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("languages %q: %s", langs, st)
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	f, err := parseGEO2(b)
	if err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewSource(2))
	sets := []addrSet{
		{"ipv4-random", randomAddrs(r, allIPv4, nRandomV4/4)},
		{"ipv6-in-network", oneInEach(r, everyKth(info.v6, nInNetworkV6/4))},
	}
	bad, n := 0, 0
	for _, set := range sets {
		for _, a := range set.addrs {
			res := mm.Lookup(a)
			var s srcFull
			if err := res.Decode(&s); err != nil {
				t.Fatalf("maxminddb lookup %s: %v", a, err)
			}
			for j := range langs {
				n++
				got, ok := f.lookupDetails(a, j)
				want := wantFull(&s, langs, j)
				if !res.Found() {
					want = ip2geo.Details{}
				}
				if ok == res.Found() && got == want {
					continue
				}
				if bad++; bad <= maxReported {
					t.Errorf("%s %s in %s (maxminddb network %s):\n  maxminddb found=%v %+v\n  parsed    ok=%v    %+v",
						set.name, a, langs[j], res.Prefix(), res.Found(), want, ok, got)
				}
			}
		}
	}
	t.Logf("%d lookups compared, %d mismatches", n, bad)
}

// TestASNMatchesSource builds an ASN source and checks every network's
// first address and random addresses.
func TestASNMatchesSource(t *testing.T) {
	src := os.Getenv(envASN)
	if src == "" {
		t.Skipf("set %s to an ASN .mmdb (GeoLite2-ASN or DB-IP ASN Lite) to run", envASN)
	}
	mm := openMaxMind(t, src)
	out := filepath.Join(t.TempDir(), "asn.geoc")
	st, err := mmdb.Build(src, out, mmdb.Options{Profile: ip2geo.ProfileASN, BuildTime: fixedBuildTime})
	if err != nil {
		t.Fatal(err)
	}
	t.Log(st)
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	f, err := parseGEO2(b)
	if err != nil {
		t.Fatal(err)
	}
	type asn struct {
		Number       uint32 `maxminddb:"autonomous_system_number"`
		Organization string `maxminddb:"autonomous_system_organization"`
	}
	r := rand.New(rand.NewSource(3))
	addrs := randomAddrs(r, allIPv4, nRandomV4/2)
	addrs = append(addrs, randomAddrs(r, globalIPv6, nRandomV6/2)...)
	for res := range mm.Networks() {
		addrs = append(addrs, res.Prefix().Addr())
	}
	bad := 0
	for _, a := range addrs {
		res := mm.Lookup(a)
		var s asn
		if err := res.Decode(&s); err != nil {
			t.Fatal(err)
		}
		got, ok := f.lookupDetails(a, 0)
		if ok == res.Found() && got == (ip2geo.Details{ASN: ip2geo.ASN(s)}) {
			continue
		}
		if bad++; bad <= maxReported {
			t.Errorf("%s: maxminddb found=%v %+v, parsed ok=%v %+v", a, res.Found(), s, ok, got.ASN)
		}
	}
	t.Logf("%d addresses compared, %d mismatches", len(addrs), bad)
}
