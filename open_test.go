package ip2geo

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Crash inputs found by fuzzing the unvalidated reader, with the header
// repaired so that the checks behind it are exercised: a record width the
// profile cannot decode, and a record id past the record count.
const (
	crashRecWidth = "GEO1\x01\x02\x04\x00\x00\x00\x00\x000000\x00\x00\x01\x00\x00\x0000000000\x00\x00\x00\x000"
	crashRecID    = "GEO1\x01\x02\x04\x00\b\x00\x00\x00\x00\x00\x02\x0000\r\x000000000000000\x02\x0000\x02\x0000\n\x000000000000\v\x0000000000000\x13\x000000000000000000000\x02\x00\x00\x000\x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000\x01\x00\x00\x0000000000\x00\x00\x00\x000"
)

func TestOpenBytesRejects(t *testing.T) {
	le := binary.LittleEndian
	cases := []struct {
		name string
		mut  func(d *testDB)                 // applied before encoding
		raw  func(b []byte, l layout) []byte // applied after encoding
		in   string                          // used as is when set
		want error
	}{
		{name: "empty", in: "", want: ErrBadMagic},
		{name: "bad magic", raw: func(b []byte, _ layout) []byte { b[0] = 'X'; return b }, want: ErrBadMagic},
		{name: "version 0", mut: func(d *testDB) { d.version = 0 }, want: ErrUnsupportedVersion},
		{name: "version 2", mut: func(d *testDB) { d.version = 2 }, want: ErrUnsupportedVersion},
		{name: "profile 3", mut: func(d *testDB) { d.profile = 3 }, want: ErrCorrupt},
		{name: "profile 255", mut: func(d *testDB) { d.profile = 255 }, want: ErrCorrupt},
		{name: "lat scale 8", mut: func(d *testDB) { d.latScale = 8 }, want: ErrCorrupt},
		{name: "lat scale 255", mut: func(d *testDB) { d.latScale = 255 }, want: ErrCorrupt},
		{name: "reserved byte", mut: func(d *testDB) { d.reserved = 1 }, want: ErrCorrupt},

		{name: "huge string count", raw: func(b []byte, l layout) []byte {
			le.PutUint32(b[l.strCount:], 0xFFFFFFFF)
			return b
		}, want: ErrCorrupt},
		{name: "string count one past remaining/2", raw: func(b []byte, l layout) []byte {
			le.PutUint32(b[l.strCount:], uint32((len(b)-l.strCount-4)/2+1))
			return b
		}, want: ErrCorrupt},
		{name: "string past end", raw: func(b []byte, l layout) []byte {
			return b[:l.strCount+4+2+3] // "US" needs 2+2 bytes after the empty string
		}, want: ErrCorrupt},
		{name: "string length past end", raw: func(b []byte, l layout) []byte {
			le.PutUint16(b[l.strCount+4:], 0xFFFF)
			return b
		}, want: ErrCorrupt},

		{name: "record width 16 for city", mut: func(d *testDB) { d.recCount, d.recWidth = 9, 16 }, want: ErrCorrupt},
		{name: "record width 0", mut: func(d *testDB) { d.recWidth = 0; d.records = nil }, want: ErrCorrupt},
		{name: "record width 49", mut: func(d *testDB) { d.recWidth = 49; d.records = append(d.records, 0, 0, 0) }, want: ErrCorrupt},
		{name: "city records under country profile", mut: func(d *testDB) { d.profile = byte(ProfileCountry) }, want: ErrCorrupt},
		{name: "country records under city profile", mut: func(d *testDB) {
			d.withProfile(ProfileCountry)
			d.profile = byte(ProfileCity)
		}, want: ErrCorrupt},
		{name: "no records", mut: func(d *testDB) { d.recCount = 0; d.records = nil }, want: ErrCorrupt},
		{name: "record count past end", mut: func(d *testDB) { d.recCount = 0xFFFFFFFF }, want: ErrCorrupt},
		{name: "record bytes wrap 32 bits", mut: func(d *testDB) { d.recCount = 1 << 28 }, want: ErrCorrupt}, // 2^28 * 48 = 3 * 2^32
		{name: "crash: record width", in: crashRecWidth, want: ErrCorrupt},
		{name: "crash: record id", in: crashRecID, want: ErrCorrupt},

		{name: "v4 record id == count", mut: func(d *testDB) { d.v4rec[1] = 3 }, want: ErrCorrupt},
		{name: "v4 record id max", mut: func(d *testDB) { d.v4rec[2] = 0xFFFFFFFF }, want: ErrCorrupt},
		{name: "v6 record id == count", mut: func(d *testDB) { d.v6rec[4] = 3 }, want: ErrCorrupt},
		{name: "v6 record id max", mut: func(d *testDB) { d.v6rec[0] = 0xFFFFFFFF }, want: ErrCorrupt},
		{name: "v4 starts descending", mut: func(d *testDB) {
			d.v4starts[1], d.v4starts[2] = d.v4starts[2], d.v4starts[1]
		}, want: ErrCorrupt},
		{name: "v4 duplicate start", mut: func(d *testDB) { d.v4starts[2] = d.v4starts[1] }, want: ErrCorrupt},
		{name: "v4 duplicate zero start", mut: func(d *testDB) { d.v4starts[1] = 0 }, want: ErrCorrupt},
		{name: "v6 starts descending", mut: func(d *testDB) {
			d.v6starts[3], d.v6starts[4] = d.v6starts[4], d.v6starts[3]
		}, want: ErrCorrupt},
		{name: "v6 duplicate start", mut: func(d *testDB) { d.v6starts[2] = d.v6starts[1] }, want: ErrCorrupt},
		{name: "v6 start below previous in last byte", mut: func(d *testDB) {
			d.v6starts[1][15] = 1
			d.v6starts[2] = d.v6starts[1]
			d.v6starts[2][15] = 0
		}, want: ErrCorrupt},
		{name: "v4 ids short", mut: func(d *testDB) { d.v4rec = d.v4rec[:2] }, want: ErrCorrupt},
		{name: "v6 ids short", mut: func(d *testDB) { d.v6rec = d.v6rec[:4] }, want: ErrCorrupt},

		{name: "v4 count past end", raw: func(b []byte, l layout) []byte {
			le.PutUint32(b[l.v4Count:], 0xFFFFFFFF)
			return b
		}, want: ErrCorrupt},
		{name: "v4 count 2^29 (8n wraps 32 bits)", raw: func(b []byte, l layout) []byte {
			le.PutUint32(b[l.v4Count:], 1<<29)
			return b
		}, want: ErrCorrupt},
		{name: "v4 count 2^30 (4n wraps 32 bits)", raw: func(b []byte, l layout) []byte {
			le.PutUint32(b[l.v4Count:], 1<<30)
			return b
		}, want: ErrCorrupt},
		{name: "v4 count 2^31 (negative int on 32 bits)", raw: func(b []byte, l layout) []byte {
			le.PutUint32(b[l.v4Count:], 1<<31)
			return b
		}, want: ErrCorrupt},
		{name: "v6 count past end", raw: func(b []byte, l layout) []byte {
			le.PutUint32(b[l.v6Count:], 0xFFFFFFFF)
			return b
		}, want: ErrCorrupt},
		{name: "v6 count 2^28 (16n wraps 32 bits)", raw: func(b []byte, l layout) []byte {
			le.PutUint32(b[l.v6Count:], 1<<28)
			return b
		}, want: ErrCorrupt},

		{name: "trailing byte", mut: func(d *testDB) { d.trailing = []byte{0} }, want: ErrCorrupt},
		{name: "trailing table", mut: func(d *testDB) { d.trailing = make([]byte, 20) }, want: ErrCorrupt},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := []byte(c.in)
			if c.in == "" && (c.mut != nil || c.raw != nil) {
				d := cityTestDB()
				if c.mut != nil {
					c.mut(d)
				}
				var l layout
				b, l = d.encode()
				if c.raw != nil {
					b = c.raw(b, l)
				}
			}
			r, err := OpenBytes(b)
			if r != nil || !errors.Is(err, c.want) {
				t.Fatalf("OpenBytes = %v, %v; want nil, %v", r, err, c.want)
			}
			if !strings.HasPrefix(err.Error(), "ip2geo: ") {
				t.Errorf("error %q lacks the ip2geo: prefix", err)
			}
		})
	}
}

func TestOpenBytesAccepts(t *testing.T) {
	cases := []struct {
		name string
		mut  func(d *testDB)
	}{
		{"city", func(d *testDB) {}},
		{"lat scale 0", func(d *testDB) { d.latScale = 0 }},
		{"lat scale 7", func(d *testDB) { d.latScale = 7 }},
		{"no strings", func(d *testDB) {
			d.strs = nil
			clear(d.records)
		}},
		{"only record 0", func(d *testDB) {
			d.recCount, d.records = 1, d.records[:RecWidth(ProfileCity)]
			clear(d.v4rec)
			clear(d.v6rec)
		}},
		{"no ranges", func(d *testDB) { d.v4starts, d.v4rec, d.v6starts, d.v6rec = nil, nil, nil, nil }},
		{"edge starts", func(d *testDB) {
			d.v4starts = append(d.v4starts, 0xFFFFFFFF)
			d.v4rec = append(d.v4rec, recLA)
			d.v6starts = append(d.v6starts, a16("ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff"))
			d.v6rec = append(d.v6rec, recBerlin)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := cityTestDB()
			c.mut(d)
			b, _ := d.encode()
			r, err := OpenBytes(b)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range fuzzProbes {
				r.Lookup(a)
			}
		})
	}

	d := cityTestDB()
	d.v4starts = append(d.v4starts, 0xFFFFFFFF)
	d.v4rec = append(d.v4rec, recLA)
	d.v6starts = append(d.v6starts, a16("ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff"))
	d.v6rec = append(d.v6rec, recBerlin)
	b, _ := d.encode()
	r, err := OpenBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	for ip, want := range map[string]bool{
		"255.255.255.254": false, "255.255.255.255": true,
		"ffff:ffff:ffff:ffff:ffff:ffff:ffff:fffe": false, "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff": true,
	} {
		if _, ok := r.Lookup(netip.MustParseAddr(ip)); ok != want {
			t.Errorf("edge starts: %s ok=%v, want %v", ip, ok, want)
		}
	}
}

// TestOpenBytesTruncated cuts the test database at every length.
func TestOpenBytesTruncated(t *testing.T) {
	b := encodeCityDB()
	for n := 0; n < len(b); n++ {
		r, err := OpenBytes(b[:n])
		want := ErrCorrupt
		if n < len(magic) {
			want = ErrBadMagic
		}
		if r != nil || !errors.Is(err, want) {
			t.Errorf("len %d of %d: OpenBytes = %v, %v; want nil, %v", n, len(b), r, err, want)
		}
	}
}

// TestOpenBytesByteFlips corrupts one structural byte at a time (string and
// record contents are free-form and left to the fuzzers). OpenBytes may
// accept the result but must not panic, and a Reader it returns must not
// panic on Lookup.
func TestOpenBytesByteFlips(t *testing.T) {
	d := cityTestDB()
	orig, l := d.encode()
	freeForm := make([]bool, len(orig))
	off := l.strCount + 4
	for _, s := range d.strs {
		for i := range len(s) {
			freeForm[off+2+i] = true
		}
		off += 2 + len(s)
	}
	for i := l.recCount + 6; i < l.v4Count; i++ {
		freeForm[i] = true
	}
	tried, opened := 0, 0
	for i := range orig {
		if freeForm[i] {
			continue
		}
		for _, v := range []byte{0x00, 0xFF, orig[i] ^ 0x01} {
			b := append([]byte(nil), orig...)
			b[i] = v
			tried++
			r, err := OpenBytes(b)
			if err != nil {
				if !isSentinel(err) {
					t.Fatalf("byte %d = %#x: error %v wraps no sentinel", i, v, err)
				}
				continue
			}
			opened++
			for _, a := range fuzzProbes {
				r.Lookup(a)
			}
		}
	}
	t.Logf("%d of %d single-byte corruptions opened", opened, tried)
}

func isSentinel(err error) bool {
	return errors.Is(err, ErrBadMagic) || errors.Is(err, ErrUnsupportedVersion) ||
		errors.Is(err, ErrCorrupt) || errors.Is(err, ErrTooLarge)
}

func totalAlloc() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.TotalAlloc
}

// TestOpenBytesHugeStringCount: 12 bytes claiming 2^32-1 strings used to
// allocate 64 GiB before the bounds check.
func TestOpenBytesHugeStringCount(t *testing.T) {
	b := []byte("GEO1\x01\x00\x00\x00\xff\xff\xff\xff")
	before := totalAlloc()
	r, err := OpenBytes(b)
	alloc := totalAlloc() - before
	if r != nil || !errors.Is(err, ErrCorrupt) {
		t.Fatalf("OpenBytes = %v, %v; want nil, ErrCorrupt", r, err)
	}
	if alloc >= 16<<20 {
		t.Errorf("allocated %d bytes, want < 16 MiB", alloc)
	}
}

// TestOpenBytesHugeCounts sets each count field to 2^32-1: every one must be
// refused before anything is allocated for it.
func TestOpenBytesHugeCounts(t *testing.T) {
	for _, field := range []string{"strings", "records", "v4", "v6"} {
		b, l := cityTestDB().encode()
		off := map[string]int{"strings": l.strCount, "records": l.recCount, "v4": l.v4Count, "v6": l.v6Count}[field]
		binary.LittleEndian.PutUint32(b[off:], 0xFFFFFFFF)
		before := totalAlloc()
		r, err := OpenBytes(b)
		alloc := totalAlloc() - before
		if r != nil || !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: OpenBytes = %v, %v; want nil, ErrCorrupt", field, r, err)
		}
		if alloc >= 1<<20 {
			t.Errorf("%s: allocated %d bytes, want < 1 MiB", field, alloc)
		}
	}
}

func writeFile(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "db.geoc")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOpen(t *testing.T) {
	r, err := Open(writeFile(t, encodeCityDB()))
	if err != nil {
		t.Fatal(err)
	}
	if rec, ok := r.Lookup(netip.MustParseAddr("10.0.0.5")); !ok || rec.City != "Los Angeles" {
		t.Errorf("Lookup = %+v, %v", rec, ok)
	}

	cases := []struct {
		name string
		in   []byte
		want error
	}{
		{"empty", nil, ErrBadMagic},
		{"short magic", []byte("GEO"), ErrBadMagic},
		{"bad magic", []byte("nope____ and more"), ErrBadMagic},
		{"short header", []byte("GEO1\x01"), ErrCorrupt},
		{"version", []byte("GEO1\x02\x00\x00\x00"), ErrUnsupportedVersion},
		{"profile", []byte("GEO1\x01\x03\x00\x00"), ErrCorrupt},
		{"header only", []byte("GEO1\x01\x00\x00\x00"), ErrCorrupt},
		{"trailing byte", append(encodeCityDB(), 0), ErrCorrupt},
	}
	for _, c := range cases {
		r, err := Open(writeFile(t, c.in))
		if r != nil || !errors.Is(err, c.want) {
			t.Errorf("%s: Open = %v, %v; want nil, %v", c.name, r, err, c.want)
		}
	}

	if _, err := Open(filepath.Join(t.TempDir(), "missing.geoc")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: got %v, want fs.ErrNotExist", err)
	}
}

// sparseFile creates a file of the given size holding only hdr, without
// writing the rest to disk.
func sparseFile(t *testing.T, hdr string, size int64) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "big.geoc")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(hdr); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Skipf("cannot create a %d-byte file: %v", size, err)
	}
	return p
}

func TestOpenTooLarge(t *testing.T) {
	p := sparseFile(t, "GEO1\x01\x00\x00\x00", maxFileSize+1)
	before := totalAlloc()
	r, err := Open(p)
	if r != nil || !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Open = %v, %v; want nil, ErrTooLarge", r, err)
	}
	if alloc := totalAlloc() - before; alloc >= 1<<20 {
		t.Errorf("allocated %d bytes refusing an oversized file", alloc)
	}
}

// TestOpenChecksHeaderFirst: a large file with a bad header is refused
// without reading the rest.
func TestOpenChecksHeaderFirst(t *testing.T) {
	for hdr, want := range map[string]error{
		"\x00\x00\x00\x00\x00\x00\x00\x00": ErrBadMagic,
		"GEO1\x09\x00\x00\x00":             ErrUnsupportedVersion,
		"GEO1\x01\x00\x08\x00":             ErrCorrupt,
	} {
		p := sparseFile(t, hdr, 64<<20)
		before := totalAlloc()
		r, err := Open(p)
		alloc := totalAlloc() - before
		if r != nil || !errors.Is(err, want) {
			t.Errorf("header %q: Open = %v, %v; want nil, %v", hdr, r, err, want)
		}
		if alloc >= 1<<20 {
			t.Errorf("header %q: allocated %d bytes, want < 1 MiB", hdr, alloc)
		}
	}
}
