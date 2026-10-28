package mmdb_test

import (
	"math/rand"
	"net/netip"
	"testing"

	"github.com/haxqer/ip2geo"
)

// benchAddrs is the number of addresses each lookup benchmark cycles
// through, generated before timing starts. A power of two, so the loop picks
// the next address with a mask rather than a division.
const benchAddrs = 1 << 20

// benchSink keeps the lookup results observable.
var benchSink int

// BenchmarkLookup measures Reader.Lookup on country- and city-profile readers
// built in memory (once per process) from the City .mmdb named by
// IP2GEO_TEST_MMDB; it skips when that is unset. Run it without -race:
//
//	IP2GEO_TEST_MMDB=GeoIP2-City.mmdb go test -run '^$' -bench BenchmarkLookup -benchtime=2s ./mmdb/
//
// Address sets: ipv4-random is uniform over all of IPv4; ipv4-hits is
// uniform IPv4 kept only where the city reader finds data; ipv6-innetwork is
// a random address inside a random IPv6 network of the source, every
// network equally likely.
func BenchmarkLookup(b *testing.B) {
	src := sourceMMDB(b)
	info := scanSource(b, src)
	country := inMemoryReader(b, src, info, ip2geo.ProfileCountry)
	city := inMemoryReader(b, src, info, ip2geo.ProfileCity)

	r := rand.New(rand.NewSource(1))
	v4 := randomAddrs(r, allIPv4, benchAddrs)
	v4hits := make([]netip.Addr, 0, benchAddrs)
	for tries := 0; len(v4hits) < benchAddrs; tries++ {
		if tries == 100*benchAddrs {
			b.Fatalf("city reader found only %d of %d random IPv4 addresses", len(v4hits), tries)
		}
		if a := randomIn(r, allIPv4); hasData(city, a) {
			v4hits = append(v4hits, a)
		}
	}
	var v6 []netip.Addr
	if len(info.v6) > 0 {
		v6 = make([]netip.Addr, benchAddrs)
		for i := range v6 {
			v6[i] = randomIn(r, info.v6[r.Intn(len(info.v6))])
		}
	}

	for _, bm := range []struct {
		name  string
		db    *ip2geo.Reader
		addrs []netip.Addr
	}{
		{"country/ipv4-random", country, v4},
		{"country/ipv6-innetwork", country, v6},
		{"city/ipv4-random", city, v4},
		{"city/ipv4-hits", city, v4hits},
		{"city/ipv6-innetwork", city, v6},
	} {
		b.Run(bm.name, func(b *testing.B) {
			if len(bm.addrs) == 0 {
				b.Skip("the source has no IPv6 networks")
			}
			benchLookup(b, bm.db, bm.addrs)
		})
	}
}

func hasData(db *ip2geo.Reader, a netip.Addr) bool {
	_, ok := db.Lookup(a)
	return ok
}

// benchLookup calls db.Lookup directly in the loop, never through a func
// value, which would cost an indirect call per lookup. len(addrs) must be
// benchAddrs.
func benchLookup(b *testing.B, db *ip2geo.Reader, addrs []netip.Addr) {
	b.ReportAllocs()
	n := 0
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if rec, ok := db.Lookup(addrs[i&(benchAddrs-1)]); ok {
			n += len(rec.CountryISO) + len(rec.City)
		}
	}
	benchSink = n
}
