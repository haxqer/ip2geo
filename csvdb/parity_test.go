package csvdb_test

import (
	"compress/gzip"
	"encoding/binary"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/csvdb"
	"github.com/haxqer/ip2geo/mmdb"
	"github.com/oschwald/maxminddb-golang/v2"
)

// TestDBIPParity builds each DB-IP Lite database of the directory
// IP2GEO_TEST_DBIP_DIR from its CSV and from its .mmdb (same month, e.g.
// dbip-city-lite-2026-10.csv and dbip-city-lite-2026-10.mmdb; the CSV may be
// gzipped) and compares lookups of 200,000 random addresses and of the
// boundaries of about 20,000 sampled rows on every field both carry.
func TestDBIPParity(t *testing.T) {
	dir := os.Getenv("IP2GEO_TEST_DBIP_DIR")
	if dir == "" {
		t.Skip("IP2GEO_TEST_DBIP_DIR is not set")
	}
	kinds := []struct {
		name    string
		format  csvdb.Format
		profile ip2geo.Profile
	}{
		{"country", csvdb.DBIPCountry, ip2geo.ProfileCountry},
		{"asn", csvdb.DBIPASN, ip2geo.ProfileASN},
		{"city", csvdb.DBIPCity, ip2geo.ProfileCity},
	}
	found := false
	for _, k := range kinds {
		mmdbs, _ := filepath.Glob(filepath.Join(dir, "dbip-"+k.name+"-lite-*.mmdb"))
		for _, m := range mmdbs {
			stem := strings.TrimSuffix(m, ".mmdb")
			src := ""
			for _, c := range []string{stem + ".csv", stem + ".csv.gz"} {
				if _, err := os.Stat(c); err == nil {
					src = c
					break
				}
			}
			if src == "" {
				continue
			}
			found = true
			t.Run(filepath.Base(stem), func(t *testing.T) {
				compareDBIP(t, src, m, k.format, k.profile)
			})
		}
	}
	if !found {
		t.Fatalf("no dbip-*-lite-*.mmdb with a matching .csv or .csv.gz in %s", dir)
	}
}

func compareDBIP(t *testing.T, csvPath, mmdbPath string, f csvdb.Format, p ip2geo.Profile) {
	fromCSV, err := csvdb.Open(csvPath, csvdb.Options{Format: f, Profile: p})
	if err != nil {
		t.Fatal(err)
	}
	defer fromCSV.Close()
	fromMMDB, err := mmdb.Open(mmdbPath, mmdb.Options{Profile: p})
	if err != nil {
		t.Fatal(err)
	}
	defer fromMMDB.Close()
	t.Logf("csv:  %+v", fromCSV.Metadata())
	t.Logf("mmdb: %+v", fromMMDB.Metadata())

	rng := rand.New(rand.NewPCG(1, 2))
	bounds, inside := sampleRows(t, csvPath, rng)
	var addrs []netip.Addr
	for range 100_000 {
		addrs = append(addrs, netip.AddrFrom4(u32(rng.Uint32())))
	}
	for range 50_000 { // anywhere in 2000::/3
		var b [16]byte
		binary.BigEndian.PutUint64(b[:8], 0x2000<<48|rng.Uint64()>>3)
		binary.BigEndian.PutUint64(b[8:], rng.Uint64())
		addrs = append(addrs, netip.AddrFrom16(b))
	}
	for range 50_000 { // inside the sampled IPv6 rows
		addrs = append(addrs, inside[rng.IntN(len(inside))](rng))
	}
	// Addresses where the two sources are known to differ (see explain).
	for _, a := range []string{"192.0.0.0", "192.0.0.63", "192.0.0.64", "192.0.0.200", "192.0.0.255",
		"fec0::", "feff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "ff00::", "ff02::1",
		"ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff"} {
		addrs = append(addrs, netip.MustParseAddr(a))
	}
	nRandom := len(addrs)
	addrs = append(addrs, bounds...)

	raw, err := maxminddb.Open(mmdbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	unexplained := map[string]int{}
	explained := map[string]int{}
	examples := 0
	for _, ip := range addrs {
		diffs := compareAt(fromCSV, fromMMDB, ip, p)
		if len(diffs) > 0 && diffs[0].field == "found" {
			// Every field differs when only one side finds ip: count the
			// address once.
			if why := explain(raw, ip, diffs[0]); why != "" {
				explained[why]++
				continue
			}
		}
		for _, d := range diffs {
			if why := explain(raw, ip, d); why != "" {
				explained[why]++
				continue
			}
			unexplained[d.field]++
			if examples < 20 {
				examples++
				t.Logf("%s %s: csv %q, mmdb %q", ip, d.field, d.csv, d.mmdb)
			}
		}
	}
	t.Logf("compared %d addresses (%d random or chosen, %d row boundaries +-1)", len(addrs), nRandom, len(bounds))
	for why, n := range explained {
		t.Logf("explained source difference, %d mismatches: %s", n, why)
	}
	if len(unexplained) > 0 {
		t.Errorf("unexplained mismatches by field: %v", unexplained)
	}
	walk(t, fromCSV, fromMMDB, raw, p)
}

// walk compares a (from the CSV) and b (from the .mmdb) over the whole
// address space: it steps through the segments in which neither database's
// answer changes, which LookupRange gives, and logs how many segments and
// addresses differ, by explanation.
func walk(t *testing.T, a, b *ip2geo.Reader, raw *maxminddb.Reader, p ip2geo.Profile) {
	type tally struct {
		segs int
		v4   uint64
		v6   float64
	}
	tallies := map[string]*tally{}
	segs := 0
	visit := func(first netip.Addr) netip.Addr {
		segs++
		ra, _ := a.LookupRange(first)
		rb, _ := b.LookupRange(first)
		last := ra.Last
		if rb.Last.Less(last) {
			last = rb.Last
		}
		diffs := compareAt(a, b, first, p)
		if len(diffs) == 0 {
			return last
		}
		why := ""
		for _, d := range diffs {
			if why = explain(raw, first, d); why == "" {
				why = "UNEXPLAINED " + d.field
				break
			}
			if d.field == "found" {
				break
			}
		}
		tl := tallies[why]
		if tl == nil {
			tl = &tally{}
			tallies[why] = tl
		}
		tl.segs++
		if first.Is4() {
			f, l := first.As4(), last.As4()
			tl.v4 += uint64(binary.BigEndian.Uint32(l[:])-binary.BigEndian.Uint32(f[:])) + 1
		} else {
			f, l := first.As16(), last.As16()
			tl.v6 += float64(binary.BigEndian.Uint64(l[:8])-binary.BigEndian.Uint64(f[:8]))*0x1p64 +
				float64(binary.BigEndian.Uint64(l[8:])) - float64(binary.BigEndian.Uint64(f[8:])) + 1
		}
		if why[0] == 'U' && tl.segs <= 5 {
			t.Logf("segment %s-%s: %v", first, last, diffs)
		}
		return last
	}
	for ip := netip.AddrFrom4([4]byte{}); ; {
		last := visit(ip)
		if last == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
			break
		}
		ip = last.Next()
	}
	// IPv6, apart from the blocks Lookup maps to IPv4.
	aliases := []netip.Prefix{netip.MustParsePrefix("::/96"), netip.MustParsePrefix("::ffff:0:0/96"),
		netip.MustParsePrefix("2001::/32"), netip.MustParsePrefix("2002::/16")}
	for ip := netip.IPv6Unspecified(); ip.IsValid(); {
		skipped := false
		for _, pfx := range aliases {
			if pfx.Contains(ip) {
				ip = lastOf(pfx).Next()
				skipped = true
			}
		}
		if !skipped {
			ip = visit(ip).Next()
		}
	}
	t.Logf("walked %d segments", segs)
	for why, tl := range tallies {
		msg := fmt.Sprintf("whole space, %s: %d segments, %d IPv4 and %.4g IPv6 addresses", why, tl.segs, tl.v4, tl.v6)
		if why[0] == 'U' {
			t.Error(msg)
		} else {
			t.Log(msg)
		}
	}
}

// lastOf returns the last address of pfx.
func lastOf(pfx netip.Prefix) netip.Addr {
	b := pfx.Addr().As16()
	for i := pfx.Bits(); i < 128; i++ {
		b[i/8] |= 0x80 >> (i % 8)
	}
	return netip.AddrFrom16(b)
}

// explain returns why the lookups of ip differ in d when the difference is
// one of the known differences between DB-IP's CSV and .mmdb files, and ""
// otherwise. raw is the .mmdb.
func explain(raw *maxminddb.Reader, ip netip.Addr, d diff) string {
	var rec struct {
		Country struct {
			ISO string `maxminddb:"iso_code"`
		} `maxminddb:"country"`
		Location struct {
			Lat float64 `maxminddb:"latitude"`
			Lon float64 `maxminddb:"longitude"`
		} `maxminddb:"location"`
	}
	res := raw.Lookup(ip)
	if res.Found() {
		if err := res.Decode(&rec); err != nil {
			return ""
		}
	}
	switch {
	case d.field == "found" && !res.Found() && ip.IsMulticast():
		// fec0::-ffff:...:ffff, the last row of the CSV, also covers the
		// multicast block, which the .mmdb leaves out.
		return "ff00::/8 (multicast) has data in the CSV but not in the .mmdb"
	case d.field == "found" && !res.Found() && ietf.Contains(ip):
		// The CSV has rows for the IETF protocol assignments block (Teredo
		// 2001::/32, which Lookup maps to IPv4, aside); the .mmdb leaves
		// the block out.
		return "2001::/23 (IETF protocol assignments) has data in the CSV but not in the .mmdb"
	case d.field == "found" && rec.Country.ISO == "ZZ":
		// The CSV marks all of 192.0.0.0/24 reserved ("ZZ"); the .mmdb
		// has records with country ZZ for most of it.
		return "the .mmdb has a ZZ record where the CSV has a reserved range"
	case d.field == "latitude" && halfway(rec.Location.Lat), d.field == "longitude" && halfway(rec.Location.Lon):
		return "a coordinate half-way between two LatScale steps whose .mmdb double is not the CSV text's nearest double"
	}
	return ""
}

var ietf = netip.MustParsePrefix("2001::/23")

// halfway reports whether x is, up to float error, a decimal whose fifth
// decimal is its last and is 5: it rounds up or down at LatScale 4
// depending on the last bit of its float64.
func halfway(x float64) bool {
	v := math.Abs(x) * 1e5
	r := math.Round(v)
	return math.Abs(v-r) < 1e-6 && int64(r)%10 == 5
}

type diff struct{ field, csv, mmdb string }

// compareAt compares the lookups of ip on the fields a DB-IP CSV carries.
func compareAt(a, b *ip2geo.Reader, ip netip.Addr, p ip2geo.Profile) []diff {
	var ds []diff
	cmp := func(field string, x, y any) {
		if x != y {
			ds = append(ds, diff{field, fmt.Sprint(x), fmt.Sprint(y)})
		}
	}
	if p == ip2geo.ProfileASN {
		x, okx := a.ASN(ip)
		y, oky := b.ASN(ip)
		cmp("found", okx, oky)
		cmp("as_number", x.Number, y.Number)
		cmp("as_organization", x.Organization, y.Organization)
		return ds
	}
	x, okx := a.Lookup(ip)
	y, oky := b.Lookup(ip)
	cmp("found", okx, oky)
	cmp("country_iso", x.CountryISO, y.CountryISO)
	if p == ip2geo.ProfileCity { // the country CSV has no continent
		cmp("continent", x.Continent, y.Continent)
		cmp("region_name", x.RegionName, y.RegionName)
		cmp("city", x.City, y.City)
		cmp("latitude", x.Latitude, y.Latitude)
		cmp("longitude", x.Longitude, y.Longitude)
	}
	return ds
}

func u32(v uint32) [4]byte { return [4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)} }

// sampleRows reads the CSV with encoding/csv, independently of csvdb, and
// returns the first and last address of about 20,000 random rows, each
// with its neighbours, and for the sampled IPv6 rows a function that
// returns a random address inside the row.
func sampleRows(t *testing.T, path string, rng *rand.Rand) ([]netip.Addr, []func(*rand.Rand) netip.Addr) {
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var in io.Reader = file
	size := int64(0)
	if fi, err := file.Stat(); err == nil {
		size = fi.Size()
	}
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(file)
		if err != nil {
			t.Fatal(err)
		}
		in = gz
		size *= 4
	}
	// About 60 bytes a row.
	rate := 20_000 / (float64(size)/60 + 1)
	r := csv.NewReader(in)
	r.FieldsPerRecord = -1
	r.ReuseRecord = true
	var bounds []netip.Addr
	var inside []func(*rand.Rand) netip.Addr
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if rng.Float64() >= rate {
			continue
		}
		first, last := netip.MustParseAddr(rec[0]), netip.MustParseAddr(rec[1])
		for _, a := range []netip.Addr{first.Prev(), first, last, last.Next()} {
			if a.IsValid() {
				bounds = append(bounds, a)
			}
		}
		if first.Is6() {
			lo, hi := first.As16(), last.As16()
			inside = append(inside, func(rng *rand.Rand) netip.Addr {
				// A random address between the two, from their 64-bit halves.
				l0, h0 := binary.BigEndian.Uint64(lo[:8]), binary.BigEndian.Uint64(hi[:8])
				var b [16]byte
				top := l0
				if h0-l0 == ^uint64(0) {
					top = rng.Uint64()
				} else {
					top += rng.Uint64N(h0 - l0 + 1)
				}
				binary.BigEndian.PutUint64(b[:8], top)
				lowLo, lowHi := uint64(0), ^uint64(0)
				if top == l0 {
					lowLo = binary.BigEndian.Uint64(lo[8:])
				}
				if top == h0 {
					lowHi = binary.BigEndian.Uint64(hi[8:])
				}
				low := lowLo
				if lowHi > lowLo {
					low += rng.Uint64N(lowHi - lowLo)
				}
				binary.BigEndian.PutUint64(b[8:], low)
				return netip.AddrFrom16(b)
			})
		}
	}
	if len(inside) == 0 {
		t.Fatalf("%s: no IPv6 rows sampled", path)
	}
	return bounds, inside
}
