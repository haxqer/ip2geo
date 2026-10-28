package ip2geo_test

import (
	"fmt"
	"net/netip"
	"time"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/mmdb"
)

// Build the fast reader in memory straight from a MaxMind City .mmdb — the
// one-call, drop-in library workflow (no files, no CLI).
func ExampleReader_fromMMDB() {
	db, err := mmdb.Open("GeoIP2-City.mmdb", mmdb.Options{Profile: ip2geo.ProfileCity})
	if err != nil {
		panic(err)
	}
	defer db.Close()

	rec, ok := db.Lookup(netip.MustParseAddr("81.2.69.142"))
	if !ok {
		fmt.Println("no data")
		return
	}
	fmt.Println(rec.CountryISO, rec.City)
}

// Load a pre-built .geoc file (created once with mmdb.Build or the ip2geo CLI).
// This path pulls in no third-party dependencies and starts in well under a
// second: the file is memory-mapped. A database already in memory, for
// example one embedded with go:embed, opens with OpenBytes; NoCopy uses the
// slice in place instead of copying it:
//
//	db, err := ip2geo.OpenBytes(b, ip2geo.NoCopy())
func ExampleOpen() {
	db, err := ip2geo.Open("city.geoc")
	if err != nil {
		panic(err)
	}
	defer db.Close()

	if rec, ok := db.Lookup(netip.MustParseAddr("8.8.8.8")); ok {
		fmt.Println(rec.CountryISO)
	}
}

// Describe a database: its format, profile, source and build time, and the
// sizes of its tables (the ip2geo info command prints the same).
func ExampleReader_Metadata() {
	db, err := ip2geo.Open("city.geoc")
	if err != nil {
		panic(err)
	}
	defer db.Close()

	md := db.Metadata()
	fmt.Printf("GEO%d %s profile, built %s from %s (%s)\n",
		md.FormatVersion, md.Profile, md.BuildTime.Format(time.DateOnly), md.SourceType, md.Description)
	fmt.Printf("%d records, %d IPv4 and %d IPv6 ranges\n", md.Records, md.IPv4Ranges, md.IPv6Ranges)
}
