package ip2geo_test

import (
	"fmt"
	"net/netip"

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
// second.
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
