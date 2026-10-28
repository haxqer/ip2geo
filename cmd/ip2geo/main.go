// Command ip2geo builds a compact ip2geo database from a MaxMind DB (.mmdb)
// City, Country, Enterprise or ASN database, looks up addresses in it and
// prints its metadata.
//
//	ip2geo build  -src GeoIP2-City.mmdb -out city.geoc -profile city
//	ip2geo build  -src GeoIP2-City.mmdb -out city-de.geoc -fields all -lang en,de
//	ip2geo build  -src GeoLite2-ASN.mmdb -out asn.geoc -profile asn
//	ip2geo lookup -db city.geoc 81.2.69.142 2001:4860:4860::8888
//	ip2geo info   -db city.geoc
//
// build flags:
//
//	-src          source .mmdb (required)
//	-out          output .geoc file, replaced atomically (default city.geoc)
//	-profile      city | region | country | asn (default city); asn needs an
//	              ASN source and the others a City, Country or Enterprise one
//	-fields       optional field sets, a comma-separated list of eu,
//	              continent, registered, subdivisions, postal, metro, traits
//	              and location, or all for every set the profile allows
//	              (default none); see ip2geo.FieldSet
//	-lang         languages of names, comma-separated, the primary one first;
//	              each must be listed by the source (default en)
//	-latscale     lat/lon decimals kept by the city profile, 0..7; 0 means 4
//	              (default 4)
//	-description  free text recorded in the file's metadata (default empty)
//
// build writes the GEO2 format of docs/FORMAT.md; lookup and info also read
// the GEO1 files written by v0.1.x.
//
// lookup prints one line per address: the country ISO code, region name,
// city name, coordinates and time zone, or for an ASN database the AS number
// and organization. When the database has field sets, an indented
// "key: value" line follows for each of their fields. -lang selects one of
// the database's languages for the names (default the primary one).
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
// -latscale outside 0..7, a -description that is not valid UTF-8 or is longer
// than 65,535 bytes, -fields the profile does not allow, an unsuitable source
// database, an unknown, empty or repeated -lang, and a file that is not a
// valid database), and 2 for usage errors (unknown command or flag, missing
// -src, unknown -profile or -fields name, unexpected arguments, lookup
// without addresses).
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/haxqer/ip2geo"
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
	fmt.Fprintln(stderr, "  ip2geo build  -src City.mmdb -out city.geoc [-profile city|region|country|asn] [-fields eu,continent,...|all]")
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
	src := fs.String("src", "", "source MaxMind DB City, Country, Enterprise or ASN .mmdb `file` (required)")
	out := fs.String("out", "city.geoc", "output .geoc `file`, replaced atomically")
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
	fset, err := parseFields(*fields)
	if err != nil {
		return usageError(fs, "%v", err)
	}
	// Range-check before the uint8 conversion, which would wrap 300 to 44.
	if *latscale < 0 || *latscale > mmdb.MaxLatScale {
		return fail(stderr, "build", fmt.Errorf("invalid -latscale %d: must be 0..%d", *latscale, mmdb.MaxLatScale))
	}
	t := time.Now()
	stats, err := mmdb.Build(*src, *out, mmdb.Options{
		Profile: p, Fields: fset, Languages: strings.Split(*lang, ","), LatScale: uint8(*latscale), Description: *desc,
	})
	if err != nil {
		return fail(stderr, "build", err)
	}
	fmt.Fprintf(stdout, "built %s in %.1fs\n  %s\n", *out, time.Since(t).Seconds(), stats)
	return 0
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
		// The Reader shares db's memory; db.Close releases it.
		if db, err = db.WithLanguage(*lang); err != nil {
			return fail(stderr, "lookup", err)
		}
	}
	for _, s := range fs.Args() {
		ip, err := netip.ParseAddr(s)
		if err != nil {
			fmt.Fprintf(stdout, "%s: invalid address\n", s)
			continue
		}
		writeLookup(stdout, db, s, ip)
	}
	return 0
}

// writeLookup prints the lookup of ip, given on the command line as s.
func writeLookup(w io.Writer, db *ip2geo.Reader, s string, ip netip.Addr) {
	if db.Profile() == ip2geo.ProfileASN {
		as, ok := db.ASN(ip)
		if !ok {
			fmt.Fprintf(w, "%s: no data\n", s)
			return
		}
		fmt.Fprintf(w, "%s: AS%d %s\n", s, as.Number, text(as.Organization))
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
	fmt.Fprintf(w, "%s: %s / %s / %s  (%.4f, %.4f) %s\n",
		s, d.CountryISO, d.RegionName, d.City, d.Latitude, d.Longitude, d.TimeZone)
	line := func(key, format string, args ...any) {
		fmt.Fprintf(w, "  %s: "+format+"\n", append([]any{key}, args...)...)
	}
	if fset&ip2geo.FieldEU != 0 {
		line("eu", "%t", d.IsInEuropeanUnion)
	}
	if fset&ip2geo.FieldContinent != 0 {
		line("continent", "%s (geoname %d)", text(d.ContinentName), d.ContinentGeoNameID)
	}
	if fset&ip2geo.FieldRegistered != 0 {
		line("registered_country", "%s", countryText(d.RegisteredCountry))
		line("represented_country", "%s type=%s", countryText(d.RepresentedCountry), text(d.RepresentedCountryType))
	}
	if fset&ip2geo.FieldSubdivisions != 0 {
		line("region_geoname", "%d", d.RegionGeoNameID)
		line("region2", "%s %s (geoname %d)", text(d.Region2ISO), text(d.Region2Name), d.Region2GeoNameID)
	}
	if fset&ip2geo.FieldPostal != 0 {
		line("postal_code", "%s", text(d.PostalCode))
	}
	if fset&ip2geo.FieldMetro != 0 {
		line("metro_code", "%d", d.MetroCode)
	}
	if fset&ip2geo.FieldTraits != 0 {
		line("traits", "anycast=%t anonymous_proxy=%t satellite_provider=%t",
			d.IsAnycast, d.IsAnonymousProxy, d.IsSatelliteProvider)
	}
	if fset&ip2geo.FieldLocation != 0 {
		line("location", "coordinates=%t accuracy_radius=%t", d.HasCoordinates, d.HasAccuracyRadius)
	}
}

// countryText formats a registered or represented country.
func countryText(c ip2geo.Country) string {
	return fmt.Sprintf("%s %s (geoname %d, eu=%t)", text(c.ISO), text(c.Name), c.GeoNameID, c.IsInEuropeanUnion)
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
