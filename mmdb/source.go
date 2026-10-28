package mmdb

import (
	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/internal/build"
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

	// names backs the name slices of the build.Record made by record.
	names [3]string
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

// record converts r to the build core's record. The name slices point
// into r, which must outlive out's use.
func (r *srcRecord) record(out *build.Record) {
	r.names = [3]string{r.countryName, r.regionName, r.cityName}
	*out = build.Record{
		Continent: build.Continent{Code: r.Continent.Code},
		Country: build.Country{
			ISOCode: r.Country.ISOCode, Names: r.names[0:1], GeoNameID: r.Country.GeoNameID,
		},
		City: build.City{Names: r.names[2:3], GeoNameID: r.City.GeoNameID},
		Location: build.Location{
			Latitude: r.Location.Lat, Longitude: r.Location.Lon,
			AccuracyRadius: r.Location.Acc, TimeZone: r.Location.TZ,
		},
	}
	out.Subdivisions[0] = build.Subdivision{ISOCode: r.regionISO, Names: r.names[1:2]}
}
