package mmdb_test

// Integration tests against a real City .mmdb. They run only when
// IP2GEO_TEST_MMDB names one, and must pass unchanged on any City source
// (MaxMind GeoIP2/GeoLite2-City, DB-IP City Lite): they hard-code no names,
// codes or coordinates, and take every expected value from maxminddb.
//
//	IP2GEO_TEST_MMDB=GeoIP2-City.mmdb go test -count=1 -timeout 30m -v \
//		-run 'TestBuildMatchesSource|TestOpenInMemory|TestRegisteredCountry' ./mmdb/
//
// Run them without -race: under the race detector each ~26 s build takes
// minutes.

import (
	"math/rand"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/mmdb"
)

// Address set sizes for TestBuildMatchesSource.
const (
	nRandomV4    = 200_000
	nRandomV6    = 100_000 // uniform in 2000::/3
	nInNetworkV6 = 100_000 // about this many, one inside each sampled IPv6 network
	nAlias       = 50_000  // per alias prefix
)

// aliasPrefixes are the IPv6 prefixes that MaxMind databases alias to the
// IPv4 tree, plus NAT64, which they do not. ip2geo must agree with
// maxminddb on whatever the source does in each.
var aliasPrefixes = []string{"::ffff:0:0/96", "::/96", "2002::/16", "2001::/32", "64:ff9b::/96"}

// paritySets returns the address sets of TestBuildMatchesSource, the same
// for every profile.
func paritySets(info *sourceInfo) []addrSet {
	r := rand.New(rand.NewSource(1))
	sets := []addrSet{
		{"ipv4-random", randomAddrs(r, allIPv4, nRandomV4)},
		{"ipv6-random 2000::/3", randomAddrs(r, globalIPv6, nRandomV6)},
		{"ipv6-in-network", oneInEach(r, everyKth(info.v6, nInNetworkV6))},
	}
	for _, p := range aliasPrefixes {
		sets = append(sets, addrSet{"alias " + p, randomAddrs(r, netip.MustParsePrefix(p), nAlias)})
	}
	return sets
}

// TestBuildMatchesSource builds each profile with mmdb.Build, opens the file
// with ip2geo.Open and checks that every address of every set gets the same
// answer as maxminddb: found or not, and every field the profile keeps.
func TestBuildMatchesSource(t *testing.T) {
	src := sourceMMDB(t)
	info := scanSource(t, src)
	mm := openMaxMind(t, src)
	sets := paritySets(info)

	start := time.Now()
	var addrs, mismatches int
	for _, p := range profiles {
		t.Run(p.String(), func(t *testing.T) {
			out := filepath.Join(t.TempDir(), p.String()+".geoc")
			buildStart := time.Now()
			st, err := mmdb.Build(src, out, mmdb.Options{Profile: p, Lang: info.lang})
			if err != nil {
				t.Fatalf("mmdb.Build: %v", err)
			}
			build := time.Since(buildStart)
			if st.Networks != info.networks {
				t.Errorf("Stats.Networks = %d, maxminddb Networks() yields %d", st.Networks, info.networks)
			}
			db, err := ip2geo.Open(out)
			if err != nil {
				t.Fatalf("ip2geo.Open: %v", err)
			}
			if db.Profile() != p {
				t.Fatalf("Profile() = %s, want %s", db.Profile(), p)
			}

			cmpStart := time.Now()
			c := newParity(t, mm, db, info.lang)
			for _, set := range sets {
				c.check(set)
			}
			t.Logf("%s: %d addresses in %d sets, %d mismatches; build %v, compare %v; %v",
				p, c.addrs, len(sets), c.mismatch, build.Round(time.Millisecond),
				time.Since(cmpStart).Round(time.Millisecond), st)
			addrs += c.addrs
			mismatches += c.mismatch
			c.done()
		})
	}
	t.Logf("summary: %d profiles, %d address comparisons, %d mismatches, %v",
		len(profiles), addrs, mismatches, time.Since(start).Round(time.Millisecond))
}

// TestOpenInMemory checks the one-call path: mmdb.Open builds a city-profile
// reader in memory that agrees with maxminddb on random IPv4 and on IPv6
// addresses inside the source's networks.
func TestOpenInMemory(t *testing.T) {
	src := sourceMMDB(t)
	info := scanSource(t, src)
	mm := openMaxMind(t, src)
	db := inMemoryReader(t, src, info, ip2geo.ProfileCity)
	if db.Profile() != ip2geo.ProfileCity {
		t.Fatalf("Profile() = %s, want city", db.Profile())
	}

	r := rand.New(rand.NewSource(1))
	c := newParity(t, mm, db, info.lang)
	c.check(addrSet{"ipv4-random", randomAddrs(r, allIPv4, 20_000)})
	c.check(addrSet{"ipv6-in-network", oneInEach(r, everyKth(info.v6, 20_000))})
	t.Logf("summary: %d addresses, %d mismatches", c.addrs, c.mismatch)
	c.done()
}

// TestRegisteredCountryOnly checks networks that carry registered_country
// but no country (GeoIP2-City has a few thousand, DB-IP Lite none). They
// have none of the fields the country profile keeps, yet maxminddb finds
// them, so ip2geo must return ok == true, with an empty Record when the
// network has none of the profile's fields either.
func TestRegisteredCountryOnly(t *testing.T) {
	src := sourceMMDB(t)
	info := scanSource(t, src)
	mm := openMaxMind(t, src)

	for _, p := range []ip2geo.Profile{ip2geo.ProfileCountry, ip2geo.ProfileCity} {
		t.Run(p.String(), func(t *testing.T) {
			if len(info.regOnly) == 0 {
				t.Skipf("%s has no network with registered_country but no country", src)
			}
			db := inMemoryReader(t, src, info, p)

			// The first address of each network and a random one inside it.
			r := rand.New(rand.NewSource(1))
			addrs := make([]netip.Addr, 0, 2*len(info.regOnly))
			for _, pfx := range info.regOnly {
				addrs = append(addrs, pfx.Masked().Addr(), randomIn(r, pfx))
			}
			c := newParity(t, mm, db, info.lang)
			found, _ := c.check(addrSet{"registered-only", addrs})
			if found != len(addrs) {
				t.Errorf("maxminddb found %d of %d addresses inside networks it iterated", found, len(addrs))
			}
			t.Logf("%d registered_country-only networks (first %s), %d addresses, %d mismatches",
				len(info.regOnly), info.regOnly[0], c.addrs, c.mismatch)
			c.done()

			// With FieldRegistered the same networks carry their registered
			// country, equal to the source's.
			reg, err := mmdb.Open(src, mmdb.Options{Profile: p, Lang: info.lang, Fields: ip2geo.FieldRegistered})
			if err != nil {
				t.Fatal(err)
			}
			bad := 0
			for _, a := range addrs {
				var s struct {
					RC struct {
						ISOCode   string            `maxminddb:"iso_code"`
						GeoNameID uint32            `maxminddb:"geoname_id"`
						EU        bool              `maxminddb:"is_in_european_union"`
						Names     map[string]string `maxminddb:"names"`
					} `maxminddb:"registered_country"`
				}
				if err := mm.Lookup(a).Decode(&s); err != nil {
					t.Fatal(err)
				}
				want := ip2geo.Country{ISO: s.RC.ISOCode, Name: s.RC.Names[info.lang], GeoNameID: s.RC.GeoNameID, IsInEuropeanUnion: s.RC.EU}
				d, ok := reg.LookupDetails(a)
				if !ok || d.RegisteredCountry != want {
					if bad++; bad <= maxReported {
						t.Errorf("%s: ok=%v registered_country %+v, want %+v", a, ok, d.RegisteredCountry, want)
					}
				}
			}
			t.Logf("FieldRegistered: %d addresses, %d registered_country mismatches", len(addrs), bad)
		})
	}
}
