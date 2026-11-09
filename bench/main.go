// Command bench compares ip2geo against oschwald/geoip2-golang on the same
// random IP sets and prints a speed table for IPv4 and one for IPv6. It lives in its own module so the
// comparison dependency never touches the ip2geo library itself.
//
//	go run . -mmdb GeoIP2-City.mmdb -city city.geoc -country country.geoc
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"net/netip"
	"time"

	"github.com/haxqer/ip2geo"
	geoip2 "github.com/oschwald/geoip2-golang/v2"
	"github.com/oschwald/maxminddb-golang/v2"
)

func main() {
	mmdbPath := flag.String("mmdb", "", "source GeoIP2-City.mmdb")
	cityGeoc := flag.String("city", "city.geoc", "ip2geo city .geoc")
	countryGeoc := flag.String("country", "country.geoc", "ip2geo country .geoc")
	n := flag.Int("n", 5_000_000, "number of random lookups per table")
	flag.Parse()

	// fixed random IPv4 set: uniform over the whole address space
	r := rand.New(rand.NewSource(12345))
	v4 := make([]netip.Addr, *n)
	for i := range v4 {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], r.Uint32())
		v4[i] = netip.AddrFrom4(b)
	}
	// fixed random IPv6 set: a random address inside a random IPv6 network
	// of the source, as most of the IPv6 space is not in the database
	nets, err := ipv6Networks(*mmdbPath)
	if err != nil {
		panic(err)
	}
	v6 := make([]netip.Addr, *n)
	for i := range v6 {
		v6[i] = randomIn(r, nets[r.Intn(len(nets))])
	}

	mm, err := geoip2.Open(*mmdbPath)
	if err != nil {
		panic(err)
	}
	defer mm.Close()
	city, err := ip2geo.Open(*cityGeoc)
	if err != nil {
		panic(err)
	}
	defer city.Close()
	country, err := ip2geo.Open(*countryGeoc)
	if err != nil {
		panic(err)
	}
	defer country.Close()

	for _, set := range []struct {
		desc string
		ips  []netip.Addr
	}{
		{"random IPv4 lookups", v4},
		{"IPv6 lookups inside the source's IPv6 networks", v6},
	} {
		table(set.desc, set.ips, mm, city, country)
	}
}

// table times every reader on ips and prints the rows.
func table(desc string, ips []netip.Addr, mm *geoip2.Reader, city, country *ip2geo.Reader) {
	type row struct {
		name string
		nsop float64
	}
	var rows []row
	run := func(name string, fn func(netip.Addr) int) {
		var sink int
		for i := range min(100000, len(ips)) { // warmup
			sink += fn(ips[i])
		}
		best := 1e18
		for rep := 0; rep < 3; rep++ {
			t := time.Now()
			s := 0
			for _, ip := range ips {
				s += fn(ip)
			}
			ns := float64(time.Since(t).Nanoseconds()) / float64(len(ips))
			sink += s
			if ns < best {
				best = ns
			}
		}
		_ = sink
		rows = append(rows, row{name, best})
	}

	// geoip2-golang: full City decode
	run("geoip2-golang City()", func(ip netip.Addr) int {
		rec, err := mm.City(ip)
		if err != nil || rec == nil {
			return 0
		}
		return len(rec.Country.ISOCode) + len(rec.City.Names.English)
	})
	// geoip2-golang: Country only
	run("geoip2-golang Country()", func(ip netip.Addr) int {
		rec, err := mm.Country(ip)
		if err != nil || rec == nil {
			return 0
		}
		return len(rec.Country.ISOCode)
	})
	// ip2geo: city profile
	run("ip2geo city Lookup()", func(ip netip.Addr) int {
		rec, ok := city.Lookup(ip)
		if !ok {
			return 0
		}
		return len(rec.CountryISO) + len(rec.City)
	})
	// ip2geo: country profile
	run("ip2geo country Lookup()", func(ip netip.Addr) int {
		rec, ok := country.Lookup(ip)
		if !ok {
			return 0
		}
		return len(rec.CountryISO)
	})

	base := rows[0].nsop // geoip2 City
	fmt.Printf("\n%d %s, best of 3 (Apple Silicon)\n\n", len(ips), desc)
	fmt.Printf("%-26s %10s %12s %12s\n", "reader", "ns/op", "Mlookup/s", "vs geoip2")
	fmt.Printf("%-26s %10s %12s %12s\n", "------", "-----", "---------", "---------")
	for _, r := range rows {
		fmt.Printf("%-26s %10.1f %12.1f %11.1fx\n", r.name, r.nsop, 1000.0/r.nsop, base/r.nsop)
	}
}

// ipv6Networks returns the IPv6 networks of the .mmdb at path, without the
// aliases of the IPv4 space.
func ipv6Networks(path string) ([]netip.Prefix, error) {
	db, err := maxminddb.Open(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var nets []netip.Prefix
	for res := range db.Networks() {
		if err := res.Err(); err != nil {
			return nil, err
		}
		if p := res.Prefix(); !p.Addr().Is4() {
			nets = append(nets, p)
		}
	}
	if len(nets) == 0 {
		return nil, fmt.Errorf("%s has no IPv6 networks", path)
	}
	return nets, nil
}

// randomIn returns a uniformly random address inside the IPv6 prefix p.
func randomIn(r *rand.Rand, p netip.Prefix) netip.Addr {
	p = p.Masked()
	a := p.Addr().As16()
	hiHost, loHost := uint64(math.MaxUint64), uint64(math.MaxUint64)
	if bits := p.Bits(); bits < 64 {
		hiHost >>= bits
	} else {
		hiHost, loHost = 0, loHost>>(bits-64)
	}
	binary.BigEndian.PutUint64(a[:8], binary.BigEndian.Uint64(a[:8])|r.Uint64()&hiHost)
	binary.BigEndian.PutUint64(a[8:], binary.BigEndian.Uint64(a[8:])|r.Uint64()&loHost)
	return netip.AddrFrom16(a)
}
