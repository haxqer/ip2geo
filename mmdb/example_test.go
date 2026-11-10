package mmdb_test

import (
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/mmdb"
)

// The examples build from a tiny synthetic GeoIP2-City database that
// sampleMMDB writes (with the test encoder in mmdbenc_test.go); in a program
// the source is a downloaded GeoLite2-City.mmdb or GeoIP2-City.mmdb.

// Build the fast reader in memory straight from a MaxMind City .mmdb: the
// one-call, drop-in library workflow (no files, no CLI).
func ExampleOpen() {
	src, cleanup := sampleMMDB() // stands in for GeoLite2-City.mmdb
	defer cleanup()

	db, err := mmdb.Open(src, mmdb.Options{Profile: ip2geo.ProfileCity})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	for _, s := range []string{"1.0.0.1", "2001:db8::1", "1.0.9.1"} {
		rec, ok := db.Lookup(netip.MustParseAddr(s))
		if !ok {
			fmt.Println(s, "not found")
			continue
		}
		fmt.Println(s, rec.CountryISO, rec.RegionName, rec.City, rec.Latitude, rec.Longitude, rec.TimeZone)
	}
	// Output:
	// 1.0.0.1 QT North Testshire Testville 12.3456 -65.4321 Testland/Capitalia
	// 2001:db8::1 QM  Southpole Station -90 180 Mockovia/Pole
	// 1.0.9.1 not found
}

// Options add optional field sets and more languages; LookupDetails reads the
// field sets and WithLanguage switches the names.
func ExampleOpen_options() {
	src, cleanup := sampleMMDB() // stands in for GeoLite2-City.mmdb
	defer cleanup()

	db, err := mmdb.Open(src, mmdb.Options{
		Profile:   ip2geo.ProfileCity,
		Fields:    ip2geo.FieldsAll,
		Languages: []string{"en", "de"},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	ip := netip.MustParseAddr("1.0.0.1")
	d, _ := db.LookupDetails(ip)
	fmt.Println(d.City, d.PostalCode, d.ContinentName, d.IsInEuropeanUnion)

	de, err := db.WithLanguage("de")
	if err != nil {
		log.Fatal(err)
	}
	d, _ = de.LookupDetails(ip)
	fmt.Println(d.City, d.PostalCode, d.ContinentName, d.IsInEuropeanUnion)
	// Output:
	// Testville TE5 7AB Exemplia true
	// Teststadt TE5 7AB Exemplien true
}

// Unsuitable options and sources are reported with sentinel errors; test for
// them with errors.Is.
func ExampleOpen_errors() {
	src, cleanup := sampleMMDB() // stands in for GeoLite2-City.mmdb
	defer cleanup()

	_, err := mmdb.Open(src, mmdb.Options{Profile: ip2geo.ProfileASN})
	fmt.Println(errors.Is(err, mmdb.ErrDatabaseType))

	_, err = mmdb.Open(src, mmdb.Options{Languages: []string{"en", "fr"}})
	fmt.Println(errors.Is(err, mmdb.ErrLanguage))

	_, err = mmdb.Open(src, mmdb.Options{Profile: ip2geo.ProfileCountry, Fields: ip2geo.FieldPostal})
	fmt.Println(errors.Is(err, mmdb.ErrOptions))
	// Output:
	// true
	// true
	// true
}

// Build a .geoc file once, for example in CI after downloading the monthly
// .mmdb, and load it with ip2geo.Open wherever lookups run: that program
// needs neither this package nor maxminddb. A fixed BuildTime makes the build
// reproducible.
func ExampleBuild() {
	src, cleanup := sampleMMDB() // stands in for GeoLite2-City.mmdb
	defer cleanup()
	out := filepath.Join(filepath.Dir(src), "city.geoc")

	st, err := mmdb.Build(src, out, mmdb.Options{
		Profile:     ip2geo.ProfileCity,
		BuildTime:   time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		Description: "GeoLite2-City sample",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%d networks, %d records, %d IPv4 and %d IPv6 boundaries\n",
		st.Networks, st.Records, st.V4Ranges, st.V6Ranges)

	db, err := ip2geo.Open(out)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	md := db.Metadata()
	fmt.Println(md.SourceType, "-", md.Description, "-", md.BuildTime.Format(time.DateOnly))
	rec, _ := db.Lookup(netip.MustParseAddr("1.0.1.1"))
	fmt.Println(rec.CountryISO, rec.City)
	// Output:
	// 3 networks, 3 records, 2 IPv4 and 2 IPv6 boundaries
	// GeoIP2-City - GeoLite2-City sample - 2026-11-01
	// QT Testville
}

// sampleMMDB writes a GeoIP2-City style database of three networks, with
// names in English and German, to a new temporary directory. It returns the
// file's path and a function that removes the directory.
func sampleMMDB() (path string, cleanup func()) {
	names := func(en, de string) map[string]any { return map[string]any{"en": en, "de": de} }
	testville := map[string]any{
		"continent":    map[string]any{"code": "XC", "geoname_id": uint32(9000100), "names": names("Exemplia", "Exemplien")},
		"country":      map[string]any{"iso_code": "QT", "geoname_id": uint32(9000001), "names": names("Testland", "Testlandien"), "is_in_european_union": true},
		"subdivisions": []any{map[string]any{"iso_code": "T1", "geoname_id": uint32(9200001), "names": names("North Testshire", "Nord-Testshire")}},
		"city":         map[string]any{"geoname_id": uint32(9100001), "names": names("Testville", "Teststadt")},
		"location":     map[string]any{"latitude": 12.3456, "longitude": -65.4321, "accuracy_radius": uint16(20), "time_zone": "Testland/Capitalia"},
		"postal":       map[string]any{"code": "TE5 7AB"},
	}
	southpole := map[string]any{
		"continent": map[string]any{"code": "XC", "geoname_id": uint32(9000100), "names": names("Exemplia", "Exemplien")},
		"country":   map[string]any{"iso_code": "QM", "geoname_id": uint32(9000002), "names": names("Mockovïa", "Mockowien")},
		"city":      map[string]any{"geoname_id": uint32(9100002), "names": names("Southpole Station", "Südpolstation")},
		"location":  map[string]any{"latitude": -90.0, "longitude": 180.0, "accuracy_radius": uint16(1000), "time_zone": "Mockovia/Pole"},
	}
	b, err := encodeMMDB(mmdbFixture{
		dbType:    "GeoIP2-City",
		languages: []string{"de", "en"},
		networks: []mmdbNet{
			{"1.0.0.0/24", testville},
			{"1.0.1.0/24", testville},
			{"2001:db8::/32", southpole},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "mmdb-example")
	if err != nil {
		log.Fatal(err)
	}
	path = filepath.Join(dir, "GeoIP2-City-sample.mmdb")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		log.Fatal(err)
	}
	return path, func() { os.RemoveAll(dir) }
}
