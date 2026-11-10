package conformance

import (
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/internal/build"
)

// Every name, code and number below is made up. ISO codes are from the
// user-assigned ranges (QM-QZ, XA-XZ), continent codes are not real ones,
// GeoNames-like ids are in the 9 000 000s, and AS numbers are from the
// documentation (64496-64511) and private-use ranges, or the u32 extremes.

// buildTime and builderName are fixed so that the files do not depend on
// when or by which binary they are generated.
var buildTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

const (
	builderName = "ip2geo-conformance"
	sourceEpoch = 1767225600 // 2026-01-01T00:00:00Z
)

// network is a source network or range and the index of its record in
// dataset.records.
type network struct {
	first, last netip.Addr
	pfx         netip.Prefix // valid when the network is added as a prefix
	rec         int
	quiet       bool // no automatic edge queries (crowded synthetic runs)
}

func pfx(s string, rec int) network {
	p := netip.MustParsePrefix(s)
	if p != p.Masked() {
		panic(fmt.Sprintf("conformance: prefix %s is not masked", s))
	}
	return network{first: p.Addr(), last: lastAddr(p), pfx: p, rec: rec}
}

func rng(first, last string, rec int) network {
	return network{first: netip.MustParseAddr(first), last: netip.MustParseAddr(last), rec: rec}
}

// lastAddr returns the last address of a masked prefix.
func lastAddr(p netip.Prefix) netip.Addr {
	a := p.Addr()
	b := a.AsSlice()
	for i := p.Bits(); i < len(b)*8; i++ {
		b[i/8] |= 0x80 >> (i % 8)
	}
	l, _ := netip.AddrFromSlice(b)
	return l
}

// query is an address to look up, with a note for human readers.
type query struct {
	addr netip.Addr
	note string
}

func q(s, note string) query { return query{netip.MustParseAddr(s), note} }

// dataset is the synthetic source of one GEO2 file.
type dataset struct {
	file    string // file name, without directory
	about   string // what the file covers, for the JSON and README
	opts    build.Options
	srcType string
	records []build.Record
	nets    []network
	// aliasOf are IPv4 addresses that are also queried in their IPv6 alias
	// forms (IPv4-mapped, IPv4-compatible, 6to4, Teredo).
	aliasOf []string
	queries []query // file-specific queries, after the common ones
}

func names(s ...string) []string { return s }

// commonQueries are looked up in every file: the ends of both address
// families and of the IPv6 prefixes the reader maps to IPv4.
var commonQueries = []query{
	q("0.0.0.0", "lowest IPv4 address"),
	q("0.0.0.1", "second IPv4 address"),
	q("255.255.255.254", "second-highest IPv4 address"),
	q("255.255.255.255", "highest IPv4 address"),
	q("::", "lowest IPv6 address; IPv4-compatible alias of 0.0.0.0"),
	q("::1", "IPv4-compatible alias of 0.0.0.1"),
	q("::ffff:ffff", "last address of ::/96, alias of 255.255.255.255"),
	q("::1:0:0", "first address after ::/96, not an alias"),
	q("::fffe:ffff:ffff", "last address before ::ffff:0:0/96, not an alias"),
	q("::ffff:0.0.0.0", "first IPv4-mapped address, alias of 0.0.0.0"),
	q("::ffff:255.255.255.255", "last IPv4-mapped address, alias of 255.255.255.255"),
	q("::1:0:0:0", "first address after ::ffff:0:0/96, not an alias"),
	q("64:ff9b::a00:1", "NAT64 64:ff9b::/96 is not mapped to IPv4 (would be 10.0.0.1)"),
	q("1fff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "last address before 2000::/3"),
	q("2000::", "first address of 2000::/3"),
	q("2000:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "last address before Teredo 2001::/32"),
	q("2001::", "first Teredo address, alias of 0.0.0.0"),
	q("2001:0:a00:1::", "Teredo, server bits 32-63 are 10.0.0.1"),
	q("2001:0:ffff:ffff:ffff:ffff:ffff:ffff", "last Teredo address, alias of 255.255.255.255"),
	q("2001:1::", "first address after Teredo 2001::/32, not an alias"),
	q("2001:db8::1", "2001:db8::/32 is not an alias"),
	q("2001:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "last address before 6to4 2002::/16"),
	q("2002::", "first 6to4 address, alias of 0.0.0.0"),
	q("2002:a00:1::", "6to4, bits 16-47 are 10.0.0.1"),
	q("2002:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "last 6to4 address, alias of 255.255.255.255"),
	q("2003::", "first address after 6to4 2002::/16, not an alias"),
	q("fe80::1", "link-local IPv6"),
	q("ffff:ffff:ffff:ffff:ffff:ffff:ffff:fffe", "second-highest IPv6 address"),
	q("ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "highest IPv6 address"),
}

func baseOpts(p ip2geo.Profile, desc string) build.Options {
	return build.Options{Profile: p, BuildTime: buildTime, Builder: builderName, Description: desc}
}

// The records shared by several geo files.
var (
	exemplia  = build.Continent{Code: "XC", Names: names("Exemplia", "Exemplien", "示例洲"), GeoNameID: 9000100}
	ypsilonia = build.Continent{Code: "YC", Names: names("Ypsilonia"), GeoNameID: 9000200}
	testland  = build.Country{ISOCode: "QT", Names: names("Testland", "Testlandien", "测试国"), GeoNameID: 9000001, IsInEuropeanUnion: true}
	mockovia  = build.Country{ISOCode: "QM", Names: names("Mockovïa", "Mockowien"), GeoNameID: 9000002}
	samplstan = build.Country{ISOCode: "XS", Names: names("Samplestan"), GeoNameID: 9000003}
	north     = build.Subdivision{ISOCode: "T1", Names: names("North Testshire", "Nord-Testshire", "北测试郡"), GeoNameID: 9200001}
	upper     = build.Subdivision{ISOCode: "T1A", Names: names("Upper Test", "Ober-Test"), GeoNameID: 9200011}
	south     = build.Subdivision{ISOCode: "T2", Names: names("South Testshire"), GeoNameID: 9200002}
)

func countryDataset() dataset {
	return dataset{
		file:    "country.geoc",
		about:   "country profile, revision 2.0 (no extension fields, one language): merged and adjacent ranges, found-but-empty records, a range across an empty /16, IPv6 ranges cut at the alias prefixes",
		opts:    baseOpts(ip2geo.ProfileCountry, "ip2geo conformance: country profile, synthetic data"),
		srcType: "Conformance-Country",
		records: []build.Record{
			0: {Continent: exemplia, Country: testland},
			1: {Continent: exemplia, Country: mockovia},
			// Only fields the country profile drops: a found network whose
			// record is all zero. It shares its id with record 5.
			2: {City: build.City{Names: names("Lonelyville"), GeoNameID: 9100099}, Subdivisions: [2]build.Subdivision{south}},
			3: {Continent: ypsilonia, Country: samplstan},
			4: {Country: build.Country{ISOCode: "XF"}}, // a code and nothing else
			5: {},
		},
		nets: []network{
			pfx("0.0.0.0/8", 0),
			pfx("1.0.0.0/24", 0),
			pfx("1.0.1.0/24", 0), // merges with 1.0.0.0/24
			pfx("1.0.2.0/23", 1),
			pfx("1.0.8.0/21", 2),
			pfx("1.0.16.0/20", 5), // same id as record 2: merges with 1.0.8.0/21
			pfx("10.0.0.0/8", 3),
			pfx("11.0.255.0/24", 0),
			pfx("11.1.0.0/24", 1), // starts at a /16 bucket base
			pfx("12.34.56.78/32", 1),
			rng("20.0.0.5", "20.0.1.250", 4),
			rng("30.0.255.255", "30.2.0.0", 1), // 30.1.0.0/16 holds no boundary
			pfx("100.64.0.0/10", 0),
			pfx("223.255.255.0/24", 1),
			pfx("255.255.255.0/24", 3),
			pfx("::1:0:0/96", 1),
			pfx("::1:0:0:0/80", 4),
			pfx("64:ff9b::/96", 0),
			pfx("2000::/3", 1), // holds both alias prefixes
			pfx("fe80::/10", 2),
			pfx("ff00::/8", 3),
		},
		aliasOf: []string{"1.0.0.0", "10.0.0.0", "255.255.255.255"},
		queries: []query{
			q("30.1.128.0", "inside a range that spans the whole of 30.1.0.0/16"),
			q("50.0.0.0", "in the gap 30.2.0.1-100.63.255.255, far from both ends"),
			q("150.0.0.0", "in the gap 100.128.0.0-223.255.254.255"),
			q("2400::1", "inside 2000::/3, between 6to4 and the end"),
			q("3fff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "last address of 2000::/3"),
		},
	}
}

func regionDataset() dataset {
	o := baseOpts(ip2geo.ProfileRegion, "ip2geo conformance: region profile with every allowed field set and two languages, synthetic data")
	o.Fields = ip2geo.FieldsAll
	o.Languages = []string{"en", "de"}
	shire := build.Record{
		Continent: exemplia, Country: testland, Subdivisions: [2]build.Subdivision{north, upper},
		RegisteredCountry: testland,
	}
	anycast := shire
	anycast.Traits.IsAnycast = true
	renamed := shire
	renamed.Country.Names = names("Testland", "Testlandia")
	return dataset{
		file:    "region.geoc",
		about:   "region profile, revision 2.1 with every field set the profile allows (EU, continent, registered/represented, subdivisions, traits) and languages en, de: name fallback, records that differ only in extension fields or in a second-language name",
		opts:    o,
		srcType: "Conformance-Region",
		records: []build.Record{
			0: shire,
			1: {
				Continent: exemplia, Country: testland, Subdivisions: [2]build.Subdivision{south},
				RegisteredCountry: mockovia, RepresentedCountry: testland, RepresentedType: "military",
			},
			2: {Continent: ypsilonia, Country: mockovia, Traits: build.Traits{IsAnycast: true}, RegisteredCountry: samplstan},
			3: {Traits: build.Traits{IsAnonymousProxy: true, IsSatelliteProvider: true}, RegisteredCountry: samplstan},
			4: {},
			5: {Country: build.Country{ISOCode: "XG", Names: names("", "Nurdeutschland"), GeoNameID: 9000007}},
			6: anycast, // same base record as 0, different traits
			7: renamed, // same as 0 but for the German country name
		},
		nets: []network{
			pfx("1.0.0.0/16", 0),
			pfx("1.1.0.0/16", 6),
			pfx("1.2.0.0/16", 7),
			pfx("1.3.0.0/16", 1),
			pfx("2.0.0.0/8", 2),
			pfx("3.0.0.0/24", 3),
			pfx("3.0.1.0/24", 4),
			pfx("3.0.2.0/24", 5),
			pfx("2001:db8::/48", 0),
			pfx("2001:db8:1::/48", 1),
			pfx("2001:db8:2::/48", 2),
			pfx("2a00::/12", 3),
			pfx("ffff::/16", 4),
		},
		aliasOf: []string{"1.3.0.0"},
	}
}

func cityDataset() dataset {
	o := baseOpts(ip2geo.ProfileCity, "ip2geo conformance: city profile with every field set and three languages, synthetic data")
	o.Fields = ip2geo.FieldsAll
	o.Languages = []string{"en", "de", "zh-CN"}
	full := build.Record{
		Continent: exemplia, Country: testland, Subdivisions: [2]build.Subdivision{north, upper},
		City: build.City{Names: names("Testville", "Teststadt", "测试市"), GeoNameID: 9100001},
		Location: build.Location{
			Latitude: 12.3456, Longitude: -65.4321, AccuracyRadius: 20, TimeZone: "Testland/Capitalia",
			MetroCode: 501, HasCoordinates: true, HasAccuracyRadius: true,
		},
		PostalCode:         "TE5 7AB",
		RegisteredCountry:  testland,
		RepresentedCountry: mockovia, RepresentedType: "military",
		Traits: build.Traits{IsAnycast: true},
	}
	d := dataset{
		file:    "city.geoc",
		about:   "city profile, revision 2.1 with every field set and languages en, de, zh-CN, latScale 4: coordinates (missing, zero, extreme, rounded), u16/u32 maxima, UTF-8 names, IPv6 starts with non-zero low halves, a crowded IPv6 /16, data stored under 6to4 that lookups never reach",
		opts:    o,
		srcType: "Conformance-City",
		records: []build.Record{
			0: full,
			1: {
				Continent: exemplia, Country: testland,
				City: build.City{Names: names("Nowhere Junction"), GeoNameID: 9100002}, // no location at all
			},
			2: {
				Continent: ypsilonia, Country: mockovia, City: build.City{Names: names("Southpole Station"), GeoNameID: 9100003},
				Location: build.Location{Latitude: -90, Longitude: 180, AccuracyRadius: 1000, TimeZone: "Mockovia/Pole", HasCoordinates: true, HasAccuracyRadius: true},
			},
			3: {
				Continent: ypsilonia, Country: mockovia, City: build.City{Names: names("Northpole Camp"), GeoNameID: 9100004},
				Location: build.Location{Latitude: 90, Longitude: -180, TimeZone: "Mockovia/Pole", HasCoordinates: true},
			},
			4: {
				Country: samplstan, City: build.City{Names: names("Roundington"), GeoNameID: 9100005},
				Location: build.Location{Latitude: 45.12345, Longitude: -0.00005, AccuracyRadius: 5, HasCoordinates: true, HasAccuracyRadius: true},
			},
			5: {
				Country: samplstan, City: build.City{Names: names("Null Island Annex"), GeoNameID: 9100006},
				Location: build.Location{Latitude: 0.00004, Longitude: 0, HasCoordinates: true}, // present, stored as 0
			},
			6: {
				Continent: exemplia, Country: testland, Subdivisions: [2]build.Subdivision{south},
				City:       build.City{Names: names("Ünïcødé-on-Sea 🧪", "Ünïcødé am Meer"), GeoNameID: 9100007},
				PostalCode: strings.Repeat("Testington", 30),
				Location:   build.Location{Latitude: -33.3333, Longitude: 151.5151, TimeZone: "Testland/Ünïcødé", HasCoordinates: true},
			},
			7: {},
			8: {
				Country: build.Country{ISOCode: "XM", GeoNameID: 4294967295},
				City:    build.City{Names: names("Maxima"), GeoNameID: 4294967295},
				Location: build.Location{
					Latitude: 1.5, Longitude: 2.5, AccuracyRadius: 65535, MetroCode: 65535,
					HasCoordinates: true, HasAccuracyRadius: true,
				},
				PostalCode: "00000",
			},
			9: {
				Country: testland,
				City:    build.City{Names: names("", "Nurstadt"), GeoNameID: 9100009}, // no English name
			},
			10: {Country: build.Country{ISOCode: "XZ", Names: names("Shadowland")}}, // only under 6to4
		},
		nets: []network{
			pfx("0.0.0.0/30", 7),
			pfx("1.0.0.0/24", 0),
			pfx("1.0.1.0/24", 1),
			pfx("1.0.2.0/24", 2),
			pfx("1.0.3.0/24", 3),
			pfx("1.0.4.0/24", 4),
			pfx("1.0.5.0/24", 5),
			pfx("1.0.6.0/24", 6),
			pfx("1.0.7.0/24", 8),
			pfx("1.0.8.0/24", 9),
			pfx("171.205.0.0/16", 1),
			pfx("255.255.255.255/32", 8),
			pfx("2001:db8::/48", 0),
			// Starts sharing their top 64 bits: low halves 0, 4000::, 8000::.
			pfx("2001:db8:ff:1::/66", 1),
			pfx("2001:db8:ff:1:4000::/66", 2),
			pfx("2001:db8:ff:1:8000::/65", 3),
			pfx("2001:db8:ff:3::1/128", 4),
			// 6to4 space: stored, but Lookup answers 2002:abcd::/32 from
			// the IPv4 table (171.205.0.0/16), and Ranges leaves it out.
			pfx("2002:abcd::/32", 10),
			pfx("ffff:ffff:ffff:ffff::/64", 8),
		},
		aliasOf: []string{"1.0.0.0", "171.205.0.0"},
		queries: []query{
			q("2002:abcd::", "6to4 alias of 171.205.0.0, not the IPv6 data stored at 2002:abcd::/32"),
			q("2002:abcd:ffff::1", "6to4 alias of 171.205.255.255"),
			q("2001:db8:ff:1:3fff:ffff:ffff:ffff", "last address of the first /66"),
			q("2001:db8:ff:1:4000::", "first address of the second /66 (same top 64 bits)"),
			q("2001:db8:ff:1:c000::", "inside the /65"),
			q("40.0.0.0", "first of 64 alternating /28s in one top-20 window"),
			q("40.0.0.17", "inside the second /28"),
			q("40.0.3.240", "first address of the last /28"),
			q("40.0.3.255", "last address of the last /28"),
			q("40.0.4.0", "just after the last /28"),
			q("39.255.255.255", "just before the first /28"),
			q("2c0f::", "first of 40 alternating /48s in one crowded IPv6 /16"),
			q("2c0f:0:1::", "gap between two /48s in the crowded /16"),
			q("2c0f:0:26::ffff", "inside the 20th /48"),
			q("2c0f:0:4e:ffff:ffff:ffff:ffff:ffff", "last address of the last /48"),
			q("2c0f:0:100::", "first /66 inside the crowded /16"),
			q("2c0f:0:100:0:4000::", "second /66 inside the crowded /16"),
			q("2c0f:0:100:0:7fff:ffff:ffff:ffff", "last address of the second /66"),
			q("2c0f:0:100:0:8000::", "just after the second /66"),
			q("2c0e:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "just before the crowded /16"),
			q("2c10::", "just after the crowded /16"),
		},
	}
	// 64 alternating /28s in 40.0.0.0/22: a crowded top-20 window.
	for i := range 64 {
		d.nets = append(d.nets, network{
			first: netip.AddrFrom4([4]byte{40, 0, byte(i >> 4), byte(i << 4)}),
			last:  netip.AddrFrom4([4]byte{40, 0, byte(i >> 4), byte(i<<4 | 15)}),
			rec:   i % 2, quiet: true,
		})
	}
	// 40 /48s, every other one, in 2c0f::/16: 80 starts in one top-16
	// slot, more than the reader's index keeps unsplit, and two /66s.
	for i := range 40 {
		p := netip.PrefixFrom(netip.AddrFrom16([16]byte{0x2c, 0x0f, 0, 0, 0, byte(2 * i)}), 48)
		d.nets = append(d.nets, network{first: p.Addr(), last: lastAddr(p), pfx: p, rec: i % 3, quiet: true})
	}
	for _, n := range []network{pfx("2c0f:0:100::/66", 5), pfx("2c0f:0:100:0:4000::/66", 6)} {
		n.quiet = true
		d.nets = append(d.nets, n)
	}
	return d
}

func cityLangsDataset() dataset {
	o := baseOpts(ip2geo.ProfileCity, "ip2geo conformance: city profile, two languages, no field sets, latScale 7, synthetic data")
	o.Languages = []string{"en", "de"}
	o.LatScale = 7
	return dataset{
		file:    "city-langs.geoc",
		about:   "city profile, revision 2.1 with languages en, de and no field set (XFIELDS bits 0), latScale 7: seven-decimal coordinates, the int32 extremes of the scale, tiny values printed in exponent form",
		opts:    o,
		srcType: "Conformance-City-Langs",
		records: []build.Record{
			0: {
				Continent: exemplia, Country: testland, Subdivisions: [2]build.Subdivision{north},
				City:     build.City{Names: names("Precisionville", "Präzisionsdorf"), GeoNameID: 9100101},
				Location: build.Location{Latitude: 12.3456789, Longitude: -98.7654321, AccuracyRadius: 1, TimeZone: "Testland/Capitalia"},
			},
			1: {
				Country:  samplstan,
				City:     build.City{Names: names("Epsilon Point"), GeoNameID: 9100102},
				Location: build.Location{Latitude: 0.0000001, Longitude: -0.0000001},
			},
			2: {
				Country:  mockovia,
				City:     build.City{Names: names("Almost Pole", "Fastpol"), GeoNameID: 9100103},
				Location: build.Location{Latitude: 89.9999999, Longitude: 179.9999999},
			},
			3: {
				Country:  mockovia,
				City:     build.City{Names: names("Antipode"), GeoNameID: 9100104},
				Location: build.Location{Latitude: -90, Longitude: -180},
			},
			4: {},
		},
		nets: []network{
			pfx("5.5.0.0/16", 0),
			pfx("5.6.0.0/16", 1),
			pfx("5.7.0.0/16", 2),
			pfx("5.8.0.0/16", 3),
			pfx("5.9.0.0/16", 4),
			pfx("2001:db8:5::/48", 0),
			pfx("2001:db8:6::/48", 3),
		},
	}
}

func asnDataset() dataset {
	return dataset{
		file:    "asn.geoc",
		about:   "ASN profile (revision 2.1): AS number 0 and u32 maximum, empty and UTF-8 organizations, a network given as an IPv4-mapped prefix",
		opts:    baseOpts(ip2geo.ProfileASN, "ip2geo conformance: ASN, synthetic data"),
		srcType: "Conformance-ASN",
		records: []build.Record{
			0: {ASN: ip2geo.ASN{Number: 64496, Organization: "Example Transit Ltd"}},
			1: {ASN: ip2geo.ASN{Number: 4200000000, Organization: "Private Use 32-bit Org"}},
			2: {ASN: ip2geo.ASN{}}, // found, number 0, no organization
			3: {ASN: ip2geo.ASN{Number: 65551}},
			4: {ASN: ip2geo.ASN{Number: 64511, Organization: "Ünicode Networks 🧪"}},
			5: {ASN: ip2geo.ASN{Number: 4294967295, Organization: "Max ASN"}},
		},
		nets: []network{
			pfx("1.0.0.0/24", 0),
			pfx("1.0.1.0/24", 0),
			pfx("1.0.2.0/24", 1),
			pfx("5.0.0.0/8", 2),
			pfx("6.0.0.0/8", 3),
			pfx("::ffff:7.0.0.0/104", 4), // stored as the IPv4 network 7.0.0.0/8
			pfx("255.0.0.0/8", 5),
			pfx("2001:db8::/32", 4),
			pfx("2400::/12", 1),
			pfx("ffff:ffff::/32", 2),
		},
		aliasOf: []string{"1.0.2.0", "7.0.0.0"},
	}
}

// datasets returns the GEO2 sources, in file name order of no importance:
// Generate sorts its output.
func datasets() []dataset {
	return []dataset{countryDataset(), regionDataset(), cityDataset(), cityLangsDataset(), asnDataset()}
}
