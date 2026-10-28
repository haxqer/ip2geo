package ip2geo

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// Tests of format revision 2.1: extension fields, languages, ASN databases,
// and the ranges API. The fixtures come from geo2_fixture_test.go, which
// encodes the spec independently of the reader and the builder.

func TestFieldSetBits(t *testing.T) {
	got := []FieldSet{FieldEU, FieldContinent, FieldRegistered, FieldSubdivisions, FieldPostal, FieldMetro, FieldTraits, FieldLocation}
	want := []FieldSet{xEU, xContinent, xRegistered, xSubdivisions, xPostal, xMetro, xTraits, xLocation}
	if !slices.Equal(got, want) || FieldsAll != 0xFF {
		t.Errorf("field set bits %v, want the spec's %v", got, want)
	}
}

// laFile is cityTestDB (or excTestDB) with laExt: every field set, three
// languages.
func laFile(d *testDB) *g2file { return toGEO2(d, g2opts{meta: testMeta, ext: laExt()}) }

// TestGEO2Details reads the hand-written extension in every language.
func TestGEO2Details(t *testing.T) {
	d := cityTestDB()
	r := openGEO2(t, laFile(d).encode())
	if r.Fields() != FieldsAll || r.Metadata().Fields != FieldsAll {
		t.Errorf("Fields = %#x, Metadata.Fields = %#x", r.Fields(), r.Metadata().Fields)
	}
	if got := r.Languages(); !slices.Equal(got, []string{"en", "de", "fr"}) {
		t.Errorf("Languages = %q", got)
	}
	r.Languages()[0] = "xx" // a copy
	if r.Languages()[0] != "en" {
		t.Error("Languages returned the Reader's slice")
	}

	la := netip.MustParseAddr("10.0.0.5")
	dt, ok := r.LookupDetails(la)
	want := Details{
		Record:        d.record(recLA),
		ContinentName: "North America", ContinentGeoNameID: 6255149,
		RegionGeoNameID: 5332921, Region2ISO: "LA", Region2Name: "Los Angeles County", Region2GeoNameID: 5368381,
		PostalCode: "90001", MetroCode: 803, HasCoordinates: true, HasAccuracyRadius: true,
		RegisteredCountry:      Country{"US", "United States", 6252001, false},
		RepresentedCountry:     Country{"DE", "Germany", 2921044, true},
		RepresentedCountryType: "military",
		IsAnycast:              true, IsSatelliteProvider: true,
	}
	if !ok || dt != want {
		t.Fatalf("LookupDetails =\n%+v, %v\nwant\n%+v", dt, ok, want)
	}

	de, err := r.WithLanguage("de")
	if err != nil {
		t.Fatal(err)
	}
	wantDE := want
	wantDE.CountryName, wantDE.RegionName, wantDE.ContinentName = "Vereinigte Staaten", "Kalifornien", "Nordamerika"
	wantDE.Region2Name, wantDE.RegisteredCountry.Name, wantDE.RepresentedCountry.Name = "Los Angeles", "Vereinigte Staaten", "Deutschland"
	if got, _ := de.LookupDetails(la); got != wantDE || got.City != "Los Angeles" {
		t.Errorf("de LookupDetails =\n%+v\nwant\n%+v", got, wantDE)
	}
	if rec, _ := de.Lookup(la); rec != wantDE.Record {
		t.Errorf("de Lookup = %+v, want %+v", rec, wantDE.Record)
	}
	fr, err := OpenBytes(laFile(d).encode(), Language("fr"))
	if err != nil {
		t.Fatal(err)
	}
	if rec, _ := fr.Lookup(la); rec.City != "Los Ángeles" || rec.CountryName != "United States" {
		t.Errorf("fr Lookup = %+v", rec)
	}
	// The views keep the database's metadata and languages.
	if de.Metadata() != r.Metadata() || !slices.Equal(de.Languages(), r.Languages()) || de.Fields() != r.Fields() {
		t.Error("a view's metadata differs")
	}
	// Berlin has no names in the other languages and falls back.
	berlin := netip.MustParseAddr("2001:db8::1")
	b0, _ := r.LookupDetails(berlin)
	if b1, _ := de.LookupDetails(berlin); b1 != b0 || !b0.IsInEuropeanUnion || b0.ContinentName != "Europe" {
		t.Errorf("Berlin: de %+v, en %+v", b1, b0)
	}
	// The primary language, by name or "", is the plain Reader.
	for _, l := range []string{"", "en"} {
		v, err := de.WithLanguage(l)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := v.LookupDetails(la); got != want || v.alt {
			t.Errorf("WithLanguage(%q) = %+v", l, got)
		}
	}
	for _, l := range []string{"xx", "EN", "de "} {
		if v, err := r.WithLanguage(l); v != nil || !errors.Is(err, ErrLanguage) || !strings.Contains(err.Error(), "en, de, fr") {
			t.Errorf("WithLanguage(%q) = %v, %v; want ErrLanguage", l, v, err)
		}
		if v, err := OpenBytes(laFile(d).encode(), Language(l)); v != nil || !errors.Is(err, ErrLanguage) {
			t.Errorf("OpenBytes(Language(%q)) = %v, %v; want ErrLanguage", l, v, err)
		}
	}
}

// TestLanguagesWithoutLANGS: one language is META's; GEO1 records none.
func TestLanguagesWithoutLANGS(t *testing.T) {
	r := openGEO2(t, encodeGEO2(cityTestDB()))
	if got := r.Languages(); !slices.Equal(got, []string{"en"}) || r.Fields() != 0 {
		t.Errorf("Languages = %q, Fields = %#x", got, r.Fields())
	}
	if v, err := r.WithLanguage("en"); err != nil || v.alt {
		t.Errorf("WithLanguage(en) = %v, %v", v, err)
	}
	if _, err := r.WithLanguage("de"); !errors.Is(err, ErrLanguage) {
		t.Errorf("WithLanguage(de): %v", err)
	}
	// No language in META: none listed.
	r = openGEO2(t, toGEO2(cityTestDB(), g2opts{}).encode())
	if got := r.Languages(); got != nil {
		t.Errorf("Languages = %q, want nil", got)
	}

	b, _ := cityTestDB().encode()
	g1 := openGEO2(t, b)
	if g1.Languages() != nil || g1.Fields() != 0 {
		t.Errorf("GEO1: Languages = %q, Fields = %#x", g1.Languages(), g1.Fields())
	}
	if _, err := g1.WithLanguage("en"); !errors.Is(err, ErrLanguage) || !strings.Contains(err.Error(), "records none") {
		t.Errorf("GEO1 WithLanguage(en): %v", err)
	}
	if _, err := OpenBytes(b, Language("en")); !errors.Is(err, ErrLanguage) {
		t.Errorf("GEO1 OpenBytes(Language(en)): %v", err)
	}
	if _, err := OpenBytes(b, Language("")); err != nil {
		t.Errorf("GEO1 OpenBytes(Language(\"\")): %v", err)
	}
	a := netip.MustParseAddr("10.0.0.5")
	if dt, ok := g1.LookupDetails(a); !ok || dt != (Details{Record: cityTestDB().record(recLA)}) {
		t.Errorf("GEO1 LookupDetails = %+v, %v", dt, ok)
	}
}

// allowedSets returns the field sets a profile allows, one bit each.
func allowedSets(p Profile) []uint32 {
	out := []uint32{xEU, xContinent, xRegistered, xTraits}
	if p >= ProfileRegion {
		out = append(out, xSubdivisions)
	}
	if p == ProfileCity {
		out = append(out, xPostal, xMetro, xLocation)
	}
	return out
}

// TestGEO2DetailsRoundTrip encodes random extensions for every profile,
// every single field set, all of them and none, with one, two and three
// languages, and compares every answer with the reference.
func TestGEO2DetailsRoundTrip(t *testing.T) {
	n := 0
	for _, p := range []Profile{ProfileCountry, ProfileRegion, ProfileCity} {
		sets := allowedSets(p)
		all := uint32(0)
		for _, s := range sets {
			all |= s
		}
		for _, fields := range append([]uint32{0, all}, sets...) {
			for _, langs := range [][]string{nil, {"en", "de"}, {"en", "pt-BR", "zh-CN"}} {
				if fields == 0 && langs == nil {
					continue // no extension at all: TestGEO1GEO2Agree
				}
				n++
				t.Run(fmt.Sprintf("%s/%#x/%d", p, fields, max(len(langs), 1)), func(t *testing.T) {
					d := randomTestDB(uint64(n), p, 150, 400, 400)
					e := randomExt(uint64(n), d, fields, langs)
					r := openGEO2(t, toGEO2(d, g2opts{meta: testMeta, ext: e}).encode())
					if r.Fields() != FieldSet(fields) {
						t.Fatalf("Fields = %#x, want %#x", r.Fields(), fields)
					}
					wantLangs := langs
					if langs == nil {
						wantLangs = []string{"en"}
					}
					if !slices.Equal(r.Languages(), wantLangs) {
						t.Fatalf("Languages = %q, want %q", r.Languages(), wantLangs)
					}
					probes := probeAddrs(d, uint64(n), 600)
					for j, l := range wantLangs {
						v, err := r.WithLanguage(l)
						if err != nil {
							t.Fatal(err)
						}
						for _, a := range probes {
							want, wok := e.lookupDetails(d, a, j)
							got, ok := v.LookupDetails(a)
							if ok != wok || got != want {
								t.Fatalf("%s LookupDetails(%v) =\n%+v, %v\nwant\n%+v, %v", l, a, got, ok, want, wok)
							}
							if rec, ok := v.Lookup(a); ok != wok || rec != want.Record {
								t.Fatalf("%s Lookup(%v) = %+v, %v; want %+v", l, a, rec, ok, want.Record)
							}
						}
						checkRanges(t, v, d, e, j)
					}
					checkDetails(t, r)
				})
			}
		}
	}
}

// checkRanges compares Ranges with the reference list and every range's
// details with the reference, in language j.
func checkRanges(t *testing.T, r *Reader, d *testDB, e *g2ext, j int) {
	t.Helper()
	wantR, wantIDs := d.ranges()
	k := 0
	for rg, dt := range r.Ranges() {
		if k >= len(wantR) {
			t.Fatalf("Ranges: extra range %v", rg)
		}
		if rg != wantR[k] {
			t.Fatalf("Ranges[%d] = %v, want %v", k, rg, wantR[k])
		}
		if want := e.details(d, wantIDs[k], j); dt != want {
			t.Fatalf("Ranges[%d] %v details =\n%+v\nwant\n%+v", k, rg, dt, want)
		}
		k++
	}
	if k != len(wantR) {
		t.Fatalf("Ranges: %d ranges, want %d", k, len(wantR))
	}
}

func TestASNDatabase(t *testing.T) {
	d := asnTestDB()
	r := openGEO2(t, toGEO2(d, g2opts{meta: testMeta}).encode())
	if r.Profile() != ProfileASN || r.Metadata().Profile != ProfileASN || r.Metadata().Records != 4 {
		t.Fatalf("Metadata = %+v", r.Metadata())
	}
	for ip, want := range map[string]ASN{
		"10.0.0.5": {64500, "EXAMPLE-AS"}, "::ffff:10.0.0.255": {64500, "EXAMPLE-AS"}, "2002:a00:1::": {64500, "EXAMPLE-AS"},
		"192.0.2.1": {13335, ""}, "2001:db8::1": {64501, "Other Networks Ltd"}, "2001:db8::8000:0:0:0": {64500, "EXAMPLE-AS"},
		"10.0.1.0": {}, "8.8.8.8": {}, "2001:db9::": {}, "::": {},
	} {
		a := netip.MustParseAddr(ip)
		covered := want != ASN{}
		if got, ok := r.ASN(a); ok != covered || got != want {
			t.Errorf("ASN(%s) = %+v, %v; want %+v", ip, got, ok, want)
		}
		if rec, ok := r.Lookup(a); ok != covered || rec != (Record{}) {
			t.Errorf("Lookup(%s) = %+v, %v; want an empty Record, %v", ip, rec, ok, covered)
		}
		if dt, ok := r.LookupDetails(a); ok != covered || dt != (Details{ASN: want}) {
			t.Errorf("LookupDetails(%s) = %+v, %v", ip, dt, ok)
		}
	}
	if _, ok := r.ASN(netip.Addr{}); ok {
		t.Error("ASN of the zero Addr")
	}
	checkRanges(t, r, d, nil, 0)
	checkDetails(t, r)
	for _, a := range probeAddrs(d, 1, 300) {
		got, ok := r.LookupRange(a)
		if want, wok := d.lookupRange(a); ok != wok || got != want {
			t.Fatalf("LookupRange(%v) = %v, %v; want %v, %v", a, got, ok, want, wok)
		}
	}
	// ASN is false on a geo database.
	g := openGEO2(t, encodeGEO2(cityTestDB()))
	if a, ok := g.ASN(netip.MustParseAddr("10.0.0.5")); ok || a != (ASN{}) {
		t.Errorf("ASN on a city database = %+v, %v", a, ok)
	}
}

func TestLookupRange(t *testing.T) {
	empty := cityTestDB()
	empty.v4starts, empty.v4rec, empty.v6starts, empty.v6rec = nil, nil, nil, nil
	for i, d := range []*testDB{cityTestDB(), excTestDB(), empty, randomTestDB(7, ProfileCity, 100, 2000, 2000)} {
		r := openGEO2(t, encodeGEO2(d))
		for _, a := range append(probeAddrs(d, uint64(i), 3000), fuzzProbes...) {
			want, wok := d.lookupRange(a)
			got, ok := r.LookupRange(a)
			if ok != wok || got != want {
				t.Fatalf("db %d: LookupRange(%v) = %v, %v; want %v, %v", i, a, got, ok, want, wok)
			}
		}
		checkRanges(t, r, d, nil, 0)
		// GEO1 converts into the same tables.
		b, _ := d.encode()
		checkRanges(t, openGEO2(t, b), d, nil, 0)
	}

	r := openGEO2(t, encodeGEO2(excTestDB()))
	p := netip.MustParsePrefix
	for ip, want := range map[string]struct {
		rg Range
		ok bool
	}{
		"10.0.0.5":             {Range{p("10.0.0.0/24").Addr(), netip.MustParseAddr("10.0.0.255")}, true},
		"::ffff:10.0.0.5":      {Range{p("10.0.0.0/24").Addr(), netip.MustParseAddr("10.0.0.255")}, true},
		"2001:0:a00:5::1":      {Range{p("10.0.0.0/24").Addr(), netip.MustParseAddr("10.0.0.255")}, true},
		"9.0.0.1":              {Range{netip.MustParseAddr("0.0.0.0"), netip.MustParseAddr("9.255.255.255")}, false},
		"10.0.1.0":             {Range{netip.MustParseAddr("10.0.1.0"), netip.MustParseAddr("255.255.255.255")}, false},
		"2001:db8::8000:0:0:1": {Range{netip.MustParseAddr("2001:db8::8000:0:0:0"), netip.MustParseAddr("2001:db8::bfff:ffff:ffff:ffff")}, true},
		"2001:db8::1%eth0":     {Range{netip.MustParseAddr("2001:db8::"), netip.MustParseAddr("2001:db8::7fff:ffff:ffff:ffff")}, true},
		"64:ff9b::a00:5":       {Range{netip.MustParseAddr("::1:0:0:0"), netip.MustParseAddr("2000:ffff:ffff:ffff:ffff:ffff:ffff:ffff")}, false},
	} {
		if got, ok := r.LookupRange(netip.MustParseAddr(ip)); got != want.rg || ok != want.ok {
			t.Errorf("LookupRange(%s) = %v, %v; want %v, %v", ip, got, ok, want.rg, want.ok)
		}
	}
	if got, ok := r.LookupRange(netip.Addr{}); got != (Range{}) || ok {
		t.Errorf("LookupRange(zero) = %v, %v", got, ok)
	}
}

// TestRangesBreak stops the iteration at every point, and checks that the
// IPv4 ranges come in IPv4 form before the IPv6 ones.
func TestRangesBreak(t *testing.T) {
	r := openGEO2(t, laFile(excTestDB()).encode())
	var all []Range
	for rg := range r.Ranges() {
		all = append(all, rg)
	}
	if len(all) != 4 || !all[0].First.Is4() || !all[len(all)-1].First.Is6() {
		t.Fatalf("Ranges = %v", all)
	}
	for stop := range len(all) + 1 {
		var got []Range
		for rg := range r.Ranges() {
			if len(got) == stop {
				break
			}
			got = append(got, rg)
		}
		if !slices.Equal(got, all[:stop]) {
			t.Errorf("break after %d: %v", stop, got)
		}
	}
	empty := cityTestDB()
	empty.v4starts, empty.v4rec, empty.v6starts, empty.v6rec = nil, nil, nil, nil
	for rg := range openGEO2(t, encodeGEO2(empty)).Ranges() {
		t.Errorf("empty database: range %v", rg)
	}
}

func TestDetailsAllocs(t *testing.T) {
	b := laFile(excTestDB()).encode()
	mapped, err := Open(writeMapped(t, b))
	if err != nil {
		t.Fatal(err)
	}
	de, err := mapped.WithLanguage("de")
	if err != nil {
		t.Fatal(err)
	}
	asn := openGEO2(t, toGEO2(asnTestDB(), g2opts{meta: testMeta}).encode())
	for name, r := range map[string]*Reader{"OpenBytes": openGEO2(t, b), "Open": mapped, "de view": de, "ASN": asn} {
		for _, ip := range []string{"10.0.0.5", "2001:db8::1", "2001:db8::8000:0:0:1", "2002:a00:5::1", "192.0.2.1", "2001:db9::1"} {
			a := netip.MustParseAddr(ip)
			for fn, f := range map[string]func(){
				"Lookup":        func() { r.Lookup(a) },
				"LookupDetails": func() { r.LookupDetails(a) },
				"ASN":           func() { r.ASN(a) },
				"LookupRange":   func() { r.LookupRange(a) },
			} {
				if n := testing.AllocsPerRun(200, f); n != 0 {
					t.Errorf("%s: %s(%s) allocates %v times", name, fn, ip, n)
				}
			}
		}
	}
}

// TestViewKeepsMapping drops a mapped Reader and keeps a WithLanguage view
// of it: the mapping stays until the view is dropped too.
func TestViewKeepsMapping(t *testing.T) {
	if !mmapSupported {
		t.Skip("no memory mapping on " + runtime.GOOS)
	}
	p := writeMapped(t, laFile(excTestDB()).encode())
	before := drainCleanups()
	a := netip.MustParseAddr("10.0.0.5")
	view := func() *Reader {
		r, err := Open(p)
		if err != nil {
			t.Fatal(err)
		}
		if !r.mapped {
			t.Fatal("the Reader is not mapped")
		}
		v, err := r.WithLanguage("de")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}()
	if n := drainCleanups(); n != before {
		t.Fatalf("%d mappings released while a view is reachable", n-before)
	}
	for range 100 {
		runtime.GC()
		if d, ok := view.LookupDetails(a); !ok || d.CountryName != "Vereinigte Staaten" || d.PostalCode != "90001" {
			t.Fatalf("LookupDetails through the view = %+v, %v", d, ok)
		}
	}
	runtime.KeepAlive(view)
	view = nil
	for deadline := time.Now().Add(5 * time.Second); unmapped.Load() == before; {
		if time.Now().After(deadline) {
			t.Fatal("the mapping was not released after the view became unreachable")
		}
		runtime.GC()
		time.Sleep(time.Millisecond)
	}
}

// TestOpenLanguageMapped: Open with Language answers in it, and a bad
// language releases the mapping at once.
func TestOpenLanguageMapped(t *testing.T) {
	p := writeMapped(t, laFile(excTestDB()).encode())
	r, err := Open(p, Language("de"))
	if err != nil {
		t.Fatal(err)
	}
	if rec, _ := r.Lookup(netip.MustParseAddr("10.0.0.5")); rec.CountryName != "Vereinigte Staaten" {
		t.Errorf("Lookup = %+v", rec)
	}
	for _, opts := range [][]Option{{Language("xx")}, {Language("xx"), NoMmap()}} {
		if r, err := Open(p, opts...); r != nil || !errors.Is(err, ErrLanguage) {
			t.Errorf("Open(%d options) = %v, %v; want ErrLanguage", len(opts), r, err)
		}
	}
	if !mmapSupported {
		return
	}
	before := drainCleanups()
	if _, err := Open(p, Language("xx")); err == nil {
		t.Fatal("opened")
	}
	if n := drainCleanups(); n != before {
		t.Errorf("a failed Open left %d cleanups", n-before)
	}
}

// checkDetails checks the consistency of the revision 2.1 API on any
// Reader that opened: LookupDetails, ASN and LookupRange against Lookup
// and each other on the fuzz probes, and the start of Ranges, in each of
// the first languages. It is the fuzzers' check.
func checkDetails(t *testing.T, r *Reader) {
	t.Helper()
	langs := r.Languages()
	if len(langs) == 0 {
		langs = []string{""}
	}
	for _, l := range langs[:min(len(langs), 3)] {
		v, err := r.WithLanguage(l)
		if err != nil {
			t.Fatalf("WithLanguage(%q) of %q: %v", l, r.Languages(), err)
		}
		probes := fuzzProbes
		for _, k := range v4Probes {
			probes = append(probes, refAddr4(k))
		}
		for _, a := range probes {
			checkAddr(t, v, a)
		}
		prev, n := netip.Addr{}, 0
		for rg, d := range v.Ranges() {
			if !rg.First.IsValid() || rg.Last.Less(rg.First) || rg.First.Is4() != rg.Last.Is4() ||
				prev.IsValid() && (prev.Is4() == rg.First.Is4() && !prev.Less(rg.First) || !prev.Is4() && rg.First.Is4()) {
				t.Fatalf("range %v after %v", rg, prev)
			}
			for _, a := range []netip.Addr{rg.First, rg.Last} {
				if _, _, is4, _ := refKey(a); is4 != a.Is4() {
					t.Fatalf("range %v reaches into an alias prefix at %v", rg, a)
				}
				if got, ok := v.LookupDetails(a); !ok || got != d {
					t.Fatalf("range %v: LookupDetails(%v) = %+v, %v; Ranges gave %+v", rg, a, got, ok, d)
				}
				checkAddr(t, v, a)
			}
			prev = rg.Last
			if n++; n == 64 {
				break
			}
		}
	}
}

// checkAddr checks LookupDetails, ASN and LookupRange at a against Lookup.
func checkAddr(t *testing.T, r *Reader, a netip.Addr) {
	t.Helper()
	rec, ok := r.Lookup(a)
	d, dok := r.LookupDetails(a)
	if dok != ok || d.Record != rec {
		t.Fatalf("LookupDetails(%v) = %+v, %v; Lookup = %+v, %v", a, d, dok, rec, ok)
	}
	asn, aok := r.ASN(a)
	if aok != (ok && r.Profile() == ProfileASN) || asn != d.ASN {
		t.Fatalf("ASN(%v) = %+v, %v; LookupDetails = %+v", a, asn, aok, d)
	}
	rg, rok := r.LookupRange(a)
	if rok != ok {
		t.Fatalf("LookupRange(%v) ok = %v, Lookup ok = %v", a, rok, ok)
	}
	if !a.IsValid() {
		if rg != (Range{}) {
			t.Fatalf("LookupRange(zero) = %v", rg)
		}
		return
	}
	k, v4, is4, _ := refKey(a)
	key := netip.AddrFrom16(k)
	if is4 {
		key = refAddr4(v4)
	}
	if key.Less(rg.First) || rg.Last.Less(key) || rg.First.Is4() != is4 || rg.Last.Is4() != is4 {
		t.Fatalf("LookupRange(%v) = %v does not hold %v", a, rg, key)
	}
	for _, e := range []netip.Addr{rg.First, rg.Last} {
		if got, gok := r.LookupRange(e); got != rg || gok != ok {
			t.Fatalf("LookupRange(%v) = %v, %v; LookupRange(%v) = %v, %v", e, got, gok, a, rg, ok)
		}
	}
}

// addString appends s to the fixture's string table and returns its id.
func addString(f *g2file, s string) uint32 {
	blob := f.sec(sStrBlob)
	blob.data = append(blob.data, s...)
	idx := f.sec(sStrIdx)
	idx.data = binary.LittleEndian.AppendUint32(idx.data, uint32(len(blob.data)))
	return uint32(len(idx.data)/4 - 2)
}

func nStrings(f *g2file) uint32 { return uint32(len(f.sec(sStrIdx).data)/4 - 1) }

func setU24(f *g2file, id uint32, off int, v uint32) {
	b := f.sec(id).data[off:]
	b[0], b[1], b[2] = byte(v), byte(v>>8), byte(v>>16)
}

// Byte offsets in an entry of laFile: the blocks of every set, then 7 names
// for each of the two extra languages; entry 1 starts at xw.
const (
	xoEU, xoCont, xoReg, xoSub, xoPostal, xoMetro, xoTraits, xoLoc, xoLangs = 0, 1, 8, 32, 46, 49, 51, 52, 53
	xw                                                                      = 53 + 2*7*3
)

// g21case is a corruption of base() for rules 9 to 13.
type g21case struct {
	name string
	base func() *g2file
	mut  func(f *g2file)
	want error
	msg  string
}

func TestGEO2RejectsRevision21(t *testing.T) {
	city := func() *g2file { return laFile(excTestDB()) }
	country := func() *g2file {
		return toGEO2(cityTestDB().withProfile(ProfileCountry), g2opts{meta: testMeta,
			ext: &g2ext{fields: xEU | xTraits, langs: []string{"en", "de"}, recs: laExt().recs}})
	}
	region := func() *g2file {
		return toGEO2(cityTestDB().withProfile(ProfileRegion), g2opts{meta: testMeta, ext: &g2ext{fields: xEU}})
	}
	asn := func() *g2file { return toGEO2(asnTestDB(), g2opts{meta: testMeta}) }
	setFields := func(v uint32) func(*g2file) { return func(f *g2file) { setU32(f, sXFields, 0, v) } }
	setLang := func(j int, v func(f *g2file) uint32) func(*g2file) {
		return func(f *g2file) { setU32(f, sLangs, j, v(f)) }
	}
	entry1 := func(off int, v byte) func(*g2file) {
		return func(f *g2file) { f.sec(sXRecords).data[xw+off] = v }
	}
	badID := func(off int) func(*g2file) {
		return func(f *g2file) { setU24(f, sXRecords, xw+off, nStrings(f)) }
	}
	cases := []g21case{
		// Rule 9: XFIELDS.
		{name: "XFIELDS short", base: city, mut: resize(sXFields, -1), msg: "XFIELDS is 7 bytes"},
		{name: "XFIELDS long", base: city, mut: resize(sXFields, 8), msg: "XFIELDS is 16 bytes"},
		{name: "XFIELDS empty", base: city, mut: func(f *g2file) { f.sec(sXFields).data = nil }, msg: "XFIELDS is 0 bytes"},
		{name: "XFIELDS reserved", base: city, mut: func(f *g2file) { setU32(f, sXFields, 1, 1) }, msg: "reserved word"},
		{name: "field bit 8", base: city, mut: setFields(0x1FF), want: ErrUnsupportedVersion, msg: "0x100"},
		{name: "field bit 31", base: country, mut: setFields(1<<31 | xEU), want: ErrUnsupportedVersion, msg: "does not know"},
		{name: "postal in country", base: country, mut: setFields(xEU | xTraits | xPostal), msg: "not allowed for the country profile"},
		{name: "subdivisions in country", base: country, mut: setFields(xSubdivisions), msg: "not allowed for the country"},
		{name: "location in region", base: region, mut: setFields(xEU | xLocation), msg: "not allowed for the region"},
		{name: "metro in region", base: region, mut: setFields(xMetro), msg: "not allowed for the region"},

		// Rule 10: which sections are present.
		{name: "XFIELDS without XRECORDS", base: city, mut: func(f *g2file) { f.without(sXRecords) }, msg: "both be present"},
		{name: "XRECORDS without XFIELDS", base: city, mut: func(f *g2file) { f.without(sXFields) }, msg: "both be present"},
		{name: "LANGS alone", base: city, mut: func(f *g2file) { f.without(sXFields).without(sXRecords) }, msg: "LANGS is present without"},
		{name: "XFIELDS and XRECORDS in ASN", base: asn, mut: func(f *g2file) {
			f.secs = append(f.secs, g2sec{sXFields, make([]byte, 8)}, g2sec{sXRecords, nil})
		}, msg: "an ASN database has"},
		{name: "LANGS in ASN", base: asn, mut: func(f *g2file) { f.secs = append(f.secs, g2sec{sLangs, []byte{0, 0, 0, 0}}) }, msg: "an ASN database has"},
		{name: "XRECORDS in ASN", base: asn, mut: func(f *g2file) { f.secs = append(f.secs, g2sec{sXRecords, nil}) }, msg: "an ASN database has"},
		{name: "XFIELDS twice", base: city, mut: func(f *g2file) { f.secs = append(f.secs, g2sec{sXFields, f.sec(sXFields).data}) }, msg: "XFIELDS is listed twice"},
		{name: "XRECORDS twice", base: city, mut: func(f *g2file) { f.secs = append(f.secs, g2sec{sXRecords, f.sec(sXRecords).data}) }, msg: "XRECORDS is listed twice"},
		{name: "LANGS twice", base: city, mut: func(f *g2file) { f.secs = append(f.secs, g2sec{sLangs, f.sec(sLangs).data}) }, msg: "LANGS is listed twice"},

		// Rule 11: XRECORDS.
		{name: "XRECORDS one byte short", base: city, mut: resize(sXRecords, -1), msg: fmt.Sprintf("XRECORDS is %d bytes, want %d", 3*xw-1, 3*xw)},
		{name: "XRECORDS one entry more", base: city, mut: resize(sXRecords, xw), msg: "XRECORDS is"},
		{name: "XRECORDS for other fields", base: city, mut: setFields(0x7F), msg: "XRECORDS is"},
		{name: "XRECORDS for two languages", base: city, mut: resize(sLangs, -4), msg: fmt.Sprintf("want %d", 3*(53+21))},
		{name: "XRECORDS empty", base: country, mut: func(f *g2file) { f.sec(sXRecords).data = nil }, msg: "XRECORDS is 0 bytes"},
		{name: "entry 0 first byte", base: city, mut: func(f *g2file) { f.sec(sXRecords).data[0] = 1 }, msg: "entry 0"},
		{name: "entry 0 last name", base: city, mut: func(f *g2file) { f.sec(sXRecords).data[xw-1] = 1 }, msg: "entry 0"},
		{name: "continent name id", base: city, mut: badID(xoCont), msg: "entry 1: string id"},
		{name: "registered ISO id", base: city, mut: badID(xoReg), msg: "entry 1: string id"},
		{name: "represented type id", base: city, mut: badID(xoReg + 20), msg: "entry 1: string id"},
		{name: "region2 name id", base: city, mut: badID(xoSub + 7), msg: "entry 1: string id"},
		{name: "postal id", base: city, mut: badID(xoPostal), msg: "entry 1: string id"},
		{name: "last language name id", base: city, mut: badID(xw - 3), msg: fmt.Sprintf("at byte %d", xw-3)},
		{name: "language name id 2^24-1", base: city, mut: func(f *g2file) { setU24(f, sXRecords, 2*xw+xoLangs, 1<<24-1) }, msg: "entry 2: string id 16777215"},
		{name: "EU flag bit 1", base: city, mut: entry1(xoEU, 2), msg: "undefined flag bits 0x2"},
		{name: "EU flag bit 7", base: country, mut: func(f *g2file) { f.sec(sXRecords).data[(1+1+3)*1+0] = 0x81 }, msg: "undefined flag bits 0x80"},
		{name: "registered flag bit 2", base: city, mut: entry1(xoReg+23, 4), msg: "undefined flag bits 0x4"},
		{name: "traits flag bit 3", base: city, mut: entry1(xoTraits, 0x0D), msg: "undefined flag bits 0x8"},
		{name: "location flag bit 2", base: city, mut: entry1(xoLoc, 0x07), msg: "undefined flag bits 0x4"},

		// Rule 12: LANGS.
		{name: "LANGS one entry", base: city, mut: resize(sLangs, -8), msg: "LANGS lists 1 language"},
		{name: "LANGS empty", base: city, mut: func(f *g2file) { f.sec(sLangs).data = nil }, msg: "LANGS lists 0"},
		{name: "LANGS odd", base: city, mut: resize(sLangs, 2), msg: "LANGS is 14 bytes"},
		{name: "LANGS id out of range", base: city, mut: setLang(2, nStrings), msg: "LANGS[2] is string"},
		{name: "LANGS id 2^32-1", base: city, mut: setLang(1, func(*g2file) uint32 { return math.MaxUint32 }), msg: "LANGS[1] is string"},
		{name: "LANGS empty language", base: city, mut: setLang(1, func(*g2file) uint32 { return 0 }), msg: "LANGS[1] is the empty string"},
		{name: "LANGS repeated id", base: city, mut: setLang(2, func(f *g2file) uint32 { return getU32(f, sLangs, 1) }), msg: `LANGS[2] "de" is listed twice`},
		{name: "LANGS repeated string", base: city, mut: setLang(2, func(f *g2file) uint32 { return addString(f, "de") }), msg: `LANGS[2] "de" is listed twice`},
		{name: "LANGS[0] not META's", base: city, mut: func(f *g2file) {
			l0, l1 := getU32(f, sLangs, 0), getU32(f, sLangs, 1)
			setU32(f, sLangs, 0, l1)
			setU32(f, sLangs, 1, l0)
		}, msg: `LANGS[0] "de" is not the META language "en"`},

		// Rule 13: ASN records.
		{name: "ASN no records", base: asn, mut: func(f *g2file) { f.sec(sRecords).data = nil }, msg: "no records"},
		{name: "ASN RECORDS not a multiple of 7", base: asn, mut: resize(sRecords, 1), msg: "RECORDS is 29 bytes, not a multiple of the asn record width 7"},
		{name: "ASN record 0", base: asn, mut: func(f *g2file) { f.sec(sRecords).data[6] = 1 }, msg: "record 0"},
		{name: "ASN organization id", base: asn, mut: func(f *g2file) { setU24(f, sRecords, 7*2+4, nStrings(f)) }, msg: "record 2: organization is string"},
		{name: "ASN IPv4 id = record count", base: asn, mut: func(f *g2file) { f.sec(sV4IDs).data[3] = 4 }, msg: "IPv4 range 3 points at record 4"},
		{name: "ASN IPv6 id", base: asn, mut: func(f *g2file) { f.sec(sV6IDs).data[1] = 200 }, msg: "IPv6 range 1 points at record 200"},
		{name: "ASN id width", base: func() *g2file { return toGEO2(asnTestDB(), g2opts{meta: testMeta, idWidth: 2}) }, msg: "record id width 2"},
	}
	for _, base := range []func() *g2file{city, country, region, asn} {
		if _, err := OpenBytes(base().encode()); err != nil {
			t.Fatalf("a base fixture does not open: %v", err)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := c.base()
			if c.mut != nil {
				c.mut(f)
			}
			want := c.want
			if want == nil {
				want = ErrCorrupt
			}
			for _, opts := range [][]Option{nil, {SkipChecksum(), NoCopy()}} {
				r, err := OpenBytes(f.encode(), opts...)
				if r != nil || !errors.Is(err, want) {
					t.Fatalf("OpenBytes = %v, %v; want nil, %v", r, err, want)
				}
				if !strings.HasPrefix(err.Error(), "ip2geo: ") || !strings.Contains(err.Error(), c.msg) {
					t.Errorf("error %q, want the ip2geo: prefix and %q", err, c.msg)
				}
			}
		})
	}
}

// TestGEO2AcceptsRevision21: what revision 2.1 allows besides laFile.
func TestGEO2AcceptsRevision21(t *testing.T) {
	d := excTestDB()
	cases := []struct {
		name string
		f    *g2file
		e    *g2ext
	}{
		{name: "XFIELDS 0 without LANGS, empty XRECORDS", e: &g2ext{}},
		{name: "languages only", e: &g2ext{langs: []string{"en", "de", "fr"}, recs: laExt().recs}},
		{name: "new sections first", e: laExt(), f: func() *g2file {
			f := laFile(d)
			n := len(f.secs)
			f.secs = append(f.secs[n-3:], f.secs[:n-3]...)
			return f
		}()},
		{name: "unknown section after LANGS", e: laExt(), f: func() *g2file {
			f := laFile(d)
			f.secs = append(f.secs, g2sec{15, []byte("revision 2.2")})
			return f
		}()},
		{name: "one record, every field", e: &g2ext{fields: 0xFF, langs: []string{"en", "de"}}, f: func() *g2file {
			d := cityTestDB()
			d.recCount, d.records = 1, d.records[:RecWidth(ProfileCity)]
			d.v4starts, d.v4rec, d.v6starts, d.v6rec = nil, nil, nil, nil
			return toGEO2(d, g2opts{meta: testMeta, ext: &g2ext{fields: 0xFF, langs: []string{"en", "de"}}})
		}()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := c.f
			if f == nil {
				f = toGEO2(d, g2opts{meta: testMeta, ext: c.e})
			}
			r := openGEO2(t, f.encode())
			if r.Fields() != FieldSet(c.e.fields) {
				t.Errorf("Fields = %#x", r.Fields())
			}
			langs := c.e.langs
			if langs == nil {
				langs = []string{"en"}
			}
			if !slices.Equal(r.Languages(), langs) {
				t.Errorf("Languages = %q", r.Languages())
			}
			if f.profile == byte(ProfileCity) && d.recCount == 3 && c.f == nil {
				for j := range langs {
					v, _ := r.WithLanguage(langs[j])
					for _, a := range probeAddrs(d, 1, 200) {
						want, wok := c.e.lookupDetails(d, a, j)
						if got, ok := v.LookupDetails(a); ok != wok || got != want {
							t.Fatalf("LookupDetails(%v) = %+v, %v; want %+v", a, got, ok, want)
						}
					}
				}
			}
			checkReader(t, r)
			checkIndex(t, r)
			checkDetails(t, r)
		})
	}
}

// TestGEO2ByteFlipsRevision21 corrupts each byte of the header, the
// directory, RECORDS and the revision 2.1 sections of laFile and of the
// ASN fixture, one at a time, with checksums skipped: no input may panic,
// fail without a sentinel, or open into a Reader that answers
// inconsistently.
func TestGEO2ByteFlipsRevision21(t *testing.T) {
	for name, f := range map[string]*g2file{"city": laFile(excTestDB()), "asn": toGEO2(asnTestDB(), g2opts{meta: testMeta})} {
		orig := f.encode()
		at := []int{}
		for i := range 32 + 24*len(f.secs) {
			at = append(at, i)
		}
		for _, e := range g2dir(orig) {
			if e.id == sRecords || e.id >= sXFields {
				for k := range int(e.n) {
					at = append(at, int(e.off)+k)
				}
			}
		}
		opened := 0
		for _, i := range at {
			for _, v := range []byte{0x00, 0xFF, orig[i] ^ 0x01, orig[i] ^ 0x80} {
				if v == orig[i] {
					continue
				}
				b := slices.Clone(orig)
				b[i] = v
				r, err := OpenBytes(b, SkipChecksum())
				if err != nil {
					if !isSentinel(err) {
						t.Fatalf("%s byte %d = %#x: error %v wraps no sentinel", name, i, v, err)
					}
					continue
				}
				opened++
				checkReader(t, r)
				checkDetails(t, r)
			}
		}
		t.Logf("%s: %d bytes flipped, %d corruptions opened", name, len(at), opened)
	}
}
