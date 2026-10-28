// Package ip2geo is a tiny, dependency-free, very fast reader for a compact
// IP-geolocation database built from a MaxMind GeoIP2/GeoLite2 City .mmdb file.
//
// The reader resolves an address with a direct-indexed range search (the
// "DXR" layout), which is far faster than walking MaxMind's bit-by-bit search
// tree. The file format is specified in docs/FORMAT.md.
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
// # Files and memory
//
// A GEO2 file (format version 2) is used in place: Open memory-maps it where
// the platform allows (Unix and Windows) and the lookup tables are the
// file's own sections. Only the strings are copied to the Go heap, so a
// returned Record never refers to the mapping, plus a few small indexes
// built at open. The mapping is released by the garbage collector once the
// Reader is unreachable. A GEO1 file (version 1, written by v0.1.x) is
// converted at open into the same layout on the Go heap and its mapping is
// released straight away.
//
// A mapped file must not be modified or truncated while a Reader uses it:
// replace it by writing a new file and renaming it over the old one, as the
// mmdb builder does. Open with NoMmap, or use OpenBytes, to keep the whole
// database on the heap instead.
//
// # Validation and errors
//
// Open and OpenBytes check the whole input before returning a Reader. For a
// GEO2 file that is every rule of the "Reader requirements" in
// docs/FORMAT.md: the header, the section directory, the section lengths,
// a CRC-32C per section (see SkipChecksum), every string and record id, and
// the order of the range starts. For a GEO1 file it is the header fields,
// every count against the bytes present, the record width, every record id,
// the order of the range starts, and that nothing trails the last table.
// Any input that opens is safe to look up in. Errors wrap ErrBadMagic,
// ErrUnsupportedVersion, ErrCorrupt or ErrTooLarge; test them with errors.Is.
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
// releases nothing and may overlap in-flight lookups; see Reader.Close.
//
// The reader package imports only the standard library.
package ip2geo

import (
	"errors"
	"time"
)

// GEO1 header constants. GEO2's live in internal/format.
const (
	magic       = "GEO1"
	version     = 1
	headerLen   = 8       // magic, version, profile, lat/lon scale, reserved (0)
	maxLatScale = 7       // 180 * 10^8 overflows the int32 coordinates
	maxFileSize = 1 << 31 // largest input Open and OpenBytes accept
)

// Errors returned, wrapped with detail, by Open and OpenBytes.
var (
	// ErrBadMagic means the input starts with neither the GEO2 nor the GEO1
	// magic.
	ErrBadMagic = errors.New("not an ip2geo database")
	// ErrUnsupportedVersion means the format version is not one this
	// package reads.
	ErrUnsupportedVersion = errors.New("unsupported format version")
	// ErrCorrupt means the input is truncated, fails a checksum, or breaks a
	// structural rule of its format: an unknown profile, a lat/lon scale
	// above 7, a non-zero reserved byte, a bad section directory or section
	// length, a record width that does not match the profile, no records, a
	// string or record id out of range, or range starts not strictly
	// ascending. A GEO1 input with trailing bytes is corrupt as well.
	ErrCorrupt = errors.New("corrupt database")
	// ErrTooLarge means the input is larger than 2 GiB, or is a GEO1 database
	// whose strings do not fit the GEO2 layout it is converted to: more than
	// 65535 distinct strings in country, region and time zone fields, or
	// more than 2^24-1 distinct strings in all.
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

// RecWidth returns the width in bytes of one record of a GEO1 (v0.1.x) file
// for a profile, in which every field is a u32. GEO2 files pack their
// records into 10, 14 or 33 bytes; docs/FORMAT.md has the layout.
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

// Metadata describes an open database: its file header, the META section of
// a GEO2 file, and the sizes of its tables. A GEO1 file has no META section,
// so for one the fields from BuildTime to DistinctRecords are zero.
type Metadata struct {
	// FormatVersion is the file format version: 2 for GEO2, 1 for GEO1.
	FormatVersion int
	// Profile is the precision profile the database was built with.
	Profile Profile
	// LatScale is the decimal scale of the stored coordinates, 0 to 7: they
	// are degrees × 10^LatScale, rounded to an integer.
	LatScale int

	// BuildTime is when the file was built, in UTC; zero when unknown.
	BuildTime time.Time
	// SourceTime is when the source database was built, in UTC; zero when
	// unknown.
	SourceTime time.Time
	// SourceType names the source database, e.g. "GeoIP2-City".
	SourceType string
	// Language is the language of the localized names, e.g. "en".
	Language string
	// Description is free text; it may be empty.
	Description string
	// Builder names the program that built the file, e.g. "ip2geo/v0.2.0".
	Builder string
	// Networks is the number of networks read from the source.
	Networks uint64
	// DistinctRecords is the number of distinct source data records the
	// builder saw. It is informational.
	DistinctRecords uint64

	// Records is the number of records, including record 0, which stands
	// for "no data".
	Records int
	// Strings is the number of strings in the file's string table.
	Strings int
	// IPv4Ranges is the number of IPv4 range starts (boundaries).
	IPv4Ranges int
	// IPv6Ranges is the number of IPv6 range starts (boundaries).
	IPv6Ranges int
}
