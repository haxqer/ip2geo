package csvdb_test

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io/fs"
	"math/big"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/csvdb"
)

// write writes content to a file called name in a new temporary directory
// and returns its path.
func write(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func open(t *testing.T, src string, opts csvdb.Options) *ip2geo.Reader {
	t.Helper()
	db, err := csvdb.Open(src, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func lookup(t *testing.T, db *ip2geo.Reader, ip string) (ip2geo.Details, bool) {
	t.Helper()
	return db.LookupDetails(netip.MustParseAddr(ip))
}

// dbipCity is a DB-IP City Lite CSV: a reserved row, quoted names with
// commas and quotes, an empty region, a gap, and IPv6 rows.
const dbipCity = `0.0.0.0,0.255.255.255,ZZ,ZZ,,,0,0
1.0.0.0,1.0.0.255,OC,AU,Queensland,"South Brisbane",-27.4767,153.017
1.0.1.0,1.0.3.255,AS,CN,Fujian,Wenquan,26.0998,119.297
1.0.4.0,1.0.7.255,OC,AU,Queensland,"South Brisbane",-27.4767,153.017
1.0.8.0,1.0.8.255,AS,SG,,"Singapore, ""Orchard""",1.29888,103.838
10.0.0.0,10.255.255.255,ZZ,ZZ,,,0,0
::,1fff:ffff:ffff:ffff:ffff:ffff:ffff:ffff,ZZ,ZZ,,,0,0
2001::,2001:0:ffff:ffff:ffff:ffff:ffff:ffff,NA,US,California,"Los Angeles",33.9829,-118.405
2001:200::,2001:200:ffff:ffff:ffff:ffff:ffff:ffff,AS,JP,Kanagawa,Fujisawa,35.3882,139.428
2a00::,2a00:ff:ffff:ffff:ffff:ffff:ffff:ffff,EU,DE,Hesse,Frankfurt,50.1109,8.68213
`

func TestDBIPCity(t *testing.T) {
	src := write(t, "dbip-city-lite-2026-10.csv", dbipCity)
	db := open(t, src, csvdb.Options{Format: csvdb.DBIPCity, Profile: ip2geo.ProfileCity, Fields: ip2geo.FieldsAll})

	md := db.Metadata()
	if md.SourceType != "DBIP-City-Lite (CSV)" || md.Language != "en" || !md.SourceTime.IsZero() {
		t.Errorf("metadata: source %q, language %q, source time %v", md.SourceType, md.Language, md.SourceTime)
	}
	if got := db.Fields(); got != ip2geo.FieldLocation {
		t.Errorf("Fields() = %#x, want FieldLocation (all that DB-IP City has)", got)
	}
	tests := []struct {
		ip                      string
		ok                      bool
		cont, iso, region, city string
		lat, lon                float64
	}{
		{"0.0.0.1", false, "", "", "", "", 0, 0}, // ZZ: reserved, no data
		{"10.1.2.3", false, "", "", "", "", 0, 0},
		{"1.0.0.0", true, "OC", "AU", "Queensland", "South Brisbane", -27.4767, 153.017},
		{"1.0.3.255", true, "AS", "CN", "Fujian", "Wenquan", 26.0998, 119.297},
		{"1.0.7.1", true, "OC", "AU", "Queensland", "South Brisbane", -27.4767, 153.017},
		{"1.0.8.7", true, "AS", "SG", "", `Singapore, "Orchard"`, 1.2989, 103.838},
		{"1.0.9.0", false, "", "", "", "", 0, 0}, // a gap
		{"::ffff:1.0.1.1", true, "AS", "CN", "Fujian", "Wenquan", 26.0998, 119.297},
		{"2001:200::1", true, "AS", "JP", "Kanagawa", "Fujisawa", 35.3882, 139.428},
		{"2a00:ff::1", true, "EU", "DE", "Hesse", "Frankfurt", 50.1109, 8.6821},
		{"2a00:100::", false, "", "", "", "", 0, 0},
		// Teredo is looked up as IPv4 (0.0.0.0 here, reserved), so its
		// row is not stored.
		{"2001::1", false, "", "", "", "", 0, 0},
	}
	for _, tc := range tests {
		d, ok := lookup(t, db, tc.ip)
		if ok != tc.ok || d.Continent != tc.cont || d.CountryISO != tc.iso || d.RegionName != tc.region ||
			d.City != tc.city || d.Latitude != tc.lat || d.Longitude != tc.lon || d.HasCoordinates != tc.ok {
			t.Errorf("%s: got %v %+v", tc.ip, ok, d)
		}
	}
	if r, _ := db.LookupRange(netip.MustParseAddr("1.0.5.0")); r.First != netip.MustParseAddr("1.0.4.0") {
		t.Errorf("LookupRange(1.0.5.0) = %v", r)
	}

	// Coarser profiles of the same file.
	region := open(t, src, csvdb.Options{Format: csvdb.DBIPCity, Profile: ip2geo.ProfileRegion})
	if d, ok := lookup(t, region, "1.0.1.1"); !ok || d.RegionName != "Fujian" || d.City != "" || d.Latitude != 0 {
		t.Errorf("region profile: %v %+v", ok, d)
	}
	country := open(t, src, csvdb.Options{Format: csvdb.DBIPCity})
	if d, ok := lookup(t, country, "1.0.1.1"); !ok || d.CountryISO != "CN" || d.Continent != "AS" || d.RegionName != "" {
		t.Errorf("country profile: %v %+v", ok, d)
	}
}

func TestDBIPCountry(t *testing.T) {
	src := write(t, "c.csv", "0.0.0.0,0.255.255.255,ZZ\n1.0.0.0,1.0.0.255,AU\n1.0.1.0,1.0.3.255,CN\n2a00::,2a00::ffff,DE\n")
	db := open(t, src, csvdb.Options{Format: csvdb.DBIPCountry, Fields: ip2geo.FieldsAll})
	if db.Fields() != 0 {
		t.Errorf("Fields() = %#x, want none", db.Fields())
	}
	for ip, want := range map[string]string{"1.0.0.9": "AU", "1.0.2.0": "CN", "2a00::5": "DE", "0.0.0.1": "", "1.0.4.0": ""} {
		d, ok := lookup(t, db, ip)
		if d.CountryISO != want || ok != (want != "") || d.Continent != "" || d.CountryName != "" {
			t.Errorf("%s: %v %+v, want %q", ip, ok, d, want)
		}
	}
	for _, p := range []ip2geo.Profile{ip2geo.ProfileRegion, ip2geo.ProfileCity, ip2geo.ProfileASN} {
		if _, err := csvdb.Open(src, csvdb.Options{Format: csvdb.DBIPCountry, Profile: p}); !errors.Is(err, csvdb.ErrDatabaseType) {
			t.Errorf("profile %s: err %v, want ErrDatabaseType", p, err)
		}
	}
}

func TestDBIPASN(t *testing.T) {
	src := write(t, "a.csv", `1.0.0.0,1.0.0.255,13335,"Cloudflare, Inc."
1.0.4.0,1.0.7.255,38803,"Gtelecom Pty Ltd"
2a00::,2a00::ffff,3320,"Deutsche Telekom AG"
`)
	db := open(t, src, csvdb.Options{Format: csvdb.DBIPASN, Profile: ip2geo.ProfileASN})
	if as, ok := db.ASN(netip.MustParseAddr("1.0.0.1")); !ok || as != (ip2geo.ASN{Number: 13335, Organization: "Cloudflare, Inc."}) {
		t.Errorf("1.0.0.1: %v %+v", ok, as)
	}
	if as, ok := db.ASN(netip.MustParseAddr("2a00::1")); !ok || as.Number != 3320 {
		t.Errorf("2a00::1: %v %+v", ok, as)
	}
	if _, ok := db.ASN(netip.MustParseAddr("1.0.1.0")); ok {
		t.Error("1.0.1.0 is in a gap")
	}
	if got := db.Metadata().SourceType; got != "DBIP-ASN-Lite (CSV)" {
		t.Errorf("source type %q", got)
	}
	if _, err := csvdb.Open(src, csvdb.Options{Format: csvdb.DBIPASN}); !errors.Is(err, csvdb.ErrDatabaseType) {
		t.Errorf("geo profile with an ASN layout: %v", err)
	}
	if _, err := csvdb.Open(src, csvdb.Options{Format: csvdb.DBIPCity, Profile: ip2geo.ProfileASN}); !errors.Is(err, csvdb.ErrDatabaseType) {
		t.Errorf("ProfileASN with a geo layout: %v", err)
	}
	if _, err := csvdb.Open(src, csvdb.Options{Format: csvdb.DBIPASN, Profile: ip2geo.ProfileASN, Fields: ip2geo.FieldPostal}); !errors.Is(err, csvdb.ErrOptions) {
		t.Errorf("Fields with ProfileASN: %v", err)
	}
}

// ip2lRow returns a quoted IP2Location row: the bounds and then the given
// columns of a DB11 row (country code, name, region, city, lat, lon, zip,
// time zone), cut to n columns in all.
func ip2lRow(from, to string, n int, cols ...string) string {
	all := append([]string{from, to}, cols...)
	return `"` + strings.Join(all[:n], `","`) + "\"\r\n"
}

// ip2lFile returns an IPv4 IP2Location file with n columns.
func ip2lFile(n int) string {
	return ip2lRow("0", "16777215", n, "-", "-", "-", "-", "0.000000", "0.000000", "-", "-") +
		ip2lRow("16777216", "16777471", n, "AU", "Australia", "Queensland", "Brisbane", "-27.467540", "153.028090", "4000", "+10:00") +
		ip2lRow("16777472", "16778239", n, "CN", "China", "Fujian", "Fuzhou", "26.061390", "119.306110", "350004", "+08:00") +
		ip2lRow("16778240", "16779263", n, "AU", "Australia", "Victoria", "Melbourne", "-37.814007", "144.963171", "3000", "+10:00") +
		ip2lRow("16779264", "16781311", n, "CN", "China", "-", "-", "34.773197", "113.722019", "-", "+08:00")
}

func TestIP2LocationLayouts(t *testing.T) {
	tests := []struct {
		db      string
		cols    int
		profile ip2geo.Profile
		fields  ip2geo.FieldSet // what FieldsAll gives
	}{
		{"DB1", 4, ip2geo.ProfileCountry, 0},
		{"DB3", 6, ip2geo.ProfileCity, 0},
		{"DB5", 8, ip2geo.ProfileCity, ip2geo.FieldLocation},
		{"DB9", 9, ip2geo.ProfileCity, ip2geo.FieldLocation | ip2geo.FieldPostal},
		{"DB11", 10, ip2geo.ProfileCity, ip2geo.FieldLocation | ip2geo.FieldPostal},
	}
	for _, tc := range tests {
		t.Run(tc.db, func(t *testing.T) {
			src := write(t, "IP2LOCATION-LITE-"+tc.db+".CSV", ip2lFile(tc.cols))
			opts := csvdb.Options{Format: csvdb.IP2Location, Profile: tc.profile, Fields: ip2geo.FieldsAll}
			db := open(t, src, opts)
			if got, want := db.Metadata().SourceType, "IP2Location-"+tc.db+" (CSV)"; got != want {
				t.Errorf("source type %q, want %q", got, want)
			}
			if db.Fields() != tc.fields {
				t.Errorf("FieldsAll gives %#x, want %#x", db.Fields(), tc.fields)
			}
			if _, ok := lookup(t, db, "0.1.2.3"); ok {
				t.Error(`0.1.2.3 ("-") is found`)
			}
			if _, ok := lookup(t, db, "1.0.32.0"); ok {
				t.Error("1.0.32.0 (after the last row) is found")
			}
			d, ok := lookup(t, db, "1.0.1.0")
			if !ok || d.CountryISO != "CN" || d.CountryName != "China" || d.TimeZone != "" {
				t.Fatalf("1.0.1.0: %v %+v", ok, d)
			}
			if tc.cols >= 6 && (d.RegionName != "Fujian" || d.City != "Fuzhou") {
				t.Errorf("1.0.1.0: region %q city %q", d.RegionName, d.City)
			}
			if tc.cols >= 8 && (d.Latitude != 26.0614 || d.Longitude != 119.3061 || !d.HasCoordinates) {
				t.Errorf("1.0.1.0: coordinates %v %v %v", d.Latitude, d.Longitude, d.HasCoordinates)
			}
			if tc.cols == 6 && (d.Latitude != 0 || d.HasCoordinates) {
				t.Errorf("DB3 has coordinates: %+v", d)
			}
			if tc.cols >= 9 && d.PostalCode != "350004" {
				t.Errorf("postal code %q", d.PostalCode)
			}
			if d, _ := lookup(t, db, "1.0.8.0"); tc.cols >= 6 && (d.RegionName != "" || d.City != "" || d.PostalCode != "") {
				t.Errorf(`"-" values: %+v`, d)
			}

			// What the layout cannot give.
			if tc.cols == 4 {
				_, err := csvdb.Open(src, csvdb.Options{Format: csvdb.IP2Location, Profile: ip2geo.ProfileCity})
				if !errors.Is(err, csvdb.ErrDatabaseType) {
					t.Errorf("profile city on DB1: %v", err)
				}
			}
			if tc.cols < 9 {
				_, err := csvdb.Open(src, csvdb.Options{Format: csvdb.IP2Location, Profile: ip2geo.ProfileCity,
					Fields: ip2geo.FieldPostal})
				if tc.cols > 4 && !errors.Is(err, csvdb.ErrOptions) || tc.cols == 4 && err == nil {
					t.Errorf("FieldPostal on %s: %v", tc.db, err)
				}
			}
		})
	}
}

// v6num returns the decimal number of address s.
func v6num(s string) string {
	a := netip.MustParseAddr(s).As16()
	return new(big.Int).SetBytes(a[:]).String()
}

func TestIP2LocationIPv6(t *testing.T) {
	row := func(from, to string, cols ...string) string {
		return ip2lRow(v6num(from), v6num(to), 2+len(cols), cols...)
	}
	content := row("::", "::fffe:ffff:ffff", "-", "-") +
		row("::ffff:0.0.0.0", "::ffff:0.255.255.255", "-", "-") +
		row("::ffff:1.0.0.0", "::ffff:1.0.0.255", "AU", "Australia") +
		// A row across the end of the IPv4-mapped block.
		row("::ffff:1.0.1.0", "::1:0:0:ff", "CN", "China") +
		row("2001::", "2001:0:ffff:ffff:ffff:ffff:ffff:ffff", "US", "United States of America") +
		row("2001:200::", "2001:200:ffff:ffff:ffff:ffff:ffff:ffff", "JP", "Japan") +
		row("2002::", "2002:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "US", "United States of America") +
		row("2a00::", "2a00::ffff", "DE", "Germany")
	for _, name := range []string{"IP2LOCATION-LITE-DB1.IPV6.CSV", "db1.csv"} {
		// The second name does not say IPv6: the first row, which ends above
		// 2^32, does.
		db := open(t, write(t, name, content), csvdb.Options{Format: csvdb.IP2Location})
		for ip, want := range map[string]string{
			"0.1.2.3": "", "1.0.0.1": "AU", "::ffff:1.0.0.1": "AU", "1.0.1.0": "CN", "255.255.255.255": "CN",
			"::1:0:0:ff": "CN", "::1:0:0:100": "", "2001:200::1": "JP", "2a00::1": "DE",
			// Teredo and 6to4 are looked up as IPv4: 2002:0100:0001:: is 1.0.0.1.
			"2002:100:1::": "AU", "2001::1": "",
		} {
			d, ok := lookup(t, db, ip)
			if d.CountryISO != want || ok != (want != "") {
				t.Errorf("%s %s: %v %q, want %q", name, ip, ok, d.CountryISO, want)
			}
		}
	}
}

func TestIP2LocationASN(t *testing.T) {
	src := write(t, "IP2LOCATION-LITE-ASN.CSV", `"ip_from","ip_to","cidr","asn","as"
"0","16777215","0.0.0.0/8","-","-"
"16777216","16777471","1.0.0.0/24","13335","CloudFlare Inc."
"16777472","16777727","1.0.1.0/24","-","-"
"16778240","16779263","1.0.4.0/22","38803","GTelecom Pty Ltd"
`)
	db := open(t, src, csvdb.Options{Format: csvdb.IP2LocationASN, Profile: ip2geo.ProfileASN})
	for ip, want := range map[string]uint32{"0.0.0.1": 0, "1.0.0.1": 13335, "1.0.1.1": 0, "1.0.5.5": 38803} {
		as, ok := db.ASN(netip.MustParseAddr(ip))
		if as.Number != want || ok != (want != 0) {
			t.Errorf("%s: %v %+v, want AS%d", ip, ok, as, want)
		}
	}
	if as, _ := db.ASN(netip.MustParseAddr("1.0.0.1")); as.Organization != "CloudFlare Inc." {
		t.Errorf("organization %q", as.Organization)
	}
	// An ASN file read as a geo one says what it is.
	_, err := csvdb.Open(src, csvdb.Options{Format: csvdb.IP2Location})
	if !errors.Is(err, csvdb.ErrSyntax) || !strings.Contains(err.Error(), "ip2location-asn") {
		t.Errorf("ASN file as IP2Location: %v", err)
	}
}

func TestSyntaxErrors(t *testing.T) {
	city := "1.0.0.0,1.0.0.255,OC,AU,Queensland,Brisbane,-27.4767,153.017\n"
	tests := []struct {
		name    string
		format  csvdb.Format
		content string
		line    int
		want    string
	}{
		{"bad address", csvdb.DBIPCity, city + "1.0.1.x,1.0.1.255,AS,CN,,,1,1\n", 2, `bad address "1.0.1.x"`},
		{"zone", csvdb.DBIPCity, city + "fe80::1%eth0,fe80::2,AS,CN,,,1,1\n", 2, "bad address"},
		{"start above end", csvdb.DBIPCity, city + "1.0.2.0,1.0.1.255,AS,CN,,,1,1\n", 2, "first address above the last"},
		{"families", csvdb.DBIPCity, city + "1.0.2.0,2a00::,AS,CN,,,1,1\n", 2, "different families"},
		{"overlap", csvdb.DBIPCity, city + "1.0.0.128,1.0.1.255,AS,CN,,,1,1\n", 2, "does not start above"},
		{"out of order", csvdb.DBIPCity, city + "0.0.1.0,0.0.1.255,AS,CN,,,1,1\n", 2, "does not start above"},
		{"duplicate", csvdb.DBIPCity, city + city, 2, "does not start above"},
		{"mapped overlap", csvdb.DBIPCity, city + "::ffff:1.0.0.5,::ffff:1.0.0.6,AS,CN,,,1,1\n", 2, "overlaps an earlier range"},
		{"columns", csvdb.DBIPCity, city + "1.0.1.0,1.0.1.255,AS,CN,,,1\n", 2, "7 fields, want 8"},
		{"latitude", csvdb.DBIPCity, city + "1.0.1.0,1.0.1.255,AS,CN,,,1.2.3,1\n", 2, `bad latitude "1.2.3"`},
		{"latitude range", csvdb.DBIPCity, city + "1.0.1.0,1.0.1.255,AS,CN,,,91,1\n", 2, "bad latitude"},
		{"longitude", csvdb.DBIPCity, city + "1.0.1.0,1.0.1.255,AS,CN,,,1,NaN\n", 2, "bad longitude"},
		{"half location", csvdb.DBIPCity, city + "1.0.1.0,1.0.1.255,AS,CN,,,1,\n", 2, "latitude without a longitude"},
		{"quote", csvdb.DBIPCity, city + "1.0.1.0,1.0.1.255,AS,CN,,\"Fuzhou,1,1\n", 2, "unterminated quote"},
		{"text after quote", csvdb.DBIPCity, city + "1.0.1.0,1.0.1.255,AS,CN,,\"Fu\"zhou,1,1\n", 2, "after the closing quote"},
		{"bare quote", csvdb.DBIPCity, city + "1.0.1.0,1.0.1.255,AS,CN,,Fu\"zhou,1,1\n", 2, "quote in an unquoted field"},
		{"blank lines count", csvdb.DBIPCity, city + "\n\n1.0.1.0,1.0.1.255,AS,CN,,,1\n", 4, "7 fields"},
		{"AS number", csvdb.DBIPASN, "1.0.0.0,1.0.0.255,AS13335,Cloudflare\n", 1, `bad AS number "AS13335"`},
		{"AS number range", csvdb.DBIPASN, "1.0.0.0,1.0.0.255,4294967296,Cloudflare\n", 1, "bad AS number"},
		{"empty", csvdb.DBIPCountry, "", 0, "no rows"},
		{"header only", csvdb.DBIPCountry, "ip_start,ip_end,country\n", 0, "no rows"},
		{"ip2l number", csvdb.IP2Location, `"16777216","1677x","AU","Australia"` + "\n", 1, `bad address number "1677x"`},
		{"ip2l v4 above 2^32", csvdb.IP2Location,
			`"16777216","16777471","AU","Australia"` + "\n" + `"16777472","4294967296","CN","China"` + "\n", 2, "above 4294967295"},
		{"ip2l above 2^128", csvdb.IP2Location,
			`"0","340282366920938463463374607431768211456","-","-"` + "\n", 1, "bad address number"},
		{"ip2l columns", csvdb.IP2Location, `"0","1","AU","Australia","x","y","z"` + "\n", 1, "7 fields, want 4, 6, 8, 9 or 10"},
		{"ip2l row columns", csvdb.IP2Location,
			`"0","1","AU","Australia"` + "\n" + `"2","3","AU","Australia","Queensland","Brisbane"` + "\n", 2, "6 fields, want 4"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := write(t, "in.csv", tc.content)
			opts := csvdb.Options{Format: tc.format, Profile: ip2geo.ProfileCity}
			if tc.format == csvdb.DBIPASN {
				opts.Profile = ip2geo.ProfileASN
			}
			if tc.format == csvdb.DBIPCountry || tc.format == csvdb.IP2Location {
				opts.Profile = ip2geo.ProfileCountry
			}
			_, err := csvdb.Open(src, opts)
			if !errors.Is(err, csvdb.ErrSyntax) {
				t.Fatalf("err = %v, want ErrSyntax", err)
			}
			where := src + ":"
			if tc.line > 0 {
				where += string(rune('0'+tc.line)) + ":"
			}
			if msg := err.Error(); !strings.HasPrefix(msg, "csvdb: "+where) || !strings.Contains(msg, tc.want) {
				t.Errorf("err = %q, want %q and %q", msg, where, tc.want)
			}
		})
	}
}

// TestUnreadColumns: the columns a build does not read are not checked
// (apart from their count), and rows that differ only in them share a
// record.
func TestUnreadColumns(t *testing.T) {
	src := write(t, "c.csv", "1.0.0.0,1.0.0.255,OC,AU,Queensland,Brisbane,x,y\n1.0.1.0,1.0.1.255,OC,AU,Victoria,Melbourne,1,2\n")
	st, err := csvdb.Build(src, filepath.Join(t.TempDir(), "out.geoc"), csvdb.Options{Format: csvdb.DBIPCity})
	if err != nil {
		t.Fatal(err)
	}
	if st.DistinctRecords != 1 || st.Records != 2 || st.Networks != 2 || st.V4Ranges != 2 {
		t.Errorf("stats %+v: want one source record and one merged range", st)
	}
}

func TestGzip(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte(dbipCity))
	zw.Close()
	plain := write(t, "dbip-city-lite.csv", dbipCity)
	gz := write(t, "dbip-city-lite.csv.gz", buf.String())
	opts := csvdb.Options{Format: csvdb.DBIPCity, Profile: ip2geo.ProfileCity, BuildTime: time.Unix(1e9, 0)}
	out := t.TempDir()
	if _, err := csvdb.Build(plain, filepath.Join(out, "a.geoc"), opts); err != nil {
		t.Fatal(err)
	}
	if _, err := csvdb.Build(gz, filepath.Join(out, "b.geoc"), opts); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(filepath.Join(out, "a.geoc"))
	b, _ := os.ReadFile(filepath.Join(out, "b.geoc"))
	if !bytes.Equal(a, b) {
		t.Error("the gzipped source builds a different file")
	}

	// A truncated gzip stream is a read error, not a syntax error.
	bad := write(t, "bad.csv.gz", buf.String()[:buf.Len()/2])
	if _, err := csvdb.Open(bad, opts); err == nil || errors.Is(err, csvdb.ErrSyntax) {
		t.Errorf("truncated gzip: %v", err)
	}
	notGz := write(t, "plain.csv.gz", dbipCity)
	if _, err := csvdb.Open(notGz, opts); !errors.Is(err, gzip.ErrHeader) {
		t.Errorf("not gzip: %v, want gzip.ErrHeader", err)
	}
}

func TestOptions(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.csv")
	tests := []struct {
		name string
		opts csvdb.Options
	}{
		{"no format", csvdb.Options{}},
		{"unknown format", csvdb.Options{Format: 99}},
		{"profile", csvdb.Options{Format: csvdb.DBIPCity, Profile: 9}},
		{"latscale", csvdb.Options{Format: csvdb.DBIPCity, Profile: ip2geo.ProfileCity, LatScale: 8}},
		{"build time", csvdb.Options{Format: csvdb.DBIPCity, BuildTime: time.Unix(-1, 0)}},
		{"description", csvdb.Options{Format: csvdb.DBIPCity, Description: "\xff"}},
		{"fields for profile", csvdb.Options{Format: csvdb.DBIPCity, Fields: ip2geo.FieldLocation}},
		{"fields for layout", csvdb.Options{Format: csvdb.DBIPCity, Profile: ip2geo.ProfileCity, Fields: ip2geo.FieldEU}},
		{"postal from DB-IP", csvdb.Options{Format: csvdb.DBIPCity, Profile: ip2geo.ProfileCity, Fields: ip2geo.FieldPostal}},
		{"ip2location metro", csvdb.Options{Format: csvdb.IP2Location, Profile: ip2geo.ProfileCity, Fields: ip2geo.FieldMetro}},
	}
	for _, tc := range tests {
		// Options are checked before the source is opened: the missing
		// file is not reported.
		if _, err := csvdb.Open(missing, tc.opts); !errors.Is(err, csvdb.ErrOptions) {
			t.Errorf("%s: err = %v, want ErrOptions", tc.name, err)
		}
	}
	if _, err := csvdb.Open(missing, csvdb.Options{Format: csvdb.DBIPCity}); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing source: %v", err)
	}

	for _, f := range []csvdb.Format{csvdb.DBIPCity, csvdb.DBIPCountry, csvdb.DBIPASN, csvdb.IP2Location, csvdb.IP2LocationASN} {
		if g, err := csvdb.ParseFormat(f.String()); g != f || err != nil {
			t.Errorf("ParseFormat(%q) = %v, %v", f, g, err)
		}
	}
	if _, err := csvdb.ParseFormat("mmdb"); !errors.Is(err, csvdb.ErrOptions) {
		t.Errorf("ParseFormat(mmdb): %v", err)
	}
	if s := csvdb.Format(0).String(); s != "Format(0)" {
		t.Errorf("Format(0).String() = %q", s)
	}
}

func TestBuild(t *testing.T) {
	src := write(t, "dbip-city-lite.csv", dbipCity)
	out := filepath.Join(t.TempDir(), "city.geoc")
	opts := csvdb.Options{Format: csvdb.DBIPCity, Profile: ip2geo.ProfileCity, BuildTime: time.Unix(1e9, 0),
		Description: "test"}
	st, err := csvdb.Build(src, out, opts)
	if err != nil {
		t.Fatal(err)
	}
	// 7 rows with data, one of them Teredo, which is not stored, and 5
	// distinct records, plus the gap record.
	if st.Profile != ip2geo.ProfileCity || st.Networks != 6 || st.DistinctRecords != 5 || st.Records != 6 {
		t.Errorf("stats %+v", st)
	}
	first, _ := os.ReadFile(out)
	if fi, _ := os.Stat(out); fi.Size() != st.Bytes {
		t.Errorf("Bytes %d, file %d", st.Bytes, fi.Size())
	}
	db, err := ip2geo.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	if md := db.Metadata(); md.Description != "test" || !md.BuildTime.Equal(time.Unix(1e9, 0)) {
		t.Errorf("metadata %+v", md)
	}
	db.Close()

	// Reproducible, and a failed build leaves the output alone.
	if _, err := csvdb.Build(src, out, opts); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(out); !bytes.Equal(first, again) {
		t.Error("two builds with the same BuildTime differ")
	}
	bad := write(t, "bad.csv", "1.0.0.0,1.0.0.255,OC\n")
	if _, err := csvdb.Build(bad, out, opts); !errors.Is(err, csvdb.ErrSyntax) {
		t.Fatalf("err = %v", err)
	}
	if after, _ := os.ReadFile(out); !bytes.Equal(first, after) {
		t.Error("a failed build changed the output")
	}
	if entries, _ := os.ReadDir(filepath.Dir(out)); len(entries) != 1 {
		t.Errorf("output directory holds %d files", len(entries))
	}
}

// TestLongLine reads a line longer than the reader's buffer, whose city
// name is too long to store.
func TestLongLine(t *testing.T) {
	long := strings.Repeat("x", 3<<20)
	src := write(t, "c.csv", "1.0.0.0,1.0.0.255,OC,AU,Queensland,Brisbane,1,2\n"+
		"1.0.1.0,1.0.1.255,OC,AU,Queensland,\""+long+"\",1,2\n")
	_, err := csvdb.Open(src, csvdb.Options{Format: csvdb.DBIPCity, Profile: ip2geo.ProfileCity})
	if !errors.Is(err, csvdb.ErrStringTooLong) {
		t.Errorf("err = %.200v, want ErrStringTooLong", err)
	}
	// The same line is fine for the country profile, which does not read
	// the city.
	if _, err := csvdb.Open(src, csvdb.Options{Format: csvdb.DBIPCity}); err != nil {
		t.Error(err)
	}
}
