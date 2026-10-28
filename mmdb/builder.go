// Package mmdb builds a fast ip2geo reader from a MaxMind DB (.mmdb) file:
// a GeoIP2 or GeoLite2 City, Country or Enterprise database, or a DB-IP
// database in the same layout (for example DBIP-City-Lite).
//
// Use Open for the one-call, drop-in library workflow (build in memory, no
// files):
//
//	db, err := mmdb.Open("GeoIP2-City.mmdb", mmdb.Options{Profile: ip2geo.ProfileCity})
//	rec, ok := db.Lookup(ip)
//
// Or use Build once to write a compact .geoc file that ip2geo.Open then loads
// in a fraction of a second with zero third-party dependencies. Build writes
// the GEO2 format specified in docs/FORMAT.md; ip2geo.Open also reads the
// GEO1 files written by v0.1.x.
//
// The zero Options value builds a ProfileCountry database with English names.
// Invalid options and unsuitable source databases are reported with the
// sentinel errors ErrOptions, ErrDatabaseType and ErrLanguage; test for them
// with errors.Is.
//
// Every network that has data in the source is found by the built reader,
// even when none of the fields kept by the profile are set (for example a
// network that carries only registered_country): such a lookup returns
// ok == true and an empty Record. Lookups return ok == false only for
// addresses that have no data in the source.
package mmdb

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/internal/build"
	"github.com/oschwald/maxminddb-golang/v2"
)

const (
	// DefaultLatScale is the number of latitude/longitude decimals that
	// ProfileCity keeps when Options.LatScale is 0 (about 11 m).
	DefaultLatScale = build.DefaultLatScale
	// MaxLatScale is the largest accepted Options.LatScale. Coordinates are
	// stored as round(x * 10^LatScale) in an int32, which overflows for
	// |x| > 21.47 from a scale of 8 on.
	MaxLatScale = build.MaxLatScale
)

// Options controls a build. The zero value builds a ProfileCountry database
// with English names. Options are validated before the source database is
// opened; an invalid value yields an error wrapping ErrOptions.
type Options struct {
	// Profile selects how much geo detail the database keeps:
	// ip2geo.ProfileCountry (the zero value), ip2geo.ProfileRegion or
	// ip2geo.ProfileCity. Region and city need a City or Enterprise source
	// database; with a Country database they fail with ErrDatabaseType.
	// Any other value fails with ErrOptions.
	Profile ip2geo.Profile

	// Lang is the language of the localized country, region and city names,
	// for example "en", "de" or "zh-CN". Empty means "en". When the source
	// metadata lists its languages, Lang must be one of them, otherwise the
	// build fails with ErrLanguage. There is no fallback: a name that is
	// missing in Lang is stored empty.
	Lang string

	// LatScale is the number of latitude/longitude decimals kept by
	// ProfileCity, 0..MaxLatScale. 0 means DefaultLatScale (4), so whole-degree
	// precision cannot be requested. Values above MaxLatScale fail with
	// ErrOptions. Other profiles store no coordinates, so their files record
	// a lat/lon scale of 0 whatever LatScale says.
	LatScale uint8

	// BuildTime is the build time recorded in the database's metadata, in
	// whole seconds (UTC); the fraction of a second is dropped. The zero
	// value means the time of the build. Set it to make builds
	// reproducible: the same source, Options and ip2geo version give
	// byte-identical files. A time before 1970 fails with ErrOptions.
	BuildTime time.Time

	// Description is free text recorded in the database's metadata, for
	// example where and when the source was downloaded. It defaults to
	// empty. It must be valid UTF-8 of at most 65,535 bytes, otherwise the
	// build fails with ErrOptions.
	Description string
}

// Stats summarizes a completed build.
type Stats struct {
	// Profile is the profile the database was built with.
	Profile ip2geo.Profile

	// Networks is the number of networks with data read from the source,
	// IPv4 and IPv6 together. IPv4 networks are counted once: the IPv4
	// aliases of an IPv6 database (::ffff:0:0/96, 2001::/32, 2002::/16) are
	// not iterated.
	Networks int

	// DistinctRecords is the number of distinct data records in the source
	// that those networks point to, counted by their offset in the source's
	// data section. Each one is decoded once, however many networks share
	// it. Source records that differ only in fields the profile does not
	// keep become one entry of Records, so Records is at most
	// DistinctRecords + 1 (the gap record).
	DistinctRecords int

	// Records is the number of distinct records in the record table. It
	// includes record 0, which is reserved for gaps (addresses without data),
	// and one record per distinct combination of the fields the profile
	// keeps, including the all-empty combination when some network has none
	// of those fields.
	Records int

	// Strings is the number of distinct strings in the string table
	// (names, ISO codes, continent codes, time zones, and the metadata's
	// source type, language, description and builder), including the empty
	// string at id 0.
	Strings int

	// V4Ranges is the number of IPv4 range boundaries: one per run of
	// adjacent networks that share a record, plus one per gap after a run.
	V4Ranges int

	// V6Ranges is the same count for the IPv6 index.
	V6Ranges int

	// Bytes is the size in bytes of the written .geoc file.
	Bytes int64
}

// String renders the stats for logging.
func (s Stats) String() string { return build.Stats(s).String() }

// coreOptions validates o and returns the options of the build core. It
// does no I/O.
func (o *Options) coreOptions() (build.Options, error) {
	lang := o.Lang
	if lang == "" {
		lang = "en"
	}
	co := build.Options{
		Profile: o.Profile, Languages: []string{lang}, LatScale: o.LatScale,
		BuildTime: o.BuildTime, Description: o.Description,
	}
	if err := co.Normalize(); err != nil {
		return build.Options{}, fmt.Errorf("mmdb: %w", err)
	}
	return co, nil
}

// checkSource rejects source databases the builder cannot convert: anything
// that is not a City, Country or Enterprise database, a Country database with
// a profile that needs city data, and a language the database does not have.
func checkSource(md *maxminddb.Metadata, src string, opts *build.Options) error {
	typ := md.DatabaseType
	hasCity := strings.Contains(typ, "City") || strings.Contains(typ, "Enterprise")
	if !hasCity && !strings.Contains(typ, "Country") {
		return fmt.Errorf("mmdb: %q has database type %q, want a City, Country or Enterprise database: %w",
			src, typ, ErrDatabaseType)
	}
	if !hasCity && opts.Profile != ip2geo.ProfileCountry {
		return fmt.Errorf("mmdb: profile %s needs a City database, %q has database type %q: %w",
			opts.Profile, src, typ, ErrDatabaseType)
	}
	for _, lang := range opts.Languages {
		if len(md.Languages) > 0 && !slices.Contains(md.Languages, lang) {
			return fmt.Errorf("mmdb: language %q is not in %q (available: %s): %w",
				lang, src, strings.Join(md.Languages, ", "), ErrLanguage)
		}
	}
	return nil
}

// buildFile reads srcMMDB and returns the encoded database, in one buffer of
// exactly its size, and the build's stats.
func buildFile(srcMMDB string, opts *Options) ([]byte, build.Stats, error) {
	co, err := opts.coreOptions()
	if err != nil {
		return nil, build.Stats{}, err
	}
	b, err := readSource(srcMMDB, co)
	if err != nil {
		return nil, build.Stats{}, err
	}
	// The source is closed (and unmapped) by now, before the encoder
	// allocates the file.
	buf, st, err := b.Finish()
	if err != nil {
		return nil, build.Stats{}, fmt.Errorf("mmdb: %w", err)
	}
	return buf, st, nil
}

// readSource feeds every network of srcMMDB to a new build with co.
func readSource(srcMMDB string, co build.Options) (*build.Builder, error) {
	db, err := maxminddb.Open(srcMMDB)
	if err != nil {
		return nil, fmt.Errorf("mmdb: open %q: %w", srcMMDB, err)
	}
	defer db.Close()
	if err := checkSource(&db.Metadata, srcMMDB, &co); err != nil {
		return nil, err
	}
	b, err := build.New(co, build.Source{
		Name: srcMMDB, Type: db.Metadata.DatabaseType, BuildEpoch: uint64(db.Metadata.BuildEpoch),
	})
	if err != nil {
		return nil, fmt.Errorf("mmdb: %w", err)
	}

	// Networks share data records (GeoIP2-City: 10.2M networks, 0.9M
	// records), so each data offset is decoded once and mapped to its record
	// id. Records are still numbered, and strings interned, in the order the
	// networks first reach them, so the output does not change.
	byOffset := map[uintptr]uint32{}
	dec := newSrcDecoder(co.Profile, co.Languages[0])
	var src srcRecord
	var rec build.Record

	for result := range db.Networks() {
		// Check the error first: a failed result carries no valid offset.
		if err := result.Err(); err != nil {
			return nil, fmt.Errorf("mmdb: %q: decode network %s: %w", srcMMDB, result.Prefix(), err)
		}
		off := result.Offset()
		recID, ok := byOffset[off]
		if !ok {
			if err := dec.decode(result, &src); err != nil {
				return nil, fmt.Errorf("mmdb: %q: decode network %s: %w", srcMMDB, result.Prefix(), err)
			}
			src.record(&rec)
			recID = b.Record(&rec)
			byOffset[off] = recID
		}
		b.AddPrefix(result.Prefix(), recID)
	}
	return b, nil
}

// Open builds the compact database in memory directly from a MaxMind City
// .mmdb and returns a ready-to-use *ip2geo.Reader. No intermediate file and no
// command-line tool are involved — this is the one-call, drop-in library entry
// point. Building scans the whole source database, which takes about 2
// seconds and peaks at about 450 MiB of memory for a GeoIP2-City source
// (ProfileCity, Apple M3); call it once at startup, or use Build +
// ip2geo.Open to cache the result.
//
// Errors wrap ErrOptions, ErrDatabaseType, ErrLanguage, ErrStringTooLong or
// ErrTooManyStrings when the options or the source database are unsuitable,
// and the underlying error (for example fs.ErrNotExist) when the source
// cannot be read.
func Open(srcMMDB string, opts Options) (*ip2geo.Reader, error) {
	buf, _, err := buildFile(srcMMDB, &opts)
	if err != nil {
		return nil, err
	}
	r, err := ip2geo.OpenBytes(buf, ip2geo.NoCopy())
	if err != nil {
		return nil, fmt.Errorf("mmdb: load built database: %w", err)
	}
	return r, nil
}

// Build reads srcMMDB and writes a compact .geoc database to outPath. This is
// the pre-build workflow: ip2geo.Open then loads the file with zero third-party
// dependencies and a sub-second startup.
//
// The output is written to a temporary file in the directory of outPath,
// synced, and renamed over outPath, so readers of outPath never observe a
// partial file. On any error the temporary file is removed and an existing
// outPath is left untouched. A new file gets mode 0644; a replaced file keeps
// its permission bits. If outPath is a symbolic link, the link itself is
// replaced.
//
// Errors are reported as for Open.
func Build(srcMMDB, outPath string, opts Options) (*Stats, error) {
	buf, st, err := buildFile(srcMMDB, &opts)
	if err != nil {
		return nil, err
	}
	if err := build.WriteFileAtomic(outPath, buf); err != nil {
		return nil, fmt.Errorf("mmdb: %w", err)
	}
	s := Stats(st)
	return &s, nil
}
