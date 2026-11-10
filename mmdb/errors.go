package mmdb

import "github.com/haxqer/ip2geo/internal/build"

// Errors returned by Open and Build, wrapped in a message that starts with
// "mmdb: " and names the offending value. Test for them with errors.Is.
var (
	// ErrDatabaseType reports a source .mmdb whose metadata database_type is
	// not a City, Country, Enterprise or ASN database (for example
	// GeoIP2-ISP); a Country database used with ProfileRegion or
	// ProfileCity, which need the region and city data only a City (or
	// Enterprise) database has; an ASN database used with a geo profile;
	// or ProfileASN with a database that is not an ASN one.
	ErrDatabaseType = build.ErrDatabaseType

	// ErrIPv4Alias reports a source network inside 2001::/32 (Teredo) or
	// 2002::/16 (6to4), whose addresses ip2geo.Reader looks up as the IPv4
	// address they embed, so the network's data could never be read. A
	// MaxMind or DB-IP database has none: it aliases both blocks to its
	// IPv4 data, and those aliases are not read as networks. No output is
	// written.
	ErrIPv4Alias = build.ErrIPv4Alias

	// ErrLanguage reports that Options.Lang, or a language of
	// Options.Languages, is not one of the languages the source database
	// lists in its metadata, so every name in it would be empty. The error
	// message lists the available languages.
	ErrLanguage = build.ErrLanguage

	// ErrOptions reports an invalid Options value: an unknown Profile, a
	// LatScale above MaxLatScale, a BuildTime before 1970, a Description
	// that is not valid UTF-8 or is longer than 65,535 bytes, Fields the
	// profile does not allow, an empty or repeated language in Languages,
	// more than one language with ProfileASN, or both Lang and Languages
	// set. It is returned before the source database is opened and before
	// any output file is created.
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
