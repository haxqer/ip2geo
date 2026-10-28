package mmdb

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"
	"testing"
)

// net4 returns the range entry for an IPv4 CIDR.
func net4(cidr string, rec uint32) rangeEntry {
	p := netip.MustParsePrefix(cidr)
	a := p.Addr().As4()
	start := binary.BigEndian.Uint32(a[:])
	return rangeEntry{start: start, last: start | uint32(uint64(1)<<(32-p.Bits())-1), rec: rec}
}

// net6 returns the range entry for an IPv6 CIDR.
func net6(cidr string, rec uint32) rangeEntry {
	p := netip.MustParsePrefix(cidr)
	s := p.Addr().As16()
	return rangeEntry{s16: s, l16: lastV6(s, p.Bits()), rec: rec}
}

func fmtV4(bs []boundary) string {
	var parts []string
	for _, b := range bs {
		var a [4]byte
		binary.BigEndian.PutUint32(a[:], b.start)
		parts = append(parts, fmt.Sprintf("%s=%d", netip.AddrFrom4(a), b.rec))
	}
	return strings.Join(parts, " ")
}

func fmtV6(bs []boundary) string {
	var parts []string
	for _, b := range bs {
		parts = append(parts, fmt.Sprintf("%s=%d", netip.AddrFrom16(b.s16), b.rec))
	}
	return strings.Join(parts, " ")
}

// TestMergeV4 checks that runs of equal records merge and that gaps, and only
// gaps, get record 0.
func TestMergeV4(t *testing.T) {
	tests := []struct {
		name string
		in   []rangeEntry
		want string
	}{
		{"empty", nil, ""},
		{"single", []rangeEntry{net4("10.0.0.0/8", 1)}, "10.0.0.0=1 11.0.0.0=0"},
		{"adjacent same record merges",
			[]rangeEntry{net4("10.0.0.0/24", 2), net4("10.0.1.0/24", 2)}, "10.0.0.0=2 10.0.2.0=0"},
		{"adjacent different records",
			[]rangeEntry{net4("10.0.0.0/24", 1), net4("10.0.1.0/24", 2)}, "10.0.0.0=1 10.0.1.0=2 10.0.2.0=0"},
		{"gap between same record",
			[]rangeEntry{net4("10.0.0.0/24", 1), net4("10.0.2.0/24", 1)}, "10.0.0.0=1 10.0.1.0=0 10.0.2.0=1 10.0.3.0=0"},
		{"unsorted input",
			[]rangeEntry{net4("10.0.2.0/24", 3), net4("10.0.0.0/24", 1)}, "10.0.0.0=1 10.0.1.0=0 10.0.2.0=3 10.0.3.0=0"},
		{"starts at zero, ends at the top",
			[]rangeEntry{net4("0.0.0.0/1", 1), net4("128.0.0.0/1", 2)}, "0.0.0.0=1 128.0.0.0=2"},
		{"ends at the top after a gap",
			[]rangeEntry{net4("1.0.0.0/8", 1), net4("255.0.0.0/8", 1)}, "1.0.0.0=1 2.0.0.0=0 255.0.0.0=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fmtV4(mergeV4(tt.in)); got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestMergeV6(t *testing.T) {
	tests := []struct {
		name string
		in   []rangeEntry
		want string
	}{
		{"empty", nil, ""},
		{"single", []rangeEntry{net6("2001:db8::/32", 1)}, "2001:db8::=1 2001:db9::=0"},
		{"adjacent same record merges",
			[]rangeEntry{net6("2001:db8::/33", 4), net6("2001:db8:8000::/33", 4)}, "2001:db8::=4 2001:db9::=0"},
		{"gap between",
			[]rangeEntry{net6("2001:db8::/32", 1), net6("2001:dc0::/32", 2)}, "2001:db8::=1 2001:db9::=0 2001:dc0::=2 2001:dc1::=0"},
		{"ends at the top",
			[]rangeEntry{net6("ff00::/8", 1)}, "ff00::=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fmtV6(mergeV6(tt.in)); got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}
