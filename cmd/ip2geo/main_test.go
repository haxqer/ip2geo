package main

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haxqer/ip2geo"
)

// geo1Country returns a v0.1 (GEO1) country database in which 81.0.0.0/8
// and 2a00::/16 are GB and every other address has no data. The commands
// read GEO1 like GEO2; GEO2 files only come out of the mmdb builder, which
// needs a .mmdb source, so the GEO2 side of info is tested on Metadata
// values in TestWriteInfo.
func geo1Country() []byte {
	le := binary.LittleEndian
	b := []byte("GEO1\x01\x00\x00\x00") // magic, version 1, country profile, lat scale 0, reserved
	b = le.AppendUint32(b, 4)
	for _, s := range []string{"", "GB", "United Kingdom", "EU"} {
		b = le.AppendUint16(b, uint16(len(s)))
		b = append(b, s...)
	}
	b = le.AppendUint32(b, 2) // records: "no data", then GB
	b = le.AppendUint16(b, uint16(ip2geo.RecWidth(ip2geo.ProfileCountry)))
	for _, v := range []uint32{0, 0, 0, 0, 1, 2, 3, 2635167} {
		b = le.AppendUint32(b, v)
	}
	b = le.AppendUint32(b, 2) // IPv4: starts, then record ids
	for _, v := range []uint32{81 << 24, 82 << 24, 1, 0} {
		b = le.AppendUint32(b, v)
	}
	b = le.AppendUint32(b, 2) // IPv6: 16-byte big-endian starts, then record ids
	for _, s := range []string{"2a00::", "2a01::"} {
		a := netip.MustParseAddr(s).As16()
		b = append(b, a[:]...)
	}
	b = le.AppendUint32(b, 1)
	return le.AppendUint32(b, 0)
}

func writeDB(t *testing.T) (string, []byte) {
	t.Helper()
	b := geo1Country()
	path := filepath.Join(t.TempDir(), "country.geoc")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, b
}

func runCLI(args ...string) (status int, stdout, stderr string) {
	var o, e bytes.Buffer
	status = run(args, &o, &e)
	return status, o.String(), e.String()
}

func TestExitStatus(t *testing.T) {
	path, _ := writeDB(t)
	missing := filepath.Join(t.TempDir(), "missing")
	for _, tc := range []struct {
		name   string
		args   []string
		status int
		stderr string // a substring of stderr
	}{
		{"no command", nil, 2, "usage:"},
		{"unknown command", []string{"convert"}, 2, "ip2geo info"},
		{"build help", []string{"build", "-h"}, 0, "-description"},
		{"build bad flag", []string{"build", "-nope"}, 2, "flag provided but not defined"},
		{"build no src", []string{"build"}, 2, "-src is required"},
		{"build argument", []string{"build", "-src", "x.mmdb", "extra"}, 2, `unexpected argument "extra"`},
		{"build profile", []string{"build", "-src", "x.mmdb", "-profile", "street"}, 2, `unknown -profile "street"`},
		{"build latscale", []string{"build", "-src", "x.mmdb", "-latscale", "300"}, 1, "invalid -latscale 300"},
		// Options are checked before the source is opened.
		{"build description", []string{"build", "-src", missing, "-description", "\xff"}, 1, "invalid options"},
		{"build missing source", []string{"build", "-src", missing, "-out", filepath.Join(t.TempDir(), "o.geoc")}, 1, "ip2geo: build: "},
		{"lookup no addresses", []string{"lookup", "-db", path}, 2, "no addresses given"},
		{"lookup missing db", []string{"lookup", "-db", missing, "1.1.1.1"}, 1, "ip2geo: lookup: "},
		{"info argument", []string{"info", "-db", path, "extra"}, 2, `unexpected argument "extra"`},
		{"info missing db", []string{"info", "-db", missing}, 1, "ip2geo: info: "},
		{"info not a database", []string{"info", "-db", filepath.Join("..", "..", "go.mod")}, 1, "not an ip2geo database"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, stderr := runCLI(tc.args...)
			if status != tc.status || !strings.Contains(stderr, tc.stderr) {
				t.Errorf("status %d, stderr:\n%s\nwant status %d and stderr containing %q", status, stderr, tc.status, tc.stderr)
			}
		})
	}
}

func TestLookup(t *testing.T) {
	path, _ := writeDB(t)
	status, stdout, stderr := runCLI("lookup", "-db", path, "81.2.69.142", "10.0.0.1", "2a00:1450::1", "::ffff:81.0.0.1", "bogus")
	want := "81.2.69.142: GB /  /   (0.0000, 0.0000) \n" +
		"10.0.0.1: no data\n" +
		"2a00:1450::1: GB /  /   (0.0000, 0.0000) \n" +
		"::ffff:81.0.0.1: GB /  /   (0.0000, 0.0000) \n" +
		"bogus: invalid address\n"
	if status != 0 || stdout != want || stderr != "" {
		t.Errorf("status %d, stdout:\n%s\nstderr:\n%s\nwant status 0 and stdout:\n%s", status, stdout, stderr, want)
	}
}

func TestInfoGEO1(t *testing.T) {
	path, b := writeDB(t)
	db, err := ip2geo.OpenBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	md := db.Metadata()
	if md.FormatVersion != 1 {
		t.Fatalf("fixture has format version %d, want 1", md.FormatVersion)
	}
	status, stdout, stderr := runCLI("info", "-db", path)
	var want strings.Builder
	writeInfo(&want, int64(len(b)), md)
	if status != 0 || stdout != want.String() || stderr != "" {
		t.Fatalf("status %d, stdout:\n%s\nstderr:\n%s\nwant status 0 and stdout:\n%s", status, stdout, stderr, want.String())
	}
	if !strings.Contains(stdout, "\nformat: GEO1\nprofile: country\nlat_scale: 0\nrecords: ") || strings.Contains(stdout, "build_time") {
		t.Errorf("GEO1 info output:\n%s", stdout)
	}
}

func TestWriteInfo(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int64
		md   ip2geo.Metadata
		want string
	}{
		{
			name: "GEO2",
			size: 1819048,
			md: ip2geo.Metadata{
				FormatVersion: 2, Profile: ip2geo.ProfileCity, LatScale: 4,
				BuildTime:   time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC),
				SourceTime:  time.Date(2024, 5, 7, 15, 4, 5, 0, time.FixedZone("CEST", 2*3600)),
				SourceType:  "GeoIP2-City",
				Language:    "en",
				Description: "test",
				Builder:     "ip2geo/v0.2.0",
				Networks:    10, DistinctRecords: 9, Records: 8, Strings: 7, IPv4Ranges: 6, IPv6Ranges: 5,
			},
			want: `size: 1819048 bytes (1.73 MiB)
format: GEO2
profile: city
lat_scale: 4
build_time: 2026-10-28T12:00:00Z
source_type: GeoIP2-City
source_time: 2024-05-07T13:04:05Z
language: en
description: test
builder: ip2geo/v0.2.0
networks: 10
distinct_records: 9
records: 8
strings: 7
ipv4_ranges: 6
ipv6_ranges: 5
`,
		},
		{
			name: "GEO2 unknown and unprintable",
			size: 100,
			md:   ip2geo.Metadata{FormatVersion: 2, Description: "two\nlines", Builder: "\x1b[31m", Language: "\xff"},
			want: `size: 100 bytes (0.00 MiB)
format: GEO2
profile: country
lat_scale: 0
build_time: unknown
source_type:
source_time: unknown
language: "\xff"
description: "two\nlines"
builder: "\x1b[31m"
networks: 0
distinct_records: 0
records: 0
strings: 0
ipv4_ranges: 0
ipv6_ranges: 0
`,
		},
		{
			name: "GEO1",
			size: 3699834,
			md:   ip2geo.Metadata{FormatVersion: 1, Profile: ip2geo.ProfileRegion, Records: 4, Strings: 3, IPv4Ranges: 2, IPv6Ranges: 1},
			want: `size: 3699834 bytes (3.53 MiB)
format: GEO1
profile: region
lat_scale: 0
records: 4
strings: 3
ipv4_ranges: 2
ipv6_ranges: 1
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			writeInfo(&b, tc.size, tc.md)
			if b.String() != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", b.String(), tc.want)
			}
		})
	}
}
