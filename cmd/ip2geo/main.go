// Command ip2geo builds a compact ip2geo database from a MaxMind DB (.mmdb)
// City, Country or Enterprise database, looks up addresses in it and prints
// its metadata.
//
//	ip2geo build  -src GeoIP2-City.mmdb -out city.geoc -profile city
//	ip2geo lookup -db city.geoc 81.2.69.142 2001:4860:4860::8888
//	ip2geo info   -db city.geoc
//
// build flags:
//
//	-src          source .mmdb (required)
//	-out          output .geoc file, replaced atomically (default city.geoc)
//	-profile      city | region | country (default city)
//	-lang         language of names; must be listed by the source (default en)
//	-latscale     lat/lon decimals kept by the city profile, 0..7; 0 means 4
//	              (default 4)
//	-description  free text recorded in the file's metadata (default empty)
//
// build writes the GEO2 format of docs/FORMAT.md; lookup and info also read
// the GEO1 files written by v0.1.x.
//
// info prints the file size and the database's metadata, one "key: value"
// per line: format (GEO2 or GEO1), profile, lat_scale, build_time,
// source_type, source_time, language, description, builder, networks,
// distinct_records, records, strings, ipv4_ranges and ipv6_ranges. Times are
// RFC 3339 in UTC, or "unknown"; text that is not printable is quoted. A GEO1
// file has no metadata section, so for one info prints format, profile,
// lat_scale and the four counts only.
//
// Errors are printed to stderr as "ip2geo: <command>: <error>". The exit
// status is 0 on success, 1 when a build, lookup or info fails (including a
// -latscale outside 0..7, a -description that is not valid UTF-8 or is longer
// than 65,535 bytes, an unsuitable source database, an unknown -lang, and a
// file that is not a valid database), and 2 for usage errors (unknown command
// or flag, missing -src, unknown -profile, unexpected arguments, lookup
// without addresses).
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
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
	fmt.Fprintln(stderr, "  ip2geo build  -src City.mmdb -out city.geoc [-profile city|region|country] [-lang en] [-latscale 0..7] [-description text]")
	fmt.Fprintln(stderr, "  ip2geo lookup -db city.geoc <ip> [ip...]")
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
	src := fs.String("src", "", "source MaxMind DB City, Country or Enterprise .mmdb `file` (required)")
	out := fs.String("out", "city.geoc", "output .geoc `file`, replaced atomically")
	profile := fs.String("profile", "city", "city | region | country")
	lang := fs.String("lang", "en", "`language` of names; must be one the source database lists")
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
	default:
		return usageError(fs, "unknown -profile %q (want city, region or country)", *profile)
	}
	// Range-check before the uint8 conversion, which would wrap 300 to 44.
	if *latscale < 0 || *latscale > mmdb.MaxLatScale {
		return fail(stderr, "build", fmt.Errorf("invalid -latscale %d: must be 0..%d", *latscale, mmdb.MaxLatScale))
	}
	t := time.Now()
	stats, err := mmdb.Build(*src, *out, mmdb.Options{
		Profile: p, Lang: *lang, LatScale: uint8(*latscale), Description: *desc,
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
	for _, s := range fs.Args() {
		ip, err := netip.ParseAddr(s)
		if err != nil {
			fmt.Fprintf(stdout, "%s: invalid address\n", s)
			continue
		}
		rec, ok := db.Lookup(ip)
		if !ok {
			fmt.Fprintf(stdout, "%s: no data\n", s)
			continue
		}
		fmt.Fprintf(stdout, "%s: %s / %s / %s  (%.4f, %.4f) %s\n",
			s, rec.CountryISO, rec.RegionName, rec.City, rec.Latitude, rec.Longitude, rec.TimeZone)
	}
	return 0
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
	writeInfo(stdout, fi.Size(), db.Metadata())
	return 0
}

// writeInfo prints a database's size and metadata, one "key: value" per
// line; see the package documentation.
func writeInfo(w io.Writer, size int64, md ip2geo.Metadata) {
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
