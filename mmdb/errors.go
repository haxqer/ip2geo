package mmdb

import "github.com/haxqer/ip2geo/internal/build"

// Errors returned by Open and Build, wrapped in a message that starts with
// "mmdb: " and names the offending value. Test for them with errors.Is.
var (
	// ErrDatabaseType reports a source .mmdb whose metadata database_type is
	// not a City, Country or Enterprise database (for example GeoLite2-ASN or
	// GeoIP2-ISP), or a Country database used with ProfileRegion or
	// ProfileCity, which need the region and city data only a City (or
	// Enterprise) database has.
	ErrDatabaseType = build.ErrDatabaseType

	// ErrLanguage reports that Options.Lang is not one of the languages the
	// source database lists in its metadata, so every localized name would be
	// empty. The error message lists the available languages.
	ErrLanguage = build.ErrLanguage

	// ErrOptions reports an invalid Options value: an unknown Profile, a
	// LatScale above MaxLatScale, a BuildTime before 1970, or a Description
	// that is not valid UTF-8 or is longer than 65,535 bytes. It is returned
	// before the source database is opened and before any output file is
	// created.
	ErrOptions = build.ErrOptions

	// ErrStringTooLong reports a source string (a name, code or time zone) of
	// 65,536 bytes or more. The builder rejects such strings, as v0.1 did
	// when its file format had 16-bit string lengths, instead of storing
	// them. No output is written.
	ErrStringTooLong = build.ErrStringTooLong

	// ErrTooManyStrings reports a source whose strings do not fit the string
	// ids of the .geoc format: more than 65,535 distinct non-empty strings in
	// the fields stored as 16-bit ids (country and continent codes, country
	// names, region codes and names, time zones), more than 16,777,216
	// strings in all (city names are 24-bit ids), or more than 4 GiB of
	// string data. No output is written.
	ErrTooManyStrings = build.ErrTooManyStrings
)
