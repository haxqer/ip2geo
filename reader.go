package ip2geo

import (
	"encoding/binary"
	"fmt"
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

// Reader is a read-only handle to a compact database. It is safe for concurrent
// use by multiple goroutines. Lookups perform no allocation.
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

// Open loads a compact database file into memory.
func Open(path string) (*Reader, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return OpenBytes(b)
}

// OpenBytes parses a compact database held in memory. The byte slice is not
// retained after Open returns.
func OpenBytes(b []byte) (*Reader, error) {
	if len(b) < 8 || string(b[:4]) != magic {
		return nil, fmt.Errorf("ip2geo: bad magic")
	}
	if b[4] != version {
		return nil, fmt.Errorf("ip2geo: unsupported version %d", b[4])
	}
	r := &Reader{profile: Profile(b[5])}
	r.latDiv = math.Pow10(int(b[6]))
	off := 8

	need := func(n int) error {
		if off+n > len(b) {
			return fmt.Errorf("ip2geo: truncated database")
		}
		return nil
	}

	// strings
	if err := need(4); err != nil {
		return nil, err
	}
	ns := binary.LittleEndian.Uint32(b[off:])
	off += 4
	r.strings = make([]string, ns)
	for i := range r.strings {
		if err := need(2); err != nil {
			return nil, err
		}
		l := int(binary.LittleEndian.Uint16(b[off:]))
		off += 2
		if err := need(l); err != nil {
			return nil, err
		}
		r.strings[i] = string(b[off : off+l])
		off += l
	}

	// records
	if err := need(6); err != nil {
		return nil, err
	}
	rc := int(binary.LittleEndian.Uint32(b[off:]))
	off += 4
	r.recWidth = int(binary.LittleEndian.Uint16(b[off:]))
	off += 2
	if err := need(rc * r.recWidth); err != nil {
		return nil, err
	}
	r.records = make([]byte, rc*r.recWidth)
	copy(r.records, b[off:off+rc*r.recWidth])
	off += rc * r.recWidth

	// v4 index
	if err := need(4); err != nil {
		return nil, err
	}
	n4 := int(binary.LittleEndian.Uint32(b[off:]))
	off += 4
	if err := need(n4 * 8); err != nil {
		return nil, err
	}
	r.v4starts = make([]uint32, n4)
	for i := range r.v4starts {
		r.v4starts[i] = binary.LittleEndian.Uint32(b[off+i*4:])
	}
	off += n4 * 4
	r.v4rec = make([]uint32, n4)
	for i := range r.v4rec {
		r.v4rec[i] = binary.LittleEndian.Uint32(b[off+i*4:])
	}
	off += n4 * 4

	// v6 index
	if err := need(4); err != nil {
		return nil, err
	}
	n6 := int(binary.LittleEndian.Uint32(b[off:]))
	off += 4
	if err := need(n6 * 16); err != nil {
		return nil, err
	}
	r.v6starts = make([]byte, n6*16)
	copy(r.v6starts, b[off:off+n6*16])
	off += n6 * 16
	if err := need(n6 * 4); err != nil {
		return nil, err
	}
	r.v6rec = make([]uint32, n6)
	for i := range r.v6rec {
		r.v6rec[i] = binary.LittleEndian.Uint32(b[off+i*4:])
	}
	off += n6 * 4

	r.buildIndex()
	return r, nil
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

// Close releases the database memory. After Close the Reader must not be used.
func (r *Reader) Close() error {
	*r = Reader{}
	return nil
}

// Lookup resolves an IP address to a Record. ok is false when the address is
// not covered by the database.
func (r *Reader) Lookup(ip netip.Addr) (rec Record, ok bool) {
	if ip.Is4() || ip.Is4In6() {
		a := ip.As4()
		key := binary.BigEndian.Uint32(a[:])
		id := r.search4(key)
		if id == 0 {
			return Record{}, false
		}
		return r.decode(id), true
	}
	a := ip.As16()
	id := r.search6(a)
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
