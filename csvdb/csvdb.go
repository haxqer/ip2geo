// Package csvdb builds a fast ip2geo reader from a CSV source: DB-IP's
// IP to City, Country and ASN Lite CSV files, and IP2Location's DB1, DB3,
// DB5, DB9, DB11 and ASN CSV files (LITE or commercial, IPv4 or IPv6). It
// is the CSV counterpart of package mmdb, with the same workflows:
//
//	db, err := csvdb.Open("dbip-city-lite-2026-10.csv.gz",
//		csvdb.Options{Format: csvdb.DBIPCity, Profile: ip2geo.ProfileCity})
//	rec, ok := db.Lookup(ip)
//
// or Build to write a .geoc file that ip2geo.Open loads. Files whose name
// ends in ".gz" are decompressed as they are read.
//
// The source is read in one pass and is never held in memory: the build
// keeps the merged ranges and one copy of each distinct record. The 690 MB
// DB-IP City Lite CSV builds (ProfileCity) in about 2.7 s with a peak of
// about 520 MiB, against 1.6 s and 400 MiB from its .mmdb (Apple M3). Rows
// must be in ascending address order and must not overlap, as the vendors
// write them; a file that breaks this, or any other malformed row, fails
// with ErrSyntax and the file and line, rather than giving a database with
// wrong data.
//
// # What is kept
//
// A CSV source has names in English only, so the database's one language
// is "en". Its META source type names the layout, e.g.
// "DBIP-City-Lite (CSV)" or "IP2Location-DB11 (CSV)", and its source time is
// unknown (0).
//
// DB-IP rows give the continent code, the country ISO code, the region
// (stateprov) and city names and the coordinates; there are no country
// names, ISO region codes, GeoNames ids, accuracy radii or time zones. A
// row whose country is "ZZ" (DB-IP's mark for the reserved ranges, such as
// 10.0.0.0/8 and ::/3) has no data: DB-IP's .mmdb files leave these ranges
// out, and so does the build, so their addresses are not found.
//
// IP2Location rows give the country ISO code and name, the region and city
// names, the coordinates and the postal code (FieldPostal, from DB9 on).
// "-" marks a missing value; a row whose country code (or, in an ASN file,
// AS number) is "-" has no data and is left out. The time_zone column of
// DB11 is a UTC offset such as "+10:00", not an IANA time zone, so
// Record.TimeZone stays empty. An IPv6 file holds the IPv4 ranges as
// IPv4-mapped addresses (::ffff:a.b.c.d), which become IPv4 ranges, so a
// build of the IPv6 file answers both families. A file is read as IPv6
// when its name contains "IPV6" (in any case) or its first range ends
// above 4294967295. The layouts follow IP2Location's published
// documentation; they are tested with synthetic files in that layout, as
// IP2Location's data needs an account to download.
//
// As in an mmdb build, data for the IPv6 blocks that ip2geo.Reader looks
// up in the IPv4 table (::/96, 2001::/32 and 2002::/16) is not stored.
//
// # DB-IP CSV and .mmdb
//
// Built from the CSV and from the .mmdb of the same month, a DB-IP
// database answers the same on every field both carry, except where
// DB-IP's two files differ. In the 2026-10 files: the .mmdb leaves out
// 2001::/23 (apart from 2001::/32) and ff00::/8, for which the CSV has
// rows; the .mmdb has records with country "ZZ" for 248 addresses of
// 192.0.0.0/24, which the CSV marks reserved; and a coordinate that lies
// exactly half-way between two LatScale steps (2.38405 at scale 4) can
// round the other way, because the .mmdb stores some of them as a float64
// one unit in the last place away from the nearest float64 of the CSV's
// decimal (about 1.7M IPv4 addresses of the city database at scale 4).
// The test TestDBIPParity measures this when IP2GEO_TEST_DBIP_DIR names a
// directory with both files.
//
// Invalid options and unsuitable sources are reported with the sentinel
// errors ErrOptions, ErrDatabaseType and ErrSyntax; test for them with
// errors.Is.
package csvdb

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/internal/build"
)

const (
	// DefaultLatScale is the number of latitude/longitude decimals that
	// ProfileCity keeps when Options.LatScale is 0 (about 11 m).
	DefaultLatScale = build.DefaultLatScale
	// MaxLatScale is the largest accepted Options.LatScale (see
	// mmdb.MaxLatScale).
	MaxLatScale = build.MaxLatScale
)

// Options controls a build. Format is required; the zero value of the
// other fields builds a ProfileCountry database. Options are validated
// before the source is opened; an invalid value yields an error wrapping
// ErrOptions.
type Options struct {
	// Format is the layout of the source: DBIPCity, DBIPCountry, DBIPASN,
	// IP2Location or IP2LocationASN. The zero value and unknown values fail
	// with ErrOptions.
	Format Format

	// Profile selects how much geo detail the database keeps:
	// ip2geo.ProfileCountry (the zero value), ip2geo.ProfileRegion or
	// ip2geo.ProfileCity, or ip2geo.ProfileASN for an ASN database. Region
	// and city need a layout with region and city names (DBIPCity, or an
	// IP2Location DB3 or later); with DBIPCountry or an IP2Location DB1
	// file they fail with ErrDatabaseType. ProfileASN needs an ASN layout
	// (DBIPASN or IP2LocationASN), and the geo profiles fail with
	// ErrDatabaseType on one. Any other value fails with ErrOptions.
	Profile ip2geo.Profile

	// Fields selects the optional field sets stored beside the profile's
	// Record fields, read with ip2geo.Reader.LookupDetails; the zero value
	// stores none. ip2geo.FieldsAll stores every set that both the profile
	// and the layout allow. Any other value must be allowed by the profile
	// (as in mmdb.Options.Fields) and filled by the layout, otherwise the
	// build fails with ErrOptions. The layouts fill FieldLocation (DBIPCity,
	// IP2Location DB5 and later) and FieldPostal (IP2Location DB9 and
	// DB11), and no other set.
	Fields ip2geo.FieldSet

	// LatScale is the number of latitude/longitude decimals kept by
	// ProfileCity, 0..MaxLatScale. 0 means DefaultLatScale (4). Values above
	// MaxLatScale fail with ErrOptions. Other profiles store no coordinates.
	LatScale uint8

	// BuildTime is the build time recorded in the database's metadata, in
	// whole seconds (UTC). The zero value means the time of the build. Set
	// it to make builds reproducible: the same source, Options and ip2geo
	// version give byte-identical files. A time before 1970 fails with
	// ErrOptions.
	BuildTime time.Time

	// Description is free text recorded in the database's metadata, for
	// example where and when the source was downloaded. It must be valid
	// UTF-8 of at most 65,535 bytes, otherwise the build fails with
	// ErrOptions.
	Description string
}

// Stats summarizes a completed build. It has the fields of mmdb.Stats.
type Stats struct {
	// Profile is the profile the database was built with.
	Profile ip2geo.Profile

	// Networks is the number of address ranges with data read from the
	// source, IPv4 and IPv6 together. Rows without data (DB-IP's "ZZ",
	// IP2Location's "-") are not counted; a row of an IPv6 file that spans
	// the IPv4-mapped block counts once per part.
	Networks int

	// DistinctRecords is the number of distinct source records, counted by
	// the text of the columns the build reads: rows with the same text in
	// those columns share one record, which is decoded once. Source records
	// that differ only in their text (say "1.5" and "1.50") become one
	// entry of Records.
	DistinctRecords int

	// Records is the number of distinct records in the record table,
	// including record 0, which is reserved for gaps (addresses without
	// data).
	Records int

	// Strings is the number of distinct strings in the string table,
	// including the empty string at id 0.
	Strings int

	// V4Ranges is the number of IPv4 range boundaries: one per run of
	// adjacent ranges that share a record, plus one per gap after a run.
	V4Ranges int

	// V6Ranges is the same count for the IPv6 index.
	V6Ranges int

	// Bytes is the size in bytes of the written .geoc file.
	Bytes int64
}

// String renders the stats for logging.
func (s Stats) String() string { return build.Stats(s).String() }

// coreOptions validates o and returns the options of the build core and
// whether o asks for every field set the layout fills. It does no I/O.
func (o *Options) coreOptions() (build.Options, bool, error) {
	if !o.Format.valid() {
		return build.Options{}, false, fmt.Errorf("csvdb: unknown Format %d: %w", o.Format, ErrOptions)
	}
	co := build.Options{
		Profile: o.Profile, Fields: o.Fields, Languages: []string{"en"}, LatScale: o.LatScale,
		BuildTime: o.BuildTime, Description: o.Description,
	}
	if err := co.Normalize(); err != nil {
		return build.Options{}, false, fmt.Errorf("csvdb: %w", err)
	}
	all := o.Fields == ip2geo.FieldsAll
	if err := checkLayout(layoutOf(o.Format), &co, all); err != nil {
		return build.Options{}, false, err
	}
	return co, all, nil
}

// checkLayout rejects a profile the layout has no data for and field sets
// it cannot fill. With all, it keeps the field sets of co that the layout
// fills instead.
func checkLayout(l *layout, co *build.Options, all bool) error {
	switch {
	case co.Profile == ip2geo.ProfileASN && !l.asn:
		return fmt.Errorf("csvdb: profile asn needs an ASN source, %s has geo data: %w", l.typ, ErrDatabaseType)
	case co.Profile == ip2geo.ProfileASN:
	case l.asn:
		return fmt.Errorf("csvdb: %s is an ASN source; build it with profile asn: %w", l.typ, ErrDatabaseType)
	case co.Profile > l.maxProfile:
		return fmt.Errorf("csvdb: profile %s needs region and city data, %s has none: %w",
			co.Profile, l.typ, ErrDatabaseType)
	}
	if all {
		co.Fields &= l.fields
	} else if bad := co.Fields &^ l.fields; bad != 0 {
		return fmt.Errorf("csvdb: Fields %#x: field sets %#x are not in a %s source: %w",
			uint32(co.Fields), uint32(bad), l.typ, ErrOptions)
	}
	return nil
}

// buildFile reads src and returns the encoded database, in one buffer of
// exactly its size, and the build's stats.
func buildFile(src string, opts *Options) ([]byte, build.Stats, error) {
	co, all, err := opts.coreOptions()
	if err != nil {
		return nil, build.Stats{}, err
	}
	b, err := readSource(src, opts.Format, co, all)
	if err != nil {
		return nil, build.Stats{}, err
	}
	buf, st, err := b.Finish()
	if err != nil {
		return nil, build.Stats{}, fmt.Errorf("csvdb: %w", err)
	}
	return buf, st, nil
}

// readSource feeds every row of src to a new build with co.
func readSource(src string, f Format, co build.Options, all bool) (*build.Builder, error) {
	file, err := os.Open(src)
	if err != nil {
		return nil, fmt.Errorf("csvdb: %w", err)
	}
	defer file.Close()
	var in io.Reader = file
	if strings.HasSuffix(strings.ToLower(src), ".gz") {
		gz, err := gzip.NewReader(file)
		if err != nil {
			return nil, fmt.Errorf("csvdb: %q: %w", src, err)
		}
		defer gz.Close()
		in = gz
	}
	sc := newScanner(src, in)
	ok, err := sc.next()
	if err == nil && ok && isHeader(sc.row) {
		ok, err = sc.next()
	}
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("csvdb: %s: no rows: %w", src, ErrSyntax)
	}

	l := layoutOf(f)
	if f == IP2Location {
		var known bool
		if l, known = ip2lLayout(len(sc.row)); !known {
			hint := ""
			if len(sc.row) == ip2lASN.cols {
				hint = "; an ASN file is format ip2location-asn"
			}
			return nil, sc.errorf("%d fields, want 4, 6, 8, 9 or 10 (an IP2Location DB1, DB3, DB5, DB9 or DB11 file)%s",
				len(sc.row), hint)
		}
		if err := checkLayout(l, &co, all); err != nil {
			return nil, err
		}
	}
	b, err := build.New(co, build.Source{Name: src, Type: l.typ})
	if err != nil {
		return nil, fmt.Errorf("csvdb: %w", err)
	}
	r := newReader(sc, l, b)
	if l.decimal {
		r.v6 = strings.Contains(strings.ToUpper(src), "IPV6") || r.endsAboveV4()
	}
	for ok {
		if err := r.row(); err != nil {
			return nil, err
		}
		if ok, err = sc.next(); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// isHeader reports whether row is a header line, which the vendors do not
// write but some tools add: its first field is "ip_start" or "ip_from".
func isHeader(row [][]byte) bool {
	h := strings.ToLower(string(row[0]))
	return h == "ip_start" || h == "ip_from"
}

// Open builds the compact database in memory from a CSV source and returns
// a ready-to-use *ip2geo.Reader, like mmdb.Open. Building reads the whole
// source, which takes some seconds for a city file; call it once at
// startup, or use Build + ip2geo.Open to cache the result.
//
// Errors wrap ErrOptions, ErrDatabaseType, ErrSyntax, ErrStringTooLong or
// ErrTooManyStrings when the options or the source are unsuitable, and the
// underlying error (for example fs.ErrNotExist or gzip.ErrHeader) when the
// source cannot be read.
func Open(src string, opts Options) (*ip2geo.Reader, error) {
	buf, _, err := buildFile(src, &opts)
	if err != nil {
		return nil, err
	}
	r, err := ip2geo.OpenBytes(buf, ip2geo.NoCopy())
	if err != nil {
		return nil, fmt.Errorf("csvdb: load built database: %w", err)
	}
	return r, nil
}

// Build reads src and writes a compact .geoc database to outPath, which
// ip2geo.Open then loads. The output is replaced atomically, as by
// mmdb.Build: on any error an existing outPath is left untouched.
//
// Errors are reported as for Open.
func Build(src, outPath string, opts Options) (*Stats, error) {
	buf, st, err := buildFile(src, &opts)
	if err != nil {
		return nil, err
	}
	if err := build.WriteFileAtomic(outPath, buf); err != nil {
		return nil, fmt.Errorf("csvdb: %w", err)
	}
	s := Stats(st)
	return &s, nil
}
