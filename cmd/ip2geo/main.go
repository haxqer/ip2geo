// Command ip2geo builds a compact ip2geo database from a MaxMind DB (.mmdb)
// City, Country, Enterprise or ASN database, or from a DB-IP or IP2Location
// CSV file, looks up addresses in it and prints its metadata.
//
//	ip2geo build  -src GeoIP2-City.mmdb -out city.geoc -profile city
//	ip2geo build  -src GeoIP2-City.mmdb -out city-de.geoc -fields all -lang en,de
//	ip2geo build  -src GeoLite2-ASN.mmdb -out asn.geoc -profile asn
//	ip2geo build  -src dbip-city-lite-2026-10.csv.gz -out city.geoc -profile city
//	ip2geo build  -src IP2LOCATION-LITE-DB11.IPV6.CSV -format ip2location -out city.geoc
//	ip2geo lookup -db city.geoc 81.2.69.142 2001:4860:4860::8888
//	ip2geo info   -db city.geoc
//
// build flags:
//
//	-src          source .mmdb or CSV file (required); a CSV file whose
//	              name ends in .gz is decompressed as it is read
//	-format       the source's format: mmdb, dbip-city, dbip-country,
//	              dbip-asn, ip2location (DB1, DB3, DB5, DB9 or DB11) or
//	              ip2location-asn; by default mmdb for a -src ending in
//	              .mmdb, and dbip-city, dbip-country or dbip-asn for one
//	              named dbip-city-lite*.csv, dbip-country-lite*.csv or
//	              dbip-asn-lite*.csv (.gz); required otherwise
//	-out          output .geoc file, replaced atomically (default
//	              <profile>.geoc: city.geoc, region.geoc, country.geoc or
//	              asn.geoc)
//	-profile      city | region | country | asn (default city); asn needs an
//	              ASN source and the others a City, Country or Enterprise one
//	              (or a CSV layout with that data)
//	-fields       optional field sets, a comma-separated list of eu,
//	              continent, registered, subdivisions, postal, metro, traits
//	              and location, or all for every set the profile allows
//	              (default none); see ip2geo.FieldSet
//	-lang         languages of names, comma-separated, the primary one first;
//	              each must be listed by the source (default en); a CSV
//	              source has English names only
//	-latscale     lat/lon decimals kept by the city profile, 0..7; 0 means 4
//	              (default 4)
//	-description  free text recorded in the file's metadata (default empty)
//
// build writes the GEO2 format of docs/FORMAT.md; lookup and info also read
// the GEO1 files written by v0.1.x. Packages mmdb and csvdb document what is
// kept from each kind of source.
//
// lookup prints one line per address with what the database's profile
// keeps: the country ISO code, then for the region and city profiles the
// region name, then for the city profile the city name, joined by " / ",
// as in
//
//	81.2.69.142: GB / England / London  (51.5142, -0.0931) Europe/London
//
// The city profile adds the coordinates in parentheses, unless the
// database has the location field set and the source has no coordinates
// for the address, and the time zone when there is one. Names missing at
// the end are left out, a missing one before a present one is printed as
// "-". For an ASN database the line has the AS number and organization.
// When the database has field sets, an indented "key: value" line follows
// for each of their fields, with the parts of the value the source does not
// have left out ("key:" when it has none). An address without data prints
// "no data". An address that does not parse is reported on stderr, and the
// others are still looked up. -lang selects one of the database's
// languages for the names (default the primary one). Text from the
// database that is not printable is quoted, as by info.
//
// info prints the file size and the database's metadata, one "key: value"
// per line: format (GEO2 or GEO1), profile, lat_scale, build_time,
// source_type, source_time, language, languages, fields, description,
// builder, networks, distinct_records, records, strings, ipv4_ranges and
// ipv6_ranges. Times are RFC 3339 in UTC, or "unknown"; lists are
// comma-separated; text that is not printable is quoted. A GEO1 file has no
// metadata section, so for one info prints format, profile, lat_scale and
// the four counts only.
//
// Errors are printed to stderr as "ip2geo: <command>: <error>". The exit
// status is 0 on success, 1 when a build, lookup or info fails (including a
// lookup address that is not a valid IP address, a -latscale outside 0..7, a
// -description that is not valid UTF-8 or is longer than 65,535 bytes,
// -fields the profile does not allow, an unsuitable source database or a
// malformed CSV file, an unknown, empty or repeated -lang, a -lang other than
// en with a CSV source, a gzipped .mmdb, and a file that is not a valid
// database), and 2 for usage errors (unknown command or flag,
// missing -src, unknown -format, no -format for a -src whose name does not
// tell its format, unknown -profile or -fields name, unexpected arguments,
// lookup without addresses).
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/csvdb"
	"github.com/haxqer/ip2geo/mmdb"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the command line args (without the program name) and returns
// the exit status.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return usage(stderr)
	}
	switch args[0] {
	case "build":
		return build(args[1:], stdout, stderr)
	case "lookup":
		return lookup(args[1:], stdout, stderr)
	case "info":
		return info(args[1:], stdout, stderr)
	default:
		return usage(stderr)
	}
}

func usage(stderr io.Writer) int {
	fmt.Fprintln(stderr, "usage:")
	fmt.Fprintln(stderr, "  ip2geo build  -src City.mmdb|source.csv[.gz] [-out <profile>.geoc] [-format "+strings.Join(formats, "|")+"]")
	fmt.Fprintln(stderr, "                [-profile city|region|country|asn] [-fields eu,continent,...|all]")
	fmt.Fprintln(stderr, "                [-lang en[,de,...]] [-latscale 0..7] [-description text]")
	fmt.Fprintln(stderr, "  ip2geo lookup -db city.geoc [-lang de] <ip> [ip...]")
	fmt.Fprintln(stderr, "  ip2geo info   -db city.geoc")
	return 2
}

// fail reports a failed command and returns exit status 1.
func fail(stderr io.Writer, cmd string, err error) int {
	fmt.Fprintf(stderr, "ip2geo: %s: %v\n", cmd, err)
	return 1
}

// usageError reports a command-line mistake, prints the flag usage and
// returns exit status 2.
func usageError(fs *flag.FlagSet, format string, args ...any) int {
	fmt.Fprintf(fs.Output(), "ip2geo: %s: %s\n", fs.Name(), fmt.Sprintf(format, args...))
	fs.Usage()
	return 2
}

// parse parses a command's flags as flag.ExitOnError would, returning the
// exit status instead of exiting: 0 for -h or -help, 2 for a bad flag (the
// flag package has printed the error and the usage), and -1 to carry on.
func parse(fs *flag.FlagSet, args []string) int {
	switch err := fs.Parse(args); {
	case err == nil:
		return -1
	case errors.Is(err, flag.ErrHelp):
		return 0
	default:
		return 2
	}
}

func build(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(stderr)
	src := fs.String("src", "", "source `file`: a MaxMind DB City, Country, Enterprise or ASN .mmdb, or a DB-IP or IP2Location CSV, gzipped or not (required)")
	format := fs.String("format", "", "source `format`: "+strings.Join(formats, ", ")+
		" (default from the -src name: mmdb for *.mmdb, dbip-city for dbip-city-lite*.csv[.gz], and so on)")
	out := fs.String("out", "", "output .geoc `file`, replaced atomically (default <profile>.geoc, e.g. asn.geoc for -profile asn)")
	profile := fs.String("profile", "city", "city | region | country | asn")
	fields := fs.String("fields", "", "optional field `sets`: comma-separated "+strings.Join(fieldSetNames(), ", ")+", or all")
	lang := fs.String("lang", "en", "`languages` of names, comma-separated, the primary one first; each must be one the source database lists")
	latscale := fs.Int("latscale", mmdb.DefaultLatScale,
		fmt.Sprintf("lat/lon `decimals` kept by the city profile, 0..%d (0 means %d)", mmdb.MaxLatScale, mmdb.DefaultLatScale))
	desc := fs.String("description", "", "free `text` recorded in the file's metadata (shown by ip2geo info)")
	if st := parse(fs, args); st >= 0 {
		return st
	}
	if *src == "" {
		return usageError(fs, "-src is required")
	}
	if fs.NArg() > 0 {
		return usageError(fs, "unexpected argument %q", fs.Arg(0))
	}
	f, err := sourceFormat(*src, *format)
	if err != nil {
		return usageError(fs, "%v", err)
	}
	var p ip2geo.Profile
	switch *profile {
	case "country":
		p = ip2geo.ProfileCountry
	case "region":
		p = ip2geo.ProfileRegion
	case "city":
		p = ip2geo.ProfileCity
	case "asn":
		p = ip2geo.ProfileASN
	default:
		return usageError(fs, "unknown -profile %q (want city, region, country or asn)", *profile)
	}
	if *out == "" {
		*out = *profile + ".geoc"
	}
	fset, err := parseFields(*fields)
	if err != nil {
		return usageError(fs, "%v", err)
	}
	// Range-check before the uint8 conversion, which would wrap 300 to 44.
	if *latscale < 0 || *latscale > mmdb.MaxLatScale {
		return fail(stderr, "build", fmt.Errorf("invalid -latscale %d: must be 0..%d", *latscale, mmdb.MaxLatScale))
	}
	t := time.Now()
	var stats fmt.Stringer
	if f == "mmdb" {
		if strings.HasSuffix(strings.ToLower(*src), ".gz") {
			return fail(stderr, "build", fmt.Errorf("%s: a gzipped .mmdb cannot be read; decompress it first", *src))
		}
		stats, err = mmdb.Build(*src, *out, mmdb.Options{
			Profile: p, Fields: fset, Languages: strings.Split(*lang, ","), LatScale: uint8(*latscale), Description: *desc,
		})
	} else {
		if *lang != "en" {
			return fail(stderr, "build", fmt.Errorf("-lang %q: a CSV source has English names only", *lang))
		}
		cf, _ := csvdb.ParseFormat(f) // sourceFormat accepted f
		stats, err = csvdb.Build(*src, *out, csvdb.Options{
			Format: cf, Profile: p, Fields: fset, LatScale: uint8(*latscale), Description: *desc,
		})
	}
	if err != nil {
		return fail(stderr, "build", err)
	}
	fmt.Fprintf(stdout, "built %s in %.1fs\n  %s\n", *out, time.Since(t).Seconds(), stats)
	return 0
}

// formats are the -format names: mmdb and the csvdb formats.
var formats = []string{"mmdb", "dbip-city", "dbip-country", "dbip-asn", "ip2location", "ip2location-asn"}

// sourceFormat returns the format to build src with: flag, the -format
// value, when it is set, and otherwise the one the name of src implies.
func sourceFormat(src, flag string) (string, error) {
	if flag != "" {
		if !slices.Contains(formats, flag) {
			return "", fmt.Errorf("unknown -format %q (want %s)", flag, strings.Join(formats, ", "))
		}
		return flag, nil
	}
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(src)), ".gz")
	switch {
	case strings.HasSuffix(name, ".mmdb"):
		return "mmdb", nil
	case strings.HasSuffix(name, ".csv"):
		for _, kind := range []string{"city", "country", "asn"} {
			if strings.HasPrefix(name, "dbip-"+kind+"-lite") {
				return "dbip-" + kind, nil
			}
		}
	}
	return "", fmt.Errorf("cannot tell the format of %q from its name; set -format", src)
}

func lookup(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lookup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "city.geoc", "compact .geoc database `file`")
	lang := fs.String("lang", "", "`language` of the names, one of the database's (default the primary one)")
	if st := parse(fs, args); st >= 0 {
		return st
	}
	if fs.NArg() == 0 {
		return usageError(fs, "no addresses given")
	}
	db, err := ip2geo.Open(*dbPath)
	if err != nil {
		return fail(stderr, "lookup", err)
	}
	defer db.Close()
	if *lang != "" {
		// The view shares db's memory, which stays mapped while either
		// Reader is reachable (Close does not unmap it).
		if db, err = db.WithLanguage(*lang); err != nil {
			return fail(stderr, "lookup", err)
		}
	}
	status := 0
	for _, s := range fs.Args() {
		ip, err := netip.ParseAddr(s)
		if err != nil {
			fmt.Fprintf(stderr, "ip2geo: lookup: %s: invalid address\n", text(s))
			status = 1
			continue
		}
		writeLookup(stdout, db, s, ip)
	}
	return status
}

// writeLookup prints the lookup of ip, given on the command line as s.
func writeLookup(w io.Writer, db *ip2geo.Reader, s string, ip netip.Addr) {
	if db.Profile() == ip2geo.ProfileASN {
		as, ok := db.ASN(ip)
		if !ok {
			fmt.Fprintf(w, "%s: no data\n", s)
			return
		}
		fmt.Fprintf(w, "%s: %s\n", s, join("AS"+strconv.FormatUint(uint64(as.Number), 10), text(as.Organization)))
		return
	}
	fset := db.Fields()
	var d ip2geo.Details
	var ok bool
	if fset == 0 {
		d.Record, ok = db.Lookup(ip)
	} else {
		d, ok = db.LookupDetails(ip)
	}
	if !ok {
		fmt.Fprintf(w, "%s: no data\n", s)
		return
	}
	fmt.Fprintf(w, "%s: %s\n", s, geoSummary(&d, db.Profile(), fset))
	line := func(key, value string) {
		if value == "" {
			fmt.Fprintf(w, "  %s:\n", key)
		} else {
			fmt.Fprintf(w, "  %s: %s\n", key, value)
		}
	}
	if fset&ip2geo.FieldEU != 0 {
		line("eu", strconv.FormatBool(d.IsInEuropeanUnion))
	}
	if fset&ip2geo.FieldContinent != 0 {
		line("continent", join(text(d.ContinentName), geoname(d.ContinentGeoNameID)))
	}
	if fset&ip2geo.FieldRegistered != 0 {
		line("registered_country", countryText(d.RegisteredCountry))
		typ := ""
		if d.RepresentedCountryType != "" {
			typ = "type=" + text(d.RepresentedCountryType)
		}
		line("represented_country", join(countryText(d.RepresentedCountry), typ))
	}
	if fset&ip2geo.FieldSubdivisions != 0 {
		line("region_geoname", strconv.FormatUint(uint64(d.RegionGeoNameID), 10))
		line("region2", join(text(d.Region2ISO), text(d.Region2Name), geoname(d.Region2GeoNameID)))
	}
	if fset&ip2geo.FieldPostal != 0 {
		line("postal_code", text(d.PostalCode))
	}
	if fset&ip2geo.FieldMetro != 0 {
		line("metro_code", strconv.FormatUint(uint64(d.MetroCode), 10))
	}
	if fset&ip2geo.FieldTraits != 0 {
		line("traits", fmt.Sprintf("anycast=%t anonymous_proxy=%t satellite_provider=%t",
			d.IsAnycast, d.IsAnonymousProxy, d.IsSatelliteProvider))
	}
	if fset&ip2geo.FieldLocation != 0 {
		line("location", fmt.Sprintf("coordinates=%t accuracy_radius=%t", d.HasCoordinates, d.HasAccuracyRadius))
	}
}

// geoSummary formats the first line of the lookup of a geo database with
// profile p and field sets fset, after the address: the names p keeps, the
// coordinates of the city profile and the time zone. It prints only what
// the profile stores and the source has, so it never ends in a space.
func geoSummary(d *ip2geo.Details, p ip2geo.Profile, fset ip2geo.FieldSet) string {
	names := []string{d.CountryISO}
	switch p {
	case ip2geo.ProfileRegion:
		names = append(names, d.RegionName)
	case ip2geo.ProfileCity:
		names = append(names, d.RegionName, d.City)
	}
	for len(names) > 1 && names[len(names)-1] == "" {
		names = names[:len(names)-1]
	}
	for i, n := range names {
		if n == "" {
			names[i] = "-" // keeps the position of the names after it
		} else {
			names[i] = text(n)
		}
	}
	var b strings.Builder
	b.WriteString(strings.Join(names, " / "))
	sep := "  "
	// Without the location set a city database cannot tell a missing
	// location from (0, 0), so it prints the coordinates as stored.
	if p == ip2geo.ProfileCity && (fset&ip2geo.FieldLocation == 0 || d.HasCoordinates) {
		fmt.Fprintf(&b, "  (%.4f, %.4f)", d.Latitude, d.Longitude)
		sep = " "
	}
	if d.TimeZone != "" {
		b.WriteString(sep)
		b.WriteString(text(d.TimeZone))
	}
	return b.String()
}

// countryText formats a registered or represented country, or returns ""
// when the source has none.
func countryText(c ip2geo.Country) string {
	if c == (ip2geo.Country{}) {
		return ""
	}
	return join(text(c.ISO), text(c.Name), fmt.Sprintf("(geoname %d, eu=%t)", c.GeoNameID, c.IsInEuropeanUnion))
}

// geoname formats a GeoNames id, or returns "" for 0, which means none.
func geoname(id uint32) string {
	if id == 0 {
		return ""
	}
	return fmt.Sprintf("(geoname %d)", id)
}

// join joins the parts that are not empty with spaces.
func join(parts ...string) string {
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), " ")
}

// fieldSetName is the name of a field set in -fields and info.
type fieldSetName struct {
	name string
	set  ip2geo.FieldSet
}

// fieldSets names the field sets, in bit order.
var fieldSets = []fieldSetName{
	{"eu", ip2geo.FieldEU},
	{"continent", ip2geo.FieldContinent},
	{"registered", ip2geo.FieldRegistered},
	{"subdivisions", ip2geo.FieldSubdivisions},
	{"postal", ip2geo.FieldPostal},
	{"metro", ip2geo.FieldMetro},
	{"traits", ip2geo.FieldTraits},
	{"location", ip2geo.FieldLocation},
}

func fieldSetNames() []string {
	names := make([]string, len(fieldSets))
	for i, f := range fieldSets {
		names[i] = f.name
	}
	return names
}

// parseFields parses a -fields value: empty, "all", or a comma-separated
// list of field set names.
func parseFields(s string) (ip2geo.FieldSet, error) {
	if s == "" {
		return 0, nil
	}
	if s == "all" {
		return ip2geo.FieldsAll, nil
	}
	var fset ip2geo.FieldSet
	for _, name := range strings.Split(s, ",") {
		i := slices.IndexFunc(fieldSets, func(f fieldSetName) bool { return f.name == name })
		if i < 0 {
			return 0, fmt.Errorf("unknown -fields name %q (want %s, or all)", name, strings.Join(fieldSetNames(), ", "))
		}
		fset |= fieldSets[i].set
	}
	return fset, nil
}

// fieldsText lists the names of the sets of fset.
func fieldsText(fset ip2geo.FieldSet) string {
	var names []string
	for _, f := range fieldSets {
		if fset&f.set != 0 {
			names = append(names, f.name)
		}
	}
	return strings.Join(names, ",")
}

func info(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "city.geoc", "compact .geoc database `file`")
	if st := parse(fs, args); st >= 0 {
		return st
	}
	if fs.NArg() > 0 {
		return usageError(fs, "unexpected argument %q", fs.Arg(0))
	}
	// Open validates the whole file, checksums included, so a file that
	// info describes is one that lookups can use.
	db, err := ip2geo.Open(*dbPath)
	if err != nil {
		return fail(stderr, "info", err)
	}
	defer db.Close()
	fi, err := os.Stat(*dbPath)
	if err != nil {
		return fail(stderr, "info", err)
	}
	writeInfo(stdout, fi.Size(), db.Metadata(), db.Languages())
	return 0
}

// writeInfo prints a database's size, metadata and languages, one "key:
// value" per line; see the package documentation.
func writeInfo(w io.Writer, size int64, md ip2geo.Metadata, langs []string) {
	line := func(key, value string) {
		if value == "" {
			fmt.Fprintf(w, "%s:\n", key)
		} else {
			fmt.Fprintf(w, "%s: %s\n", key, value)
		}
	}
	num := func(key string, n uint64) { line(key, strconv.FormatUint(n, 10)) }

	line("size", fmt.Sprintf("%d bytes (%.2f MiB)", size, float64(size)/(1<<20)))
	switch md.FormatVersion {
	case 1:
		line("format", "GEO1")
	case 2:
		line("format", "GEO2")
	default:
		line("format", fmt.Sprintf("version %d", md.FormatVersion))
	}
	line("profile", md.Profile.String())
	num("lat_scale", uint64(md.LatScale))
	if md.FormatVersion != 1 { // GEO1 files have no META section
		line("build_time", timeText(md.BuildTime))
		line("source_type", text(md.SourceType))
		line("source_time", timeText(md.SourceTime))
		line("language", text(md.Language))
		quoted := make([]string, len(langs))
		for i, l := range langs {
			quoted[i] = text(l)
		}
		line("languages", strings.Join(quoted, ","))
		line("fields", fieldsText(md.Fields))
		line("description", text(md.Description))
		line("builder", text(md.Builder))
		num("networks", md.Networks)
		num("distinct_records", md.DistinctRecords)
	}
	num("records", uint64(md.Records))
	num("strings", uint64(md.Strings))
	num("ipv4_ranges", uint64(md.IPv4Ranges))
	num("ipv6_ranges", uint64(md.IPv6Ranges))
}

// timeText formats t as RFC 3339 in UTC, or "unknown" for the zero time.
func timeText(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.UTC().Format(time.RFC3339)
}

// text returns s as it is when it is valid UTF-8 and every rune of it is
// printable, and as a quoted Go string otherwise, so that a string read from
// a file can neither break the one-field-per-line output nor send control
// sequences to a terminal.
func text(s string) string {
	if !utf8.ValidString(s) || strings.ContainsFunc(s, func(r rune) bool { return !strconv.IsPrint(r) }) {
		return strconv.Quote(s)
	}
	return s
}
