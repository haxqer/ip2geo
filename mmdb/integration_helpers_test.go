package mmdb_test

// Helpers shared by the integration tests (integration_test.go) and the
// lookup benchmarks (bench_test.go). They read a real City .mmdb named by
// IP2GEO_TEST_MMDB and assume nothing about its contents, so they work the
// same on MaxMind GeoIP2/GeoLite2-City and on DB-IP City Lite.

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"net/netip"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/mmdb"
	maxminddb "github.com/oschwald/maxminddb-golang/v2"
)

// envMMDB names the environment variable that points at the source .mmdb.
// When it is unset every integration test and benchmark skips.
const envMMDB = "IP2GEO_TEST_MMDB"

// maxReported is how many mismatches a comparison prints before it only
// counts them.
const maxReported = 10

var (
	allIPv4    = netip.MustParsePrefix("0.0.0.0/0")
	globalIPv6 = netip.MustParsePrefix("2000::/3")
)

// sourceMMDB returns the source .mmdb path, or skips tb when none is set.
func sourceMMDB(tb testing.TB) string {
	tb.Helper()
	src := os.Getenv(envMMDB)
	if src == "" {
		tb.Skipf("set %s to a City .mmdb (MaxMind GeoIP2/GeoLite2-City or DB-IP City Lite) to run", envMMDB)
	}
	return src
}

// openMaxMind opens the source with maxminddb, closed when tb ends.
func openMaxMind(tb testing.TB, src string) *maxminddb.Reader {
	tb.Helper()
	mm, err := maxminddb.Open(src)
	if err != nil {
		tb.Fatalf("maxminddb.Open(%q): %v", src, err)
	}
	tb.Cleanup(func() { mm.Close() })
	return mm
}

// sourceInfo is what one pass over the source's Networks() yields.
type sourceInfo struct {
	lang     string         // language the builds use for names
	networks int            // networks with data, as Networks() yields them
	v6       []netip.Prefix // every IPv6 network with data, in address order
	regOnly  []netip.Prefix // networks with registered_country but no country
}

var (
	scanOnce sync.Once
	scanned  *sourceInfo
	errScan  error
)

// scanSource walks the source's networks once per process and caches the
// result for every test and benchmark.
func scanSource(tb testing.TB, src string) *sourceInfo {
	tb.Helper()
	scanOnce.Do(func() {
		start := time.Now()
		scanned, errScan = scan(src)
		if errScan == nil {
			tb.Logf("scanned %s in %v: %d networks, %d IPv6, %d registered_country-only, names in %q",
				src, time.Since(start).Round(time.Millisecond), scanned.networks, len(scanned.v6),
				len(scanned.regOnly), scanned.lang)
		}
	})
	if errScan != nil {
		tb.Fatalf("scan %s: %v", src, errScan)
	}
	return scanned
}

func scan(src string) (*sourceInfo, error) {
	mm, err := maxminddb.Open(src)
	if err != nil {
		return nil, err
	}
	defer mm.Close()

	// The builder's default language when the source has it (or lists
	// none), otherwise the first one the source lists.
	info := &sourceInfo{lang: "en"}
	if langs := mm.Metadata.Languages; len(langs) > 0 && !slices.Contains(langs, "en") {
		info.lang = langs[0]
	}

	type countries struct {
		Country struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"country"`
		RegisteredCountry struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"registered_country"`
	}
	// Many networks share a data record, so decode each record once.
	regOnly := map[uintptr]bool{}
	for res := range mm.Networks() {
		if err := res.Err(); err != nil {
			return nil, err
		}
		info.networks++
		p := res.Prefix()
		if !p.Addr().Is4() {
			info.v6 = append(info.v6, p)
		}
		ro, seen := regOnly[res.Offset()]
		if !seen {
			var c countries
			if err := res.Decode(&c); err != nil {
				return nil, fmt.Errorf("decode %s: %w", p, err)
			}
			ro = c.Country.ISOCode == "" && c.RegisteredCountry.ISOCode != ""
			regOnly[res.Offset()] = ro
		}
		if ro {
			info.regOnly = append(info.regOnly, p)
		}
	}
	return info, nil
}

// inMemory caches readers built with mmdb.Open, one per profile, for the
// life of the test process: each build scans the whole source.
var inMemory struct {
	sync.Mutex
	readers map[ip2geo.Profile]*ip2geo.Reader
}

// inMemoryReader returns a reader for profile p built in memory with
// mmdb.Open from src, building it on first use.
func inMemoryReader(tb testing.TB, src string, info *sourceInfo, p ip2geo.Profile) *ip2geo.Reader {
	tb.Helper()
	inMemory.Lock()
	defer inMemory.Unlock()
	if db, ok := inMemory.readers[p]; ok {
		return db
	}
	start := time.Now()
	db, err := mmdb.Open(src, mmdb.Options{Profile: p, Lang: info.lang})
	if err != nil {
		tb.Fatalf("mmdb.Open(%q, %s): %v", src, p, err)
	}
	tb.Logf("mmdb.Open %s profile in %v", p, time.Since(start).Round(time.Millisecond))
	if inMemory.readers == nil {
		inMemory.readers = map[ip2geo.Profile]*ip2geo.Reader{}
	}
	inMemory.readers[p] = db
	return db
}

// randomIn returns a uniformly random address inside p.
func randomIn(r *rand.Rand, p netip.Prefix) netip.Addr {
	p = p.Masked()
	if p.Addr().Is4() {
		a := p.Addr().As4()
		host := uint32(uint64(1)<<(32-p.Bits()) - 1)
		binary.BigEndian.PutUint32(a[:], binary.BigEndian.Uint32(a[:])|r.Uint32()&host)
		return netip.AddrFrom4(a)
	}
	a := p.Addr().As16()
	hiHost, loHost := uint64(math.MaxUint64), uint64(math.MaxUint64)
	if bits := p.Bits(); bits < 64 {
		hiHost >>= bits
	} else {
		hiHost, loHost = 0, loHost>>(bits-64)
	}
	binary.BigEndian.PutUint64(a[:8], binary.BigEndian.Uint64(a[:8])|r.Uint64()&hiHost)
	binary.BigEndian.PutUint64(a[8:], binary.BigEndian.Uint64(a[8:])|r.Uint64()&loHost)
	return netip.AddrFrom16(a)
}

// randomAddrs returns n uniformly random addresses inside p.
func randomAddrs(r *rand.Rand, p netip.Prefix, n int) []netip.Addr {
	addrs := make([]netip.Addr, n)
	for i := range addrs {
		addrs[i] = randomIn(r, p)
	}
	return addrs
}

// everyKth keeps every k-th prefix, k chosen so that at most n remain.
func everyKth(ps []netip.Prefix, n int) []netip.Prefix {
	k := max(1, (len(ps)+n-1)/n)
	out := make([]netip.Prefix, 0, (len(ps)+k-1)/k)
	for i := 0; i < len(ps); i += k {
		out = append(out, ps[i])
	}
	return out
}

// oneInEach returns one random address inside each prefix.
func oneInEach(r *rand.Rand, ps []netip.Prefix) []netip.Addr {
	addrs := make([]netip.Addr, len(ps))
	for i, p := range ps {
		addrs[i] = randomIn(r, p)
	}
	return addrs
}

// addrSet is a named list of addresses to compare.
type addrSet struct {
	name  string
	addrs []netip.Addr
}

// sourceRecord holds the fields of a City record that the builder keeps.
type sourceRecord struct {
	Continent struct {
		Code string `maxminddb:"code"`
	} `maxminddb:"continent"`
	Country struct {
		ISOCode   string            `maxminddb:"iso_code"`
		GeoNameID uint32            `maxminddb:"geoname_id"`
		Names     map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
	Subdivisions []struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
	City struct {
		GeoNameID uint32            `maxminddb:"geoname_id"`
		Names     map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Location struct {
		Latitude       *float64 `maxminddb:"latitude"`
		Longitude      *float64 `maxminddb:"longitude"`
		AccuracyRadius uint16   `maxminddb:"accuracy_radius"`
		TimeZone       string   `maxminddb:"time_zone"`
	} `maxminddb:"location"`
}

// fields is what the comparison checks for one address. Coordinates are in
// stored units, round(x * 10^LatScale), so both sides are rounded alike.
type fields struct {
	CountryISO       string
	CountryName      string
	Continent        string
	CountryGeoNameID uint32
	RegionISO        string
	RegionName       string
	City             string
	CityGeoNameID    uint32
	Lat, Lon         int32
	AccuracyRadius   uint16
	TimeZone         string
}

// coordScale is 10^LatScale for the builds here, which use the default.
var coordScale = math.Pow10(mmdb.DefaultLatScale)

func scaled(x float64) int32 { return int32(math.Round(x * coordScale)) }

// expected maps a source record to the fields a profile-p reader returns,
// exactly as the builder does: names in lang, the first subdivision only,
// absent coordinates as 0. Fields outside the profile are zero.
func expected(s *sourceRecord, p ip2geo.Profile, lang string) fields {
	f := fields{
		CountryISO:       s.Country.ISOCode,
		CountryName:      s.Country.Names[lang],
		Continent:        s.Continent.Code,
		CountryGeoNameID: s.Country.GeoNameID,
	}
	if p >= ip2geo.ProfileRegion && len(s.Subdivisions) > 0 {
		f.RegionISO = s.Subdivisions[0].ISOCode
		f.RegionName = s.Subdivisions[0].Names[lang]
	}
	if p >= ip2geo.ProfileCity {
		f.City = s.City.Names[lang]
		f.CityGeoNameID = s.City.GeoNameID
		if s.Location.Latitude != nil {
			f.Lat = scaled(*s.Location.Latitude)
		}
		if s.Location.Longitude != nil {
			f.Lon = scaled(*s.Location.Longitude)
		}
		f.AccuracyRadius = s.Location.AccuracyRadius
		f.TimeZone = s.Location.TimeZone
	}
	return f
}

// actual is the fields of an ip2geo record. It does not project by profile:
// a reader that fills a field its profile does not keep is a mismatch.
func actual(r ip2geo.Record) fields {
	return fields{
		CountryISO:       r.CountryISO,
		CountryName:      r.CountryName,
		Continent:        r.Continent,
		CountryGeoNameID: r.CountryGeoNameID,
		RegionISO:        r.RegionISO,
		RegionName:       r.RegionName,
		City:             r.City,
		CityGeoNameID:    r.CityGeoNameID,
		Lat:              scaled(r.Latitude),
		Lon:              scaled(r.Longitude),
		AccuracyRadius:   r.AccuracyRadius,
		TimeZone:         r.TimeZone,
	}
}

// parity compares an ip2geo reader with maxminddb, address by address:
// found-ness (Result.Found vs ok) and every field of the profile.
type parity struct {
	t        *testing.T
	mm       *maxminddb.Reader
	db       *ip2geo.Reader
	lang     string
	addrs    int // addresses compared
	mismatch int // addresses that differ
}

func newParity(t *testing.T, mm *maxminddb.Reader, db *ip2geo.Reader, lang string) *parity {
	return &parity{t: t, mm: mm, db: db, lang: lang}
}

// check compares every address of set, logs one line for it, and returns
// how many addresses maxminddb found and how many differ. The first
// maxReported mismatches of the reader are reported with both values.
func (c *parity) check(set addrSet) (found, bad int) {
	c.t.Helper()
	p := c.db.Profile()
	for _, a := range set.addrs {
		res := c.mm.Lookup(a)
		var s sourceRecord
		if err := res.Decode(&s); err != nil {
			c.t.Fatalf("maxminddb lookup %s: %v", a, err)
		}
		want := expected(&s, p, c.lang)
		rec, ok := c.db.Lookup(a)
		got := actual(rec)
		if res.Found() {
			found++
		}
		if ok == res.Found() && got == want {
			continue
		}
		bad++
		if c.mismatch+bad <= maxReported {
			c.t.Errorf("%s %s (maxminddb network %s):\n  maxminddb found=%v %+v\n  ip2geo    ok=%v    %+v",
				set.name, a, res.Prefix(), res.Found(), want, ok, got)
		}
	}
	c.addrs += len(set.addrs)
	c.mismatch += bad
	c.t.Logf("%-22s %7d addresses, %7d found by maxminddb (%6.2f%%), %d mismatches",
		set.name, len(set.addrs), found, percent(found, len(set.addrs)), bad)
	return found, bad
}

// done fails the test with the total when any address differed.
func (c *parity) done() {
	c.t.Helper()
	if c.mismatch > 0 {
		c.t.Errorf("%s profile: %d of %d addresses differ from maxminddb", c.db.Profile(), c.mismatch, c.addrs)
	}
}

func percent(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}
