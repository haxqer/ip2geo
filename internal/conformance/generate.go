package conformance

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"net/netip"
	"slices"
	"strings"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/internal/build"
)

// File is one generated file of testdata/conformance.
type File struct {
	Name string // base name, e.g. "city.geoc" or "city.json"
	Data []byte
}

// Generate builds every conformance database from its synthetic source and
// the JSON document of its expected results, which it gets by querying the
// built database with the Go reader. The output is deterministic and
// sorted by name: each database is followed by its JSON document.
func Generate() ([]File, error) {
	var out []File
	for _, d := range datasets() {
		db, _, err := buildDataset(&d)
		if err != nil {
			return nil, fmt.Errorf("conformance: %s: %w", d.file, err)
		}
		doc, err := expect(d.file, d.about, db, datasetQueries(&d))
		if err != nil {
			return nil, fmt.Errorf("conformance: %s: %w", d.file, err)
		}
		out = append(out, File{d.file, db}, File{jsonName(d.file), doc})
	}
	g := geo1CityDataset()
	db := encodeGEO1(&g)
	qs := append(slices.Clone(commonQueries), geo1Queries(&g)...)
	doc, err := expect(g.file, g.about, db, dedupe(qs))
	if err != nil {
		return nil, fmt.Errorf("conformance: %s: %w", g.file, err)
	}
	out = append(out, File{g.file, db}, File{jsonName(g.file), doc})
	slices.SortFunc(out, func(a, b File) int { return cmp.Compare(a.Name, b.Name) })
	return out, nil
}

// jsonName returns the name of the JSON document of database file f.
func jsonName(f string) string { return strings.TrimSuffix(f, ".geoc") + ".json" }

// buildDataset encodes d with internal/build and returns the database and
// the record id of each of d.records.
func buildDataset(d *dataset) ([]byte, []uint32, error) {
	b, err := build.New(d.opts, build.Source{Name: d.file, Type: d.srcType, BuildEpoch: sourceEpoch})
	if err != nil {
		return nil, nil, err
	}
	ids := make([]uint32, len(d.records))
	for i := range d.records {
		if ids[i], err = b.Record(&d.records[i]); err != nil {
			return nil, nil, fmt.Errorf("record %d: %w", i, err)
		}
	}
	for _, n := range sortedNets(d.nets) {
		if n.pfx.IsValid() {
			err = b.AddPrefix(n.pfx, ids[n.rec])
		} else {
			err = b.AddRange(n.first, n.last, ids[n.rec])
		}
		if err != nil {
			return nil, nil, err
		}
	}
	db, _, err := b.Finish()
	if err != nil {
		return nil, nil, err
	}
	if err := build.CheckDatabase(db); err != nil {
		return nil, nil, err
	}
	return db, ids, nil
}

// sortedNets returns nets in ascending address order, IPv4 first, an
// IPv4-mapped network counting as IPv4.
func sortedNets(nets []network) []network {
	s := slices.Clone(nets)
	slices.SortStableFunc(s, func(a, b network) int { return a.first.Unmap().Compare(b.first.Unmap()) })
	return s
}

// datasetQueries returns the queries of d: the common ones, the alias forms
// of d.aliasOf, the edges of every network that is not quiet, and d's own.
func datasetQueries(d *dataset) []query {
	qs := slices.Clone(commonQueries)
	for _, s := range d.aliasOf {
		qs = append(qs, aliasQueries(netip.MustParseAddr(s))...)
	}
	for _, n := range sortedNets(d.nets) {
		if n.quiet {
			continue
		}
		first, last := n.first.Unmap(), n.last.Unmap()
		what := first.String() + "-" + last.String()
		if n.pfx.IsValid() {
			what = netip.PrefixFrom(first, n.pfx.Bits()-(n.first.BitLen()-first.BitLen())).String()
		}
		qs = append(qs, query{first, "first address of " + what}, query{last, "last address of " + what})
		if p := first.Prev(); p.IsValid() {
			qs = append(qs, query{p, "just before " + what})
		}
		if nx := last.Next(); nx.IsValid() {
			qs = append(qs, query{nx, "just after " + what})
		}
	}
	return dedupe(append(qs, d.queries...))
}

// geo1Queries returns the edge queries of a GEO1 file's starts and its own.
func geo1Queries(g *geo1Dataset) []query {
	var qs []query
	for _, list := range [][]geo1Start{g.v4, g.v6} {
		for _, s := range list {
			qs = append(qs, query{s.start, "a range start"})
			if p := s.start.Prev(); p.IsValid() {
				qs = append(qs, query{p, "just before a range start"})
			}
		}
	}
	return append(qs, g.queries...)
}

// aliasQueries returns the IPv6 forms of IPv4 address a that the reader
// maps to it.
func aliasQueries(a netip.Addr) []query {
	v := a.As4()
	compat := [16]byte{12: v[0], 13: v[1], 14: v[2], 15: v[3]}
	sixToFour := [16]byte{0: 0x20, 1: 0x02, 2: v[0], 3: v[1], 4: v[2], 5: v[3]}
	teredo := [16]byte{0: 0x20, 1: 0x01, 4: v[0], 5: v[1], 6: v[2], 7: v[3]}
	return []query{
		{netip.AddrFrom16(a.As16()), "IPv4-mapped alias of " + a.String()},
		{netip.AddrFrom16(compat), "IPv4-compatible alias of " + a.String()},
		{netip.AddrFrom16(sixToFour), "6to4 alias of " + a.String()},
		{netip.AddrFrom16(teredo), "Teredo alias of " + a.String()},
	}
}

// dedupe drops repeated addresses, keeping the first position and joining
// the notes.
func dedupe(qs []query) []query {
	at := map[netip.Addr]int{}
	var out []query
	for _, q := range qs {
		i, ok := at[q.addr]
		if !ok {
			at[q.addr] = len(out)
			out = append(out, q)
			continue
		}
		if q.note != "" && !strings.Contains(out[i].note, q.note) {
			out[i].note += "; " + q.note
		}
	}
	return out
}

// expect opens db with the Go reader and returns the JSON document of its
// answers to qs.
func expect(file, about string, db []byte, qs []query) ([]byte, error) {
	r, err := ip2geo.OpenBytes(db)
	if err != nil {
		return nil, err
	}
	m := r.Metadata()
	langs := r.Languages()
	doc := FileJSON{
		SchemaVersion: SchemaVersion,
		File:          file,
		About:         about,
		Metadata: MetadataJSON{
			FormatVersion: m.FormatVersion, Profile: uint8(m.Profile), ProfileName: m.Profile.String(),
			LatScale: m.LatScale, BuildTime: unixOrZero(m.BuildTime.IsZero(), m.BuildTime.Unix()),
			SourceTime: unixOrZero(m.SourceTime.IsZero(), m.SourceTime.Unix()),
			SourceType: m.SourceType, Language: m.Language, Fields: uint32(m.Fields),
			Description: m.Description, Builder: m.Builder, Networks: m.Networks,
			DistinctRecords: m.DistinctRecords, Records: m.Records, Strings: m.Strings,
			IPv4Ranges: m.IPv4Ranges, IPv6Ranges: m.IPv6Ranges,
		},
		Languages: langs,
		Fields:    uint32(r.Fields()),
		Queries:   []QueryJSON{},
		Ranges:    []RangeEntryJSON{},
	}
	views := map[string]*ip2geo.Reader{}
	for _, l := range langs[min(1, len(langs)):] {
		if views[l], err = r.WithLanguage(l); err != nil {
			return nil, err
		}
	}
	div := math.Pow10(m.LatScale)
	for _, q := range qs {
		ip := q.addr
		qj := QueryJSON{IP: ip.String(), Note: q.note}
		if qj.Lookup, qj.Details, err = lookups(r, ip, div); err != nil {
			return nil, fmt.Errorf("%s: %w", ip, err)
		}
		a, ok := r.ASN(ip)
		qj.ASN.Found = ok
		if ok {
			qj.ASN.ASN = &ASNValueJSON{a.Number, a.Organization}
		} else if a != (ip2geo.ASN{}) {
			return nil, fmt.Errorf("%s: ASN not found but %+v", ip, a)
		}
		rg, ok := r.LookupRange(ip)
		qj.Range = RangeJSON{Found: ok, First: rg.First.String(), Last: rg.Last.String()}
		if len(views) > 0 {
			qj.Localized = map[string]LocalizedJSON{}
			for l, v := range views {
				var lj LocalizedJSON
				if lj.Lookup, lj.Details, err = lookups(v, ip, div); err != nil {
					return nil, fmt.Errorf("%s in %s: %w", ip, l, err)
				}
				qj.Localized[l] = lj
			}
		}
		doc.Queries = append(doc.Queries, qj)
	}
	for rg, d := range r.Ranges() {
		rec, err := recordJSON(d.Record, div)
		if err != nil {
			return nil, fmt.Errorf("range %s-%s: %w", rg.First, rg.Last, err)
		}
		doc.Ranges = append(doc.Ranges, RangeEntryJSON{
			First: rg.First.String(), Last: rg.Last.String(), Record: rec, Details: extrasJSON(&d),
		})
	}
	return encodeDoc(&doc)
}

func unixOrZero(zero bool, sec int64) int64 {
	if zero {
		return 0
	}
	return sec
}

// lookups returns r's Lookup and LookupDetails answers for ip. It fails
// when the two disagree on whether ip is found or on the Record, which
// the JSON stores once.
func lookups(r *ip2geo.Reader, ip netip.Addr, div float64) (LookupJSON, DetailsJSON, error) {
	rec, ok := r.Lookup(ip)
	d, dok := r.LookupDetails(ip)
	switch {
	case ok != dok:
		return LookupJSON{}, DetailsJSON{}, fmt.Errorf("found by Lookup %v, by LookupDetails %v", ok, dok)
	case rec != d.Record:
		return LookupJSON{}, DetailsJSON{}, fmt.Errorf("record of Lookup %+v, of LookupDetails %+v", rec, d.Record)
	case !ok && d != (ip2geo.Details{}):
		return LookupJSON{}, DetailsJSON{}, fmt.Errorf("not found but details %+v", d)
	}
	if !ok {
		return LookupJSON{}, DetailsJSON{}, nil
	}
	rj, err := recordJSON(rec, div)
	if err != nil {
		return LookupJSON{}, DetailsJSON{}, err
	}
	x := extrasJSON(&d)
	return LookupJSON{true, &rj}, DetailsJSON{true, &x}, nil
}

// recordJSON converts a Record, recovering the stored coordinate integers
// from the coordinates and the scale divisor div; it fails if they do not
// convert back exactly.
func recordJSON(r ip2geo.Record, div float64) (RecordJSON, error) {
	lat, lon := int64(math.Round(r.Latitude*div)), int64(math.Round(r.Longitude*div))
	if float64(lat)/div != r.Latitude || float64(lon)/div != r.Longitude {
		return RecordJSON{}, fmt.Errorf("coordinates %v, %v are not integers over %v", r.Latitude, r.Longitude, div)
	}
	return RecordJSON{
		CountryISO: r.CountryISO, CountryName: r.CountryName, Continent: r.Continent,
		CountryGeoNameID: r.CountryGeoNameID, RegionISO: r.RegionISO, RegionName: r.RegionName,
		City: r.City, CityGeoNameID: r.CityGeoNameID,
		Latitude: r.Latitude, LatitudeInt: lat, Longitude: r.Longitude, LongitudeInt: lon,
		AccuracyRadius: r.AccuracyRadius, TimeZone: r.TimeZone,
	}, nil
}

func extrasJSON(d *ip2geo.Details) ExtrasJSON {
	country := func(c ip2geo.Country) CountryJSON {
		return CountryJSON{c.ISO, c.Name, c.GeoNameID, c.IsInEuropeanUnion}
	}
	return ExtrasJSON{
		ContinentName: d.ContinentName, ContinentGeoNameID: d.ContinentGeoNameID,
		IsInEuropeanUnion: d.IsInEuropeanUnion, RegionGeoNameID: d.RegionGeoNameID,
		Region2ISO: d.Region2ISO, Region2Name: d.Region2Name, Region2GeoNameID: d.Region2GeoNameID,
		PostalCode: d.PostalCode, MetroCode: d.MetroCode,
		HasCoordinates: d.HasCoordinates, HasAccuracyRadius: d.HasAccuracyRadius,
		RegisteredCountry: country(d.RegisteredCountry), RepresentedCountry: country(d.RepresentedCountry),
		RepresentedCountryType: d.RepresentedCountryType,
		IsAnycast:              d.IsAnycast, IsAnonymousProxy: d.IsAnonymousProxy, IsSatelliteProvider: d.IsSatelliteProvider,
		ASN: ASNValueJSON{d.ASN.Number, d.ASN.Organization},
	}
}

// encodeDoc writes doc as JSON with one top-level key per line and one
// query or range per line, so that a change shows as a short diff.
func encodeDoc(doc *FileJSON) ([]byte, error) {
	queries := make([]any, len(doc.Queries))
	for i := range doc.Queries {
		queries[i] = doc.Queries[i]
	}
	ranges := make([]any, len(doc.Ranges))
	for i := range doc.Ranges {
		ranges[i] = doc.Ranges[i]
	}
	keys := []struct {
		name string
		v    any
		list []any // when set, written one element per line instead of v
	}{
		{"schema_version", doc.SchemaVersion, nil},
		{"file", doc.File, nil},
		{"about", doc.About, nil},
		{"metadata", doc.Metadata, nil},
		{"languages", doc.Languages, nil},
		{"fields", doc.Fields, nil},
		{"queries", nil, queries},
		{"ranges", nil, ranges},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// val writes v compactly; Encode ends it with a newline, dropped here.
	val := func(v any) error {
		if err := enc.Encode(v); err != nil {
			return err
		}
		buf.Truncate(buf.Len() - 1)
		return nil
	}
	buf.WriteString("{\n")
	for i, k := range keys {
		fmt.Fprintf(&buf, "  %q: ", k.name)
		if k.list == nil {
			if err := val(k.v); err != nil {
				return nil, err
			}
		} else {
			buf.WriteByte('[')
			for j, e := range k.list {
				buf.WriteString("\n    ")
				if err := val(e); err != nil {
					return nil, err
				}
				if j < len(k.list)-1 {
					buf.WriteByte(',')
				}
			}
			if len(k.list) > 0 {
				buf.WriteString("\n  ")
			}
			buf.WriteByte(']')
		}
		if i < len(keys)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString("}\n")
	return buf.Bytes(), nil
}
