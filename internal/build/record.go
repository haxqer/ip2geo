package build

import (
	"math"

	"github.com/haxqer/ip2geo"
)

// Record is one source data record: every field a geo or ASN database
// record can hold. The builder keeps the ones the build's profile and field
// sets store and ignores the others. Names are indexed by language, in
// Options.Languages order; a name slice shorter than the language list, and
// an empty string, mean the source has no name in that language.
//
// A frontend may reuse one Record (and its name slices) for every source
// record: Builder.Record does not retain it.
type Record struct {
	Continent Continent
	Country   Country
	// Subdivisions are the first two subdivisions of the source, the
	// largest first. The region profile keeps the first one's ISO code and
	// name, and ip2geo.FieldSubdivisions the rest of both.
	Subdivisions [2]Subdivision
	City         City
	Location     Location
	PostalCode   string

	// RegisteredCountry and RepresentedCountry are kept with
	// ip2geo.FieldRegistered; RepresentedType is the represented
	// country's type, e.g. "military".
	RegisteredCountry  Country
	RepresentedCountry Country
	RepresentedType    string

	Traits Traits

	// ASN is the record of a ProfileASN build.
	ASN ip2geo.ASN
}

// Continent is a continent of a Record.
type Continent struct {
	Code      string // two-letter continent code, e.g. "EU"
	Names     []string
	GeoNameID uint32
}

// Country is a country of a Record.
type Country struct {
	ISOCode           string
	Names             []string
	GeoNameID         uint32
	IsInEuropeanUnion bool
}

// Subdivision is a subdivision (region) of a Record.
type Subdivision struct {
	ISOCode   string
	Names     []string
	GeoNameID uint32
}

// City is the city of a Record.
type City struct {
	Names     []string
	GeoNameID uint32
}

// Location is the location of a Record. A missing coordinate is 0; the
// Has fields say whether the source has the values.
type Location struct {
	Latitude, Longitude float64
	AccuracyRadius      uint16
	TimeZone            string
	MetroCode           uint16
	HasCoordinates      bool // latitude and longitude are both present
	HasAccuracyRadius   bool
}

// Traits are the network traits of a Record.
type Traits struct {
	IsAnycast           bool
	IsAnonymousProxy    bool
	IsSatelliteProvider bool
}

// name returns names[j], or "" when names has no entry j.
func name(names []string, j int) string {
	if j < len(names) {
		return names[j]
	}
	return ""
}

// recKey is a record: its string ids and its other fields, and the id of
// its extension entry (see xTable), 0 when the build has none. A ProfileASN
// record keeps the AS number in countryGeo and the organization in
// cityName, the base fields with the same width.
type recKey struct {
	countryISO, countryName, continent, regionISO, regionName, cityName, tz uint32
	countryGeo, cityGeo                                                     uint32
	lat, lon                                                                int32
	acc                                                                     uint16
	ext                                                                     uint32
}

// key returns the record key of r for profile p, with provisional string
// ids. It interns the strings in a fixed order, so string ids depend only on
// the order in which records are first seen, and marks every string of a
// 16-bit field as such.
func (r *Record) key(p ip2geo.Profile, scale float64, strs *interner) recKey {
	var k recKey
	if p == ip2geo.ProfileASN {
		k.countryGeo = r.ASN.Number
		k.cityName = strs.id(r.ASN.Organization)
		return k
	}
	k.countryISO = strs.id16(r.Country.ISOCode)
	k.countryName = strs.id16(name(r.Country.Names, 0))
	k.continent = strs.id16(r.Continent.Code)
	k.countryGeo = r.Country.GeoNameID
	if p >= ip2geo.ProfileRegion {
		k.regionISO = strs.id16(r.Subdivisions[0].ISOCode)
		k.regionName = strs.id16(name(r.Subdivisions[0].Names, 0))
	}
	if p >= ip2geo.ProfileCity {
		k.cityName = strs.id(name(r.City.Names, 0))
		k.cityGeo = r.City.GeoNameID
		k.lat = int32(math.Round(r.Location.Latitude * scale))
		k.lon = int32(math.Round(r.Location.Longitude * scale))
		k.acc = r.Location.AccuracyRadius
		k.tz = strs.id16(r.Location.TimeZone)
	}
	return k
}
