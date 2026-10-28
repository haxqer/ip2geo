package build

import (
	"math"

	"github.com/haxqer/ip2geo"
)

// Record is one source data record: the fields a database record can hold.
// The builder keeps the ones the build's profile stores and ignores the
// others. Names are indexed by language, in Options.Languages order; a
// name slice shorter than the language list, and an empty string, mean
// the source has no name in that language.
//
// A frontend may reuse one Record (and its name slices) for every source
// record: Record does not retain it.
type Record struct {
	Continent Continent
	Country   Country
	// Subdivisions are the first two subdivisions of the source, the
	// largest first. Region profiles keep the first one's ISO code and
	// name.
	Subdivisions [2]Subdivision
	City         City
	Location     Location
}

// Continent is a continent of a Record.
type Continent struct {
	Code string // two-letter continent code, e.g. "EU"
}

// Country is a country of a Record.
type Country struct {
	ISOCode   string
	Names     []string
	GeoNameID uint32
}

// Subdivision is a subdivision (region) of a Record.
type Subdivision struct {
	ISOCode string
	Names   []string
}

// City is the city of a Record.
type City struct {
	Names     []string
	GeoNameID uint32
}

// Location is the location of a Record. A missing coordinate is 0.
type Location struct {
	Latitude, Longitude float64
	AccuracyRadius      uint16
	TimeZone            string
}

// name returns names[j], or "" when names has no entry j.
func name(names []string, j int) string {
	if j < len(names) {
		return names[j]
	}
	return ""
}

// recKey is a record: its string ids and its other fields.
type recKey struct {
	countryISO, countryName, continent, regionISO, regionName, cityName, tz uint32
	countryGeo, cityGeo                                                     uint32
	lat, lon                                                                int32
	acc                                                                     uint16
}

// key returns the record key of r for profile p, with provisional string
// ids. It interns the strings in a fixed order, so string ids depend only on
// the order in which records are first seen, and marks every string but the
// city name as used by a 16-bit field.
func (r *Record) key(p ip2geo.Profile, scale float64, strs *interner) recKey {
	var k recKey
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
