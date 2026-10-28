package mmdb_test

// An independent GEO2 parser for the builder tests, written from
// docs/FORMAT.md alone. It shares no code with the builder's writer and
// takes from internal/format only the geo record field offsets
// (format.Rec*), so a writer bug is not hidden by the same bug in a shared
// helper. The revision 2.1 parts (XFIELDS, XRECORDS, LANGS and ASN
// records) are decoded from the spec's tables, written out again below.
//
// parseGEO2 checks every rule a conforming reader checks (FORMAT.md, "Reader
// requirements") and, on top, the choices the spec leaves to the writer and
// this builder makes: sections in id order, tightly packed, zero padding, no
// trailing bytes, exception sections only when there are exceptions,
// distinct and all-referenced strings and records, u16-first string
// numbering, merged boundaries. Its lookup follows the spec's algorithms,
// so the tests can check a built file's answers without package ip2geo's
// reader.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"net/netip"
	"slices"
	"sort"
	"unicode/utf8"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/internal/format"
)

// Section ids from the spec's table.
const (
	gsMeta     = 1
	gsStrIdx   = 2
	gsStrBlob  = 3
	gsRecords  = 4
	gsV4Idx    = 5
	gsV4Starts = 6
	gsV4IDs    = 7
	gsV6Starts = 8
	gsV6IDs    = 9
	gsV6ExcIdx = 10
	gsV6ExcLo  = 11
	gsXFields  = 12
	gsXRecords = 13
	gsLangs    = 14
)

var geo2SectionNames = map[uint32]string{
	gsMeta: "META", gsStrIdx: "STRIDX", gsStrBlob: "STRBLOB", gsRecords: "RECORDS",
	gsV4Idx: "V4IDX", gsV4Starts: "V4STARTS", gsV4IDs: "V4IDS", gsV6Starts: "V6STARTS",
	gsV6IDs: "V6IDS", gsV6ExcIdx: "V6EXCIDX", gsV6ExcLo: "V6EXCLO",
	gsXFields: "XFIELDS", gsXRecords: "XRECORDS", gsLangs: "LANGS",
}

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// geo2Section is one directory entry.
type geo2Section struct {
	id, crc     uint32
	off, length uint64
}

// geo2Meta is the META section with its string ids resolved.
type geo2Meta struct {
	buildTime, sourceTime, networks            uint64
	sourceType, language, description, builder string
	distinctRecords                            uint32
}

// geo2Rec is a decoded record with its raw string ids.
type geo2Rec struct {
	countryISO, countryName, continent, regionISO, regionName, cityName, tz uint32
	countryGeo, cityGeo                                                     uint32
	lat, lon                                                                int32
	acc                                                                     uint16
	asn, org                                                                uint32 // ASN profile
}

// geo2File is a parsed GEO2 file.
type geo2File struct {
	size     int
	profile  ip2geo.Profile
	latScale uint8
	idWidth  uint8
	dir      []geo2Section // in file order
	sec      map[uint32][]byte
	meta     geo2Meta

	strs []string
	// u16Strings is the number of strings at the start of the table that
	// 16-bit record fields use (the empty string included); no 16-bit field
	// uses a string at or after it.
	u16Strings int

	recWidth int
	recs     []geo2Rec

	// Revision 2.1: the XFIELDS bits, the XRECORDS entries (nil without
	// XRECORDS), the name kinds each extra language stores, and the
	// languages (nil without LANGS).
	xfields uint32
	xWidth  int
	x       []geo2X
	kinds   []int
	langs   []string

	v4idx   []uint32
	v4low   []uint16
	v4start []uint32 // absolute starts
	v4id    []uint32

	v6hi, v6lo []uint64
	v6id       []uint32
	excIdx     []uint32
	excLo      []uint64
}

func le16(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }
func le24(b []byte) uint32 { return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 }
func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
func le64(b []byte) uint64 { return binary.LittleEndian.Uint64(b) }

// minIDWidth is the smallest of 1..4 bytes that holds nRecords-1.
func minIDWidth(nRecords int) uint8 {
	switch m := nRecords - 1; {
	case m < 1<<8:
		return 1
	case m < 1<<16:
		return 2
	case m < 1<<24:
		return 3
	}
	return 4
}

func readID(b []byte, w uint8, i int) uint32 {
	var v uint32
	for j := int(w) - 1; j >= 0; j-- {
		v = v<<8 | uint32(b[i*int(w)+j])
	}
	return v
}

// parseGEO2 decodes b and checks it against the spec and the builder's
// layout rules. The first broken rule is returned as an error.
func parseGEO2(b []byte) (*geo2File, error) {
	f := &geo2File{size: len(b), sec: map[uint32][]byte{}}

	// Header.
	if len(b) < 32 {
		return nil, fmt.Errorf("file of %d bytes is shorter than the header", len(b))
	}
	if string(b[:4]) != "GEO2" || b[4] != 2 {
		return nil, fmt.Errorf("magic %q version %d, want GEO2 2", b[:4], b[4])
	}
	if b[5] > 3 || b[6] > 7 || b[7] < 1 || b[7] > 4 {
		return nil, fmt.Errorf("header profile %d latScale %d idWidth %d", b[5], b[6], b[7])
	}
	f.profile, f.latScale, f.idWidth = ip2geo.Profile(b[5]), b[6], b[7]
	if !allZero(b[12:32]) {
		return nil, errors.New("reserved header bytes are not zero")
	}
	count := uint64(le32(b[8:]))
	dirEnd := 32 + 24*count
	if dirEnd > uint64(len(b)) {
		return nil, fmt.Errorf("directory of %d entries does not fit in %d bytes", count, len(b))
	}

	// Directory: every id known and once, in ascending id order (builder
	// rule), each section at the next multiple of 8 after the previous one
	// (spec: ascending offsets, no overlap; builder: tight), zero padding,
	// and the file ending with the last section.
	end := dirEnd
	for i := range count {
		e := b[32+24*i:]
		s := geo2Section{id: le32(e), crc: le32(e[4:]), off: le64(e[8:]), length: le64(e[16:])}
		name, known := geo2SectionNames[s.id]
		switch {
		case !known:
			return nil, fmt.Errorf("directory entry %d: unknown section id %d", i, s.id)
		case i > 0 && s.id <= f.dir[i-1].id:
			return nil, fmt.Errorf("%s: section ids not strictly ascending", name)
		case s.off%8 != 0:
			return nil, fmt.Errorf("%s: offset %d is not a multiple of 8", name, s.off)
		case s.off < end:
			return nil, fmt.Errorf("%s: offset %d overlaps what precedes it (ends at %d)", name, s.off, end)
		case s.off != (end+7)&^7:
			return nil, fmt.Errorf("%s: offset %d, want the next multiple of 8 after %d", name, s.off, end)
		case s.off > uint64(len(b)) || s.length > uint64(len(b))-s.off:
			return nil, fmt.Errorf("%s: [%d, +%d) ends past the file (%d bytes)", name, s.off, s.length, len(b))
		}
		if !allZero(b[end:s.off]) {
			return nil, fmt.Errorf("%s: padding before it is not zero", name)
		}
		data := b[s.off : s.off+s.length]
		if got := crc32.Checksum(data, castagnoli); got != s.crc {
			return nil, fmt.Errorf("%s: CRC-32C %#08x, directory says %#08x", name, got, s.crc)
		}
		f.dir = append(f.dir, s)
		f.sec[s.id] = data
		end = s.off + s.length
	}
	if end != uint64(len(b)) {
		return nil, fmt.Errorf("%d bytes after the last section", uint64(len(b))-end)
	}
	for id := uint32(gsMeta); id <= gsV6IDs; id++ {
		if _, ok := f.sec[id]; !ok {
			return nil, fmt.Errorf("required section %s is missing", geo2SectionNames[id])
		}
	}
	_, hasExcIdx := f.sec[gsV6ExcIdx]
	_, hasExcLo := f.sec[gsV6ExcLo]
	if hasExcIdx != hasExcLo {
		return nil, errors.New("only one of V6EXCIDX and V6EXCLO is present")
	}

	// Section lengths.
	sec := f.sec
	f.recWidth = format.RecordWidth(uint8(f.profile))
	if f.profile == ip2geo.ProfileASN {
		f.recWidth = asnRecWidth
	}
	switch {
	case len(sec[gsMeta]) != 64:
		return nil, fmt.Errorf("META is %d bytes, want 64", len(sec[gsMeta]))
	case len(sec[gsStrIdx]) < 4 || len(sec[gsStrIdx])%4 != 0:
		return nil, fmt.Errorf("STRIDX is %d bytes", len(sec[gsStrIdx]))
	case len(sec[gsRecords]) == 0 || len(sec[gsRecords])%f.recWidth != 0:
		return nil, fmt.Errorf("RECORDS is %d bytes, not a non-zero multiple of %d", len(sec[gsRecords]), f.recWidth)
	case len(sec[gsV4Idx]) != 65537*4:
		return nil, fmt.Errorf("V4IDX is %d bytes", len(sec[gsV4Idx]))
	case len(sec[gsV4Starts])%2 != 0 || len(sec[gsV4IDs]) != len(sec[gsV4Starts])/2*int(f.idWidth):
		return nil, fmt.Errorf("V4STARTS %d bytes, V4IDS %d bytes", len(sec[gsV4Starts]), len(sec[gsV4IDs]))
	case len(sec[gsV6Starts])%8 != 0 || len(sec[gsV6IDs]) != len(sec[gsV6Starts])/8*int(f.idWidth):
		return nil, fmt.Errorf("V6STARTS %d bytes, V6IDS %d bytes", len(sec[gsV6Starts]), len(sec[gsV6IDs]))
	case len(sec[gsV6ExcIdx])%4 != 0 || len(sec[gsV6ExcLo]) != len(sec[gsV6ExcIdx])/4*8:
		return nil, fmt.Errorf("V6EXCIDX %d bytes, V6EXCLO %d bytes", len(sec[gsV6ExcIdx]), len(sec[gsV6ExcLo]))
	case hasExcIdx && len(sec[gsV6ExcIdx]) == 0:
		return nil, errors.New("empty exception sections are present (the builder omits them)")
	}

	if err := f.parseStrings(); err != nil {
		return nil, err
	}
	if err := f.parseRecords(); err != nil {
		return nil, err
	}
	if err := f.parseMeta(); err != nil {
		return nil, err
	}
	if err := f.parseExt(); err != nil {
		return nil, err
	}
	if err := f.checkRecordsDistinct(); err != nil {
		return nil, err
	}
	if err := f.checkStringUse(); err != nil {
		return nil, err
	}
	if err := f.parseV4(); err != nil {
		return nil, err
	}
	if err := f.parseV6(); err != nil {
		return nil, err
	}
	return f, f.checkRecordUse()
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// parseStrings checks STRIDX and STRBLOB: offsets from 0, non-decreasing,
// ending at the blob's length; string 0 empty; UTF-8; no duplicates
// (builder rule: strings are interned).
func (f *geo2File) parseStrings() error {
	idx, blob := f.sec[gsStrIdx], f.sec[gsStrBlob]
	n := len(idx)/4 - 1
	if le32(idx) != 0 {
		return fmt.Errorf("STRIDX[0] = %d, want 0", le32(idx))
	}
	if int(le32(idx[4*n:])) != len(blob) {
		return fmt.Errorf("STRIDX[%d] = %d, want len(STRBLOB) = %d", n, le32(idx[4*n:]), len(blob))
	}
	seen := make(map[string]int, n)
	f.strs = make([]string, n)
	for i := range n {
		a, z := le32(idx[4*i:]), le32(idx[4*i+4:])
		if z < a {
			return fmt.Errorf("STRIDX decreases at %d: %d then %d", i, a, z)
		}
		s := string(blob[a:z])
		if !utf8.ValidString(s) {
			return fmt.Errorf("string %d %.40q is not valid UTF-8", i, s)
		}
		if j, dup := seen[s]; dup {
			return fmt.Errorf("strings %d and %d are both %.40q", j, i, s)
		}
		seen[s] = i
		f.strs[i] = s
	}
	if f.strs[0] != "" {
		return fmt.Errorf("string 0 is %q, want empty", f.strs[0])
	}
	return nil
}

// parseRecords decodes RECORDS at the format.Rec* offsets (an ASN record:
// u32 number @0, u24 organization @4) and checks record 0, the string ids
// and the id width.
func (f *geo2File) parseRecords() error {
	raw := f.sec[gsRecords]
	n := len(raw) / f.recWidth
	if !allZero(raw[:f.recWidth]) {
		return errors.New("record 0 is not all zero")
	}
	if want := minIDWidth(n); f.idWidth != want {
		return fmt.Errorf("idWidth %d for %d records, want %d", f.idWidth, n, want)
	}
	f.recs = make([]geo2Rec, n)
	for i := range n {
		b := raw[i*f.recWidth : (i+1)*f.recWidth]
		r := &f.recs[i]
		if f.profile == ip2geo.ProfileASN {
			r.asn, r.org = le32(b), le24(b[4:])
			if int(r.org) >= len(f.strs) {
				return fmt.Errorf("record %d: organization string id %d, only %d strings", i, r.org, len(f.strs))
			}
			continue
		}
		r.countryISO = uint32(le16(b[format.RecCountryISO:]))
		r.countryName = uint32(le16(b[format.RecCountryName:]))
		r.continent = uint32(le16(b[format.RecContinent:]))
		r.countryGeo = le32(b[format.RecCountryGeo:])
		if f.profile >= ip2geo.ProfileRegion {
			r.regionISO = uint32(le16(b[format.RecRegionISO:]))
			r.regionName = uint32(le16(b[format.RecRegionName:]))
		}
		if f.profile >= ip2geo.ProfileCity {
			r.cityName = le24(b[format.RecCityName:])
			r.cityGeo = le32(b[format.RecCityGeo:])
			r.lat = int32(le32(b[format.RecLat:]))
			r.lon = int32(le32(b[format.RecLon:]))
			r.acc = le16(b[format.RecAccuracy:])
			r.tz = uint32(le16(b[format.RecTimeZone:]))
		}
		for _, id := range []uint32{r.countryISO, r.countryName, r.continent, r.regionISO, r.regionName, r.cityName, r.tz} {
			if int(id) >= len(f.strs) {
				return fmt.Errorf("record %d: string id %d, only %d strings", i, id, len(f.strs))
			}
		}
	}
	return nil
}

// checkRecordsDistinct checks that the records are distinct (builder rule;
// record 0 and the all-empty record may both be zero). With XRECORDS a
// record is its RECORDS entry and its XRECORDS entry together, so RECORDS
// alone may repeat.
func (f *geo2File) checkRecordsDistinct() error {
	raw, xraw := f.sec[gsRecords], f.sec[gsXRecords]
	seen := make(map[string]int, len(f.recs))
	for i := 1; i < len(f.recs); i++ {
		k := string(raw[i*f.recWidth:(i+1)*f.recWidth]) + "|"
		if f.x != nil {
			k += string(xraw[i*f.xWidth : (i+1)*f.xWidth])
		}
		if j, dup := seen[k]; dup {
			return fmt.Errorf("records %d and %d are equal", j, i)
		}
		seen[k] = i
	}
	return nil
}

func (f *geo2File) parseMeta() error {
	m := f.sec[gsMeta]
	if !allZero(m[44:]) {
		return errors.New("reserved META bytes are not zero")
	}
	ids := []uint32{le32(m[24:]), le32(m[28:]), le32(m[32:]), le32(m[36:])}
	for _, id := range ids {
		if int(id) >= len(f.strs) {
			return fmt.Errorf("META string id %d, only %d strings", id, len(f.strs))
		}
	}
	f.meta = geo2Meta{
		buildTime: le64(m), sourceTime: le64(m[8:]), networks: le64(m[16:]),
		sourceType: f.strs[ids[0]], language: f.strs[ids[1]], description: f.strs[ids[2]], builder: f.strs[ids[3]],
		distinctRecords: le32(m[40:]),
	}
	return nil
}

// checkStringUse checks the u16-first numbering (the strings 16-bit fields
// use are exactly ids 0..k-1) and that every string is used by a record or
// by META.
func (f *geo2File) checkStringUse() error {
	used16 := make([]bool, len(f.strs))
	used := make([]bool, len(f.strs))
	used16[0], used[0] = true, true
	for _, r := range f.recs {
		for _, id := range []uint32{r.countryISO, r.countryName, r.continent, r.regionISO, r.regionName, r.tz} {
			used16[id], used[id] = true, true
		}
		used[r.cityName] = true
		used[r.org] = true
	}
	for i := range f.x {
		for _, id := range f.x[i].strings() {
			used[id] = true
		}
	}
	for _, l := range f.langs {
		used[slices.Index(f.strs, l)] = true // strings are distinct
	}
	m := f.sec[gsMeta]
	for _, off := range []int{24, 28, 32, 36} {
		used[le32(m[off:])] = true
	}
	k := 0
	for k < len(used16) && used16[k] {
		k++
	}
	for i := k; i < len(used16); i++ {
		if used16[i] {
			return fmt.Errorf("string %d %.40q is used by a 16-bit field but string %d %.40q, before it, is not",
				i, f.strs[i], k, f.strs[k])
		}
	}
	f.u16Strings = k
	for i, u := range used {
		if !u {
			return fmt.Errorf("string %d %.40q is not used by any record or by META", i, f.strs[i])
		}
	}
	return nil
}

// checkIDs checks a boundary id list: below nRecords; and, as the merge
// makes them, the first non-zero and no two neighbors equal.
func (f *geo2File) checkIDs(family string, ids []uint32) error {
	for i, id := range ids {
		switch {
		case int(id) >= len(f.recs):
			return fmt.Errorf("%s boundary %d: id %d, only %d records", family, i, id, len(f.recs))
		case i == 0 && id == 0:
			return fmt.Errorf("%s: the first boundary is a gap", family)
		case i > 0 && id == ids[i-1]:
			return fmt.Errorf("%s boundaries %d and %d both have id %d (not merged)", family, i-1, i, id)
		}
	}
	return nil
}

// parseV4 checks V4IDX (V4IDX[0] = 0, non-decreasing, V4IDX[65536] = nV4),
// derives the absolute starts and checks that they strictly ascend.
func (f *geo2File) parseV4() error {
	idx, starts, ids := f.sec[gsV4Idx], f.sec[gsV4Starts], f.sec[gsV4IDs]
	n := len(starts) / 2
	f.v4idx = make([]uint32, 65537)
	for g := range f.v4idx {
		f.v4idx[g] = le32(idx[4*g:])
		if g > 0 && f.v4idx[g] < f.v4idx[g-1] {
			return fmt.Errorf("V4IDX decreases at %d", g)
		}
	}
	if f.v4idx[0] != 0 || int(f.v4idx[65536]) != n {
		return fmt.Errorf("V4IDX[0] = %d, V4IDX[65536] = %d; want 0 and nV4 = %d", f.v4idx[0], f.v4idx[65536], n)
	}
	f.v4low = make([]uint16, n)
	f.v4start = make([]uint32, n)
	f.v4id = make([]uint32, n)
	for g := range 65536 {
		for i := f.v4idx[g]; i < f.v4idx[g+1]; i++ {
			f.v4low[i] = le16(starts[2*i:])
			f.v4start[i] = uint32(g)<<16 | uint32(f.v4low[i])
			if i > 0 && f.v4start[i] <= f.v4start[i-1] {
				return fmt.Errorf("IPv4 starts not strictly ascending at %d", i)
			}
		}
	}
	for i := range n {
		f.v4id[i] = readID(ids, f.idWidth, i)
	}
	return f.checkIDs("IPv4", f.v4id)
}

// parseV6 checks V6STARTS (non-decreasing), the exception table (strictly
// ascending indices below nV6, non-zero low halves) and that the 128-bit
// starts strictly ascend.
func (f *geo2File) parseV6() error {
	starts, ids := f.sec[gsV6Starts], f.sec[gsV6IDs]
	n := len(starts) / 8
	f.v6hi = make([]uint64, n)
	f.v6lo = make([]uint64, n)
	f.v6id = make([]uint32, n)
	for i := range n {
		f.v6hi[i] = le64(starts[8*i:])
		f.v6id[i] = readID(ids, f.idWidth, i)
	}
	ei, el := f.sec[gsV6ExcIdx], f.sec[gsV6ExcLo]
	for j := range len(ei) / 4 {
		i, lo := le32(ei[4*j:]), le64(el[8*j:])
		switch {
		case int(i) >= n:
			return fmt.Errorf("V6EXCIDX[%d] = %d, only %d boundaries", j, i, n)
		case j > 0 && i <= f.excIdx[j-1]:
			return fmt.Errorf("V6EXCIDX not strictly ascending at %d", j)
		case lo == 0:
			return fmt.Errorf("V6EXCLO[%d] is zero", j)
		}
		f.excIdx = append(f.excIdx, i)
		f.excLo = append(f.excLo, lo)
		f.v6lo[i] = lo
	}
	for i := 1; i < n; i++ {
		if f.v6hi[i] < f.v6hi[i-1] {
			return fmt.Errorf("V6STARTS decreases at %d", i)
		}
		if f.v6hi[i] == f.v6hi[i-1] && f.v6lo[i] <= f.v6lo[i-1] {
			return fmt.Errorf("IPv6 starts not strictly ascending at %d", i)
		}
	}
	return f.checkIDs("IPv6", f.v6id)
}

// checkRecordUse checks that every record but 0 is the record of some
// boundary (builder rule: records are made for networks).
func (f *geo2File) checkRecordUse() error {
	used := make([]bool, len(f.recs))
	for _, id := range f.v4id {
		used[id] = true
	}
	for _, id := range f.v6id {
		used[id] = true
	}
	for i := 1; i < len(used); i++ {
		if !used[i] {
			return fmt.Errorf("record %d is not used by any boundary", i)
		}
	}
	return nil
}

// record returns record id as a lookup result.
func (f *geo2File) record(id uint32) ip2geo.Record {
	r := f.recs[id]
	div := math.Pow10(int(f.latScale))
	return ip2geo.Record{
		CountryISO: f.strs[r.countryISO], CountryName: f.strs[r.countryName], Continent: f.strs[r.continent],
		CountryGeoNameID: r.countryGeo, RegionISO: f.strs[r.regionISO], RegionName: f.strs[r.regionName],
		City: f.strs[r.cityName], CityGeoNameID: r.cityGeo,
		Latitude: float64(r.lat) / div, Longitude: float64(r.lon) / div,
		AccuracyRadius: r.acc, TimeZone: f.strs[r.tz],
	}
}

// search4 is the spec's IPv4 lookup: the last boundary of a's bucket whose
// low 16 bits are <= a's, else the one before the bucket.
func (f *geo2File) search4(a uint32) uint32 {
	g := a >> 16
	lo, hi := int(f.v4idx[g]), int(f.v4idx[g+1])
	i := lo + sort.Search(hi-lo, func(j int) bool { return f.v4low[lo+j] > uint16(a) }) - 1
	if i < 0 {
		return 0
	}
	return f.v4id[i]
}

// search6 is the spec's IPv6 lookup: the last boundary with hi <= kh, then
// back over the run of equal hi whose lo is above kl.
func (f *geo2File) search6(kh, kl uint64) uint32 {
	i := sort.Search(len(f.v6hi), func(j int) bool { return f.v6hi[j] > kh }) - 1
	for i >= 0 && f.v6hi[i] == kh && f.v6lo[i] > kl {
		i--
	}
	if i < 0 {
		return 0
	}
	return f.v6id[i]
}

// lookupID returns the record id of ip, mapping IPv4 in IPv6 form as the
// spec says (::ffff:0:0/96, ::/96, 2002::/16, 2001::/32; not 64:ff9b::/96).
func (f *geo2File) lookupID(ip netip.Addr) uint32 {
	if !ip.IsValid() {
		return 0
	}
	if ip.Is4() || ip.Is4In6() {
		a := ip.As4()
		return f.search4(binary.BigEndian.Uint32(a[:]))
	}
	a := ip.As16()
	hi, lo := binary.BigEndian.Uint64(a[:8]), binary.BigEndian.Uint64(a[8:])
	switch {
	case hi>>48 == 0x2002:
		return f.search4(uint32(hi >> 16))
	case hi>>32 == 0x20010000:
		return f.search4(uint32(hi))
	case hi == 0 && lo>>32 == 0:
		return f.search4(uint32(lo))
	}
	return f.search6(hi, lo)
}

// lookup is Reader.Lookup on the parsed file.
func (f *geo2File) lookup(ip netip.Addr) (ip2geo.Record, bool) {
	id := f.lookupID(ip)
	if id == 0 {
		return ip2geo.Record{}, false
	}
	return f.record(id), true
}

// v4Boundaries renders the IPv4 boundaries as "start=id" for comparisons.
func (f *geo2File) v4Boundaries() string {
	var buf bytes.Buffer
	for i, s := range f.v4start {
		var a [4]byte
		binary.BigEndian.PutUint32(a[:], s)
		if i > 0 {
			buf.WriteByte(' ')
		}
		fmt.Fprintf(&buf, "%s=%d", netip.AddrFrom4(a), f.v4id[i])
	}
	return buf.String()
}

// v6Boundaries is v4Boundaries for IPv6.
func (f *geo2File) v6Boundaries() string {
	var buf bytes.Buffer
	for i := range f.v6hi {
		var a [16]byte
		binary.BigEndian.PutUint64(a[:8], f.v6hi[i])
		binary.BigEndian.PutUint64(a[8:], f.v6lo[i])
		if i > 0 {
			buf.WriteByte(' ')
		}
		fmt.Fprintf(&buf, "%s=%d", netip.AddrFrom16(a), f.v6id[i])
	}
	return buf.String()
}

// Revision 2.1, from the spec's tables.

// asnRecWidth is the width of a profile 3 (ASN) record.
const asnRecWidth = 7

// xSetWidth[b] is the block width of field set bit b.
var xSetWidth = [8]int{1, 7, 24, 14, 3, 2, 1, 1}

// Name kinds, in the spec's order.
const (
	kCountry = iota
	kRegion
	kCity
	kContinent
	kRegion2
	kRegistered
	kRepresented
)

// xAllowed reports whether field set bits are allowed for a profile:
// "every set except subdivisions, postal, metro and location for country;
// every set except postal, metro and location for region; all for city;
// none for ASN".
func xAllowed(profile ip2geo.Profile, bits uint32) bool {
	const subdivisions, postal, metro, location = 1 << 3, 1 << 4, 1 << 5, 1 << 7
	switch profile {
	case ip2geo.ProfileCountry:
		return bits&(subdivisions|postal|metro|location) == 0
	case ip2geo.ProfileRegion:
		return bits&(postal|metro|location) == 0
	case ip2geo.ProfileCity:
		return true
	}
	return bits == 0
}

// geo2X is a decoded XRECORDS entry with its raw string ids.
type geo2X struct {
	eu                                  bool
	contName, contGeo                   uint32
	regISO, regName, regGeo             uint32
	repISO, repName, repGeo, repType    uint32
	regEU, repEU                        bool
	sub1Geo, sub2ISO, sub2Name, sub2Geo uint32
	postal                              uint32
	metro                               uint16
	anycast, anonymousProxy, satellite  bool
	hasCoordinates, hasAccuracy         bool
	names                               [][]uint32 // names[j-1][i]: extra language j, kind kinds[i]
}

// strings returns the entry's string ids.
func (x *geo2X) strings() []uint32 {
	ids := []uint32{x.contName, x.regISO, x.regName, x.repISO, x.repName, x.repType, x.sub2ISO, x.sub2Name, x.postal}
	for _, n := range x.names {
		ids = append(ids, n...)
	}
	return ids
}

// parseExt checks and decodes XFIELDS, XRECORDS and LANGS (spec rules
// 9-12) and the builder's rule that they are present only when needed.
func (f *geo2File) parseExt() error {
	xf, hasXF := f.sec[gsXFields]
	xr, hasXR := f.sec[gsXRecords]
	lg, hasLangs := f.sec[gsLangs]
	switch {
	case hasXF != hasXR:
		return errors.New("only one of XFIELDS and XRECORDS is present")
	case hasLangs && !hasXF:
		return errors.New("LANGS without XFIELDS and XRECORDS")
	case f.profile == ip2geo.ProfileASN && (hasXF || hasLangs):
		return errors.New("an ASN file with extension sections")
	case !hasXF:
		return nil
	case len(xf) != 8 || le32(xf[4:]) != 0:
		return fmt.Errorf("XFIELDS is %d bytes %x, want 8 with a zero reserved word", len(xf), xf)
	}
	f.xfields = le32(xf)
	if f.xfields > 0xff || !xAllowed(f.profile, f.xfields) {
		return fmt.Errorf("XFIELDS bits %#x not allowed for profile %v", f.xfields, f.profile)
	}

	nLangs := 1
	if hasLangs {
		if len(lg)%4 != 0 || len(lg) < 8 {
			return fmt.Errorf("LANGS is %d bytes, want at least 2 u32", len(lg))
		}
		nLangs = len(lg) / 4
		for i := range nLangs {
			id := le32(lg[4*i:])
			switch {
			case int(id) >= len(f.strs):
				return fmt.Errorf("LANGS[%d] = %d, only %d strings", i, id, len(f.strs))
			case f.strs[id] == "" || slices.Contains(f.langs, f.strs[id]):
				return fmt.Errorf("LANGS[%d] = %q is empty or repeated", i, f.strs[id])
			}
			f.langs = append(f.langs, f.strs[id])
		}
		if f.langs[0] != f.meta.language {
			return fmt.Errorf("LANGS[0] = %q, META.language = %q", f.langs[0], f.meta.language)
		}
	}
	if f.xfields == 0 && !hasLangs {
		return errors.New("XFIELDS and XRECORDS without field sets or LANGS (the builder omits them)")
	}

	// The blocks of the sets present, in bit order, then the names.
	var off [8]int
	for b := range 8 {
		off[b] = -1
		if f.xfields&(1<<b) != 0 {
			off[b] = f.xWidth
			f.xWidth += xSetWidth[b]
		}
	}
	blocks := f.xWidth
	f.kinds = []int{kCountry}
	if f.profile >= ip2geo.ProfileRegion {
		f.kinds = append(f.kinds, kRegion)
	}
	if f.profile >= ip2geo.ProfileCity {
		f.kinds = append(f.kinds, kCity)
	}
	if off[1] >= 0 {
		f.kinds = append(f.kinds, kContinent)
	}
	if off[3] >= 0 {
		f.kinds = append(f.kinds, kRegion2)
	}
	if off[2] >= 0 {
		f.kinds = append(f.kinds, kRegistered, kRepresented)
	}
	f.xWidth += (nLangs - 1) * 3 * len(f.kinds)
	if len(xr) != len(f.recs)*f.xWidth {
		return fmt.Errorf("XRECORDS is %d bytes, want %d records × %d", len(xr), len(f.recs), f.xWidth)
	}
	if !allZero(xr[:f.xWidth]) {
		return errors.New("XRECORDS entry 0 is not all zero")
	}

	f.x = make([]geo2X, len(f.recs))
	for i := range f.x {
		e := xr[i*f.xWidth : (i+1)*f.xWidth]
		x := &f.x[i]
		flags := func(b, at int, mask byte) (byte, error) {
			v := e[off[b]+at]
			if v&^mask != 0 {
				return 0, fmt.Errorf("XRECORDS entry %d: field set %d flags %#x has undefined bits", i, b, v)
			}
			return v, nil
		}
		if o := off[0]; o >= 0 {
			v, err := flags(0, 0, 1)
			if err != nil {
				return err
			}
			x.eu = v&1 != 0
		}
		if o := off[1]; o >= 0 {
			x.contName, x.contGeo = le24(e[o:]), le32(e[o+3:])
		}
		if o := off[2]; o >= 0 {
			x.regISO, x.regName, x.regGeo = le24(e[o:]), le24(e[o+3:]), le32(e[o+6:])
			x.repISO, x.repName, x.repGeo, x.repType = le24(e[o+10:]), le24(e[o+13:]), le32(e[o+16:]), le24(e[o+20:])
			v, err := flags(2, 23, 3)
			if err != nil {
				return err
			}
			x.regEU, x.repEU = v&1 != 0, v&2 != 0
		}
		if o := off[3]; o >= 0 {
			x.sub1Geo, x.sub2ISO, x.sub2Name, x.sub2Geo = le32(e[o:]), le24(e[o+4:]), le24(e[o+7:]), le32(e[o+10:])
		}
		if o := off[4]; o >= 0 {
			x.postal = le24(e[o:])
		}
		if o := off[5]; o >= 0 {
			x.metro = le16(e[o:])
		}
		if o := off[6]; o >= 0 {
			v, err := flags(6, 0, 7)
			if err != nil {
				return err
			}
			x.anycast, x.anonymousProxy, x.satellite = v&1 != 0, v&2 != 0, v&4 != 0
		}
		if o := off[7]; o >= 0 {
			v, err := flags(7, 0, 3)
			if err != nil {
				return err
			}
			x.hasCoordinates, x.hasAccuracy = v&1 != 0, v&2 != 0
		}
		for j := 1; j < nLangs; j++ {
			ids := make([]uint32, len(f.kinds))
			for k := range ids {
				ids[k] = le24(e[blocks+(j-1)*3*len(f.kinds)+3*k:])
			}
			x.names = append(x.names, ids)
		}
		for _, id := range x.strings() {
			if int(id) >= len(f.strs) {
				return fmt.Errorf("XRECORDS entry %d: string id %d, only %d strings", i, id, len(f.strs))
			}
		}
	}
	return nil
}

// details returns record id as LookupDetails would, with the names in
// language j of LANGS (0 is the primary language): a name missing in
// language j is the primary one.
func (f *geo2File) details(id uint32, j int) ip2geo.Details {
	if f.profile == ip2geo.ProfileASN {
		r := f.recs[id]
		return ip2geo.Details{ASN: ip2geo.ASN{Number: r.asn, Organization: f.strs[r.org]}}
	}
	d := ip2geo.Details{Record: f.record(id)}
	if f.x == nil {
		return d
	}
	x := &f.x[id]
	name := func(primary uint32, kind int) string {
		if j > 0 {
			if k := slices.Index(f.kinds, kind); k >= 0 && x.names[j-1][k] != 0 {
				return f.strs[x.names[j-1][k]]
			}
		}
		return f.strs[primary]
	}
	r := f.recs[id]
	d.CountryName = name(r.countryName, kCountry)
	d.RegionName = name(r.regionName, kRegion)
	d.City = name(r.cityName, kCity)
	has := func(b int) bool { return f.xfields&(1<<b) != 0 }
	if has(0) {
		d.IsInEuropeanUnion = x.eu
	}
	if has(1) {
		d.ContinentName, d.ContinentGeoNameID = name(x.contName, kContinent), x.contGeo
	}
	if has(2) {
		d.RegisteredCountry = ip2geo.Country{ISO: f.strs[x.regISO], Name: name(x.regName, kRegistered),
			GeoNameID: x.regGeo, IsInEuropeanUnion: x.regEU}
		d.RepresentedCountry = ip2geo.Country{ISO: f.strs[x.repISO], Name: name(x.repName, kRepresented),
			GeoNameID: x.repGeo, IsInEuropeanUnion: x.repEU}
		d.RepresentedCountryType = f.strs[x.repType]
	}
	if has(3) {
		d.RegionGeoNameID = x.sub1Geo
		d.Region2ISO, d.Region2Name, d.Region2GeoNameID = f.strs[x.sub2ISO], name(x.sub2Name, kRegion2), x.sub2Geo
	}
	if has(4) {
		d.PostalCode = f.strs[x.postal]
	}
	if has(5) {
		d.MetroCode = x.metro
	}
	if has(6) {
		d.IsAnycast, d.IsAnonymousProxy, d.IsSatelliteProvider = x.anycast, x.anonymousProxy, x.satellite
	}
	if has(7) {
		d.HasCoordinates, d.HasAccuracyRadius = x.hasCoordinates, x.hasAccuracy
	}
	return d
}

// lookupDetails is Reader.LookupDetails on the parsed file, with the names
// in language j of LANGS.
func (f *geo2File) lookupDetails(ip netip.Addr, j int) (ip2geo.Details, bool) {
	id := f.lookupID(ip)
	if id == 0 {
		return ip2geo.Details{}, false
	}
	return f.details(id, j), true
}
