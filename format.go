// Package ip2geo is a tiny, dependency-free, very fast reader for a compact
// IP-geolocation database built from a MaxMind GeoIP2/GeoLite2 City .mmdb file.
//
// The reader keeps the whole database resident in a single []byte and resolves
// an address with a direct-indexed range search (the "DXR" layout), which is
// far faster than walking MaxMind's bit-by-bit search tree.
//
// # Basic usage
//
//	db, err := ip2geo.Open("city.geoc")
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer db.Close()
//
//	rec, ok := db.Lookup(netip.MustParseAddr("81.2.69.142"))
//	if ok {
//		fmt.Println(rec.CountryISO, rec.City, rec.Latitude, rec.Longitude)
//	}
//
// Build a .geoc file from a MaxMind .mmdb with the ip2geo command
// (see cmd/ip2geo) or the mmdb subpackage.
//
// The reader package imports only the standard library.
package ip2geo

const magic = "GEO1"
const version = 1

// Profile selects how much geo detail the database retains. Coarser profiles
// merge more adjacent IP ranges and produce dramatically smaller, faster files.
type Profile uint8

const (
	// ProfileCountry keeps country (ISO code, name, continent, geoname id).
	ProfileCountry Profile = 0
	// ProfileRegion keeps country plus the top-level subdivision (region/state).
	ProfileRegion Profile = 1
	// ProfileCity keeps country, region, city, and lat/lon, accuracy, time zone.
	ProfileCity Profile = 2
)

// String returns the profile name.
func (p Profile) String() string {
	switch p {
	case ProfileCountry:
		return "country"
	case ProfileRegion:
		return "region"
	case ProfileCity:
		return "city"
	default:
		return "unknown"
	}
}

// RecWidth returns the fixed on-disk width in bytes of one record for a profile.
// All string references are u32 ids into the strings table.
func RecWidth(p Profile) int {
	switch p {
	case ProfileCountry:
		return 4 * 4 // countryISO, countryName, continent, countryGeoname
	case ProfileRegion:
		return 4*4 + 4*2 // + regionISO, regionName
	case ProfileCity:
		return 4*4 + 4*2 + 4*6 // + cityName, cityGeoname, lat, lon, acc, tz
	default:
		return 0
	}
}

// Record is the decoded result of a lookup. Fields absent from the active
// profile (or missing in the source data) are left at their zero value.
type Record struct {
	CountryISO       string  // ISO 3166-1 alpha-2, e.g. "GB"
	CountryName      string  // localized country name
	Continent        string  // two-letter continent code, e.g. "EU"
	CountryGeoNameID uint32  // GeoNames id of the country
	RegionISO        string  // top subdivision ISO code, e.g. "ENG"
	RegionName       string  // top subdivision name
	City             string  // city name
	CityGeoNameID    uint32  // GeoNames id of the city
	Latitude         float64 // approximate latitude
	Longitude        float64 // approximate longitude
	AccuracyRadius   uint16  // accuracy radius in km
	TimeZone         string  // IANA time zone, e.g. "Europe/London"
}
