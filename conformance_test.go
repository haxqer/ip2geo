package ip2geo_test

import (
	"bytes"
	"encoding/json"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/internal/conformance"
)

// The conformance suite in testdata/conformance is language-neutral: each
// database comes with a JSON document of the answers a reader must give
// (testdata/conformance/README.md). These tests hold the Go reader to it,
// reading the documents as a reader in another language would, and check
// that the committed files are what internal/conformance generates.

const conformanceDir = "testdata/conformance"

// conformanceDocs returns the JSON documents of the suite, by file name.
func conformanceDocs(t *testing.T) map[string]*conformance.FileJSON {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(conformanceDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatalf("no JSON documents in %s", conformanceDir)
	}
	docs := map[string]*conformance.FileJSON{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		var doc conformance.FileJSON
		if err := dec.Decode(&doc); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if doc.SchemaVersion != conformance.SchemaVersion {
			t.Fatalf("%s: schema_version %d, want %d", p, doc.SchemaVersion, conformance.SchemaVersion)
		}
		docs[doc.File] = &doc
	}
	return docs
}

func TestConformance(t *testing.T) {
	for file, doc := range conformanceDocs(t) {
		t.Run(file, func(t *testing.T) {
			r, err := ip2geo.Open(filepath.Join(conformanceDir, file))
			if err != nil {
				t.Fatal(err)
			}
			checkConformance(t, r, doc)
		})
	}
}

func checkConformance(t *testing.T, r *ip2geo.Reader, doc *conformance.FileJSON) {
	m, w := r.Metadata(), doc.Metadata
	got := conformance.MetadataJSON{
		FormatVersion: m.FormatVersion, Profile: uint8(m.Profile), ProfileName: m.Profile.String(),
		LatScale: m.LatScale, SourceType: m.SourceType, Language: m.Language, Fields: uint32(m.Fields),
		Description: m.Description, Builder: m.Builder, Networks: m.Networks,
		DistinctRecords: m.DistinctRecords, Records: m.Records, Strings: m.Strings,
		IPv4Ranges: m.IPv4Ranges, IPv6Ranges: m.IPv6Ranges,
	}
	if !m.BuildTime.IsZero() {
		got.BuildTime = m.BuildTime.Unix()
	}
	if !m.SourceTime.IsZero() {
		got.SourceTime = m.SourceTime.Unix()
	}
	if got != w {
		t.Errorf("Metadata = %+v, want %+v", got, w)
	}
	if langs := r.Languages(); !slices.Equal(langs, doc.Languages) || (langs == nil) != (doc.Languages == nil) {
		t.Errorf("Languages = %q, want %q", langs, doc.Languages)
	}
	if f := uint32(r.Fields()); f != doc.Fields {
		t.Errorf("Fields = %#x, want %#x", f, doc.Fields)
	}

	views := map[string]*ip2geo.Reader{}
	for _, l := range doc.Languages[min(1, len(doc.Languages)):] {
		v, err := r.WithLanguage(l)
		if err != nil {
			t.Fatal(err)
		}
		views[l] = v
	}
	scale := w.LatScale
	for _, q := range doc.Queries {
		ip, err := netip.ParseAddr(q.IP)
		if err != nil {
			t.Fatalf("query %q: %v", q.IP, err)
		}
		checkLookups(t, r, ip, "", q.Lookup, q.Details, scale)
		if len(views) != len(q.Localized) {
			t.Errorf("%s: %d localized answers, the file has %d other languages", ip, len(q.Localized), len(views))
		}
		for l, lj := range q.Localized {
			v, ok := views[l]
			if !ok {
				t.Errorf("%s: answer in %q, not a language of the file", ip, l)
				continue
			}
			checkLookups(t, v, ip, l, lj.Lookup, lj.Details, scale)
		}

		a, ok := r.ASN(ip)
		want := ip2geo.ASN{}
		if q.ASN.ASN != nil {
			want = ip2geo.ASN{Number: q.ASN.ASN.Number, Organization: q.ASN.ASN.Organization}
		}
		if ok != q.ASN.Found || a != want || (q.ASN.Found != (q.ASN.ASN != nil)) {
			t.Errorf("ASN(%s) = %+v, %v; want %+v, %v", ip, a, ok, want, q.ASN.Found)
		}

		rg, ok := r.LookupRange(ip)
		wantRange := ip2geo.Range{First: netip.MustParseAddr(q.Range.First), Last: netip.MustParseAddr(q.Range.Last)}
		if ok != q.Range.Found || rg != wantRange {
			t.Errorf("LookupRange(%s) = %s-%s, %v; want %s-%s, %v", ip, rg.First, rg.Last, ok, wantRange.First, wantRange.Last, q.Range.Found)
		}
	}

	i := 0
	for rg, d := range r.Ranges() {
		if i >= len(doc.Ranges) {
			t.Errorf("Ranges yields %s-%s past the %d expected", rg.First, rg.Last, len(doc.Ranges))
			break
		}
		e := doc.Ranges[i]
		want := ip2geo.Range{First: netip.MustParseAddr(e.First), Last: netip.MustParseAddr(e.Last)}
		wd := details(t, &e.Record, &e.Details, scale)
		if rg != want || d != wd {
			t.Errorf("Ranges[%d] = %s-%s %+v, want %s-%s %+v", i, rg.First, rg.Last, d, want.First, want.Last, wd)
		}
		i++
	}
	if i < len(doc.Ranges) {
		t.Errorf("Ranges yields %d ranges, want %d", i, len(doc.Ranges))
	}
}

// checkLookups checks Lookup and LookupDetails of ip on r (in language
// lang, "" for the primary one) against a query's answers.
func checkLookups(t *testing.T, r *ip2geo.Reader, ip netip.Addr, lang string, lj conformance.LookupJSON, dj conformance.DetailsJSON, scale int) {
	t.Helper()
	if lj.Found != (lj.Record != nil) || dj.Found != (dj.Details != nil) || lj.Found != dj.Found {
		t.Errorf("%s %s: inconsistent found flags and values in the document", ip, lang)
		return
	}
	var want ip2geo.Details
	if lj.Found {
		want = details(t, lj.Record, dj.Details, scale)
	}
	rec, ok := r.Lookup(ip)
	if ok != lj.Found || rec != want.Record {
		t.Errorf("Lookup(%s) %s = %+v, %v; want %+v, %v", ip, lang, rec, ok, want.Record, lj.Found)
	}
	d, ok := r.LookupDetails(ip)
	if ok != dj.Found || d != want {
		t.Errorf("LookupDetails(%s) %s = %+v, %v; want %+v, %v", ip, lang, d, ok, want, dj.Found)
	}
}

// details converts a document's record and details to ip2geo.Details,
// checking that the coordinates are the stored integers over 10^scale.
func details(t *testing.T, r *conformance.RecordJSON, x *conformance.ExtrasJSON, scale int) ip2geo.Details {
	t.Helper()
	div := math.Pow10(scale)
	if lat := float64(r.LatitudeInt) / div; math.Float64bits(lat) != math.Float64bits(r.Latitude) {
		t.Errorf("latitude %v is not latitude_int %d / 10^%d = %v", r.Latitude, r.LatitudeInt, scale, lat)
	}
	if lon := float64(r.LongitudeInt) / div; math.Float64bits(lon) != math.Float64bits(r.Longitude) {
		t.Errorf("longitude %v is not longitude_int %d / 10^%d = %v", r.Longitude, r.LongitudeInt, scale, lon)
	}
	country := func(c conformance.CountryJSON) ip2geo.Country {
		return ip2geo.Country{ISO: c.ISO, Name: c.Name, GeoNameID: c.GeoNameID, IsInEuropeanUnion: c.IsInEuropeanUnion}
	}
	return ip2geo.Details{
		Record: ip2geo.Record{
			CountryISO: r.CountryISO, CountryName: r.CountryName, Continent: r.Continent,
			CountryGeoNameID: r.CountryGeoNameID, RegionISO: r.RegionISO, RegionName: r.RegionName,
			City: r.City, CityGeoNameID: r.CityGeoNameID, Latitude: r.Latitude, Longitude: r.Longitude,
			AccuracyRadius: r.AccuracyRadius, TimeZone: r.TimeZone,
		},
		ContinentName: x.ContinentName, ContinentGeoNameID: x.ContinentGeoNameID,
		IsInEuropeanUnion: x.IsInEuropeanUnion, RegionGeoNameID: x.RegionGeoNameID,
		Region2ISO: x.Region2ISO, Region2Name: x.Region2Name, Region2GeoNameID: x.Region2GeoNameID,
		PostalCode: x.PostalCode, MetroCode: x.MetroCode,
		HasCoordinates: x.HasCoordinates, HasAccuracyRadius: x.HasAccuracyRadius,
		RegisteredCountry: country(x.RegisteredCountry), RepresentedCountry: country(x.RepresentedCountry),
		RepresentedCountryType: x.RepresentedCountryType,
		IsAnycast:              x.IsAnycast, IsAnonymousProxy: x.IsAnonymousProxy, IsSatelliteProvider: x.IsSatelliteProvider,
		ASN: ip2geo.ASN{Number: x.ASN.Number, Organization: x.ASN.Organization},
	}
}

// TestConformanceFilesUpToDate regenerates the suite in memory and checks
// that testdata/conformance holds exactly those files, byte for byte.
// Regenerate with: go generate ./internal/conformance
func TestConformanceFilesUpToDate(t *testing.T) {
	files, err := conformance.Generate()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, f := range files {
		want[f.Name] = true
		b, err := os.ReadFile(filepath.Join(conformanceDir, f.Name))
		if err != nil {
			t.Errorf("%v (regenerate with go generate ./internal/conformance)", err)
			continue
		}
		if !bytes.Equal(b, f.Data) {
			t.Errorf("%s differs from a fresh generation (regenerate with go generate ./internal/conformance)", f.Name)
		}
	}
	entries, err := os.ReadDir(conformanceDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if n := e.Name(); n != "README.md" && !want[n] {
			t.Errorf("%s: not a generated file", filepath.Join(conformanceDir, n))
		}
	}
	for _, f := range files {
		if strings.HasSuffix(f.Name, ".geoc") && !want[strings.TrimSuffix(f.Name, ".geoc")+".json"] {
			t.Errorf("%s has no JSON document", f.Name)
		}
	}
}
