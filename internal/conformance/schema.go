package conformance

// The types below are the JSON documents of testdata/conformance, one per
// database file. testdata/conformance/README.md specifies them for readers
// in other languages; keep the two in step. Every field is always present
// (none is omitted when empty) except Query.Note and Query.Localized.

// SchemaVersion is the version of the JSON documents. It changes when a
// field is added, removed or changes meaning.
const SchemaVersion = 1

// FileJSON is the expected results for one database file.
type FileJSON struct {
	SchemaVersion int    `json:"schema_version"`
	File          string `json:"file"`
	About         string `json:"about"`
	// Metadata is Reader.Metadata.
	Metadata MetadataJSON `json:"metadata"`
	// Languages is Reader.Languages, null for a GEO1 file.
	Languages []string `json:"languages"`
	// Fields is Reader.Fields, the field set bits.
	Fields uint32 `json:"fields"`
	// Queries are the looked-up addresses and every answer for them.
	Queries []QueryJSON `json:"queries"`
	// Ranges is what Reader.Ranges yields, in order, in the primary
	// language.
	Ranges []RangeEntryJSON `json:"ranges"`
}

// MetadataJSON is ip2geo.Metadata. Times are Unix seconds, 0 for the zero
// time.
type MetadataJSON struct {
	FormatVersion   int    `json:"format_version"`
	Profile         uint8  `json:"profile"`
	ProfileName     string `json:"profile_name"`
	LatScale        int    `json:"lat_scale"`
	BuildTime       int64  `json:"build_time"`
	SourceTime      int64  `json:"source_time"`
	SourceType      string `json:"source_type"`
	Language        string `json:"language"`
	Fields          uint32 `json:"fields"`
	Description     string `json:"description"`
	Builder         string `json:"builder"`
	Networks        uint64 `json:"networks"`
	DistinctRecords uint64 `json:"distinct_records"`
	Records         int    `json:"records"`
	Strings         int    `json:"strings"`
	IPv4Ranges      int    `json:"ipv4_ranges"`
	IPv6Ranges      int    `json:"ipv6_ranges"`
}

// QueryJSON is one looked-up address.
type QueryJSON struct {
	IP   string `json:"ip"`
	Note string `json:"note,omitempty"`
	// Lookup, Details, ASN and Range are Reader.Lookup, LookupDetails,
	// ASN and LookupRange in the primary language.
	Lookup  LookupJSON  `json:"lookup"`
	Details DetailsJSON `json:"details"`
	ASN     ASNJSON     `json:"asn"`
	Range   RangeJSON   `json:"range"`
	// Localized holds Lookup and LookupDetails through WithLanguage for
	// every language but the primary one; absent for a file with fewer
	// than two languages.
	Localized map[string]LocalizedJSON `json:"localized,omitempty"`
}

// LocalizedJSON is Lookup and LookupDetails in one language.
type LocalizedJSON struct {
	Lookup  LookupJSON  `json:"lookup"`
	Details DetailsJSON `json:"details"`
}

// LookupJSON is a Lookup result: Record is null when Found is false.
type LookupJSON struct {
	Found  bool        `json:"found"`
	Record *RecordJSON `json:"record"`
}

// DetailsJSON is a LookupDetails result: Details is null when Found is
// false. The Record part of ip2geo.Details is not repeated: it equals the
// Lookup record of the same query and language.
type DetailsJSON struct {
	Found   bool        `json:"found"`
	Details *ExtrasJSON `json:"details"`
}

// ASNJSON is an ASN result: ASN is null when Found is false.
type ASNJSON struct {
	Found bool          `json:"found"`
	ASN   *ASNValueJSON `json:"asn"`
}

// RangeJSON is a LookupRange result. First and Last are always set (every
// query is a valid address); Found is false for an uncovered gap.
type RangeJSON struct {
	Found bool   `json:"found"`
	First string `json:"first"`
	Last  string `json:"last"`
}

// RangeEntryJSON is one range Reader.Ranges yields with its Details,
// split into the Record and the rest.
type RangeEntryJSON struct {
	First   string     `json:"first"`
	Last    string     `json:"last"`
	Record  RecordJSON `json:"record"`
	Details ExtrasJSON `json:"details"`
}

// RecordJSON is ip2geo.Record. LatitudeInt and LongitudeInt are the stored
// integers, the coordinates times 10^lat_scale: Latitude is exactly
// float64(LatitudeInt) / 10^lat_scale in IEEE 754 double precision.
type RecordJSON struct {
	CountryISO       string  `json:"country_iso"`
	CountryName      string  `json:"country_name"`
	Continent        string  `json:"continent"`
	CountryGeoNameID uint32  `json:"country_geoname_id"`
	RegionISO        string  `json:"region_iso"`
	RegionName       string  `json:"region_name"`
	City             string  `json:"city"`
	CityGeoNameID    uint32  `json:"city_geoname_id"`
	Latitude         float64 `json:"latitude"`
	LatitudeInt      int64   `json:"latitude_int"`
	Longitude        float64 `json:"longitude"`
	LongitudeInt     int64   `json:"longitude_int"`
	AccuracyRadius   uint16  `json:"accuracy_radius"`
	TimeZone         string  `json:"time_zone"`
}

// ExtrasJSON is ip2geo.Details without its Record.
type ExtrasJSON struct {
	ContinentName          string       `json:"continent_name"`
	ContinentGeoNameID     uint32       `json:"continent_geoname_id"`
	IsInEuropeanUnion      bool         `json:"is_in_european_union"`
	RegionGeoNameID        uint32       `json:"region_geoname_id"`
	Region2ISO             string       `json:"region2_iso"`
	Region2Name            string       `json:"region2_name"`
	Region2GeoNameID       uint32       `json:"region2_geoname_id"`
	PostalCode             string       `json:"postal_code"`
	MetroCode              uint16       `json:"metro_code"`
	HasCoordinates         bool         `json:"has_coordinates"`
	HasAccuracyRadius      bool         `json:"has_accuracy_radius"`
	RegisteredCountry      CountryJSON  `json:"registered_country"`
	RepresentedCountry     CountryJSON  `json:"represented_country"`
	RepresentedCountryType string       `json:"represented_country_type"`
	IsAnycast              bool         `json:"is_anycast"`
	IsAnonymousProxy       bool         `json:"is_anonymous_proxy"`
	IsSatelliteProvider    bool         `json:"is_satellite_provider"`
	ASN                    ASNValueJSON `json:"asn"`
}

// CountryJSON is ip2geo.Country.
type CountryJSON struct {
	ISO               string `json:"iso"`
	Name              string `json:"name"`
	GeoNameID         uint32 `json:"geoname_id"`
	IsInEuropeanUnion bool   `json:"is_in_european_union"`
}

// ASNValueJSON is ip2geo.ASN.
type ASNValueJSON struct {
	Number       uint32 `json:"number"`
	Organization string `json:"organization"`
}
