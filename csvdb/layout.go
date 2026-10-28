package csvdb

import (
	"fmt"
	"strconv"

	"github.com/haxqer/ip2geo"
)

// Format is the column layout of a CSV source.
type Format uint8

const (
	// DBIPCity is DB-IP's IP to City Lite CSV (dbip-city-lite-*.csv):
	// ip_start, ip_end, continent, country, stateprov, city, latitude,
	// longitude, with the addresses in text form.
	DBIPCity Format = iota + 1
	// DBIPCountry is DB-IP's IP to Country Lite CSV
	// (dbip-country-lite-*.csv): ip_start, ip_end, country.
	DBIPCountry
	// DBIPASN is DB-IP's IP to ASN Lite CSV (dbip-asn-lite-*.csv):
	// ip_start, ip_end, as_number, as_organisation.
	DBIPASN
	// IP2Location is an IP2Location DB1, DB3, DB5, DB9 or DB11 CSV (LITE
	// or commercial), told apart by the number of columns of the first
	// row: ip_from, ip_to, country_code, country_name, then region_name and
	// city_name (DB3 on), latitude and longitude (DB5 on), zip_code (DB9
	// on) and time_zone (DB11), with the addresses as decimal numbers.
	IP2Location
	// IP2LocationASN is IP2Location's ASN CSV: ip_from, ip_to, cidr, asn,
	// as.
	IP2LocationASN
)

var formatNames = [...]string{
	DBIPCity:       "dbip-city",
	DBIPCountry:    "dbip-country",
	DBIPASN:        "dbip-asn",
	IP2Location:    "ip2location",
	IP2LocationASN: "ip2location-asn",
}

// String returns the name of f that ParseFormat accepts, e.g. "dbip-city".
func (f Format) String() string {
	if f.valid() {
		return formatNames[f]
	}
	return "Format(" + strconv.Itoa(int(f)) + ")"
}

func (f Format) valid() bool { return f >= DBIPCity && int(f) < len(formatNames) }

// ParseFormat returns the Format named s: "dbip-city", "dbip-country",
// "dbip-asn", "ip2location" or "ip2location-asn". Other names fail with
// ErrOptions.
func ParseFormat(s string) (Format, error) {
	for f, name := range formatNames {
		if name != "" && name == s {
			return Format(f), nil
		}
	}
	return 0, fmt.Errorf("csvdb: unknown format %q: %w", s, ErrOptions)
}

// layout describes the columns of a CSV layout. Columns 0 and 1 are the
// first and last address of a range, so a column index of 0 means that the
// layout does not have the column.
type layout struct {
	typ  string // the source type recorded in META
	cols int
	// decimal: the addresses are decimal numbers (IP2Location), not text.
	decimal bool
	asn     bool
	// maxProfile is the most detailed geo profile the layout has data for.
	maxProfile ip2geo.Profile
	// fields are the field sets the layout can fill.
	fields ip2geo.FieldSet
	// none is the value of the key column (the country, or the AS number
	// of an ASN layout) of a row without data, which the build skips.
	none string
	// dash: "-" in a text column means the value is missing.
	dash bool

	continent, country, countryName, region, city, lat, lon, postal, asNum, asOrg int
}

var (
	// DB-IP writes "ZZ" as the country of the reserved ranges (0.0.0.0/8,
	// 10.0.0.0/8, ::/3, ...); its .mmdb files leave them out.
	dbipCity = &layout{typ: "DBIP-City-Lite (CSV)", cols: 8, maxProfile: ip2geo.ProfileCity,
		fields: ip2geo.FieldLocation, none: "ZZ",
		continent: 2, country: 3, region: 4, city: 5, lat: 6, lon: 7}
	dbipCountry = &layout{typ: "DBIP-Country-Lite (CSV)", cols: 3, maxProfile: ip2geo.ProfileCountry,
		none: "ZZ", country: 2}
	dbipASN = &layout{typ: "DBIP-ASN-Lite (CSV)", cols: 4, asn: true, asNum: 2, asOrg: 3}

	// The IP2Location layouts. DB11 adds time_zone, a UTC offset such as
	// "+10:00" rather than an IANA zone, which the build does not keep.
	ip2lDB1 = &layout{typ: "IP2Location-DB1 (CSV)", cols: 4, decimal: true,
		maxProfile: ip2geo.ProfileCountry, none: "-", dash: true, country: 2, countryName: 3}
	ip2lDB3 = &layout{typ: "IP2Location-DB3 (CSV)", cols: 6, decimal: true,
		maxProfile: ip2geo.ProfileCity, none: "-", dash: true, country: 2, countryName: 3, region: 4, city: 5}
	ip2lDB5 = &layout{typ: "IP2Location-DB5 (CSV)", cols: 8, decimal: true,
		maxProfile: ip2geo.ProfileCity, fields: ip2geo.FieldLocation, none: "-", dash: true,
		country: 2, countryName: 3, region: 4, city: 5, lat: 6, lon: 7}
	ip2lDB9 = &layout{typ: "IP2Location-DB9 (CSV)", cols: 9, decimal: true,
		maxProfile: ip2geo.ProfileCity, fields: ip2geo.FieldLocation | ip2geo.FieldPostal, none: "-", dash: true,
		country: 2, countryName: 3, region: 4, city: 5, lat: 6, lon: 7, postal: 8}
	ip2lDB11 = &layout{typ: "IP2Location-DB11 (CSV)", cols: 10, decimal: true,
		maxProfile: ip2geo.ProfileCity, fields: ip2geo.FieldLocation | ip2geo.FieldPostal, none: "-", dash: true,
		country: 2, countryName: 3, region: 4, city: 5, lat: 6, lon: 7, postal: 8}
	ip2lASN = &layout{typ: "IP2Location-ASN (CSV)", cols: 5, decimal: true, asn: true, none: "-", dash: true,
		asNum: 3, asOrg: 4}

	ip2lLayouts = []*layout{ip2lDB1, ip2lDB3, ip2lDB5, ip2lDB9, ip2lDB11}
)

// layoutOf returns the layout of a format; for IP2Location, whose layout
// depends on the file, it returns the one with the most data (DB11).
func layoutOf(f Format) *layout {
	switch f {
	case DBIPCity:
		return dbipCity
	case DBIPCountry:
		return dbipCountry
	case DBIPASN:
		return dbipASN
	case IP2Location:
		return ip2lDB11
	default:
		return ip2lASN
	}
}

// ip2lLayout returns the IP2Location layout with cols columns.
func ip2lLayout(cols int) (*layout, bool) {
	for _, l := range ip2lLayouts {
		if l.cols == cols {
			return l, true
		}
	}
	return nil, false
}
