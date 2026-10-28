package mmdb

import (
	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/internal/build"
	"github.com/oschwald/maxminddb-golang/v2"
)

// srcCountry is the part of a source data record that every geo profile
// keeps, apart from the localized names.
type srcCountry struct {
	Continent struct {
		Code string `maxminddb:"code"`
	} `maxminddb:"continent"`
	Country struct {
		ISOCode   string `maxminddb:"iso_code"`
		GeoNameID uint32 `maxminddb:"geoname_id"`
	} `maxminddb:"country"`
}

// srcRecord is the part of a source data record that the base record of a
// geo profile keeps.
type srcRecord struct {
	srcCountry

	// City and Location are decoded for ProfileCity only.
	City struct {
		GeoNameID uint32 `maxminddb:"geoname_id"`
	} `maxminddb:"city"`
	Location struct {
		// A missing coordinate stays 0, which is what the builder has
		// always stored for it.
		Lat float64 `maxminddb:"latitude"`
		Lon float64 `maxminddb:"longitude"`
		Acc uint16  `maxminddb:"accuracy_radius"`
		TZ  string  `maxminddb:"time_zone"`
	} `maxminddb:"location"`
}

// srcASN is the record of an ASN database.
type srcASN struct {
	Number       uint32 `maxminddb:"autonomous_system_number"`
	Organization string `maxminddb:"autonomous_system_organization"`
}

// The name kinds of a source record, the index of each in srcDecoder.names.
const (
	nkCountry = iota
	nkContinent
	nkRegion
	nkRegion2
	nkCity
	nkRegistered
	nkRepresented
	nameKinds
)

// srcDecoder reads source data records into a build.Record for one profile,
// field set and language list, decoding only what the build keeps.
//
// The base record is decoded as it always was: one struct decode for the
// fields that are not names, and DecodePath for each name in the primary
// language (a name missing in it stays empty) and for the first
// subdivision's iso_code. Only when the build has field sets or more than
// one language does a second pass, extDecoder, read the rest.
type srcDecoder struct {
	profile ip2geo.Profile
	// DecodePath paths, built once per build.
	countryName, regionISO, regionName, cityName []any

	// names backs the name slices of the Record: one slot per language for
	// each name kind.
	names [nameKinds][]string
	src   srcRecord
	asn   srcASN
	ext   *extDecoder // nil without field sets and extra languages
}

func newSrcDecoder(p ip2geo.Profile, fields ip2geo.FieldSet, langs []string) *srcDecoder {
	lang := langs[0]
	d := &srcDecoder{
		profile:     p,
		countryName: []any{"country", "names", lang},
		regionISO:   []any{"subdivisions", 0, "iso_code"},
		regionName:  []any{"subdivisions", 0, "names", lang},
		cityName:    []any{"city", "names", lang},
	}
	for k := range d.names {
		d.names[k] = make([]string, len(langs))
	}
	if p != ip2geo.ProfileASN && (fields != 0 || len(langs) > 1) {
		d.ext = newExtDecoder(p, fields, langs)
	}
	return d
}

// decode reads the data record of res into out.
func (d *srcDecoder) decode(res maxminddb.Result, out *build.Record) error {
	*out = build.Record{}
	if d.profile == ip2geo.ProfileASN {
		d.asn = srcASN{}
		if err := res.Decode(&d.asn); err != nil {
			return err
		}
		out.ASN = ip2geo.ASN{Number: d.asn.Number, Organization: d.asn.Organization}
		return nil
	}

	for k := range d.names {
		clear(d.names[k])
	}
	r := &d.src
	*r = srcRecord{}
	if d.profile >= ip2geo.ProfileCity {
		if err := res.Decode(r); err != nil {
			return err
		}
	} else if err := res.Decode(&r.srcCountry); err != nil {
		return err
	}
	if err := res.DecodePath(&d.names[nkCountry][0], d.countryName...); err != nil {
		return err
	}
	if d.profile >= ip2geo.ProfileRegion {
		if err := res.DecodePath(&out.Subdivisions[0].ISOCode, d.regionISO...); err != nil {
			return err
		}
		if err := res.DecodePath(&d.names[nkRegion][0], d.regionName...); err != nil {
			return err
		}
	}
	if d.profile >= ip2geo.ProfileCity {
		if err := res.DecodePath(&d.names[nkCity][0], d.cityName...); err != nil {
			return err
		}
	}

	out.Continent = build.Continent{Code: r.Continent.Code, Names: d.names[nkContinent]}
	out.Country = build.Country{ISOCode: r.Country.ISOCode, Names: d.names[nkCountry], GeoNameID: r.Country.GeoNameID}
	out.Subdivisions[0].Names = d.names[nkRegion]
	out.Subdivisions[1].Names = d.names[nkRegion2]
	out.City = build.City{Names: d.names[nkCity], GeoNameID: r.City.GeoNameID}
	out.Location = build.Location{
		Latitude: r.Location.Lat, Longitude: r.Location.Lon,
		AccuracyRadius: r.Location.Acc, TimeZone: r.Location.TZ,
	}
	out.RegisteredCountry.Names = d.names[nkRegistered]
	out.RepresentedCountry.Names = d.names[nkRepresented]
	if d.ext != nil {
		return d.ext.decode(res, out)
	}
	return nil
}
