package ip2geo_test

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/netip"
	"os"
	"sync/atomic"
	"time"

	"github.com/haxqer/ip2geo"
)

// The examples read the synthetic databases of the conformance suite in
// testdata/conformance: made-up countries ("Testland", "Mockovïa"), ISO codes
// from the user-assigned ranges and AS numbers for documentation. Its
// README.md lists what each file holds.

// Open a pre-built .geoc file, written once by the ip2geo command, mmdb.Build
// or csvdb.Build, and look up an address. The file is memory-mapped, so Open
// returns in well under a second whatever the database's size, and the
// program needs nothing beyond this package.
func ExampleOpen() {
	db, err := ip2geo.Open("testdata/conformance/city.geoc")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	rec, ok := db.Lookup(netip.MustParseAddr("1.0.0.1"))
	if !ok {
		fmt.Println("not found")
		return
	}
	fmt.Println(rec.CountryISO, rec.CountryName)
	fmt.Println(rec.RegionName, "/", rec.City)
	fmt.Println(rec.Latitude, rec.Longitude, rec.TimeZone)
	// Output:
	// QT Testland
	// North Testshire / Testville
	// 12.3456 -65.4321 Testland/Capitalia
}

// Open tells a file that is not a database, or a damaged one, apart from an
// I/O error with errors.Is.
func ExampleOpen_errors() {
	_, err := ip2geo.Open("testdata/conformance/missing.geoc")
	fmt.Println(errors.Is(err, fs.ErrNotExist))

	_, err = ip2geo.Open("testdata/conformance/README.md")
	fmt.Println(errors.Is(err, ip2geo.ErrBadMagic))

	b, err := os.ReadFile("testdata/conformance/country.geoc")
	if err != nil {
		log.Fatal(err)
	}
	_, err = ip2geo.OpenBytes(b[:len(b)-8]) // truncated
	fmt.Println(errors.Is(err, ip2geo.ErrCorrupt))
	// Output:
	// true
	// true
	// true
}

// Open also reads the GEO1 files written by v0.1.x, converting them at open.
// They have no META section, so most of Metadata is zero.
func ExampleOpen_geo1() {
	db, err := ip2geo.Open("testdata/conformance/geo1-city.geoc")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	md := db.Metadata()
	fmt.Printf("GEO%d %s, languages %v\n", md.FormatVersion, md.Profile, db.Languages())
	rec, _ := db.Lookup(netip.MustParseAddr("255.255.255.255"))
	fmt.Println(rec.CountryISO, rec.City)
	// Output:
	// GEO1 city, languages []
	// QT Oldtown
}

// OpenBytes reads a database already in memory, for example one embedded in
// the binary with go:embed:
//
//	//go:embed country.geoc
//	var countryDB []byte
//
// It copies b unless NoCopy is given.
func ExampleOpenBytes() {
	b, err := os.ReadFile("testdata/conformance/country.geoc")
	if err != nil {
		log.Fatal(err)
	}
	db, err := ip2geo.OpenBytes(b)
	if err != nil {
		log.Fatal(err)
	}

	for _, ip := range []string{"1.0.0.1", "1.0.2.1", "2001:db8::1"} {
		rec, _ := db.Lookup(netip.MustParseAddr(ip))
		fmt.Println(ip, rec.CountryISO, rec.CountryName)
	}
	// Output:
	// 1.0.0.1 QT Testland
	// 1.0.2.1 QM Mockovïa
	// 2001:db8::1 QM Mockovïa
}

// NoCopy makes OpenBytes use the slice in place: with an embedded database
// the Reader then costs almost no heap. The slice must not change while the
// Reader is in use.
func ExampleNoCopy() {
	b, err := os.ReadFile("testdata/conformance/country.geoc")
	if err != nil {
		log.Fatal(err)
	}
	db, err := ip2geo.OpenBytes(b, ip2geo.NoCopy())
	if err != nil {
		log.Fatal(err)
	}

	rec, ok := db.Lookup(netip.MustParseAddr("10.1.2.3"))
	fmt.Println(rec.CountryISO, ok)
	// Output:
	// XS true
}

// NoMmap reads the file into the heap instead of mapping it, for a file
// system where mapping is unwelcome (a network mount) or a file that may be
// changed in place.
func ExampleNoMmap() {
	db, err := ip2geo.Open("testdata/conformance/region.geoc", ip2geo.NoMmap())
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	rec, _ := db.Lookup(netip.MustParseAddr("1.0.0.1"))
	fmt.Println(rec.CountryISO, rec.RegionISO, rec.RegionName)
	// Output:
	// QT T1 North Testshire
}

// SkipChecksum leaves out the CRC-32C check of every section, which reads the
// whole file at open: Open of a large mapped file then touches only the pages
// it validates. The structural checks still run, so lookups stay safe.
func ExampleSkipChecksum() {
	db, err := ip2geo.Open("testdata/conformance/city.geoc", ip2geo.SkipChecksum())
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	rec, _ := db.Lookup(netip.MustParseAddr("2001:db8::1"))
	fmt.Println(rec.CountryISO, rec.City)
	// Output:
	// QT Testville
}

// Language selects the language of the names when the database has several.
// A name missing in that language is returned in the primary one; a language
// the database does not have is an error.
func ExampleLanguage() {
	db, err := ip2geo.Open("testdata/conformance/city.geoc", ip2geo.Language("de"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	rec, _ := db.Lookup(netip.MustParseAddr("1.0.0.1"))
	fmt.Println(rec.CountryName, "/", rec.RegionName, "/", rec.City)

	_, err = ip2geo.Open("testdata/conformance/city.geoc", ip2geo.Language("fr"))
	fmt.Println(errors.Is(err, ip2geo.ErrLanguage))
	// Output:
	// Testlandien / Nord-Testshire / Teststadt
	// true
}

// Look up IPv4 and IPv6 addresses. The IPv6 forms of an IPv4 address
// (IPv4-mapped, 6to4, Teredo) answer from the IPv4 data; ok is false for an
// address the database does not cover.
func ExampleReader_Lookup() {
	db, err := ip2geo.Open("testdata/conformance/city.geoc")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	for _, s := range []string{"1.0.2.1", "2001:db8::1", "::ffff:1.0.2.1", "2002:100:201::", "192.0.2.1"} {
		rec, ok := db.Lookup(netip.MustParseAddr(s))
		if !ok {
			fmt.Printf("%-15s not found\n", s)
			continue
		}
		fmt.Printf("%-15s %s %s (%g, %g)\n", s, rec.CountryISO, rec.City, rec.Latitude, rec.Longitude)
	}
	// Output:
	// 1.0.2.1         QM Southpole Station (-90, 180)
	// 2001:db8::1     QT Testville (12.3456, -65.4321)
	// ::ffff:1.0.2.1  QM Southpole Station (-90, 180)
	// 2002:100:201::  QM Southpole Station (-90, 180)
	// 192.0.2.1       not found
}

// LookupDetails returns the Record together with the optional field sets the
// database was built with (Reader.Fields).
func ExampleReader_LookupDetails() {
	db, err := ip2geo.Open("testdata/conformance/city.geoc")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	d, ok := db.LookupDetails(netip.MustParseAddr("1.0.0.1"))
	if !ok {
		fmt.Println("not found")
		return
	}
	fmt.Println(d.City, d.PostalCode, d.ContinentName, "EU:", d.IsInEuropeanUnion)
	fmt.Println("registered in", d.RegisteredCountry.ISO, "- represents", d.RepresentedCountry.Name, "("+d.RepresentedCountryType+")")
	fmt.Println("subdivisions:", d.RegionISO, d.Region2ISO, "- anycast:", d.IsAnycast)
	if d.HasCoordinates {
		fmt.Printf("at %g, %g within %d km\n", d.Latitude, d.Longitude, d.AccuracyRadius)
	}
	// Output:
	// Testville TE5 7AB Exemplia EU: true
	// registered in QT - represents Mockovïa (military)
	// subdivisions: T1 T1A - anycast: true
	// at 12.3456, -65.4321 within 20 km
}

// An ASN database maps addresses to autonomous systems. AS number 0 with no
// organization is still a found answer; ok is false only outside the data.
func ExampleReader_ASN() {
	db, err := ip2geo.Open("testdata/conformance/asn.geoc")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	for _, s := range []string{"1.0.0.1", "2001:db8::1", "5.6.7.8", "192.0.2.1"} {
		as, ok := db.ASN(netip.MustParseAddr(s))
		fmt.Printf("%s: AS%d %q %v\n", s, as.Number, as.Organization, ok)
	}
	// Output:
	// 1.0.0.1: AS64496 "Example Transit Ltd" true
	// 2001:db8::1: AS64511 "Ünicode Networks 🧪" true
	// 5.6.7.8: AS0 "" true
	// 192.0.2.1: AS0 "" false
}

// LookupRange returns the whole range that shares an address's answer, for
// example to cache it, or to aggregate log lines by network. A gap is
// returned too, with ok false. An IPv6 range stops short of the prefixes
// answered from the IPv4 data: here the stored 2000::/3 is cut after Teredo
// 2001::/32 and before 6to4 2002::/16.
func ExampleReader_LookupRange() {
	db, err := ip2geo.Open("testdata/conformance/country.geoc")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	for _, s := range []string{"1.0.2.77", "1.0.5.1", "2001:db8::1"} {
		r, ok := db.LookupRange(netip.MustParseAddr(s))
		fmt.Println(s, "in", r.First, "-", r.Last, ok)
	}
	// Output:
	// 1.0.2.77 in 1.0.2.0 - 1.0.3.255 true
	// 1.0.5.1 in 1.0.4.0 - 1.0.7.255 false
	// 2001:db8::1 in 2001:1:: - 2001:ffff:ffff:ffff:ffff:ffff:ffff:ffff true
}

// Ranges walks every range with data in address order, IPv4 first, for
// example to export the database or load it into another system.
func ExampleReader_Ranges() {
	db, err := ip2geo.Open("testdata/conformance/asn.geoc")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	for r, d := range db.Ranges() {
		if d.ASN.Number == 0 {
			continue
		}
		fmt.Printf("%s-%s AS%d\n", r.First, r.Last, d.ASN.Number)
	}
	// Output:
	// 1.0.0.0-1.0.1.255 AS64496
	// 1.0.2.0-1.0.2.255 AS4200000000
	// 6.0.0.0-6.255.255.255 AS65551
	// 7.0.0.0-7.255.255.255 AS64511
	// 255.0.0.0-255.255.255.255 AS4294967295
	// 2001:db8::-2001:db8:ffff:ffff:ffff:ffff:ffff:ffff AS64511
	// 2400::-240f:ffff:ffff:ffff:ffff:ffff:ffff:ffff AS4200000000
}

// Describe a database: its format, profile, source and build time, and the
// sizes of its tables (the ip2geo info command prints the same).
func ExampleReader_Metadata() {
	db, err := ip2geo.Open("testdata/conformance/city.geoc")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	md := db.Metadata()
	fmt.Printf("GEO%d %s profile, built %s from %s\n",
		md.FormatVersion, md.Profile, md.BuildTime.Format(time.DateOnly), md.SourceType)
	fmt.Printf("%d records, %d IPv4 and %d IPv6 ranges\n", md.Records, md.IPv4Ranges, md.IPv6Ranges)
	// Output:
	// GEO2 city profile, built 2026-01-02 from Conformance-City
	// 12 records, 80 IPv4 and 94 IPv6 ranges
}

// Languages lists the languages of the names, the primary one first, and
// WithLanguage gives a Reader in each of them that shares the database.
func ExampleReader_WithLanguage() {
	db, err := ip2geo.Open("testdata/conformance/city.geoc")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	ip := netip.MustParseAddr("1.0.0.1")
	for _, lang := range db.Languages() {
		ldb, err := db.WithLanguage(lang)
		if err != nil {
			log.Fatal(err)
		}
		rec, _ := ldb.Lookup(ip)
		fmt.Println(lang, rec.CountryName, rec.City)
	}
	// Output:
	// en Testland Testville
	// de Testlandien Teststadt
	// zh-CN 测试国 测试市
}

// Fields reports the optional field sets a database holds; Details fields of
// the other sets are always zero.
func ExampleReader_Fields() {
	for _, name := range []string{"country", "city"} {
		db, err := ip2geo.Open("testdata/conformance/" + name + ".geoc")
		if err != nil {
			log.Fatal(err)
		}
		f := db.Fields()
		fmt.Printf("%s: postal %v, EU %v, all %v\n", name,
			f&ip2geo.FieldPostal != 0, f&ip2geo.FieldEU != 0, f == ip2geo.FieldsAll)
		db.Close()
	}
	// Output:
	// country: postal false, EU false, all false
	// city: postal true, EU true, all true
}

// Profile tells how much detail a database keeps, and whether it is an ASN
// database.
func ExampleReader_Profile() {
	for _, name := range []string{"country", "region", "city", "asn"} {
		db, err := ip2geo.Open("testdata/conformance/" + name + ".geoc")
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(name+".geoc:", db.Profile(), db.Profile() == ip2geo.ProfileASN)
		db.Close()
	}
	// Output:
	// country.geoc: country false
	// region.geoc: region false
	// city.geoc: city false
	// asn.geoc: asn true
}

// Country is Lookup under the name geoip2-golang uses; with a country
// database only the country fields are set.
func ExampleReader_Country() {
	db, err := ip2geo.Open("testdata/conformance/country.geoc")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	rec, ok := db.Country(netip.MustParseAddr("1.0.0.1"))
	fmt.Printf("%s %s %q %v\n", rec.CountryISO, rec.Continent, rec.City, ok)
	// Output:
	// QT XC "" true
}

// City is Lookup under the name geoip2-golang uses.
func ExampleReader_City() {
	db, err := ip2geo.Open("testdata/conformance/city.geoc")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	rec, _ := db.City(netip.MustParseAddr("1.0.3.1"))
	fmt.Println(rec.City, rec.TimeZone)
	// Output:
	// Northpole Camp Mockovia/Pole
}

// Replace the database of a running program: lookups load the current Reader
// from an atomic.Pointer, and the reloader swaps in a new one and closes the
// old one. Close never waits and never invalidates the old Reader, so a
// lookup still running on it finishes normally; its memory is released once
// nothing refers to it. examples/httpserver does this for an HTTP service.
func ExampleReader_Close() {
	var current atomic.Pointer[ip2geo.Reader]
	open := func(path string) {
		db, err := ip2geo.Open(path)
		if err != nil {
			log.Print(err) // keep serving the old database
			return
		}
		if old := current.Swap(db); old != nil {
			old.Close()
		}
	}

	ip := netip.MustParseAddr("1.0.0.1")
	open("testdata/conformance/country.geoc")
	rec, _ := current.Load().Lookup(ip)
	fmt.Printf("%s %q\n", rec.CountryISO, rec.City)

	open("testdata/conformance/city.geoc")
	rec, _ = current.Load().Lookup(ip)
	fmt.Printf("%s %q\n", rec.CountryISO, rec.City)
	// Output:
	// QT ""
	// QT "Testville"
}

// Profile names are the ones the ip2geo command accepts.
func ExampleProfile_String() {
	for _, p := range []ip2geo.Profile{ip2geo.ProfileCountry, ip2geo.ProfileRegion, ip2geo.ProfileCity, ip2geo.ProfileASN} {
		fmt.Print(p, " ")
	}
	fmt.Println()
	// Output:
	// country region city asn
}

// RecWidth is the record width of a legacy GEO1 file, where every field is a
// u32; it matters only to tools that inspect such files.
func ExampleRecWidth() {
	for _, p := range []ip2geo.Profile{ip2geo.ProfileCountry, ip2geo.ProfileRegion, ip2geo.ProfileCity} {
		fmt.Println(p, ip2geo.RecWidth(p))
	}
	// Output:
	// country 16
	// region 24
	// city 48
}
