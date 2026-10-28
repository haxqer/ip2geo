// Command ip2geo builds a compact ip2geo database from a MaxMind DB (.mmdb)
// City, Country or Enterprise database and looks up addresses in it.
//
//	ip2geo build  -src GeoIP2-City.mmdb -out city.geoc -profile city
//	ip2geo lookup -db city.geoc 81.2.69.142 2001:4860:4860::8888
//
// build flags:
//
//	-src       source .mmdb (required)
//	-out       output .geoc file, replaced atomically (default city.geoc)
//	-profile   city | region | country (default city)
//	-lang      language of names; must be listed by the source (default en)
//	-latscale  lat/lon decimals kept by the city profile, 0..7; 0 means 4
//	           (default 4)
//
// Errors are printed to stderr as "ip2geo: <command>: <error>". The exit
// status is 0 on success, 1 when a build or lookup fails (including a
// -latscale outside 0..7, an unsuitable source database or an unknown -lang),
// and 2 for usage errors (unknown command or flag, missing -src, unknown
// -profile).
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
	fmt.Fprintln(os.Stderr, "  ip2geo build  -src City.mmdb -out city.geoc [-profile city|region|country] [-lang en] [-latscale 0..7]")
	fmt.Fprintln(os.Stderr, "  ip2geo lookup -db city.geoc <ip> [ip...]")
	os.Exit(2)
}

// fail reports a failed command and exits with status 1.
func fail(cmd string, err error) {
	fmt.Fprintf(os.Stderr, "ip2geo: %s: %v\n", cmd, err)
	os.Exit(1)
}

// usageError reports a command-line mistake, prints the flag usage and exits
// with status 2.
func usageError(fs *flag.FlagSet, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ip2geo: %s: %s\n", fs.Name(), fmt.Sprintf(format, args...))
	fs.Usage()
	os.Exit(2)
}

func build(args []string) {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	src := fs.String("src", "", "source MaxMind DB City, Country or Enterprise .mmdb `file` (required)")
	out := fs.String("out", "city.geoc", "output .geoc `file`, replaced atomically")
	profile := fs.String("profile", "city", "city | region | country")
	lang := fs.String("lang", "en", "`language` of names; must be one the source database lists")
	latscale := fs.Int("latscale", mmdb.DefaultLatScale,
		fmt.Sprintf("lat/lon `decimals` kept by the city profile, 0..%d (0 means %d)", mmdb.MaxLatScale, mmdb.DefaultLatScale))
	_ = fs.Parse(args) // flag.ExitOnError: Parse exits on a bad flag
	if *src == "" {
		usageError(fs, "-src is required")
	}
	if fs.NArg() > 0 {
		usageError(fs, "unexpected argument %q", fs.Arg(0))
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
		usageError(fs, "unknown -profile %q (want city, region or country)", *profile)
	}
	// Range-check before the uint8 conversion, which would wrap 300 to 44.
	if *latscale < 0 || *latscale > mmdb.MaxLatScale {
		fail("build", fmt.Errorf("invalid -latscale %d: must be 0..%d", *latscale, mmdb.MaxLatScale))
	}
	t := time.Now()
	stats, err := mmdb.Build(*src, *out, mmdb.Options{Profile: p, Lang: *lang, LatScale: uint8(*latscale)})
	if err != nil {
		fail("build", err)
	}
	fmt.Printf("built %s in %.1fs\n  %s\n", *out, time.Since(t).Seconds(), stats)
}

func lookup(args []string) {
	fs := flag.NewFlagSet("lookup", flag.ExitOnError)
	dbPath := fs.String("db", "city.geoc", "compact .geoc database")
	_ = fs.Parse(args) // flag.ExitOnError: Parse exits on a bad flag
	if fs.NArg() == 0 {
		usageError(fs, "no addresses given")
	}
	db, err := ip2geo.Open(*dbPath)
	if err != nil {
		fail("lookup", err)
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
