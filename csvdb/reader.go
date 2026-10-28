package csvdb

import (
	"encoding/binary"
	"math"
	"net/netip"
	"strconv"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/internal/build"
)

// reader turns the rows of a scanner into records and ranges of a build.
type reader struct {
	sc *scanner
	l  *layout
	b  *build.Builder
	// v6: the decimal addresses of an IP2Location file are 128-bit.
	v6 bool

	profile ip2geo.Profile
	fields  ip2geo.FieldSet
	// keyCols are the columns the build reads. ids maps their text, as
	// appended by key, to the record id, so that each distinct source
	// record is decoded and interned once.
	keyCols []int
	ids     map[string]uint32
	key     []byte

	rec   build.Record
	names [3][1]string // backing of the country, region and city names

	// prev is the last address of the previous row of each family as
	// written (IPv4 text or 32-bit, and IPv6), for the order check.
	prev [2]netip.Addr
	// end4 and end6 are the last addresses added to the build, for the
	// order check after IPv4-mapped rows have become IPv4 ranges.
	end4, end6 netip.Addr
}

func newReader(sc *scanner, l *layout, b *build.Builder) *reader {
	o := b.Options()
	r := &reader{sc: sc, l: l, b: b, profile: o.Profile, fields: o.Fields, ids: map[string]uint32{}}
	add := func(cols ...int) {
		for _, c := range cols {
			if c != 0 {
				r.keyCols = append(r.keyCols, c)
			}
		}
	}
	switch {
	case o.Profile == ip2geo.ProfileASN:
		add(l.asNum, l.asOrg)
	default:
		add(l.continent, l.country, l.countryName)
		if o.Profile >= ip2geo.ProfileRegion {
			add(l.region)
		}
		if o.Profile >= ip2geo.ProfileCity {
			add(l.city, l.lat, l.lon)
			if o.Fields&ip2geo.FieldPostal != 0 {
				add(l.postal)
			}
		}
	}
	return r
}

// endsAboveV4 reports whether the current row's last address, a decimal
// number, is above the IPv4 range.
func (r *reader) endsAboveV4() bool {
	hi, lo, ok := parseUint128(r.sc.row[1])
	return ok && (hi != 0 || lo > math.MaxUint32)
}

// value returns column c of the current row, or nil when the layout has
// no such column or the value is empty or missing.
func (r *reader) value(c int) []byte {
	if c == 0 {
		return nil
	}
	v := r.sc.row[c]
	if len(v) == 0 || r.l.dash && len(v) == 1 && v[0] == '-' {
		return nil
	}
	return v
}

// row adds the current row to the build.
func (r *reader) row() error {
	row := r.sc.row
	if len(row) != r.l.cols {
		return r.sc.errorf("%d fields, want %d (%s)", len(row), r.l.cols, r.l.typ)
	}
	first, last, err := r.bounds()
	if err != nil {
		return err
	}
	fam := 0
	if first.Is6() {
		fam = 1
	}
	if p := r.prev[fam]; p.IsValid() && !p.Less(first) {
		return r.sc.errorf("range %s-%s does not start above the previous range, which ends at %s "+
			"(rows must be in ascending order and must not overlap)", first, last, p)
	}
	r.prev[fam] = last

	if r.l.none != "" && string(row[r.keyCol()]) == r.l.none {
		return nil // a row without data
	}
	if unread(first, last) {
		return nil
	}
	r.key = r.key[:0]
	for _, c := range r.keyCols {
		r.key = binary.AppendUvarint(r.key, uint64(len(row[c])))
		r.key = append(r.key, row[c]...)
	}
	id, ok := r.ids[string(r.key)]
	if !ok {
		if err := r.fill(); err != nil {
			return err
		}
		id = r.b.Record(&r.rec)
		r.ids[string(r.key)] = id
	}
	return r.add(first, last, id)
}

// keyCol is the column whose value l.none marks a row without data.
func (r *reader) keyCol() int {
	if r.l.asn {
		return r.l.asNum
	}
	return r.l.country
}

// bounds parses the first and last address of the current row.
func (r *reader) bounds() (first, last netip.Addr, err error) {
	if first, err = r.addr(0); err != nil {
		return first, last, err
	}
	if last, err = r.addr(1); err != nil {
		return first, last, err
	}
	switch {
	case first.Is4() != last.Is4():
		return first, last, r.sc.errorf("range %s-%s: addresses of different families", first, last)
	case last.Less(first):
		return first, last, r.sc.errorf("range %s-%s: first address above the last", first, last)
	}
	return first, last, nil
}

// addr parses the address in column c of the current row.
func (r *reader) addr(c int) (netip.Addr, error) {
	f := r.sc.row[c]
	if !r.l.decimal {
		a, err := netip.ParseAddr(string(f))
		if err != nil || a.Zone() != "" {
			return a, r.sc.errorf("field %d: bad address %q", c+1, f)
		}
		return a, nil
	}
	hi, lo, ok := parseUint128(f)
	switch {
	case !ok:
		return netip.Addr{}, r.sc.errorf("field %d: bad address number %q", c+1, f)
	case r.v6:
		var b [16]byte
		binary.BigEndian.PutUint64(b[:8], hi)
		binary.BigEndian.PutUint64(b[8:], lo)
		return netip.AddrFrom16(b), nil
	case hi != 0 || lo > math.MaxUint32:
		return netip.Addr{}, r.sc.errorf("field %d: address number %s is above 4294967295 in an IPv4 file "+
			"(a file is IPv6 when its name contains IPV6 or its first range ends above that)", c+1, f)
	}
	return netip.AddrFrom4([4]byte{byte(lo >> 24), byte(lo >> 16), byte(lo >> 8), byte(lo)}), nil
}

// fill decodes the key columns of the current row into r.rec.
func (r *reader) fill() error {
	l, rec := r.l, &r.rec
	*rec = build.Record{}
	str := func(c int) string { return string(r.value(c)) }
	if r.profile == ip2geo.ProfileASN {
		n, ok := parseUint32(r.value(l.asNum))
		if !ok {
			return r.sc.errorf("field %d: bad AS number %q", l.asNum+1, r.sc.row[l.asNum])
		}
		rec.ASN = ip2geo.ASN{Number: n, Organization: str(l.asOrg)}
		return nil
	}
	r.names = [3][1]string{{str(l.countryName)}}
	rec.Continent.Code = str(l.continent)
	rec.Country = build.Country{ISOCode: str(l.country), Names: r.names[0][:]}
	if r.profile >= ip2geo.ProfileRegion {
		r.names[1][0] = str(l.region)
		rec.Subdivisions[0].Names = r.names[1][:]
	}
	if r.profile < ip2geo.ProfileCity {
		return nil
	}
	r.names[2][0] = str(l.city)
	rec.City.Names = r.names[2][:]
	if r.fields&ip2geo.FieldPostal != 0 {
		rec.PostalCode = str(l.postal)
	}
	lat, lon := r.value(l.lat), r.value(l.lon)
	switch {
	case lat == nil && lon == nil:
		return nil // no coordinates (an IP2Location DB3 file, or "-")
	case lat == nil || lon == nil:
		return r.sc.errorf("fields %d and %d: a latitude without a longitude or the other way round",
			l.lat+1, l.lon+1)
	}
	var err error
	loc := &rec.Location
	if loc.Latitude, err = strconv.ParseFloat(string(lat), 64); err != nil || !(math.Abs(loc.Latitude) <= 90) {
		return r.sc.errorf("field %d: bad latitude %q", l.lat+1, lat)
	}
	if loc.Longitude, err = strconv.ParseFloat(string(lon), 64); err != nil || !(math.Abs(loc.Longitude) <= 180) {
		return r.sc.errorf("field %d: bad longitude %q", l.lon+1, lon)
	}
	loc.HasCoordinates = true
	return nil
}

// The IPv6 blocks that ip2geo.Reader looks up in the IPv4 table, in address
// order: ::/96 (IPv4-compatible), ::ffff:0:0/96 (IPv4-mapped), 2001::/32
// (Teredo) and 2002::/16 (6to4). Rows of the IPv4-mapped block are IPv4
// ranges; data for the others is never read, so it is not stored, as in an
// mmdb build.
var aliasBlocks = []struct {
	first, last netip.Addr
	v4          bool
}{
	{netip.MustParseAddr("::"), netip.MustParseAddr("::ffff:ffff"), false},
	{netip.MustParseAddr("::ffff:0.0.0.0"), netip.MustParseAddr("::ffff:255.255.255.255"), true},
	{netip.MustParseAddr("2001::"), netip.MustParseAddr("2001:0:ffff:ffff:ffff:ffff:ffff:ffff"), false},
	{netip.MustParseAddr("2002::"), netip.MustParseAddr("2002:ffff:ffff:ffff:ffff:ffff:ffff:ffff"), false},
}

// unread reports whether first-last lies in one of the alias blocks whose
// data is not stored.
func unread(first, last netip.Addr) bool {
	for _, blk := range aliasBlocks {
		if !blk.v4 && !first.Less(blk.first) && !blk.last.Less(last) {
			return true
		}
	}
	return false
}

// add adds the range first-last of record id, splitting an IPv6 range at
// the alias blocks.
func (r *reader) add(first, last netip.Addr, id uint32) error {
	if first.Is4() {
		return r.addRange(first, last, id)
	}
	cur := first
	for _, blk := range aliasBlocks {
		if last.Less(blk.first) {
			break
		}
		if blk.last.Less(cur) {
			continue
		}
		if cur.Less(blk.first) {
			if err := r.addRange(cur, blk.first.Prev(), id); err != nil {
				return err
			}
			cur = blk.first
		}
		if blk.v4 {
			if err := r.addRange(cur.Unmap(), minAddr(last, blk.last).Unmap(), id); err != nil {
				return err
			}
		}
		if !blk.last.Less(last) {
			return nil
		}
		cur = blk.last.Next()
	}
	return r.addRange(cur, last, id)
}

func minAddr(a, b netip.Addr) netip.Addr {
	if a.Less(b) {
		return a
	}
	return b
}

// addRange adds first-last, both IPv4 or both IPv6 (not IPv4-mapped), to
// the build, checking that it starts above the last range of its family.
func (r *reader) addRange(first, last netip.Addr, id uint32) error {
	end := &r.end6
	if first.Is4() {
		end = &r.end4
	}
	if end.IsValid() && !end.Less(first) {
		return r.sc.errorf("range %s-%s overlaps an earlier range, which ends at %s", first, last, *end)
	}
	*end = last
	if err := r.b.AddRange(first, last, id); err != nil {
		return r.sc.errorf("%v", err)
	}
	return nil
}
