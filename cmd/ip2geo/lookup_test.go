package main

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haxqer/ip2geo"
	core "github.com/haxqer/ip2geo/internal/build"
)

// Test networks: full has every field, sparse only a country (and for the
// city profile a time zone), bare nothing but a field set value, and
// noLoc a city without coordinates.
const (
	fullNet   = "81.2.69.0/24"
	sparseNet = "10.1.0.0/16"
	bareNet   = "10.2.0.0/16"
	noLocNet  = "2a00::/16"
)

// fullRecord has every field a geo record can hold.
func fullRecord() *core.Record {
	return &core.Record{
		Continent: core.Continent{Code: "EU", Names: []string{"Europe"}, GeoNameID: 6255148},
		Country:   core.Country{ISOCode: "GB", Names: []string{"United Kingdom"}, GeoNameID: 2635167},
		Subdivisions: [2]core.Subdivision{
			{ISOCode: "ENG", Names: []string{"England"}, GeoNameID: 6269131},
			{ISOCode: "WSM", Names: []string{"Westminster"}, GeoNameID: 3333218},
		},
		City: core.City{Names: []string{"London"}, GeoNameID: 2643743},
		Location: core.Location{
			Latitude: 51.5142, Longitude: -0.0931, AccuracyRadius: 10, TimeZone: "Europe/London",
			MetroCode: 501, HasCoordinates: true, HasAccuracyRadius: true,
		},
		PostalCode:         "EC2V",
		RegisteredCountry:  core.Country{ISOCode: "DE", Names: []string{"Germany"}, GeoNameID: 2921044, IsInEuropeanUnion: true},
		RepresentedCountry: core.Country{ISOCode: "US", Names: []string{"United States"}, GeoNameID: 6252001},
		RepresentedType:    "military",
		Traits:             core.Traits{IsAnycast: true},
	}
}

// buildDB builds a database with profile p and field sets fset from
// records, a map from network to record, writes it to a temporary file and
// returns its path.
func buildDB(t *testing.T, p ip2geo.Profile, fset ip2geo.FieldSet, records map[string]*core.Record) string {
	t.Helper()
	b, err := core.New(core.Options{Profile: p, Fields: fset}, core.Source{Name: "test", Type: "test"})
	if err != nil {
		t.Fatal(err)
	}
	for pfx, r := range records {
		id, err := b.Record(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.AddPrefix(netip.MustParsePrefix(pfx), id); err != nil {
			t.Fatal(err)
		}
	}
	buf, _, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), p.String()+".geoc")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// geoRecords are the records of the geo databases of TestLookupProfiles.
func geoRecords() map[string]*core.Record {
	noLoc := fullRecord()
	noLoc.Location = core.Location{TimeZone: "Europe/Berlin"}
	noLoc.Subdivisions[0].Names = nil // a city without a region
	return map[string]*core.Record{
		fullNet: fullRecord(),
		sparseNet: {
			Country:  core.Country{ISOCode: "FR"},
			Location: core.Location{TimeZone: "Europe/Paris"},
		},
		bareNet:  {Traits: core.Traits{IsSatelliteProvider: true}},
		noLocNet: noLoc,
	}
}

func TestLookupProfiles(t *testing.T) {
	addrs := []string{"81.2.69.142", "10.1.0.1", "10.2.0.1", "2a00::1", "192.0.2.1"}
	for _, tc := range []struct {
		name    string
		profile ip2geo.Profile
		fields  ip2geo.FieldSet
		want    string
	}{
		{
			name: "country", profile: ip2geo.ProfileCountry,
			want: "81.2.69.142: GB\n10.1.0.1: FR\n10.2.0.1: -\n2a00::1: GB\n192.0.2.1: no data\n",
		},
		{
			name: "region", profile: ip2geo.ProfileRegion,
			want: "81.2.69.142: GB / England\n10.1.0.1: FR\n10.2.0.1: -\n2a00::1: GB\n192.0.2.1: no data\n",
		},
		{
			// Without the location set a missing location is (0, 0).
			name: "city", profile: ip2geo.ProfileCity,
			want: "81.2.69.142: GB / England / London  (51.5142, -0.0931) Europe/London\n" +
				"10.1.0.1: FR  (0.0000, 0.0000) Europe/Paris\n" +
				"10.2.0.1: -  (0.0000, 0.0000)\n" +
				"2a00::1: GB / - / London  (0.0000, 0.0000) Europe/Berlin\n" +
				"192.0.2.1: no data\n",
		},
		{
			name: "country all fields", profile: ip2geo.ProfileCountry, fields: ip2geo.FieldsAll,
			want: "81.2.69.142: GB\n" +
				"  eu: false\n" +
				"  continent: Europe (geoname 6255148)\n" +
				"  registered_country: DE Germany (geoname 2921044, eu=true)\n" +
				"  represented_country: US United States (geoname 6252001, eu=false) type=military\n" +
				"  traits: anycast=true anonymous_proxy=false satellite_provider=false\n" +
				"10.1.0.1: FR\n" +
				"  eu: false\n" +
				"  continent:\n" +
				"  registered_country:\n" +
				"  represented_country:\n" +
				"  traits: anycast=false anonymous_proxy=false satellite_provider=false\n" +
				"10.2.0.1: -\n" +
				"  eu: false\n" +
				"  continent:\n" +
				"  registered_country:\n" +
				"  represented_country:\n" +
				"  traits: anycast=false anonymous_proxy=false satellite_provider=true\n" +
				"2a00::1: GB\n" +
				"  eu: false\n" +
				"  continent: Europe (geoname 6255148)\n" +
				"  registered_country: DE Germany (geoname 2921044, eu=true)\n" +
				"  represented_country: US United States (geoname 6252001, eu=false) type=military\n" +
				"  traits: anycast=true anonymous_proxy=false satellite_provider=false\n" +
				"192.0.2.1: no data\n",
		},
		{
			name: "region subdivisions", profile: ip2geo.ProfileRegion, fields: ip2geo.FieldSubdivisions,
			want: "81.2.69.142: GB / England\n" +
				"  region_geoname: 6269131\n" +
				"  region2: WSM Westminster (geoname 3333218)\n" +
				"10.1.0.1: FR\n" +
				"  region_geoname: 0\n" +
				"  region2:\n" +
				"10.2.0.1: -\n" +
				"  region_geoname: 0\n" +
				"  region2:\n" +
				"2a00::1: GB\n" + // no region name: nothing after the country
				"  region_geoname: 6269131\n" +
				"  region2: WSM Westminster (geoname 3333218)\n" +
				"192.0.2.1: no data\n",
		},
		{
			name: "city postal metro location", profile: ip2geo.ProfileCity,
			fields: ip2geo.FieldPostal | ip2geo.FieldMetro | ip2geo.FieldLocation,
			want: "81.2.69.142: GB / England / London  (51.5142, -0.0931) Europe/London\n" +
				"  postal_code: EC2V\n" +
				"  metro_code: 501\n" +
				"  location: coordinates=true accuracy_radius=true\n" +
				"10.1.0.1: FR  Europe/Paris\n" +
				"  postal_code:\n" +
				"  metro_code: 0\n" +
				"  location: coordinates=false accuracy_radius=false\n" +
				"10.2.0.1: -\n" +
				"  postal_code:\n" +
				"  metro_code: 0\n" +
				"  location: coordinates=false accuracy_radius=false\n" +
				"2a00::1: GB / - / London  Europe/Berlin\n" +
				"  postal_code: EC2V\n" +
				"  metro_code: 0\n" +
				"  location: coordinates=false accuracy_radius=false\n" +
				"192.0.2.1: no data\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := buildDB(t, tc.profile, tc.fields, geoRecords())
			status, stdout, stderr := runCLI(append([]string{"lookup", "-db", path}, addrs...)...)
			if status != 0 || stdout != tc.want || stderr != "" {
				t.Errorf("status %d, stdout:\n%s\nstderr:\n%s\nwant status 0 and stdout:\n%s", status, stdout, stderr, tc.want)
			}
			for _, l := range strings.Split(stdout, "\n") {
				if strings.HasSuffix(l, " ") || strings.Contains(l, " /  / ") {
					t.Errorf("line %q has trailing space or an empty name", l)
				}
			}
		})
	}
}

func TestLookupASN(t *testing.T) {
	path := buildDB(t, ip2geo.ProfileASN, 0, map[string]*core.Record{
		"8.8.8.0/24": {ASN: ip2geo.ASN{Number: 15169, Organization: "Google LLC"}},
		"9.9.9.0/24": {ASN: ip2geo.ASN{Number: 19281}},
		"1.1.1.0/24": {ASN: ip2geo.ASN{Number: 13335, Organization: "\x1b]0;pwned\x07"}},
	})
	status, stdout, stderr := runCLI("lookup", "-db", path, "8.8.8.8", "9.9.9.9", "1.1.1.1", "10.0.0.1")
	want := "8.8.8.8: AS15169 Google LLC\n" +
		"9.9.9.9: AS19281\n" +
		`1.1.1.1: AS13335 "\x1b]0;pwned\a"` + "\n" +
		"10.0.0.1: no data\n"
	if status != 0 || stdout != want || stderr != "" {
		t.Errorf("status %d, stdout:\n%s\nstderr:\n%s\nwant status 0 and stdout:\n%s", status, stdout, stderr, want)
	}
}

// TestLookupEscape checks that every string from the database is quoted
// when it is not printable, so that a database cannot send control
// sequences to the terminal.
func TestLookupEscape(t *testing.T) {
	const evil = "\x1b[31mEvil\x1b[0m"
	const quoted = `"\x1b[31mEvil\x1b[0m"`
	t.Run("csv", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "dbip-city-lite-2026-10.csv")
		csv := "1.0.0.0,1.0.0.255,EU,DE," + evil + "," + evil + ",50.1109,8.68213\n"
		if err := os.WriteFile(src, []byte(csv), 0o644); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(dir, "city.geoc")
		if status, stdout, stderr := runCLI("build", "-src", src, "-out", out); status != 0 {
			t.Fatalf("build: status %d\n%s%s", status, stdout, stderr)
		}
		status, stdout, _ := runCLI("lookup", "-db", out, "1.0.0.1")
		want := "1.0.0.1: DE / " + quoted + " / " + quoted + "  (50.1109, 8.6821)\n"
		if status != 0 || stdout != want {
			t.Errorf("status %d, stdout:\n%q\nwant:\n%q", status, stdout, want)
		}
	})
	t.Run("all fields", func(t *testing.T) {
		r := &core.Record{
			Continent: core.Continent{Code: "EU", Names: []string{evil}, GeoNameID: 1},
			Country:   core.Country{ISOCode: "\x1b[", Names: []string{evil}},
			Subdivisions: [2]core.Subdivision{
				{ISOCode: "X", Names: []string{evil}},
				{ISOCode: "\x07", Names: []string{evil}},
			},
			City:               core.City{Names: []string{evil}},
			Location:           core.Location{TimeZone: evil, HasCoordinates: true},
			PostalCode:         evil,
			RegisteredCountry:  core.Country{ISOCode: "\x1b]", Names: []string{evil}},
			RepresentedCountry: core.Country{ISOCode: "US", Names: []string{evil}},
			RepresentedType:    evil,
		}
		path := buildDB(t, ip2geo.ProfileCity, ip2geo.FieldsAll, map[string]*core.Record{fullNet: r})
		status, stdout, stderr := runCLI("lookup", "-db", path, "81.2.69.1")
		if status != 0 || stderr != "" {
			t.Fatalf("status %d\n%s%s", status, stdout, stderr)
		}
		if strings.ContainsAny(stdout, "\x1b\x07") {
			t.Errorf("raw control characters in the output:\n%q", stdout)
		}
		for _, want := range []string{
			`81.2.69.1: "\x1b[" / ` + quoted + " / " + quoted + "  (0.0000, 0.0000) " + quoted + "\n",
			"  continent: " + quoted + " (geoname 1)\n",
			`  registered_country: "\x1b]" ` + quoted + " (geoname 0, eu=false)\n",
			"  represented_country: US " + quoted + " (geoname 0, eu=false) type=" + quoted + "\n",
			`  region2: "\a" ` + quoted + "\n",
			"  postal_code: " + quoted + "\n",
		} {
			if !strings.Contains(stdout, want) {
				t.Errorf("output:\n%s\nhas no line %q", stdout, want)
			}
		}
	})
}

// TestLookupInvalid checks that an address that does not parse is reported
// on stderr, that the others are still looked up and that the exit status
// is 1, while an address without data is an ordinary result.
func TestLookupInvalid(t *testing.T) {
	path, _ := writeDB(t)
	for _, tc := range []struct {
		name   string
		addrs  []string
		status int
		stdout string
		stderr string
	}{
		{"valid", []string{"81.2.69.142", "10.0.0.1"}, 0, "81.2.69.142: GB\n10.0.0.1: no data\n", ""},
		{
			"invalid", []string{"bogus", "81.2.69.142", "1.2.3", "10.0.0.1"}, 1,
			"81.2.69.142: GB\n10.0.0.1: no data\n",
			"ip2geo: lookup: bogus: invalid address\nip2geo: lookup: 1.2.3: invalid address\n",
		},
		{"unprintable", []string{"\x1b[2J"}, 1, "", "ip2geo: lookup: \"\\x1b[2J\": invalid address\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, stdout, stderr := runCLI(append([]string{"lookup", "-db", path}, tc.addrs...)...)
			if status != tc.status || stdout != tc.stdout || stderr != tc.stderr {
				t.Errorf("status %d, stdout:\n%s\nstderr:\n%s\nwant status %d, stdout:\n%s\nstderr:\n%s",
					status, stdout, stderr, tc.status, tc.stdout, tc.stderr)
			}
		})
	}
}

// TestBuildDefaultOut checks that build writes <profile>.geoc when -out is
// not given.
func TestBuildDefaultOut(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	city := write("dbip-city-lite-2026-10.csv", "1.0.0.0,1.0.0.255,OC,AU,Queensland,Brisbane,-27.4767,153.017\n")
	country := write("dbip-country-lite-2026-10.csv", "1.0.0.0,1.0.0.255,AU\n")
	asn := write("dbip-asn-lite-2026-10.csv", "1.0.0.0,1.0.0.255,13335,Cloudflare\n")
	t.Chdir(t.TempDir())
	for _, tc := range []struct {
		src, profile, out, lookup string
	}{
		{city, "", "city.geoc", "1.0.0.1: AU / Queensland / Brisbane  (-27.4767, 153.0170)\n"},
		{city, "city", "city.geoc", "1.0.0.1: AU / Queensland / Brisbane  (-27.4767, 153.0170)\n"},
		{city, "region", "region.geoc", "1.0.0.1: AU / Queensland\n"},
		{country, "country", "country.geoc", "1.0.0.1: AU\n"},
		{asn, "asn", "asn.geoc", "1.0.0.1: AS13335 Cloudflare\n"},
	} {
		args := []string{"build", "-src", tc.src}
		if tc.profile != "" {
			args = append(args, "-profile", tc.profile)
		}
		status, stdout, stderr := runCLI(args...)
		if status != 0 || !strings.HasPrefix(stdout, "built "+tc.out+" ") {
			t.Errorf("%q: status %d\n%s%s\nwant built %s", args, status, stdout, stderr, tc.out)
			continue
		}
		if status, stdout, _ := runCLI("lookup", "-db", tc.out, "1.0.0.1"); status != 0 || stdout != tc.lookup {
			t.Errorf("%q: lookup: status %d\n%s\nwant\n%s", args, status, stdout, tc.lookup)
		}
	}
	if _, stdout, stderr := runCLI("build", "-h"); !strings.Contains(stdout+stderr, "(default <profile>.geoc") {
		t.Errorf("build -h does not document the -out default:\n%s", stderr)
	}
}
