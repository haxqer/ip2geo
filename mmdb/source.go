package mmdb

import (
	"math"

	"github.com/haxqer/ip2geo"
	"github.com/oschwald/maxminddb-golang/v2"
)

// srcCountry is the part of a source data record that every profile keeps,
// apart from the localized names.
type srcCountry struct {
	Continent struct {
		Code string `maxminddb:"code"`
	} `maxminddb:"continent"`
	Country struct {
		ISOCode   string `maxminddb:"iso_code"`
		GeoNameID uint32 `maxminddb:"geoname_id"`
	} `maxminddb:"country"`
}

// srcRecord is the part of a source data record that the builder keeps.
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

	// Read with DecodePath by srcDecoder: the names in one language, and
	// the first (largest) subdivision.
	countryName, regionISO, regionName, cityName string
}

// srcDecoder reads source data records for one profile and language. It
// decodes only what the profile keeps: the names maps are not decoded,
// DecodePath reads the one name in the build's language (a name missing in
// it stays empty, as a lookup in the decoded map did), and of the
// subdivisions only the first one's iso_code and name are read.
type srcDecoder struct {
	profile ip2geo.Profile
	// DecodePath paths, built once per build.
	countryName, regionISO, regionName, cityName []any
}

func newSrcDecoder(p ip2geo.Profile, lang string) *srcDecoder {
	return &srcDecoder{
		profile:     p,
		countryName: []any{"country", "names", lang},
		regionISO:   []any{"subdivisions", 0, "iso_code"},
		regionName:  []any{"subdivisions", 0, "names", lang},
		cityName:    []any{"city", "names", lang},
	}
}

// decode reads the data record of res into r.
func (d *srcDecoder) decode(res maxminddb.Result, r *srcRecord) error {
	*r = srcRecord{}
	if d.profile >= ip2geo.ProfileCity {
		if err := res.Decode(r); err != nil {
			return err
		}
	} else if err := res.Decode(&r.srcCountry); err != nil {
		return err
	}
	if err := res.DecodePath(&r.countryName, d.countryName...); err != nil {
		return err
	}
	if d.profile >= ip2geo.ProfileRegion {
		if err := res.DecodePath(&r.regionISO, d.regionISO...); err != nil {
			return err
		}
		if err := res.DecodePath(&r.regionName, d.regionName...); err != nil {
			return err
		}
	}
	if d.profile >= ip2geo.ProfileCity {
		if err := res.DecodePath(&r.cityName, d.cityName...); err != nil {
			return err
		}
	}
	return nil
}

// key returns the record key of r for profile p. It interns the strings in
// a fixed order, so string ids depend only on the order in which records
// are first seen.
func (r *srcRecord) key(p ip2geo.Profile, scale float64, strs *interner) recKey {
	var k recKey
	k.countryISO = strs.id(r.Country.ISOCode)
	k.countryName = strs.id(r.countryName)
	k.continent = strs.id(r.Continent.Code)
	k.countryGeo = r.Country.GeoNameID
	if p >= ip2geo.ProfileRegion {
		k.regionISO = strs.id(r.regionISO)
		k.regionName = strs.id(r.regionName)
	}
	if p >= ip2geo.ProfileCity {
		k.cityName = strs.id(r.cityName)
		k.cityGeo = r.City.GeoNameID
		k.lat = int32(math.Round(r.Location.Lat * scale))
		k.lon = int32(math.Round(r.Location.Lon * scale))
		k.acc = r.Location.Acc
		k.tz = strs.id(r.Location.TZ)
	}
	return k
}
