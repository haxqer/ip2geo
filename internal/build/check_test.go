package build

import (
	"bytes"
	"errors"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haxqer/ip2geo"
)

var testBuildTime = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func newTestBuilder(t *testing.T, p ip2geo.Profile) *Builder {
	t.Helper()
	b, err := New(Options{Profile: p, BuildTime: testBuildTime}, Source{Name: "test", Type: "GeoIP2-City"})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// smallDatabase returns a valid ProfileCity database of one network.
func smallDatabase(t *testing.T) []byte {
	t.Helper()
	b := newTestBuilder(t, ip2geo.ProfileCity)
	id, err := b.Record(&Record{Country: Country{ISOCode: "AU"}, Location: Location{Latitude: -27.4767, Longitude: 153.017}})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.AddPrefix(netip.MustParsePrefix("1.0.0.0/24"), id); err != nil {
		t.Fatal(err)
	}
	buf, _, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return buf
}

func TestRecordCoordinates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		lat, lon float64
		bad      string // what the error names; "" for a valid coordinate
	}{
		{"zero", 0, 0, ""},
		{"corners", -90, 180, ""},
		{"other corners", 90, -180, ""},
		{"latitude NaN", math.NaN(), 0, "latitude NaN"},
		{"longitude NaN", 0, math.NaN(), "longitude NaN"},
		{"latitude +Inf", math.Inf(1), 0, "latitude +Inf"},
		{"longitude -Inf", 0, math.Inf(-1), "longitude -Inf"},
		{"latitude above 90", 90.00001, 0, "latitude 90.00001"},
		{"latitude below -90", -91, 0, "latitude -91"},
		{"longitude above 180", 0, 180.5, "longitude 180.5"},
		{"longitude below -180", 0, -1e300, "longitude -1e+300"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &Record{Country: Country{ISOCode: "AU"}, Location: Location{Latitude: tt.lat, Longitude: tt.lon}}
			b := newTestBuilder(t, ip2geo.ProfileCity)
			id, err := b.Record(r)
			if tt.bad == "" {
				if err != nil || id == 0 {
					t.Fatalf("Record = %d, %v; want a record id", id, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.bad) {
				t.Fatalf("Record = %d, %v; want an error naming %q", id, err, tt.bad)
			}
			if b.distinct != 0 || b.recs.n != 1 || len(b.keys) != 0 {
				t.Errorf("a failed Record changed the build: distinct=%d records=%d keys=%d", b.distinct, b.recs.n, len(b.keys))
			}

			// Profiles that store no coordinates do not look at them.
			for _, p := range []ip2geo.Profile{ip2geo.ProfileCountry, ip2geo.ProfileRegion} {
				if _, err := newTestBuilder(t, p).Record(r); err != nil {
					t.Errorf("profile %s: %v", p, err)
				}
			}
		})
	}
}

// TestScaleCoordOverflow covers the int32 guard, which coordinates in
// range cannot reach with a LatScale Normalize accepts.
func TestScaleCoordOverflow(t *testing.T) {
	t.Parallel()
	maxScale := math.Pow10(MaxLatScale)
	for _, v := range []float64{-180, 180} {
		if got, err := scaleCoord("longitude", v, 180, maxScale); err != nil || float64(got) != v*maxScale {
			t.Errorf("scaleCoord(%v, 1e%d) = %d, %v", v, MaxLatScale, got, err)
		}
	}
	for _, v := range []float64{-180, 180, 21.48} {
		if got, err := scaleCoord("longitude", v, 180, 1e8); err == nil || !strings.Contains(err.Error(), "does not fit an int32") {
			t.Errorf("scaleCoord(%v, 1e8) = %d, %v; want an int32 overflow error", v, got, err)
		}
	}
}

// TestErrLanguageIsReaderSentinel: errors.Is(err, ip2geo.ErrLanguage) holds
// for a builder's language error, and the other builder sentinels are
// distinct values.
func TestErrLanguageIsReaderSentinel(t *testing.T) {
	t.Parallel()
	if ErrLanguage != ip2geo.ErrLanguage {
		t.Fatal("ErrLanguage is not ip2geo.ErrLanguage")
	}
	all := []error{ErrDatabaseType, ErrLanguage, ErrOptions, ErrStringTooLong, ErrTooManyStrings,
		ip2geo.ErrBadMagic, ip2geo.ErrUnsupportedVersion, ip2geo.ErrCorrupt, ip2geo.ErrTooLarge}
	for i, a := range all {
		for _, b := range all[i+1:] {
			if errors.Is(a, b) {
				t.Errorf("%v is %v", a, b)
			}
		}
	}
}

// writeExisting creates path with known contents and returns them.
func writeExisting(t *testing.T, path string) []byte {
	t.Helper()
	old := []byte("previous database")
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	return old
}

// checkUntouched fails unless path still holds old and dir has no
// temporary file.
func checkUntouched(t *testing.T, path string, old []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, old) {
		t.Errorf("%s was replaced", path)
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".geoc-*")); len(m) != 0 {
		t.Errorf("temporary files left: %v", m)
	}
}

func TestWriteDatabase(t *testing.T) {
	t.Parallel()
	good := smallDatabase(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "out.geoc")
	if err := WriteDatabase(out, good); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(got, good) {
		t.Fatalf("written file differs from the buffer (err %v)", err)
	}

	corrupt := bytes.Clone(good)
	i := bytes.Index(corrupt, []byte("GeoIP2-City")) // the source type, in the string data
	if i < 0 {
		t.Fatal("source type not found in the database")
	}
	corrupt[i] = 'g' // breaks the section checksum only
	truncated := good[:len(good)-8]
	for _, tt := range []struct {
		name string
		buf  []byte
		want error
	}{
		{"checksum", corrupt, ip2geo.ErrCorrupt},
		{"truncated", truncated, ip2geo.ErrCorrupt},
		{"garbage", []byte("not a database"), ip2geo.ErrBadMagic},
		{"empty", nil, ip2geo.ErrBadMagic},
	} {
		old := writeExisting(t, out)
		err := WriteDatabase(out, tt.buf)
		if !errors.Is(err, tt.want) || !strings.Contains(err.Error(), "not written") {
			t.Errorf("%s: err = %v, want %v and \"not written\"", tt.name, err, tt.want)
		}
		checkUntouched(t, out, old)
	}
}

// TestWriteDatabaseTooLarge lowers the size limit, which the test cannot
// reach with a real 2 GiB buffer. It does not run in parallel: it changes
// maxFileSize.
func TestWriteDatabaseTooLarge(t *testing.T) {
	good := smallDatabase(t)
	defer func(v uint64) { maxFileSize = v }(maxFileSize)
	maxFileSize = uint64(len(good))
	if err := CheckDatabase(good); err != nil {
		t.Fatalf("at the limit: %v", err)
	}
	maxFileSize = uint64(len(good) - 1)

	out := filepath.Join(t.TempDir(), "out.geoc")
	old := writeExisting(t, out)
	err := WriteDatabase(out, good)
	if !errors.Is(err, ip2geo.ErrTooLarge) || !strings.Contains(err.Error(), "over the") {
		t.Errorf("err = %v, want ErrTooLarge naming the limit", err)
	}
	checkUntouched(t, out, old)
}

func TestWriteFileAtomicNewFile(t *testing.T) {
	t.Parallel()
	out := filepath.Join(t.TempDir(), "new.geoc")
	if err := WriteFileAtomic(out, []byte("x")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(out)
	if err != nil || fi.Size() != 1 || fi.Mode().Perm() != 0o644 {
		t.Fatalf("stat = %v, %v; want a 1-byte file of mode 0644", fi, err)
	}
	syncDir(filepath.Join(t.TempDir(), "missing")) // best effort: no panic, no error
}

func TestAddPrefix(t *testing.T) {
	t.Parallel()
	b := newTestBuilder(t, ip2geo.ProfileCountry)
	id, err := b.Record(&Record{Country: Country{ISOCode: "AU"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"::/0", "::/1", "::/64", "::/95", "::/80"} {
		err := b.AddPrefix(netip.MustParsePrefix(p), id)
		if err == nil || !strings.Contains(err.Error(), "covers all of IPv4") {
			t.Errorf("AddPrefix(%s) = %v, want an error", p, err)
		}
	}
	if b.networks != 0 {
		t.Errorf("rejected networks were counted: %d", b.networks)
	}
	// ::/96 itself is IPv4-compatible space, not the IPv4 subtree as
	// maxminddb yields it (that comes as IPv4 prefixes), and an IPv6
	// prefix that does not contain :: is fine; an IPv4-mapped prefix is
	// IPv4.
	for _, p := range []string{"::/96", "::1:0:0:0/80", "8000::/1", "::ffff:1.0.0.0/120", "2.0.0.0/8"} {
		if err := b.AddPrefix(netip.MustParsePrefix(p), id); err != nil {
			t.Errorf("AddPrefix(%s) = %v", p, err)
		}
	}
	buf, st, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	r, err := ip2geo.OpenBytes(buf)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		ip    string
		found bool
	}{
		{"1.0.0.7", true}, {"::ffff:1.0.0.7", true}, {"1.0.1.0", false}, {"2.1.2.3", true},
		{"8000::1", true}, {"::1:0:0:1", true}, {"::2:0:0:1", false},
	} {
		if _, ok := r.Lookup(netip.MustParseAddr(c.ip)); ok != c.found {
			t.Errorf("Lookup(%s) ok = %v, want %v", c.ip, ok, c.found)
		}
	}
	if st.Networks != 5 {
		t.Errorf("Networks = %d, want 5", st.Networks)
	}
}
