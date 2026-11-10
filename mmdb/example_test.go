package mmdb_test

import (
	"fmt"
	"net/netip"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/mmdb"
)

// Build the fast reader in memory straight from a MaxMind City .mmdb — the
// one-call, drop-in library workflow (no files, no CLI).
func ExampleOpen() {
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
