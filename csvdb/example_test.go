package csvdb_test

import (
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/csvdb"
)

// The examples read the small synthetic files in testdata, which are in the
// layouts of DB-IP's Lite CSV downloads (see testdata/README.md).

// Build a reader in memory straight from a DB-IP City Lite CSV. Rows marked
// "ZZ" (reserved ranges) have no data, as in DB-IP's .mmdb files.
func ExampleOpen() {
	db, err := csvdb.Open("testdata/dbip-city-lite-sample.csv",
		csvdb.Options{Format: csvdb.DBIPCity, Profile: ip2geo.ProfileCity})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	for _, s := range []string{"1.0.1.7", "1.0.8.1", "2001:db8::1", "10.0.0.1"} {
		rec, ok := db.Lookup(netip.MustParseAddr(s))
		if !ok {
			fmt.Println(s, "not found")
			continue
		}
		fmt.Printf("%s %s %s %q (%g, %g)\n", s, rec.Continent, rec.CountryISO, rec.City, rec.Latitude, rec.Longitude)
	}
	// Output:
	// 1.0.1.7 XC QT "Testville" (12.3456, -65.4321)
	// 1.0.8.1 XC XS "Roundington, \"Old Town\"" (45.1235, -0.0001)
	// 2001:db8::1 XC QT "Testville" (12.3456, -65.4321)
	// 10.0.0.1 not found
}

// An ASN CSV builds an ASN database, read with Reader.ASN.
func ExampleOpen_asn() {
	db, err := csvdb.Open("testdata/dbip-asn-lite-sample.csv",
		csvdb.Options{Format: csvdb.DBIPASN, Profile: ip2geo.ProfileASN})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	for _, s := range []string{"1.0.1.200", "2001:db8::1"} {
		as, ok := db.ASN(netip.MustParseAddr(s))
		fmt.Printf("%s AS%d %s %v\n", s, as.Number, as.Organization, ok)
	}
	// Output:
	// 1.0.1.200 AS64496 Example Transit Ltd true
	// 2001:db8::1 AS64511 Example Networks, Inc. true
}

// Options that do not fit the source fail before anything is read or
// written; test the cause with errors.Is.
func ExampleOpen_errors() {
	_, err := csvdb.Open("testdata/dbip-asn-lite-sample.csv",
		csvdb.Options{Format: csvdb.DBIPASN, Profile: ip2geo.ProfileCity})
	fmt.Println(errors.Is(err, csvdb.ErrDatabaseType))

	_, err = csvdb.Open("testdata/dbip-city-lite-sample.csv",
		csvdb.Options{Format: csvdb.DBIPCity, LatScale: csvdb.MaxLatScale + 1})
	fmt.Println(errors.Is(err, csvdb.ErrOptions))
	// Output:
	// true
	// true
}

// Build a .geoc file once, for example in CI after downloading the monthly
// CSV, and load it with ip2geo.Open wherever lookups run. A fixed BuildTime
// makes the build reproducible.
func ExampleBuild() {
	dir, err := os.MkdirTemp("", "csvdb-example")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "city.geoc")

	st, err := csvdb.Build("testdata/dbip-city-lite-sample.csv", out, csvdb.Options{
		Format:      csvdb.DBIPCity,
		Profile:     ip2geo.ProfileCity,
		Fields:      ip2geo.FieldsAll, // what DB-IP City has: FieldLocation
		BuildTime:   time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		Description: "DB-IP City Lite sample",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%d networks, %d records, %d IPv4 boundaries\n", st.Networks, st.Records, st.V4Ranges)

	db, err := ip2geo.Open(out)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	md := db.Metadata()
	fmt.Println(md.SourceType, "-", md.Description)
	d, _ := db.LookupDetails(netip.MustParseAddr("1.0.2.1"))
	fmt.Println(d.City, d.HasCoordinates, db.Fields() == ip2geo.FieldLocation)
	// Output:
	// 5 networks, 4 records, 5 IPv4 boundaries
	// DBIP-City-Lite (CSV) - DB-IP City Lite sample
	// Southpole Station true true
}

// ParseFormat reads a layout name, for example from a command-line flag;
// Format.String gives the name back.
func ExampleParseFormat() {
	f, err := csvdb.ParseFormat("ip2location")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(f == csvdb.IP2Location, f)

	_, err = csvdb.ParseFormat("maxmind")
	fmt.Println(errors.Is(err, csvdb.ErrOptions))
	// Output:
	// true ip2location
	// true
}

// The names of the formats are the ones ParseFormat and the ip2geo command's
// -format flag accept.
func ExampleFormat_String() {
	for _, f := range []csvdb.Format{csvdb.DBIPCity, csvdb.DBIPCountry, csvdb.DBIPASN, csvdb.IP2Location, csvdb.IP2LocationASN} {
		fmt.Println(f)
	}
	// Output:
	// dbip-city
	// dbip-country
	// dbip-asn
	// ip2location
	// ip2location-asn
}
