// Command sizetable builds the README's table of .geoc file sizes from one
// month of DB-IP Lite sources and writes README.md with the table replaced.
// scripts/size-table.sh drives it; run that instead:
//
//	scripts/size-table.sh update SRCDIR   rewrite the table in README.md
//	scripts/size-table.sh check  SRCDIR   fail with a diff when it is stale
//
// The table is the text between the lines "<!-- size-table:begin -->" and
// "<!-- size-table:end -->". Every row is built with mmdb.Build and a fixed
// BuildTime (the first second of the month), so the same sources and the
// same ip2geo tree always give the same table.
//
//	go run ./scripts/sizetable -month 2026-10 -city dbip-city-lite-2026-10.mmdb \
//		-country dbip-country-lite-2026-10.mmdb -asn dbip-asn-lite-2026-10.mmdb \
//		-readme README.md -out README.md
//
// Exit status: 0 on success, 2 on any error.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/mmdb"
)

const (
	beginMarker = "<!-- size-table:begin -->"
	endMarker   = "<!-- size-table:end -->"
)

// row is one build of the table.
type row struct {
	source  string // "city", "country" or "asn": which DB-IP Lite edition
	flags   string // the equivalent ip2geo build flags, shown in the table
	profile ip2geo.Profile
	fields  ip2geo.FieldSet
	langs   []string
}

// fieldSets are the -fields names of the CLI, in bit order.
var fieldSets = []struct {
	name string
	set  ip2geo.FieldSet
}{
	{"eu", ip2geo.FieldEU},
	{"continent", ip2geo.FieldContinent},
	{"registered", ip2geo.FieldRegistered},
	{"subdivisions", ip2geo.FieldSubdivisions},
	{"postal", ip2geo.FieldPostal},
	{"metro", ip2geo.FieldMetro},
	{"traits", ip2geo.FieldTraits},
	{"location", ip2geo.FieldLocation},
}

// rows lists the table in order.
func rows() []row {
	three := []string{"en", "de", "fr"}
	rs := []row{
		{source: "city", flags: "-profile country", profile: ip2geo.ProfileCountry},
		{source: "city", flags: "-profile region", profile: ip2geo.ProfileRegion},
		{source: "city", flags: "-profile city", profile: ip2geo.ProfileCity},
	}
	for _, f := range fieldSets {
		rs = append(rs, row{source: "city", flags: "-profile city -fields " + f.name, profile: ip2geo.ProfileCity, fields: f.set})
	}
	return append(rs,
		row{source: "city", flags: "-profile city -fields all", profile: ip2geo.ProfileCity, fields: ip2geo.FieldsAll},
		row{source: "city", flags: "-profile city -lang en,de,fr", profile: ip2geo.ProfileCity, langs: three},
		row{source: "city", flags: "-profile city -fields all -lang en,de,fr", profile: ip2geo.ProfileCity, fields: ip2geo.FieldsAll, langs: three},
		row{source: "country", flags: "-profile country", profile: ip2geo.ProfileCountry},
		row{source: "asn", flags: "-profile asn", profile: ip2geo.ProfileASN},
	)
}

// sourceNames are the table's names of the DB-IP Lite editions.
var sourceNames = map[string]string{
	"city":    "City Lite",
	"country": "Country Lite",
	"asn":     "ASN Lite",
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "sizetable: %v\n", err)
		os.Exit(2)
	}
}

func run() error {
	month := flag.String("month", "", "DB-IP Lite `month` of the sources, YYYY-MM (required)")
	city := flag.String("city", "", "DB-IP City Lite .mmdb `file` (required)")
	country := flag.String("country", "", "DB-IP Country Lite .mmdb `file` (required)")
	asn := flag.String("asn", "", "DB-IP ASN Lite .mmdb `file` (required)")
	readme := flag.String("readme", "README.md", "README `file` to read")
	out := flag.String("out", "", "`file` to write the updated README to (required)")
	flag.Parse()
	if flag.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", flag.Arg(0))
	}
	if *month == "" || *city == "" || *country == "" || *asn == "" || *out == "" {
		return errors.New("-month, -city, -country, -asn and -out are required")
	}
	buildTime, err := time.Parse("2006-01", *month)
	if err != nil {
		return fmt.Errorf("-month %q: want YYYY-MM", *month)
	}
	doc, err := os.ReadFile(*readme)
	if err != nil {
		return err
	}
	srcs := map[string]string{"city": *city, "country": *country, "asn": *asn}

	tmp, err := os.MkdirTemp("", "sizetable-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	block, err := table(*month, buildTime, srcs, tmp)
	if err != nil {
		return err
	}
	updated, err := splice(doc, block)
	if err != nil {
		return fmt.Errorf("%s: %w", *readme, err)
	}
	return os.WriteFile(*out, updated, 0o644)
}

// table builds every row and returns the Markdown between the markers.
func table(month string, buildTime time.Time, srcs map[string]string, tmp string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Each profile, field set and language option built from one free source,\n")
	fmt.Fprintf(&b, "DB-IP Lite %s (City, Country and ASN editions, CC BY 4.0), by\n", month)
	b.WriteString("`scripts/size-table.sh`; ranges are the IPv4 and IPv6 index boundaries:\n\n")
	b.WriteString("| source | `ip2geo build` flags | size | records | IPv4 ranges | IPv6 ranges |\n")
	b.WriteString("|---|---|--:|--:|--:|--:|\n")
	out := filepath.Join(tmp, "out.geoc")
	for _, r := range rows() {
		start := time.Now()
		st, err := mmdb.Build(srcs[r.source], out, mmdb.Options{
			Profile: r.profile, Fields: r.fields, Languages: r.langs, BuildTime: buildTime,
		})
		if err != nil {
			return "", fmt.Errorf("%s %s: %w", r.source, r.flags, err)
		}
		fi, err := os.Stat(out)
		if err != nil {
			return "", err
		}
		if fi.Size() != st.Bytes {
			return "", fmt.Errorf("%s %s: Stats.Bytes %d, file %d bytes", r.source, r.flags, st.Bytes, fi.Size())
		}
		fmt.Fprintf(os.Stderr, "sizetable: %-7s %-42s %s (%.1fs)\n", r.source, r.flags, st, time.Since(start).Seconds())
		// MiB with two decimals, as ip2geo info prints the size.
		fmt.Fprintf(&b, "| %s | `%s` | %.2f MiB | %s | %s | %s |\n",
			sourceNames[r.source], r.flags, float64(st.Bytes)/(1<<20),
			group(st.Records), group(st.V4Ranges), group(st.V6Ranges))
	}
	return b.String(), nil
}

// group formats n with thousands separators.
func group(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// splice replaces the lines between the begin and end markers of doc with
// block. Each marker must be a line of its own and appear exactly once.
func splice(doc []byte, block string) ([]byte, error) {
	lines := bytes.SplitAfter(doc, []byte("\n"))
	begin, end := -1, -1
	for i, l := range lines {
		switch string(bytes.TrimRight(l, "\r\n")) {
		case beginMarker:
			if begin >= 0 {
				return nil, fmt.Errorf("%s appears twice", beginMarker)
			}
			begin = i
		case endMarker:
			if end >= 0 {
				return nil, fmt.Errorf("%s appears twice", endMarker)
			}
			end = i
		}
	}
	if begin < 0 || end < 0 || end < begin {
		return nil, fmt.Errorf("want one %s line followed by one %s line", beginMarker, endMarker)
	}
	var b bytes.Buffer
	for _, l := range lines[:begin+1] {
		b.Write(l)
	}
	b.WriteString(block)
	for _, l := range lines[end:] {
		b.Write(l)
	}
	return b.Bytes(), nil
}
