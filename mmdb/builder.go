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
// in a fraction of a second with zero third-party dependencies.
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
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/haxqer/ip2geo"
	"github.com/oschwald/maxminddb-golang/v2"
)

const magic = "GEO1"
const version = 1

const (
	// DefaultLatScale is the number of latitude/longitude decimals that
	// ProfileCity keeps when Options.LatScale is 0 (about 11 m).
	DefaultLatScale = 4
	// MaxLatScale is the largest accepted Options.LatScale. Coordinates are
	// stored as round(x * 10^LatScale) in an int32, which overflows for
	// |x| > 21.47 from a scale of 8 on.
	MaxLatScale = 7
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
	// ErrOptions. Other profiles store no coordinates and only record the
	// value in the file header.
	LatScale uint8
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

	// Records is the number of distinct records in the record table. It
	// includes record 0, which is reserved for gaps (addresses without data),
	// and one record per distinct combination of the fields the profile
	// keeps, including the all-empty combination when some network has none
	// of those fields.
	Records int

	// Strings is the number of distinct strings in the string table
	// (names, ISO codes, continent codes, time zones), including the empty
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
func (s Stats) String() string {
	return fmt.Sprintf("profile=%s networks=%d records=%d strings=%d v4ranges=%d v6ranges=%d size=%.2fMB",
		s.Profile, s.Networks, s.Records, s.Strings, s.V4Ranges, s.V6Ranges, float64(s.Bytes)/1024/1024)
}

type srcRecord struct {
	Continent struct {
		Code string `maxminddb:"code"`
	} `maxminddb:"continent"`
	Country struct {
		ISOCode   string            `maxminddb:"iso_code"`
		GeoNameID uint32            `maxminddb:"geoname_id"`
		Names     map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
	City struct {
		GeoNameID uint32            `maxminddb:"geoname_id"`
		Names     map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Location struct {
		Lat *float64 `maxminddb:"latitude"`
		Lon *float64 `maxminddb:"longitude"`
		Acc uint16   `maxminddb:"accuracy_radius"`
		TZ  string   `maxminddb:"time_zone"`
	} `maxminddb:"location"`
	Subdivisions []struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
}

type recKey struct {
	countryISO, countryName, continent, regionISO, regionName, cityName, tz uint32
	countryGeo, cityGeo                                                     uint32
	lat, lon                                                                int32
	acc                                                                     uint16
}

type interner struct {
	m    map[string]uint32
	list []string
}

func newInterner() *interner { return &interner{m: map[string]uint32{"": 0}, list: []string{""}} }

func (in *interner) id(s string) uint32 {
	if s == "" {
		return 0
	}
	if v, ok := in.m[s]; ok {
		return v
	}
	v := uint32(len(in.list))
	in.m[s] = v
	in.list = append(in.list, s)
	return v
}

type rangeEntry struct {
	start, last uint32
	s16, l16    [16]byte
	rec         uint32
}

type buildResult struct {
	strs     []string
	recs     []recKey
	v4b, v6b []boundary
	networks int
}

// normalize validates o and fills in the defaults. It does no I/O.
func (o *Options) normalize() error {
	switch o.Profile {
	case ip2geo.ProfileCountry, ip2geo.ProfileRegion, ip2geo.ProfileCity:
	default:
		return fmt.Errorf("mmdb: unknown Profile %d (want ProfileCountry, ProfileRegion or ProfileCity): %w",
			o.Profile, ErrOptions)
	}
	if o.LatScale > MaxLatScale {
		return fmt.Errorf("mmdb: LatScale %d is above %d (coordinates would overflow int32): %w",
			o.LatScale, MaxLatScale, ErrOptions)
	}
	if o.Lang == "" {
		o.Lang = "en"
	}
	if o.Profile == ip2geo.ProfileCity && o.LatScale == 0 {
		o.LatScale = DefaultLatScale
	}
	return nil
}

// checkSource rejects source databases the builder cannot convert: anything
// that is not a City, Country or Enterprise database, a Country database with
// a profile that needs city data, and a language the database does not have.
func checkSource(md *maxminddb.Metadata, src string, opts *Options) error {
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
	if len(md.Languages) > 0 && !slices.Contains(md.Languages, opts.Lang) {
		return fmt.Errorf("mmdb: language %q is not in %q (available: %s): %w",
			opts.Lang, src, strings.Join(md.Languages, ", "), ErrLanguage)
	}
	return nil
}

// checkStrings rejects strings whose length does not fit the 16-bit length
// prefix of the strings table.
func checkStrings(src string, strs []string) error {
	for _, s := range strs {
		if len(s) > math.MaxUint16 {
			return fmt.Errorf("mmdb: %q: string of %d bytes (max %d) starting %.40q: %w",
				src, len(s), math.MaxUint16, s, ErrStringTooLong)
		}
	}
	return nil
}

func buildData(srcMMDB string, opts *Options) (*buildResult, error) {
	if err := opts.normalize(); err != nil {
		return nil, err
	}

	db, err := maxminddb.Open(srcMMDB)
	if err != nil {
		return nil, fmt.Errorf("mmdb: open %q: %w", srcMMDB, err)
	}
	defer db.Close()
	if err := checkSource(&db.Metadata, srcMMDB, opts); err != nil {
		return nil, err
	}
	scale := math.Pow10(int(opts.LatScale))

	strs := newInterner()
	// Record id 0 is reserved for gaps (addresses without data) and is never
	// handed to a network: a network whose kept fields are all empty gets its
	// own non-zero id, so the reader reports it as found.
	recKeys := map[recKey]uint32{}
	recs := []recKey{{}}

	var v4, v6 []rangeEntry
	networks := 0

	for result := range db.Networks() {
		var s srcRecord
		if err := result.Decode(&s); err != nil {
			return nil, fmt.Errorf("mmdb: %q: decode network %s: %w", srcMMDB, result.Prefix(), err)
		}
		networks++

		var k recKey
		k.countryISO = strs.id(s.Country.ISOCode)
		k.countryName = strs.id(s.Country.Names[opts.Lang])
		k.continent = strs.id(s.Continent.Code)
		k.countryGeo = s.Country.GeoNameID
		if opts.Profile >= ip2geo.ProfileRegion && len(s.Subdivisions) > 0 {
			top := s.Subdivisions[0]
			k.regionISO = strs.id(top.ISOCode)
			k.regionName = strs.id(top.Names[opts.Lang])
		}
		if opts.Profile >= ip2geo.ProfileCity {
			k.cityName = strs.id(s.City.Names[opts.Lang])
			k.cityGeo = s.City.GeoNameID
			if s.Location.Lat != nil {
				k.lat = int32(math.Round(*s.Location.Lat * scale))
			}
			if s.Location.Lon != nil {
				k.lon = int32(math.Round(*s.Location.Lon * scale))
			}
			k.acc = s.Location.Acc
			k.tz = strs.id(s.Location.TZ)
		}

		recID, ok := recKeys[k]
		if !ok {
			recID = uint32(len(recs))
			recKeys[k] = recID
			recs = append(recs, k)
		}

		pfx := result.Prefix()
		addr := pfx.Addr()
		if addr.Is4() || addr.Is4In6() {
			a := addr.As4()
			start := binary.BigEndian.Uint32(a[:])
			bits := pfx.Bits()
			if addr.Is4In6() {
				bits -= 96
			}
			last := start | uint32((uint64(1)<<(32-bits))-1)
			v4 = append(v4, rangeEntry{start: start, last: last, rec: recID})
		} else {
			s16 := addr.As16()
			v6 = append(v6, rangeEntry{s16: s16, l16: lastV6(s16, pfx.Bits()), rec: recID})
		}
	}

	if err := checkStrings(srcMMDB, strs.list); err != nil {
		return nil, err
	}

	return &buildResult{
		strs:     strs.list,
		recs:     recs,
		v4b:      mergeV4(v4),
		v6b:      mergeV6(v6),
		networks: networks,
	}, nil
}

// Open builds the compact database in memory directly from a MaxMind City
// .mmdb and returns a ready-to-use *ip2geo.Reader. No intermediate file and no
// command-line tool are involved — this is the one-call, drop-in library entry
// point. Building scans the whole source database, which takes about half a
// minute and about 2 GiB of memory for a City source;
// call it once at startup, or use Build + ip2geo.Open to cache the result.
//
// Errors wrap ErrOptions, ErrDatabaseType, ErrLanguage or ErrStringTooLong
// when the options or the source database are unsuitable, and the underlying
// error (for example fs.ErrNotExist) when the source cannot be read.
func Open(srcMMDB string, opts Options) (*ip2geo.Reader, error) {
	br, err := buildData(srcMMDB, &opts)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if _, err := writeAll(&buf, opts, br); err != nil {
		return nil, fmt.Errorf("mmdb: encode: %w", err)
	}
	r, err := ip2geo.OpenBytes(buf.Bytes())
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
	br, err := buildData(srcMMDB, &opts)
	if err != nil {
		return nil, err
	}
	n, err := writeFileAtomic(outPath, func(w io.Writer) (int64, error) {
		return writeAll(w, opts, br)
	})
	if err != nil {
		return nil, err
	}
	return &Stats{
		Profile: opts.Profile, Networks: br.networks, Records: len(br.recs),
		Strings: len(br.strs), V4Ranges: len(br.v4b), V6Ranges: len(br.v6b), Bytes: n,
	}, nil
}

// writeFileAtomic writes path through a temporary file in the same directory
// that is renamed over path only after write, flush, sync and close succeed.
func writeFileAtomic(path string, write func(io.Writer) (int64, error)) (n int64, err error) {
	perm := os.FileMode(0o644)
	if fi, statErr := os.Stat(path); statErr == nil && fi.Mode().IsRegular() {
		perm = fi.Mode().Perm()
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".geoc-*")
	if err != nil {
		return 0, fmt.Errorf("mmdb: create temporary file in %q: %w", dir, err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			f.Close() // already closed on the rename path; the error is moot
			os.Remove(tmp)
		}
	}()

	w := bufio.NewWriterSize(f, 1<<20)
	if n, err = write(w); err != nil {
		return 0, fmt.Errorf("mmdb: write %q: %w", tmp, err)
	}
	if err = w.Flush(); err != nil {
		return 0, fmt.Errorf("mmdb: write %q: %w", tmp, err)
	}
	if err = f.Chmod(perm); err != nil {
		return 0, fmt.Errorf("mmdb: chmod %q: %w", tmp, err)
	}
	if err = f.Sync(); err != nil {
		return 0, fmt.Errorf("mmdb: sync %q: %w", tmp, err)
	}
	if err = f.Close(); err != nil {
		return 0, fmt.Errorf("mmdb: close %q: %w", tmp, err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return 0, fmt.Errorf("mmdb: replace %q: %w", path, err)
	}
	return n, nil
}

// boundary starts a range of addresses that maps to record rec, up to the
// next boundary. rec 0 marks a gap (no data).
type boundary struct {
	start uint32
	s16   [16]byte
	rec   uint32
}

// mergeV4 turns IPv4 networks into sorted range boundaries, merging adjacent
// networks with the same record and emitting a rec-0 boundary after a run
// that is followed by a gap. Networks must not overlap and must not use
// rec 0, which buildData reserves for gaps.
func mergeV4(in []rangeEntry) []boundary {
	sort.Slice(in, func(i, j int) bool { return in[i].start < in[j].start })
	var out []boundary
	var expected uint64
	var lastRec uint32
	for _, e := range in {
		if uint64(e.start) > expected && lastRec != 0 {
			out = append(out, boundary{start: uint32(expected), rec: 0})
			lastRec = 0
		}
		if e.rec != lastRec {
			out = append(out, boundary{start: e.start, rec: e.rec})
			lastRec = e.rec
		}
		expected = uint64(e.last) + 1
	}
	if lastRec != 0 && expected <= math.MaxUint32 {
		out = append(out, boundary{start: uint32(expected), rec: 0})
	}
	return out
}

// mergeV6 is mergeV4 for IPv6 networks.
func mergeV6(in []rangeEntry) []boundary {
	sort.Slice(in, func(i, j int) bool { return cmp16b(in[i].s16, in[j].s16) < 0 })
	var out []boundary
	var expected [16]byte
	haveExpected := false
	var lastRec uint32
	for _, e := range in {
		if lastRec != 0 {
			if haveExpected && cmp16b(e.s16, expected) > 0 {
				out = append(out, boundary{s16: expected, rec: 0})
				lastRec = 0
			} else if !haveExpected && e.s16 != expected {
				out = append(out, boundary{s16: expected, rec: 0})
				lastRec = 0
			}
		}
		if e.rec != lastRec {
			out = append(out, boundary{s16: e.s16, rec: e.rec})
			lastRec = e.rec
		}
		next, overflow := inc16(e.l16)
		expected = next
		haveExpected = !overflow
	}
	if lastRec != 0 && haveExpected {
		out = append(out, boundary{s16: expected, rec: 0})
	}
	return out
}

// writeAll encodes a GEO1 database. String lengths must already have been
// checked by checkStrings.
func writeAll(w io.Writer, opts Options, br *buildResult) (int64, error) {
	strs, recs, v4, v6 := br.strs, br.recs, br.v4b, br.v6b
	cw := &countWriter{w: w}
	var b4 [4]byte
	var b2 [2]byte

	cw.Write([]byte(magic))
	cw.Write([]byte{version, byte(opts.Profile), opts.LatScale, 0})

	putU32(cw, b4[:], uint32(len(strs)))
	for _, s := range strs {
		binary.LittleEndian.PutUint16(b2[:], uint16(len(s)))
		cw.Write(b2[:])
		cw.Write([]byte(s))
	}

	putU32(cw, b4[:], uint32(len(recs)))
	rw := ip2geo.RecWidth(opts.Profile)
	binary.LittleEndian.PutUint16(b2[:], uint16(rw))
	cw.Write(b2[:])
	rec := make([]byte, rw)
	for _, k := range recs {
		putRecordU32(rec, opts.Profile, k)
		cw.Write(rec)
	}

	putU32(cw, b4[:], uint32(len(v4)))
	for _, e := range v4 {
		putU32(cw, b4[:], e.start)
	}
	for _, e := range v4 {
		putU32(cw, b4[:], e.rec)
	}

	putU32(cw, b4[:], uint32(len(v6)))
	for _, e := range v6 {
		cw.Write(e.s16[:])
	}
	for _, e := range v6 {
		putU32(cw, b4[:], e.rec)
	}
	return cw.n, cw.err
}

func putRecordU32(dst []byte, p ip2geo.Profile, k recKey) {
	put := func(i int, v uint32) { binary.LittleEndian.PutUint32(dst[i*4:], v) }
	put(0, k.countryISO)
	put(1, k.countryName)
	put(2, k.continent)
	put(3, k.countryGeo)
	if p >= ip2geo.ProfileRegion {
		put(4, k.regionISO)
		put(5, k.regionName)
	}
	if p >= ip2geo.ProfileCity {
		put(6, k.cityName)
		put(7, k.cityGeo)
		put(8, uint32(k.lat))
		put(9, uint32(k.lon))
		put(10, uint32(k.acc))
		put(11, k.tz)
	}
}

type countWriter struct {
	w   io.Writer
	n   int64
	err error
}

func (c *countWriter) Write(p []byte) {
	if c.err != nil {
		return
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	c.err = err
}

func putU32(c *countWriter, buf []byte, v uint32) {
	binary.LittleEndian.PutUint32(buf, v)
	c.Write(buf)
}

func cmp16b(a, b [16]byte) int {
	for i := 0; i < 16; i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func inc16(a [16]byte) (res [16]byte, overflow bool) {
	res = a
	for i := 15; i >= 0; i-- {
		res[i]++
		if res[i] != 0 {
			return res, false
		}
	}
	return res, true
}

func lastV6(start [16]byte, bits int) [16]byte {
	last := start
	for i := bits; i < 128; i++ {
		last[i/8] |= 1 << (7 - uint(i%8))
	}
	return last
}
