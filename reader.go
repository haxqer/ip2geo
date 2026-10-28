package ip2geo

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/netip"
	"os"
)

// idxBits for the direct-index (DXR) jump tables, chosen for a good
// size/speed balance. v4 uses the top 20 bits, v6 the top 16.
const (
	v4IdxBits = 20
	v4Shift   = 32 - v4IdxBits
	v6IdxBits = 16
)

// Reader is a read-only handle to a compact database. It is safe for
// concurrent use by multiple goroutines, Close included. Lookups perform no
// allocation.
type Reader struct {
	profile Profile
	latDiv  float64

	strings []string // id -> string (id 0 == "")

	records  []byte
	recWidth int

	// IPv4 index (DXR)
	v4starts []uint32
	v4rec    []uint32
	lo4      []int32

	// IPv6 index (DXR), starts stored as raw 16-byte keys
	v6starts []byte
	v6rec    []uint32
	lo6      []int32
}

// Open loads a compact database file into memory. It refuses files larger
// than 2 GiB with ErrTooLarge and checks the 8-byte header before reading the
// rest, so a wrong path fails fast. The contents are then validated as by
// OpenBytes. Errors from the file system are returned as they are.
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := fi.Size()
	if size > maxFileSize || size >= math.MaxInt { // MaxInt bounds 32-bit builds
		return nil, fmt.Errorf("ip2geo: %s is %d bytes, over the %d-byte limit: %w", path, size, int64(maxFileSize), ErrTooLarge)
	}

	var hdr [headerLen]byte
	n, err := io.ReadFull(f, hdr[:])
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	if err := checkHeader(hdr[:n]); err != nil {
		return nil, err
	}

	// Stat's size is a hint: the file may change underfoot, and pipes and
	// special files report 0. Read to EOF, but never past maxFileSize.
	b := make([]byte, headerLen, int(max(size, headerLen)+1))
	copy(b, hdr[:])
	for {
		if len(b) == cap(b) {
			b = append(b, 0)[:len(b)]
		}
		m, err := f.Read(b[len(b):cap(b)])
		b = b[:len(b)+m]
		if uint64(len(b)) > maxFileSize {
			return nil, fmt.Errorf("ip2geo: %s is over the %d-byte limit: %w", path, int64(maxFileSize), ErrTooLarge)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return OpenBytes(b)
}

// OpenBytes parses a compact database held in memory. b is not retained.
//
// The whole input is validated first: the header (magic, version, a known
// profile, a lat/lon scale of at most 7, a zero reserved byte), every count
// against the bytes left, a record width equal to RecWidth(profile), at least
// one record, every record id below the record count, strictly ascending
// IPv4 and IPv6 range starts, and no trailing bytes. Inputs over 2 GiB are
// refused. Errors wrap ErrBadMagic, ErrUnsupportedVersion, ErrCorrupt or
// ErrTooLarge.
func OpenBytes(b []byte) (*Reader, error) {
	if uint64(len(b)) > maxFileSize {
		return nil, fmt.Errorf("ip2geo: input is %d bytes, over the %d-byte limit: %w", len(b), int64(maxFileSize), ErrTooLarge)
	}
	if err := checkHeader(b); err != nil {
		return nil, err
	}
	r := &Reader{profile: Profile(b[5]), latDiv: math.Pow10(int(b[6]))}
	c := cursor{b: b, off: headerLen}

	// Strings. Each needs at least its 2-byte length, which bounds the count
	// before anything is allocated.
	ns, err := c.u32("string count")
	if err != nil {
		return nil, err
	}
	if uint64(ns) > c.rem()/2 {
		return nil, fmt.Errorf("ip2geo: %d strings cannot fit in %d bytes: %w", ns, c.rem(), ErrCorrupt)
	}
	r.strings = make([]string, ns)
	for i := range r.strings {
		l, err := c.u16("string length")
		if err != nil {
			return nil, err
		}
		s, err := c.take(uint64(l), 1, "string")
		if err != nil {
			return nil, err
		}
		r.strings[i] = string(s)
	}

	// Records. Record 0 is the not-found sentinel, so there is at least one,
	// and the width must be exactly what decode reads for the profile.
	rc, err := c.u32("record count")
	if err != nil {
		return nil, err
	}
	rw, err := c.u16("record width")
	if err != nil {
		return nil, err
	}
	if rc == 0 {
		return nil, fmt.Errorf("ip2geo: no records, record 0 is required: %w", ErrCorrupt)
	}
	if int(rw) != RecWidth(r.profile) {
		return nil, fmt.Errorf("ip2geo: record width %d, %s profile needs %d: %w", rw, r.profile, RecWidth(r.profile), ErrCorrupt)
	}
	recs, err := c.take(uint64(rc), uint64(rw), "records")
	if err != nil {
		return nil, err
	}
	r.records = bytes.Clone(recs)
	r.recWidth = int(rw)

	// IPv4 ranges: n4 starts, then n4 record ids.
	n4, err := c.u32("IPv4 range count")
	if err != nil {
		return nil, err
	}
	starts4, err := c.take(uint64(n4), 4, "IPv4 starts")
	if err != nil {
		return nil, err
	}
	ids4, err := c.take(uint64(n4), 4, "IPv4 record ids")
	if err != nil {
		return nil, err
	}
	r.v4starts = make([]uint32, n4)
	for i := range r.v4starts {
		s := binary.LittleEndian.Uint32(starts4[i*4:])
		if i > 0 && s <= r.v4starts[i-1] {
			return nil, fmt.Errorf("ip2geo: IPv4 start %d is not above start %d: %w", i, i-1, ErrCorrupt)
		}
		r.v4starts[i] = s
	}
	if r.v4rec, err = recordIDs(ids4, rc, "IPv4"); err != nil {
		return nil, err
	}

	// IPv6 ranges: n6 16-byte big-endian starts, then n6 record ids.
	n6, err := c.u32("IPv6 range count")
	if err != nil {
		return nil, err
	}
	starts6, err := c.take(uint64(n6), 16, "IPv6 starts")
	if err != nil {
		return nil, err
	}
	ids6, err := c.take(uint64(n6), 4, "IPv6 record ids")
	if err != nil {
		return nil, err
	}
	for i := 16; i < len(starts6); i += 16 {
		if bytes.Compare(starts6[i-16:i], starts6[i:i+16]) >= 0 {
			return nil, fmt.Errorf("ip2geo: IPv6 start %d is not above start %d: %w", i/16, i/16-1, ErrCorrupt)
		}
	}
	r.v6starts = bytes.Clone(starts6)
	if r.v6rec, err = recordIDs(ids6, rc, "IPv6"); err != nil {
		return nil, err
	}

	if c.rem() != 0 {
		return nil, fmt.Errorf("ip2geo: %d trailing bytes: %w", c.rem(), ErrCorrupt)
	}
	r.buildIndex()
	return r, nil
}

// checkHeader validates the fixed header: magic, version, profile, lat/lon
// scale and the reserved byte. Only the first headerLen bytes are read.
func checkHeader(h []byte) error {
	if len(h) < len(magic) || string(h[:len(magic)]) != magic {
		return fmt.Errorf("ip2geo: magic %q, want %q: %w", h[:min(len(h), len(magic))], magic, ErrBadMagic)
	}
	if len(h) < headerLen {
		return fmt.Errorf("ip2geo: truncated header, %d of %d bytes: %w", len(h), headerLen, ErrCorrupt)
	}
	if h[4] != version {
		return fmt.Errorf("ip2geo: format version %d, want %d: %w", h[4], version, ErrUnsupportedVersion)
	}
	if Profile(h[5]) > ProfileCity {
		return fmt.Errorf("ip2geo: unknown profile %d: %w", h[5], ErrCorrupt)
	}
	if h[6] > maxLatScale {
		return fmt.Errorf("ip2geo: lat/lon scale %d is over %d: %w", h[6], maxLatScale, ErrCorrupt)
	}
	if h[7] != 0 {
		return fmt.Errorf("ip2geo: reserved header byte is %d, want 0: %w", h[7], ErrCorrupt)
	}
	return nil
}

// cursor walks the input. A size is a u32 count times a width of at most
// 2^16, formed in uint64 so it cannot wrap, and is checked against the bytes
// left before any slice is taken. 32-bit builds are as safe as 64-bit ones.
type cursor struct {
	b   []byte
	off int
}

func (c *cursor) rem() uint64 { return uint64(len(c.b) - c.off) }

// take returns the next count*width bytes.
func (c *cursor) take(count, width uint64, what string) ([]byte, error) {
	n := count * width
	if n > c.rem() {
		return nil, fmt.Errorf("ip2geo: truncated %s, need %d bytes, have %d: %w", what, n, c.rem(), ErrCorrupt)
	}
	s := c.b[c.off : c.off+int(n)]
	c.off += int(n)
	return s, nil
}

func (c *cursor) u32(what string) (uint32, error) {
	s, err := c.take(1, 4, what)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(s), nil
}

func (c *cursor) u16(what string) (uint16, error) {
	s, err := c.take(1, 2, what)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(s), nil
}

// recordIDs decodes little-endian u32 record ids, each of which must be
// below the record count rc.
func recordIDs(b []byte, rc uint32, family string) ([]uint32, error) {
	ids := make([]uint32, len(b)/4)
	for i := range ids {
		id := binary.LittleEndian.Uint32(b[i*4:])
		if id >= rc {
			return nil, fmt.Errorf("ip2geo: %s range %d points at record %d of %d: %w", family, i, id, rc, ErrCorrupt)
		}
		ids[i] = id
	}
	return ids, nil
}

// buildIndex constructs the DXR direct-index jump tables.
func (r *Reader) buildIndex() {
	// v4: top v4IdxBits bits
	size4 := (1 << v4IdxBits) + 1
	r.lo4 = make([]int32, size4)
	bi := 0
	n4 := len(r.v4starts)
	for g := 0; g < size4; g++ {
		var thr uint64
		if g == size4-1 {
			thr = 1 << 32
		} else {
			thr = uint64(g) << v4Shift
		}
		for bi < n4 && uint64(r.v4starts[bi]) < thr {
			bi++
		}
		r.lo4[g] = int32(bi)
	}

	// v6: top v6IdxBits bits (first 2 bytes)
	size6 := (1 << v6IdxBits) + 1
	r.lo6 = make([]int32, size6)
	bi = 0
	n6 := len(r.v6rec)
	for g := 0; g < size6; g++ {
		for bi < n6 && int(r.v6starts[bi*16])<<8|int(r.v6starts[bi*16+1]) < g {
			bi++
		}
		r.lo6[g] = int32(bi)
	}
}

// Profile reports the precision profile the database was built with.
func (r *Reader) Profile() Profile { return r.profile }

// Close does nothing and returns nil. The database lives on the Go heap, so
// there is nothing to release today; the garbage collector reclaims it once
// the Reader is unreachable. Close is safe to call concurrently with Lookup,
// and the Reader keeps answering after Close. The method is kept so that a
// future memory-mapped Reader can release its mapping.
func (r *Reader) Close() error { return nil }

// Lookup resolves an IP address to a Record. ok is false when the address is
// not covered by the database, and for the zero netip.Addr.
//
// IPv4 addresses in IPv6 form are looked up in the IPv4 table, as MaxMind
// databases alias them: ::ffff:0:0/96 (IPv4-mapped), ::/96 (IPv4-compatible),
// 2002::/16 (6to4, bits 16-47) and 2001::/32 (Teredo server, bits 32-63).
// NAT64 64:ff9b::/96 is not mapped.
func (r *Reader) Lookup(ip netip.Addr) (rec Record, ok bool) {
	var id uint32
	if ip.Is4() || ip.Is4In6() {
		a := ip.As4()
		id = r.search4(binary.BigEndian.Uint32(a[:]))
	} else if ip.IsValid() {
		a := ip.As16()
		if v4, isV4 := embeddedV4(&a); isV4 {
			id = r.search4(v4)
		} else {
			id = r.search6(a)
		}
	}
	if id == 0 {
		return Record{}, false
	}
	return r.decode(id), true
}

// City is an alias for Lookup, provided for familiarity with geoip2-golang.
func (r *Reader) City(ip netip.Addr) (Record, bool) { return r.Lookup(ip) }

// Country is an alias for Lookup. The returned Record's country fields are the
// ones of interest; other fields depend on the database profile.
func (r *Reader) Country(ip netip.Addr) (Record, bool) { return r.Lookup(ip) }

// embeddedV4 returns the IPv4 address an IPv6 key is aliased to: bytes 12-15
// of ::/96, bytes 2-5 of 2002::/16 (6to4) and bytes 4-7 of 2001:0::/32
// (Teredo). Lookup handles ::ffff:0:0/96 through netip's Is4In6 instead.
func embeddedV4(k *[16]byte) (uint32, bool) {
	hi := binary.BigEndian.Uint64(k[:8])
	switch {
	case hi>>48 == 0x2002:
		return uint32(hi >> 16), true
	case hi>>32 == 0x2001_0000:
		return uint32(hi), true
	case hi == 0 && binary.BigEndian.Uint32(k[8:12]) == 0:
		return binary.BigEndian.Uint32(k[12:]), true
	}
	return 0, false
}

func (r *Reader) search4(key uint32) uint32 {
	g := key >> v4Shift
	lo := int(r.lo4[g])
	if lo > 0 {
		lo--
	}
	hi := int(r.lo4[g+1])
	l, h := lo, hi
	s := r.v4starts
	for l < h {
		mid := (l + h) >> 1
		if s[mid] > key {
			h = mid
		} else {
			l = mid + 1
		}
	}
	if l == 0 {
		return 0
	}
	return r.v4rec[l-1]
}

func (r *Reader) search6(key [16]byte) uint32 {
	g := int(key[0])<<8 | int(key[1])
	lo := int(r.lo6[g])
	if lo > 0 {
		lo--
	}
	hi := int(r.lo6[g+1])
	l, h := lo, hi
	for l < h {
		mid := (l + h) >> 1
		if cmp16(r.v6starts[mid*16:], key[:]) > 0 {
			h = mid
		} else {
			l = mid + 1
		}
	}
	if l == 0 {
		return 0
	}
	return r.v6rec[l-1]
}

func cmp16(a []byte, b []byte) int {
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

func (r *Reader) str(id uint32) string {
	if id == 0 || int(id) >= len(r.strings) {
		return ""
	}
	return r.strings[id]
}

func (r *Reader) decode(id uint32) Record {
	b := r.records[int(id)*r.recWidth:]
	u := func(i int) uint32 { return binary.LittleEndian.Uint32(b[i*4:]) }
	var rec Record
	rec.CountryISO = r.str(u(0))
	rec.CountryName = r.str(u(1))
	rec.Continent = r.str(u(2))
	rec.CountryGeoNameID = u(3)
	if r.profile >= ProfileRegion {
		rec.RegionISO = r.str(u(4))
		rec.RegionName = r.str(u(5))
	}
	if r.profile >= ProfileCity {
		rec.City = r.str(u(6))
		rec.CityGeoNameID = u(7)
		rec.Latitude = float64(int32(u(8))) / r.latDiv
		rec.Longitude = float64(int32(u(9))) / r.latDiv
		rec.AccuracyRadius = uint16(u(10))
		rec.TimeZone = r.str(u(11))
	}
	return rec
}
