// Command ip2geo builds a compact ip2geo database from a MaxMind City .mmdb and
// looks up addresses in it.
//
//	ip2geo build  -src GeoIP2-City.mmdb -out city.geoc -profile city
//	ip2geo lookup -db city.geoc 81.2.69.142 2001:4860:4860::8888
package main

import (
	"flag"
	"fmt"
	"net/netip"
	"os"
	"time"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/mmdb"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "build":
		build(os.Args[2:])
	case "lookup":
		lookup(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  ip2geo build  -src City.mmdb -out city.geoc [-profile city|region|country] [-lang en]")
	fmt.Fprintln(os.Stderr, "  ip2geo lookup -db city.geoc <ip> [ip...]")
	os.Exit(2)
}

func build(args []string) {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	src := fs.String("src", "", "source MaxMind City .mmdb")
	out := fs.String("out", "city.geoc", "output .geoc file")
	profile := fs.String("profile", "city", "city | region | country")
	lang := fs.String("lang", "en", "language code for names")
	latscale := fs.Int("latscale", 4, "lat/lon decimals kept (city profile)")
	fs.Parse(args)
	if *src == "" {
		fs.Usage()
		os.Exit(2)
	}
	var p ip2geo.Profile
	switch *profile {
	case "country":
		p = ip2geo.ProfileCountry
	case "region":
		p = ip2geo.ProfileRegion
	case "city":
		p = ip2geo.ProfileCity
	default:
		fmt.Fprintf(os.Stderr, "unknown profile %q\n", *profile)
		os.Exit(2)
	}
	t := time.Now()
	stats, err := mmdb.Build(*src, *out, mmdb.Options{Profile: p, Lang: *lang, LatScale: uint8(*latscale)})
	if err != nil {
		fmt.Fprintln(os.Stderr, "build failed:", err)
		os.Exit(1)
	}
	fmt.Printf("built %s in %.1fs\n  %s\n", *out, time.Since(t).Seconds(), stats)
}

func lookup(args []string) {
	fs := flag.NewFlagSet("lookup", flag.ExitOnError)
	dbPath := fs.String("db", "city.geoc", "compact .geoc database")
	fs.Parse(args)
	if fs.NArg() == 0 {
		fs.Usage()
		os.Exit(2)
	}
	db, err := ip2geo.Open(*dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open failed:", err)
		os.Exit(1)
	}
	defer db.Close()
	for _, s := range fs.Args() {
		ip, err := netip.ParseAddr(s)
		if err != nil {
			fmt.Printf("%s: invalid address\n", s)
			continue
		}
		rec, ok := db.Lookup(ip)
		if !ok {
			fmt.Printf("%s: no data\n", s)
			continue
		}
		fmt.Printf("%s: %s / %s / %s  (%.4f, %.4f) %s\n",
			s, rec.CountryISO, rec.RegionName, rec.City, rec.Latitude, rec.Longitude, rec.TimeZone)
	}
}
