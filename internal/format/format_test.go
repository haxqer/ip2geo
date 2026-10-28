package format

import (
	"errors"
	"testing"
)

func TestHeaderRoundTrip(t *testing.T) {
	h := Header{Profile: 2, LatScale: 4, IDWidth: 3, SectionCount: 11}
	b := h.AppendTo(nil)
	if len(b) != HeaderSize {
		t.Fatalf("header is %d bytes, want %d", len(b), HeaderSize)
	}
	got, err := ParseHeader(b)
	if err != nil || got != h {
		t.Fatalf("ParseHeader = %+v, %v; want %+v", got, err, h)
	}
	for _, c := range []struct {
		name string
		mut  func([]byte)
		want error
	}{
		{"magic", func(b []byte) { b[0] = 'X' }, ErrMagic},
		{"version", func(b []byte) { b[4] = 1 }, ErrVersion},
		{"profile", func(b []byte) { b[5] = 3 }, ErrHeader},
		{"latscale", func(b []byte) { b[6] = 8 }, ErrHeader},
		{"idwidth0", func(b []byte) { b[7] = 0 }, ErrHeader},
		{"idwidth5", func(b []byte) { b[7] = 5 }, ErrHeader},
		{"reserved", func(b []byte) { b[31] = 1 }, ErrHeader},
	} {
		m := append([]byte(nil), b...)
		c.mut(m)
		if _, err := ParseHeader(m); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if _, err := ParseHeader(b[:HeaderSize-1]); !errors.Is(err, ErrHeader) {
		t.Errorf("short header: err = %v", err)
	}
	if _, err := ParseHeader([]byte("GE")); !errors.Is(err, ErrMagic) {
		t.Errorf("tiny input: err = %v", err)
	}
}

func TestDirEntryRoundTrip(t *testing.T) {
	e := DirEntry{ID: SecV6ExcLo, CRC: 0xdeadbeef, Offset: 1 << 40, Length: 12345}
	b := e.AppendTo(nil)
	if len(b) != DirEntrySize {
		t.Fatalf("entry is %d bytes, want %d", len(b), DirEntrySize)
	}
	if got := ParseDirEntry(b); got != e {
		t.Fatalf("ParseDirEntry = %+v, want %+v", got, e)
	}
}

func TestMetaRoundTrip(t *testing.T) {
	m := Meta{BuildTime: 1761600000, SourceTime: 1588597376, Networks: 10176453,
		SourceType: 1, Language: 2, Description: 0, Builder: 3, DistinctRecords: 886091}
	b := m.AppendTo(nil)
	if len(b) != MetaSize {
		t.Fatalf("meta is %d bytes, want %d", len(b), MetaSize)
	}
	got, err := ParseMeta(b)
	if err != nil || got != m {
		t.Fatalf("ParseMeta = %+v, %v; want %+v", got, err, m)
	}
	b[63] = 1
	if _, err := ParseMeta(b); !errors.Is(err, ErrMeta) {
		t.Errorf("reserved byte: err = %v", err)
	}
	if _, err := ParseMeta(b[:10]); !errors.Is(err, ErrMeta) {
		t.Errorf("short meta: err = %v", err)
	}
}

func TestWidths(t *testing.T) {
	for n, want := range map[uint64]uint8{0: 1, 1: 1, 256: 1, 257: 2, 65536: 2, 65537: 3, 1 << 24: 3, 1<<24 + 1: 4, 754052: 3, 254: 1} {
		if got := IDWidth(n); got != want {
			t.Errorf("IDWidth(%d) = %d, want %d", n, got, want)
		}
	}
	if RecordWidth(0) != 10 || RecordWidth(1) != 14 || RecordWidth(2) != 33 || RecordWidth(3) != 0 {
		t.Error("RecordWidth")
	}
	if RecTimeZone+2 != RecWidthCity || RecRegionName+2 != RecWidthRegion || RecCountryGeo+4 != RecWidthCountry {
		t.Error("record field offsets do not end at the record width")
	}
	for _, n := range []uint64{0, 1, 7, 8, 9, 1<<40 + 3} {
		if a := Align8(n); a%8 != 0 || a < n || a-n > 7 {
			t.Errorf("Align8(%d) = %d", n, a)
		}
	}
}

func TestIDs(t *testing.T) {
	for w := uint8(1); w <= 4; w++ {
		var b []byte
		vals := []uint32{0, 1, 200, 255}
		if w >= 2 {
			vals = append(vals, 65535)
		}
		if w >= 3 {
			vals = append(vals, 1<<24-1)
		}
		if w == 4 {
			vals = append(vals, 1<<32-1)
		}
		for _, v := range vals {
			b = AppendID(b, w, v)
		}
		if len(b) != len(vals)*int(w) {
			t.Fatalf("width %d: %d bytes for %d ids", w, len(b), len(vals))
		}
		for i, v := range vals {
			if got := ReadID(b, w, i); got != v {
				t.Errorf("width %d: ReadID(%d) = %d, want %d", w, i, got, v)
			}
		}
	}
	var u [3]byte
	PutUint24(u[:], 0x123456)
	if Uint24(u[:]) != 0x123456 {
		t.Error("Uint24 round trip")
	}
}

func TestChecksum(t *testing.T) {
	// CRC-32C of "123456789" is the well-known check value 0xE3069283.
	if got := Checksum([]byte("123456789")); got != 0xE3069283 {
		t.Errorf("Checksum = %#x, want 0xE3069283", got)
	}
}
