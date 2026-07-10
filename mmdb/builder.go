// Package mmdb builds a fast ip2geo reader from a MaxMind GeoIP2/GeoLite2 City
// .mmdb file.
//
// Use Open for the one-call, drop-in library workflow (build in memory, no
// files):
//
//	db, err := mmdb.Open("GeoIP2-City.mmdb", mmdb.Options{Profile: ip2geo.ProfileCity})
//	rec, ok := db.Lookup(ip)
//
// Or use Build once to write a compact .geoc file that ip2geo.Open then loads
// in a fraction of a second with zero third-party dependencies.
package mmdb

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"sort"

	"github.com/haxqer/ip2geo"
	"github.com/oschwald/maxminddb-golang/v2"
)

const magic = "GEO1"
const version = 1

// Options controls a build.
type Options struct {
	Profile  ip2geo.Profile
	Lang     string // language for names, default "en"
	LatScale uint8  // lat/lon decimals kept in ProfileCity, default 4
}

// Stats summarizes a completed build.
type Stats struct {
	Profile  ip2geo.Profile
	Networks int
	Records  int
	Strings  int
	V4Ranges int
	V6Ranges int
	Bytes    int64
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

func buildData(srcMMDB string, opts *Options) (*buildResult, error) {
	if opts.Lang == "" {
		opts.Lang = "en"
	}
	if opts.Profile == ip2geo.ProfileCity && opts.LatScale == 0 {
		opts.LatScale = 4
	}
	scale := math.Pow10(int(opts.LatScale))

	db, err := maxminddb.Open(srcMMDB)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	strs := newInterner()
	recKeys := map[recKey]uint32{{}: 0}
	recs := []recKey{{}}

	var v4, v6 []rangeEntry
	networks := 0

	for result := range db.Networks() {
		var s srcRecord
		if err := result.Decode(&s); err != nil {
			return nil, err
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
// point. Building scans the whole source database, so it takes a few seconds;
// call it once at startup, or use Build + ip2geo.Open to cache the result.
func Open(srcMMDB string, opts Options) (*ip2geo.Reader, error) {
	br, err := buildData(srcMMDB, &opts)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if _, werr := writeAll(&buf, opts, br.strs, br.recs, br.v4b, br.v6b); werr != nil {
		return nil, werr
	}
	return ip2geo.OpenBytes(buf.Bytes())
}

// Build reads srcMMDB and writes a compact .geoc database to outPath. This is
// the pre-build workflow: ip2geo.Open then loads the file with zero third-party
// dependencies and a sub-second startup.
func Build(srcMMDB, outPath string, opts Options) (*Stats, error) {
	br, err := buildData(srcMMDB, &opts)
	if err != nil {
		return nil, err
	}
	out, err := os.Create(outPath)
	if err != nil {
		return nil, err
	}
	w := bufio.NewWriterSize(out, 1<<20)
	n, werr := writeAll(w, opts, br.strs, br.recs, br.v4b, br.v6b)
	if werr != nil {
		out.Close()
		return nil, werr
	}
	if err := w.Flush(); err != nil {
		out.Close()
		return nil, err
	}
	if err := out.Close(); err != nil {
		return nil, err
	}
	return &Stats{
		Profile: opts.Profile, Networks: br.networks, Records: len(br.recs),
		Strings: len(br.strs), V4Ranges: len(br.v4b), V6Ranges: len(br.v6b), Bytes: n,
	}, nil
}

type boundary struct {
	start uint32
	s16   [16]byte
	rec   uint32
}

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

func writeAll(w io.Writer, opts Options, strs []string, recs []recKey, v4, v6 []boundary) (int64, error) {
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
