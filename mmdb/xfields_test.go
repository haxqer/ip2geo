package mmdb_test

// Tests of revision 2.1 builds: field sets, languages and ASN databases,
// checked with the independent parser of geo2parse_test.go against a model
// of the source records.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"net/netip"
	"slices"
	"testing"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/mmdb"
)

// tNames is a names map: language -> name.
type tNames map[string]string

// tCountry is a country of a model record.
type tCountry struct {
	iso   string
	geo   uint32
	eu    bool
	names tNames
}

// tSub is a subdivision of a model record.
type tSub struct {
	iso   string
	geo   uint32
	names tNames
}

// tRec models a source data record. Its data method encodes it for the
// fixture writer and its want method says what a build keeps of it.
type tRec struct {
	contCode  string
	contGeo   uint32
	contNames tNames
	// country is omitted from the data when nil, as are registered and
	// represented.
	country, registered, represented *tCountry
	repType                          string
	subs                             []tSub
	cityGeo                          uint32
	cityNames                        tNames
	lat, lon                         *float64 // nil when absent
	acc                              *uint16  // nil when absent
	tz                               string
	metro                            any // nil when absent; uint16 or a wider integer type
	postal                           string
	anycast, anonymous, satellite    bool
}

func ptr[T any](v T) *T { return &v }

func (n tNames) data() map[string]any {
	m := map[string]any{}
	for l, s := range n {
		m[l] = s
	}
	return m
}

func (c *tCountry) data() map[string]any {
	m := map[string]any{"iso_code": c.iso, "geoname_id": c.geo, "is_in_european_union": c.eu}
	if c.names != nil {
		m["names"] = c.names.data()
	}
	return m
}

// data returns the record as a MaxMind DB data map, with only the keys the
// model has.
func (r *tRec) data() map[string]any {
	m := map[string]any{}
	if r.contCode != "" {
		// The continent's id is a uint64 in the data, which the builder
		// accepts for its uint32 field.
		m["continent"] = map[string]any{"code": r.contCode, "geoname_id": uint64(r.contGeo), "names": r.contNames.data()}
	}
	if r.country != nil {
		m["country"] = r.country.data()
	}
	if r.registered != nil {
		m["registered_country"] = r.registered.data()
	}
	if r.represented != nil {
		rep := r.represented.data()
		rep["type"] = r.repType
		m["represented_country"] = rep
	}
	if len(r.subs) > 0 {
		var subs []any
		for _, s := range r.subs {
			subs = append(subs, map[string]any{"iso_code": s.iso, "geoname_id": s.geo, "names": s.names.data()})
		}
		m["subdivisions"] = subs
	}
	if r.cityNames != nil {
		m["city"] = map[string]any{"geoname_id": r.cityGeo, "names": r.cityNames.data()}
	}
	loc := map[string]any{}
	if r.lat != nil {
		loc["latitude"] = *r.lat
	}
	if r.lon != nil {
		loc["longitude"] = *r.lon
	}
	if r.acc != nil {
		loc["accuracy_radius"] = *r.acc
	}
	if r.tz != "" {
		loc["time_zone"] = r.tz
	}
	if r.metro != nil {
		loc["metro_code"] = r.metro
	}
	if len(loc) > 0 {
		m["location"] = loc
	}
	if r.postal != "" {
		m["postal"] = map[string]any{"code": r.postal}
	}
	if r.anycast || r.anonymous || r.satellite {
		m["traits"] = map[string]any{
			"is_anycast": r.anycast, "is_anonymous_proxy": r.anonymous, "is_satellite_provider": r.satellite,
		}
	}
	return m
}

// name returns the name in language j of langs, or the primary one when it
// has none in that language.
func name(n tNames, langs []string, j int) string {
	if s := n[langs[j]]; s != "" {
		return s
	}
	return n[langs[0]]
}

// want returns what LookupDetails returns for the record from a build with
// profile p (a geo one), field sets fs and languages langs, with the names
// in language j.
func (r *tRec) want(p ip2geo.Profile, fs ip2geo.FieldSet, langs []string, j int) ip2geo.Details {
	var d ip2geo.Details
	c := r.country
	if c == nil {
		c = &tCountry{}
	}
	d.CountryISO, d.CountryName, d.Continent, d.CountryGeoNameID = c.iso, name(c.names, langs, j), r.contCode, c.geo
	sub := func(i int) tSub {
		if i < len(r.subs) {
			return r.subs[i]
		}
		return tSub{}
	}
	if p >= ip2geo.ProfileRegion {
		d.RegionISO, d.RegionName = sub(0).iso, name(sub(0).names, langs, j)
	}
	if p >= ip2geo.ProfileCity {
		d.City, d.CityGeoNameID, d.TimeZone = name(r.cityNames, langs, j), r.cityGeo, r.tz
		if r.lat != nil {
			d.Latitude = *r.lat
		}
		if r.lon != nil {
			d.Longitude = *r.lon
		}
		if r.acc != nil {
			d.AccuracyRadius = *r.acc
		}
	}
	if fs&ip2geo.FieldEU != 0 {
		d.IsInEuropeanUnion = c.eu
	}
	if fs&ip2geo.FieldContinent != 0 {
		d.ContinentName, d.ContinentGeoNameID = name(r.contNames, langs, j), r.contGeo
	}
	if fs&ip2geo.FieldRegistered != 0 {
		country := func(c *tCountry) ip2geo.Country {
			if c == nil {
				return ip2geo.Country{}
			}
			return ip2geo.Country{ISO: c.iso, Name: name(c.names, langs, j), GeoNameID: c.geo, IsInEuropeanUnion: c.eu}
		}
		d.RegisteredCountry, d.RepresentedCountry = country(r.registered), country(r.represented)
		if r.represented != nil {
			d.RepresentedCountryType = r.repType
		}
	}
	if fs&ip2geo.FieldSubdivisions != 0 {
		d.RegionGeoNameID = sub(0).geo
		d.Region2ISO, d.Region2Name, d.Region2GeoNameID = sub(1).iso, name(sub(1).names, langs, j), sub(1).geo
	}
	if fs&ip2geo.FieldPostal != 0 {
		d.PostalCode = r.postal
	}
	if fs&ip2geo.FieldMetro != 0 {
		switch m := r.metro.(type) {
		case uint16:
			d.MetroCode = m
		case uint32:
			d.MetroCode = uint16(m)
		}
	}
	if fs&ip2geo.FieldTraits != 0 {
		d.IsAnycast, d.IsAnonymousProxy, d.IsSatelliteProvider = r.anycast, r.anonymous, r.satellite
	}
	if fs&ip2geo.FieldLocation != 0 {
		d.HasCoordinates, d.HasAccuracyRadius = r.lat != nil && r.lon != nil, r.acc != nil
	}
	return d
}

// richRecs are the model records of richFixture, by network.
var richRecs = func() map[string]*tRec {
	full := tRec{
		contCode: "EU", contGeo: 6255148,
		contNames: tNames{"en": "Europe", "de": "Europa", "fr": "Europe", "es": "Europa"},
		country:   &tCountry{"GB", 2635167, true, tNames{"en": "United Kingdom", "de": "Vereinigtes Königreich", "fr": "Royaume-Uni"}},
		registered: &tCountry{"US", 6252001, false,
			tNames{"en": "United States", "de": "Vereinigte Staaten", "fr": "États-Unis"}},
		represented: &tCountry{"DE", 2921044, true, tNames{"en": "Germany", "de": "Deutschland"}},
		repType:     "military",
		subs: []tSub{
			{"ENG", 6269131, tNames{"en": "England", "de": "England", "fr": "Angleterre"}},
			{"MRT", 3333171, tNames{"en": "Merton", "fr": "Merton (fr)"}},
			{"XYZ", 99, tNames{"en": "Third level"}}, // not kept
		},
		cityGeo: 2633866, cityNames: tNames{"en": "Wimbledon", "de": "Wimbledon (de)", "fr": "Wimbledon (fr)"},
		lat: ptr(51.4248), lon: ptr(-0.2093), acc: ptr(uint16(20)), tz: "Europe/London", metro: uint16(501),
		postal: "SW19", anycast: true, anonymous: true, satellite: true,
	}
	postal := full
	postal.postal = "SW20" // differs from full in the postal code only
	frName := full
	frName.cityNames = tNames{"en": "Wimbledon", "de": "Wimbledon (de)", "fr": "Wimbledon bis"} // in a French name only
	partial := tRec{
		contCode: "AS", contGeo: 6255147, contNames: tNames{"en": "Asia"},
		country: &tCountry{"JP", 1861060, false, tNames{"en": "Japan"}},
		subs:    []tSub{{"13", 1850144, tNames{"en": "Tokyo"}}},
		cityGeo: 1850147, cityNames: tNames{"en": "Tokyo"},
		tz: "Asia/Tokyo", metro: uint32(123), // a wider integer type
	}
	latOnly := tRec{country: &tCountry{"FR", 3017382, true, tNames{"fr": "France"}}, lat: ptr(48.85), acc: ptr(uint16(1000))}
	registeredOnly := tRec{registered: &tCountry{"US", 6252001, false, tNames{"en": "United States"}}, anonymous: true}
	v6 := full
	v6.country = &tCountry{"JP", 1861060, false, tNames{"en": "Japan", "de": "Japan", "fr": "Japon"}}
	return map[string]*tRec{
		"1.0.0.0/24":    &full,
		"1.0.1.0/24":    &postal,
		"1.0.2.0/24":    &frName,
		"1.0.3.0/24":    &registeredOnly,
		"1.0.4.0/22":    &partial,
		"1.0.8.0/24":    &latOnly,
		"1.0.9.0/24":    {}, // data, but none of the fields
		"2001:db8::/32": &v6,
		"2001:dc0::/32": &registeredOnly,
	}
}()

// richFixture is a City database with every field set, four languages,
// three subdivisions, records that differ only in a field set or a
// language, and found-but-empty networks. 1.0.10.0 onwards and the rest of
// the IPv6 space are gaps.
func richFixture() mmdbFixture {
	fx := mmdbFixture{dbType: "GeoIP2-City", languages: []string{"de", "en", "es", "fr"}, aliasV4: true}
	for _, p := range slices.Sorted(func(yield func(string) bool) {
		for p := range richRecs {
			if !yield(p) {
				return
			}
		}
	}) {
		fx.networks = append(fx.networks, mmdbNet{p, richRecs[p].data()})
	}
	return fx
}

// richAddrs returns an address in each network of richFixture (its first
// and its last), and gaps.
func richAddrs() (found map[netip.Addr]*tRec, gaps []netip.Addr) {
	found = map[netip.Addr]*tRec{}
	for p, r := range richRecs {
		pfx := netip.MustParsePrefix(p)
		a := pfx.Addr().AsSlice()
		for i := pfx.Bits(); i < len(a)*8; i++ {
			a[i/8] |= 0x80 >> (i % 8)
		}
		last, _ := netip.AddrFromSlice(a)
		found[pfx.Addr()], found[last] = r, r
	}
	for _, s := range []string{"0.255.255.255", "1.0.10.0", "255.255.255.255", "2001:db7::1", "2001:dc1::", "::1"} {
		gaps = append(gaps, netip.MustParseAddr(s))
	}
	return found, gaps
}

// allowedFields is the model of the field sets each profile allows.
func allowedFields(p ip2geo.Profile) ip2geo.FieldSet {
	switch p {
	case ip2geo.ProfileCountry:
		return ip2geo.FieldEU | ip2geo.FieldContinent | ip2geo.FieldRegistered | ip2geo.FieldTraits
	case ip2geo.ProfileRegion:
		return ip2geo.FieldEU | ip2geo.FieldContinent | ip2geo.FieldRegistered | ip2geo.FieldSubdivisions | ip2geo.FieldTraits
	case ip2geo.ProfileCity:
		return ip2geo.FieldsAll
	}
	return 0
}

// TestExtFields builds richFixture with every profile, every single field
// set the profile allows, FieldsAll, and several language lists, and checks
// every field of every network in every language through the parser.
func TestExtFields(t *testing.T) {
	t.Parallel()
	src := writeMMDB(t, richFixture())
	found, gaps := richAddrs()
	for _, p := range profiles {
		sets := []ip2geo.FieldSet{0, ip2geo.FieldsAll}
		for f := ip2geo.FieldSet(1); f&ip2geo.FieldsAll != 0; f <<= 1 {
			if allowedFields(p)&f != 0 {
				sets = append(sets, f)
			}
		}
		for _, fs := range sets {
			for _, langs := range [][]string{{"en"}, {"en", "de", "fr"}, {"fr", "en"}} {
				name := fmt.Sprintf("%v/fields=%#x/%v", p, uint32(fs), langs)
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					f, _ := buildGEO2(t, src, mmdb.Options{Profile: p, Fields: fs, Languages: langs, BuildTime: fixedBuildTime})
					checkExt(t, f, fs&allowedFields(p), langs, found, gaps)
				})
			}
		}
	}
}

// checkExt checks a parsed build of richFixture made with field sets fs
// (already masked to the profile) and languages langs.
func checkExt(t *testing.T, f *geo2File, fs ip2geo.FieldSet, langs []string, found map[netip.Addr]*tRec, gaps []netip.Addr) {
	t.Helper()
	ids := sectionIDs(f)
	ext := fs != 0 || len(langs) > 1
	if slices.Contains(ids, gsXFields) != ext || slices.Contains(ids, gsXRecords) != ext ||
		slices.Contains(ids, gsLangs) != (len(langs) > 1) {
		t.Errorf("sections %v for field sets %#x and %d languages", ids, uint32(fs), len(langs))
	}
	if f.xfields != uint32(fs) {
		t.Errorf("XFIELDS %#x, want %#x", f.xfields, uint32(fs))
	}
	if f.meta.language != langs[0] {
		t.Errorf("META language %q, want %q", f.meta.language, langs[0])
	}
	if len(langs) > 1 && !slices.Equal(f.langs, langs) {
		t.Errorf("LANGS %q, want %q", f.langs, langs)
	}
	for j := range langs {
		for ip, r := range found {
			d, ok := f.lookupDetails(ip, j)
			if want := r.want(f.profile, fs, langs, j); !ok || d != want {
				t.Errorf("%s in %s: ok=%v\n got %+v\nwant %+v", ip, langs[j], ok, d, want)
			}
		}
		for _, ip := range gaps {
			if d, ok := f.lookupDetails(ip, j); ok {
				t.Errorf("%s (a gap): found %+v", ip, d)
			}
		}
	}

	// Records that differ only in a kept field set or name are distinct;
	// otherwise they are one record.
	id := func(s string) uint32 { return f.lookupID(netip.MustParseAddr(s)) }
	if distinct := fs&ip2geo.FieldPostal != 0; (id("1.0.0.1") != id("1.0.1.1")) != distinct {
		t.Errorf("networks differing in the postal code only: ids %d and %d, want distinct %v",
			id("1.0.0.1"), id("1.0.1.1"), distinct)
	}
	if distinct := f.profile == ip2geo.ProfileCity && slices.Contains(langs, "fr"); (id("1.0.0.1") != id("1.0.2.1")) != distinct {
		t.Errorf("networks differing in a French city name only: ids %d and %d, want distinct %v",
			id("1.0.0.1"), id("1.0.2.1"), distinct)
	}
	if fs&ip2geo.FieldPostal != 0 && f.recs[id("1.0.0.1")] != f.recs[id("1.0.1.1")] {
		t.Error("the base records of networks differing in the postal code only differ")
	}
}

// TestExtNameMissing: an extra language's name is id 0 when the source has
// no name in it, so a reader falls back to the primary language.
func TestExtNameMissing(t *testing.T) {
	t.Parallel()
	f, _ := buildGEO2(t, writeMMDB(t, richFixture()), mmdb.Options{
		Profile: ip2geo.ProfileCity, Fields: ip2geo.FieldsAll, Languages: []string{"en", "de", "es"}, BuildTime: fixedBuildTime,
	})
	x := f.x[f.lookupID(netip.MustParseAddr("1.0.4.1"))] // names in English only
	for j, ids := range x.names {
		for k, id := range ids {
			if id != 0 {
				t.Errorf("language %d, kind %d: string id %d (%q), want 0", j+1, f.kinds[k], id, f.strs[id])
			}
		}
	}
	// es has the continent name only.
	x = f.x[f.lookupID(netip.MustParseAddr("1.0.0.1"))]
	for k, id := range x.names[1] {
		if (f.kinds[k] == kContinent) != (id != 0) {
			t.Errorf("es, kind %d: string id %d (%q)", f.kinds[k], id, f.strs[id])
		}
	}
	if want := []int{kCountry, kRegion, kCity, kContinent, kRegion2, kRegistered, kRepresented}; !slices.Equal(f.kinds, want) {
		t.Errorf("name kinds %v, want %v", f.kinds, want)
	}
}

// TestExtDefaultIsRevision20: without field sets and with one language the
// file has none of the revision 2.1 sections, whichever way the language
// is given, and is the same file.
func TestExtDefaultIsRevision20(t *testing.T) {
	t.Parallel()
	src := writeMMDB(t, richFixture())
	for _, p := range profiles {
		f, want := buildGEO2(t, src, mmdb.Options{Profile: p, BuildTime: fixedBuildTime})
		if ids := sectionIDs(f); slices.ContainsFunc(ids, func(id uint32) bool { return id > gsV6ExcLo }) {
			t.Errorf("%v: sections %v", p, ids)
		}
		for _, o := range []mmdb.Options{
			{Profile: p, Lang: "en", BuildTime: fixedBuildTime},
			{Profile: p, Languages: []string{"en"}, BuildTime: fixedBuildTime},
			{Profile: p, Languages: []string{"en"}, Fields: 0, BuildTime: fixedBuildTime},
		} {
			if _, got := buildGEO2(t, src, o); !bytes.Equal(got, want) {
				t.Errorf("%v: Options %+v give a different file", p, o)
			}
		}
	}
}

// TestExtStringsAbove16Bits: the strings of field sets are 24-bit ids, so
// more than 65,535 distinct postal codes fit and stay after the strings of
// 16-bit fields.
func TestExtStringsAbove16Bits(t *testing.T) {
	t.Parallel()
	const n = 70_000
	fx := mmdbFixture{dbType: "GeoIP2-City", languages: []string{"en"}}
	for i := range n {
		fx.networks = append(fx.networks, mmdbNet{seqV4(i), map[string]any{
			"country": map[string]any{"iso_code": "GB"},
			"postal":  map[string]any{"code": fmt.Sprintf("P%05d", i)},
		}})
	}
	f, _ := buildGEO2(t, writeMMDB(t, fx), mmdb.Options{
		Profile: ip2geo.ProfileCity, Fields: ip2geo.FieldPostal, BuildTime: fixedBuildTime,
	})
	if f.u16Strings != 2 || len(f.recs) != n+1 {
		t.Errorf("%d strings of 16-bit fields, %d records; want 2 and %d", f.u16Strings, len(f.recs), n+1)
	}
	d, ok := f.lookupDetails(netip.MustParseAddr("10.1.17.111"), 0)
	if !ok || d.PostalCode != fmt.Sprintf("P%05d", n-1) || d.CountryISO != "GB" {
		t.Errorf("last network: %+v ok=%v", d, ok)
	}
	if id := f.x[f.lookupID(netip.MustParseAddr("10.1.17.111"))].postal; id < math.MaxUint16 {
		t.Errorf("last postal code id %d, want above 65535", id)
	}
}

// asnFixture is an ASN database: IPv4 and IPv6 networks, two sharing an
// organization, a network with an empty record, and a gap.
func asnFixture(dbType string) mmdbFixture {
	as := func(n uint32, org string) map[string]any {
		return map[string]any{"autonomous_system_number": n, "autonomous_system_organization": org}
	}
	return mmdbFixture{dbType: dbType, aliasV4: true, networks: []mmdbNet{
		{"1.0.0.0/24", as(13335, "Cloudflare, Inc.")},
		{"1.0.1.0/24", as(13335, "Cloudflare, Inc.")}, // the same data
		{"1.0.2.0/23", as(4134, "Chinanet")},
		{"1.0.4.0/24", map[string]any{}}, // found, no data
		// 1.0.5.0/24 is a gap.
		{"1.0.6.0/24", map[string]any{"autonomous_system_number": uint64(4294967295)}}, // no organization
		{"8.8.8.0/24", as(15169, "Google LLC")},
		{"2001:4860::/32", as(15169, "Google LLC")},
	}}
}

func TestASN(t *testing.T) {
	t.Parallel()
	for _, typ := range []string{"GeoLite2-ASN", "DBIP-ASN-Lite (compat=GeoLite2-ASN)"} {
		src := writeMMDB(t, asnFixture(typ))
		for _, fs := range []ip2geo.FieldSet{0, ip2geo.FieldsAll} {
			f, b := buildGEO2(t, src, mmdb.Options{Profile: ip2geo.ProfileASN, Fields: fs, BuildTime: fixedBuildTime})
			if b[5] != 3 || f.latScale != 0 || f.recWidth != 7 {
				t.Errorf("%s: header profile %d latScale %d, record width %d; want 3, 0, 7", typ, b[5], f.latScale, f.recWidth)
			}
			if ids := sectionIDs(f); slices.ContainsFunc(ids, func(id uint32) bool { return id > gsV6ExcLo }) {
				t.Errorf("%s: sections %v", typ, ids)
			}
			if f.meta.sourceType != typ || f.meta.language != "en" {
				t.Errorf("META source type %q language %q", f.meta.sourceType, f.meta.language)
			}
			// Records: 0, Cloudflare, Chinanet, the empty one, the one without
			// an organization, Google.
			if len(f.recs) != 6 {
				t.Errorf("%s: %d records, want 6", typ, len(f.recs))
			}
			for _, c := range []struct {
				ip    string
				found bool
				want  ip2geo.ASN
			}{
				{"0.255.255.255", false, ip2geo.ASN{}},
				{"1.0.0.0", true, ip2geo.ASN{Number: 13335, Organization: "Cloudflare, Inc."}},
				{"1.0.1.255", true, ip2geo.ASN{Number: 13335, Organization: "Cloudflare, Inc."}},
				{"1.0.3.7", true, ip2geo.ASN{Number: 4134, Organization: "Chinanet"}},
				{"1.0.4.4", true, ip2geo.ASN{}},
				{"1.0.5.5", false, ip2geo.ASN{}},
				{"1.0.6.6", true, ip2geo.ASN{Number: 4294967295}},
				{"::ffff:8.8.8.8", true, ip2geo.ASN{Number: 15169, Organization: "Google LLC"}},
				{"2001:4860::8888", true, ip2geo.ASN{Number: 15169, Organization: "Google LLC"}},
				{"2001:4861::", false, ip2geo.ASN{}},
			} {
				d, ok := f.lookupDetails(netip.MustParseAddr(c.ip), 0)
				if ok != c.found || d != (ip2geo.Details{ASN: c.want}) {
					t.Errorf("%s %s: %+v ok=%v, want %+v ok=%v", typ, c.ip, d.ASN, ok, c.want, c.found)
				}
			}
			// The two Cloudflare networks share a record and merge.
			if f.v4Boundaries() != "1.0.0.0=1 1.0.2.0=2 1.0.4.0=3 1.0.5.0=0 1.0.6.0=4 1.0.7.0=0 8.8.8.0=5 8.8.9.0=0" {
				t.Errorf("%s: IPv4 boundaries %s", typ, f.v4Boundaries())
			}
		}
	}
}

// TestGEO2ExtParserRejects keeps the parser's revision 2.1 checks honest:
// each broken rule is caught even with the section's CRC fixed up.
func TestGEO2ExtParserRejects(t *testing.T) {
	t.Parallel()
	f, b := buildGEO2(t, writeMMDB(t, richFixture()), mmdb.Options{
		Profile: ip2geo.ProfileRegion, Fields: ip2geo.FieldEU | ip2geo.FieldRegistered | ip2geo.FieldTraits,
		Languages: []string{"en", "de"}, BuildTime: fixedBuildTime,
	})
	mutate := func(name string, id uint32, m func(sec []byte)) {
		t.Helper()
		c := bytes.Clone(b)
		for i, s := range f.dir {
			if s.id == id {
				m(c[s.off : s.off+s.length])
				binary.LittleEndian.PutUint32(c[32+24*i+4:], crc32.Checksum(c[s.off:s.off+s.length], castagnoli))
			}
		}
		if _, err := parseGEO2(c); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
	// The XRECORDS layout: EU flags @0, the registered block @1 (its flags
	// @24), traits flags @25, then the German names.
	entry := func(sec []byte, id uint32) []byte { return sec[int(id)*f.xWidth : int(id+1)*f.xWidth] }
	rec := f.lookupID(netip.MustParseAddr("1.0.0.1"))
	mutate("XFIELDS reserved", gsXFields, func(s []byte) { s[4] = 1 })
	mutate("XFIELDS unknown bit", gsXFields, func(s []byte) { s[1] = 1 })
	mutate("XFIELDS postal for region", gsXFields, func(s []byte) { s[0] |= 1 << 4 })
	mutate("XRECORDS entry 0", gsXRecords, func(s []byte) { s[0] = 1 })
	mutate("EU flag bit 1", gsXRecords, func(s []byte) { entry(s, rec)[0] |= 2 })
	mutate("registered flag bit 2", gsXRecords, func(s []byte) { entry(s, rec)[1+23] |= 4 })
	mutate("traits flag bit 3", gsXRecords, func(s []byte) { entry(s, rec)[25] |= 8 })
	mutate("name string id", gsXRecords, func(s []byte) { entry(s, rec)[26+2] = 0xff })
	mutate("LANGS repeated", gsLangs, func(s []byte) { copy(s[4:8], s[0:4]) })
	mutate("LANGS[0] is not META.language", gsLangs, func(s []byte) {
		var first [4]byte
		copy(first[:], s[0:4])
		copy(s[0:4], s[4:8])
		copy(s[4:8], first[:])
	})
	mutate("LANGS empty language", gsLangs, func(s []byte) { binary.LittleEndian.PutUint32(s[4:], 0) })
}
