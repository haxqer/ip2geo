package mmdb_test

import (
	"bytes"
	"errors"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/mmdb"
)

// TestBadCoordinates: a ProfileCity build of a source with a coordinate
// that cannot be stored fails, naming the network, and leaves the output
// alone; the other profiles do not store coordinates and build.
func TestBadCoordinates(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		lat, lon float64
		want     string
	}{
		{"latitude NaN", math.NaN(), 0, "latitude NaN"},
		{"longitude +Inf", 0, math.Inf(1), "longitude +Inf"},
		{"latitude 91", 91, 0, "latitude 91"},
		{"longitude -180.01", 0, -180.01, "longitude -180.01"},
		{"longitude 1e10", 0, 1e10, "longitude 1e+10"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fx := cityFixture()
			fx.networks[3].data = cityData("EU", "DE", 2921044, "Germany", "Deutschland", "BY", "Bavaria",
				2867714, "Munich", tt.lat, tt.lon, 10, "Europe/Berlin")
			src := writeMMDB(t, fx)

			dir := t.TempDir()
			out := filepath.Join(dir, "city.geoc")
			old := []byte("previous")
			if err := os.WriteFile(out, old, 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := mmdb.Build(src, out, mmdb.Options{Profile: ip2geo.ProfileCity})
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "1.0.4.0/22") {
				t.Fatalf("Build: err = %v, want an error naming %q and the network", err, tt.want)
			}
			if got, _ := os.ReadFile(out); !bytes.Equal(got, old) {
				t.Error("a failed build replaced the output")
			}
			if m := tempFiles(t, dir); len(m) != 0 {
				t.Errorf("temporary files left: %v", m)
			}
			if _, err := mmdb.Open(src, mmdb.Options{Profile: ip2geo.ProfileCity, Fields: ip2geo.FieldsAll}); err == nil {
				t.Error("Open with field sets: no error")
			}
			for _, p := range []ip2geo.Profile{ip2geo.ProfileCountry, ip2geo.ProfileRegion} {
				db, err := mmdb.Open(src, mmdb.Options{Profile: p})
				if err != nil {
					t.Fatalf("profile %s: %v", p, err)
				}
				if rec, ok := db.Lookup(netip.MustParseAddr("1.0.4.1")); !ok || rec.CountryISO != "DE" {
					t.Errorf("profile %s: Lookup = %+v, %v", p, rec, ok)
				}
			}
		})
	}
}

// TestNetworkCoveringIPv4: a network ::/n with n < 96 holds the IPv4
// subtree of the source, which the IPv4 index would not get; the build
// refuses it instead of losing the IPv4 data.
func TestNetworkCoveringIPv4(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"::/64", "::/95", "::/8"} {
		fx := mmdbFixture{dbType: "GeoIP2-Country", networks: []mmdbNet{
			{p, map[string]any{"country": map[string]any{"iso_code": "AU"}}},
			{"2001:db8::/32", map[string]any{"country": map[string]any{"iso_code": "JP"}}},
		}}
		_, err := mmdb.Open(writeMMDB(t, fx), mmdb.Options{})
		if err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "covers all of IPv4") {
			t.Errorf("%s: err = %v, want an error naming the network", p, err)
		}
	}
}

// TestNetworkInIPv4Alias: a source that stores data of its own under
// Teredo 2001::/32 or 6to4 2002::/16, instead of aliasing them to its IPv4
// subtree as MaxMind and DB-IP do, fails with ErrIPv4Alias: the reader
// answers those addresses from the IPv4 data, so the data would never be
// read. Nothing is written. A network that contains a block is built.
func TestNetworkInIPv4Alias(t *testing.T) {
	t.Parallel()
	au := map[string]any{"country": map[string]any{"iso_code": "AU"}}
	jp := map[string]any{"country": map[string]any{"iso_code": "JP"}}
	for _, tt := range []struct{ net, block string }{
		{"2001:0:102:300::/56", "2001::/32"},
		{"2002:abcd::/32", "2002::/16"},
		{"2002::/16", "2002::/16"},
	} {
		fx := mmdbFixture{dbType: "GeoIP2-Country", aliasV4: true, networks: []mmdbNet{
			{"1.0.0.0/24", au},
			{tt.net, jp},
			{"2400::/12", jp},
		}}
		src := writeMMDB(t, fx)
		_, err := mmdb.Open(src, mmdb.Options{})
		if !errors.Is(err, mmdb.ErrIPv4Alias) || !strings.Contains(err.Error(), tt.net+" is inside "+tt.block) {
			t.Errorf("%s: err = %v, want ErrIPv4Alias naming the network and %s", tt.net, err, tt.block)
		}
		out := filepath.Join(t.TempDir(), "out.geoc")
		if _, err := mmdb.Build(src, out, mmdb.Options{}); !errors.Is(err, mmdb.ErrIPv4Alias) {
			t.Errorf("%s: Build err = %v, want ErrIPv4Alias", tt.net, err)
		}
		if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: Build wrote %s (stat err %v)", tt.net, out, err)
		}
	}

	fx := mmdbFixture{dbType: "GeoIP2-Country", aliasV4: true, networks: []mmdbNet{
		{"171.205.0.0/16", au},
		{"2000::/3", jp},
	}}
	db, err := mmdb.Open(writeMMDB(t, fx), mmdb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ ip, iso string }{
		{"2002:abcd::1", "AU"}, {"2001:0:abcd:1::", "AU"}, {"2001:1::1", "JP"}, {"2003::1", "JP"},
	} {
		rec, ok := db.Lookup(netip.MustParseAddr(c.ip))
		if !ok || rec.CountryISO != c.iso {
			t.Errorf("Lookup(%s) = %q, %v; want %q", c.ip, rec.CountryISO, ok, c.iso)
		}
	}
}

// TestMappedNetworkWithoutAlias: maxminddb yields the networks of
// ::ffff:0:0/96 when that block holds data of its own instead of aliasing
// the IPv4 subtree. They are IPv4 networks, as IPv4-mapped rows are in a
// CSV source, and are found by IPv4 lookups.
func TestMappedNetworkWithoutAlias(t *testing.T) {
	t.Parallel()
	fx := mmdbFixture{dbType: "GeoIP2-Country", networks: []mmdbNet{
		{"::ffff:1.0.0.0/120", map[string]any{"country": map[string]any{"iso_code": "AU"}}},
		{"2001:db8::/32", map[string]any{"country": map[string]any{"iso_code": "JP"}}},
	}}
	db, err := mmdb.Open(writeMMDB(t, fx), mmdb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ ip, iso string }{
		{"1.0.0.1", "AU"}, {"::ffff:1.0.0.1", "AU"}, {"1.0.1.1", ""}, {"2001:db8::1", "JP"},
	} {
		rec, ok := db.Lookup(netip.MustParseAddr(c.ip))
		if ok != (c.iso != "") || rec.CountryISO != c.iso {
			t.Errorf("Lookup(%s) = %q, %v; want %q", c.ip, rec.CountryISO, ok, c.iso)
		}
	}
}

// TestErrLanguageIsReaderSentinel: the language error of a build matches
// ip2geo.ErrLanguage, the reader's sentinel, as well as mmdb.ErrLanguage.
func TestErrLanguageIsReaderSentinel(t *testing.T) {
	t.Parallel()
	src := writeMMDB(t, cityFixture())
	out := filepath.Join(t.TempDir(), "city.geoc")
	for _, opts := range []mmdb.Options{{Lang: "xx"}, {Profile: ip2geo.ProfileCity, Languages: []string{"en", "xx"}}} {
		_, err := mmdb.Build(src, out, opts)
		if !errors.Is(err, ip2geo.ErrLanguage) || !errors.Is(err, mmdb.ErrLanguage) {
			t.Errorf("Build(%+v): err = %v, want ip2geo.ErrLanguage", opts, err)
		}
	}
	if mmdb.ErrLanguage != ip2geo.ErrLanguage {
		t.Error("mmdb.ErrLanguage is not ip2geo.ErrLanguage")
	}
}
