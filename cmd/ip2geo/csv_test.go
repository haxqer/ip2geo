package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceFormat(t *testing.T) {
	for _, tc := range []struct {
		src, flag, want string
	}{
		{"GeoIP2-City.mmdb", "", "mmdb"},
		{"dir/GeoLite2-ASN.MMDB", "", "mmdb"},
		{"x.mmdb.gz", "", "mmdb"},
		{"dbip-city-lite-2026-10.csv", "", "dbip-city"},
		{"/data/dbip-country-lite-2026-10.csv.gz", "", "dbip-country"},
		{"DBIP-ASN-LITE-2026-10.CSV", "", "dbip-asn"},
		{"dbip-city-lite-2026-10.mmdb", "", "mmdb"},
		{"IP2LOCATION-LITE-DB11.CSV", "ip2location", "ip2location"},
		{"anything", "ip2location-asn", "ip2location-asn"},
		{"dbip-city-lite-2026-10.csv", "mmdb", "mmdb"},
		{"IP2LOCATION-LITE-DB11.CSV", "", ""},
		{"dbip-city.csv", "", ""},
		{"city", "", ""},
		{"x.mmdb", "csv", ""},
	} {
		got, err := sourceFormat(tc.src, tc.flag)
		if got != tc.want || (err != nil) != (tc.want == "") {
			t.Errorf("sourceFormat(%q, %q) = %q, %v; want %q", tc.src, tc.flag, got, err, tc.want)
		}
	}
}

// TestBuildCSV builds from small DB-IP and IP2Location CSV files.
func TestBuildCSV(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	city := write("dbip-city-lite-2026-10.csv", "0.0.0.0,0.255.255.255,ZZ,ZZ,,,0,0\n"+
		`1.0.0.0,1.0.0.255,OC,AU,Queensland,"South Brisbane",-27.4767,153.017`+"\n"+
		"2a00::,2a00::ffff,EU,DE,Hesse,Frankfurt,50.1109,8.68213\n")
	out := filepath.Join(dir, "city.geoc")
	status, stdout, stderr := runCLI("build", "-src", city, "-out", out, "-fields", "all")
	if status != 0 || !strings.Contains(stdout, "profile=city networks=2 ") {
		t.Fatalf("build: status %d\n%s%s", status, stdout, stderr)
	}
	status, stdout, _ = runCLI("lookup", "-db", out, "1.0.0.1", "2a00::1", "0.0.0.1")
	want := "1.0.0.1: AU / Queensland / South Brisbane  (-27.4767, 153.0170) \n" +
		"  location: coordinates=true accuracy_radius=false\n" +
		"2a00::1: DE / Hesse / Frankfurt  (50.1109, 8.6821) \n" +
		"  location: coordinates=true accuracy_radius=false\n" +
		"0.0.0.1: no data\n"
	if status != 0 || stdout != want {
		t.Errorf("lookup: status %d\n%s\nwant\n%s", status, stdout, want)
	}
	status, stdout, _ = runCLI("info", "-db", out)
	if status != 0 || !strings.Contains(stdout, "\nsource_type: DBIP-City-Lite (CSV)\nsource_time: unknown\nlanguage: en\n") ||
		!strings.Contains(stdout, "\nfields: location\n") {
		t.Errorf("info: status %d\n%s", status, stdout)
	}

	// IP2Location needs -format.
	ip2l := write("IP2LOCATION-LITE-DB1.CSV", `"0","16777215","-","-"`+"\r\n"+`"16777216","16777471","AU","Australia"`+"\r\n")
	status, _, stderr = runCLI("build", "-src", ip2l, "-out", out, "-profile", "country")
	if status != 2 || !strings.Contains(stderr, "set -format") {
		t.Errorf("no -format: status %d\n%s", status, stderr)
	}
	status, stdout, stderr = runCLI("build", "-src", ip2l, "-format", "ip2location", "-out", out, "-profile", "country")
	if status != 0 || !strings.Contains(stdout, "profile=country networks=1 ") {
		t.Fatalf("build: status %d\n%s%s", status, stdout, stderr)
	}
	if status, stdout, _ = runCLI("lookup", "-db", out, "1.0.0.1"); status != 0 || stdout != "1.0.0.1: AU /  /   (0.0000, 0.0000) \n" {
		t.Errorf("lookup: status %d\n%s", status, stdout)
	}

	for _, tc := range []struct {
		name   string
		args   []string
		status int
		stderr string
	}{
		{"unknown format", []string{"-src", city, "-format", "csv"}, 2, `unknown -format "csv"`},
		{"lang", []string{"-src", city, "-lang", "en,de"}, 1, "a CSV source has English names only"},
		{"profile", []string{"-src", ip2l, "-format", "ip2location"}, 1, "unsupported database type"},
		{"fields", []string{"-src", city, "-fields", "postal"}, 1, "invalid options"},
		{"asn", []string{"-src", city, "-profile", "asn"}, 1, "unsupported database type"},
		{"syntax", []string{"-src", ip2l, "-format", "dbip-country", "-profile", "country"}, 1, ip2l + ":1: "},
		{"gzipped mmdb", []string{"-src", filepath.Join(dir, "x.mmdb.gz")}, 1, "decompress it first"},
	} {
		status, _, stderr := runCLI(append([]string{"build", "-out", filepath.Join(dir, "never.geoc")}, tc.args...)...)
		if status != tc.status || !strings.Contains(stderr, tc.stderr) {
			t.Errorf("%s: status %d, stderr:\n%s\nwant status %d and %q", tc.name, status, stderr, tc.status, tc.stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "never.geoc")); err == nil {
		t.Error("a failed build wrote its output")
	}
}
