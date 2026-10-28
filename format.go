// Package ip2geo is a tiny, dependency-free, very fast reader for a compact
// IP-geolocation database built from a MaxMind GeoIP2/GeoLite2 City .mmdb file.
//
// The reader keeps the whole database resident in memory and resolves an
// address with a direct-indexed range search (the "DXR" layout), which is far
// faster than walking MaxMind's bit-by-bit search tree.
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
// # Validation and errors
//
// Open and OpenBytes check the whole input before returning a Reader: the
// header fields, every count against the bytes present, the record width,
// every record id, the order of the range starts, and that nothing trails the
// last table. Any input that opens is safe to look up in. Errors wrap
// ErrBadMagic, ErrUnsupportedVersion, ErrCorrupt or ErrTooLarge; test them
// with errors.Is.
//
// # IPv4 in IPv6 form
//
// MaxMind databases alias several IPv6 prefixes to their IPv4 data, and Lookup
// does the same: IPv4-mapped ::ffff:0:0/96, IPv4-compatible ::/96, 6to4
// 2002::/16 and Teredo 2001::/32 (the server address in bits 32-63) are looked
// up in the IPv4 table. NAT64 64:ff9b::/96 is not mapped.
//
// # Concurrency
//
// A Reader is safe for concurrent use, and Lookup does not allocate. Close
// releases nothing today and may overlap in-flight lookups; see Reader.Close.
//
// The reader package imports only the standard library.
package ip2geo

import "errors"

const (
	magic       = "GEO1"
	version     = 1
	headerLen   = 8       // magic, version, profile, lat/lon scale, reserved (0)
	maxLatScale = 7       // 180 * 10^8 overflows the int32 coordinates
	maxFileSize = 1 << 31 // largest input Open and OpenBytes accept
)

// Errors returned, wrapped with detail, by Open and OpenBytes.
var (
	// ErrBadMagic means the input does not start with the GEO1 magic.
	ErrBadMagic = errors.New("not a GEO1 database")
	// ErrUnsupportedVersion means the format version is not one this
	// package reads.
	ErrUnsupportedVersion = errors.New("unsupported format version")
	// ErrCorrupt means the input is truncated, has trailing bytes, or breaks a
	// structural rule: an unknown profile, a lat/lon scale above 7, a non-zero
	// reserved byte, a record width that does not match the profile, no
	// records, a record id out of range, or range starts not strictly
	// ascending.
	ErrCorrupt = errors.New("corrupt database")
	// ErrTooLarge means the input is larger than 2 GiB.
	ErrTooLarge = errors.New("database too large")
)

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
