// Command bench compares ip2geo against oschwald/geoip2-golang on the same
// random IP set and prints a speed table. It lives in its own module so the
// comparison dependency never touches the ip2geo library itself.
//
//	go run . -mmdb GeoIP2-City.mmdb -city city.geoc -country country.geoc
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math/rand"
	"net/netip"
	"time"

	"github.com/haxqer/ip2geo"
	geoip2 "github.com/oschwald/geoip2-golang/v2"
)

func main() {
	mmdbPath := flag.String("mmdb", "", "source GeoIP2-City.mmdb")
	cityGeoc := flag.String("city", "city.geoc", "ip2geo city .geoc")
	countryGeoc := flag.String("country", "country.geoc", "ip2geo country .geoc")
	n := flag.Int("n", 5_000_000, "number of random lookups")
	flag.Parse()

	// fixed random IPv4 set
	r := rand.New(rand.NewSource(12345))
	ips := make([]netip.Addr, *n)
	for i := range ips {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], r.Uint32())
		ips[i] = netip.AddrFrom4(b)
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

	type row struct {
		name string
		nsop float64
	}
	var rows []row
	run := func(name string, fn func(netip.Addr) int) {
		var sink int
		for i := 0; i < 100000; i++ { // warmup
			sink += fn(ips[i])
		}
		best := 1e18
		for rep := 0; rep < 3; rep++ {
			t := time.Now()
			s := 0
			for _, ip := range ips {
				s += fn(ip)
			}
			ns := float64(time.Since(t).Nanoseconds()) / float64(*n)
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
	fmt.Printf("\n%d random IPv4 lookups, best of 3 (Apple Silicon)\n\n", *n)
	fmt.Printf("%-26s %10s %12s %12s\n", "reader", "ns/op", "Mlookup/s", "vs geoip2")
	fmt.Printf("%-26s %10s %12s %12s\n", "------", "-----", "---------", "---------")
	for _, r := range rows {
		fmt.Printf("%-26s %10.1f %12.1f %11.1fx\n", r.name, r.nsop, 1000.0/r.nsop, base/r.nsop)
	}
}
