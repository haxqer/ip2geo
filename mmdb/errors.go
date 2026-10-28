package mmdb

import "errors"

// Errors returned by Open and Build, wrapped in a message that starts with
// "mmdb: " and names the offending value. Test for them with errors.Is.
var (
	// ErrDatabaseType reports a source .mmdb whose metadata database_type is
	// not a City, Country or Enterprise database (for example GeoLite2-ASN or
	// GeoIP2-ISP), or a Country database used with ProfileRegion or
	// ProfileCity, which need the region and city data only a City (or
	// Enterprise) database has.
	ErrDatabaseType = errors.New("unsupported database type")

	// ErrLanguage reports that Options.Lang is not one of the languages the
	// source database lists in its metadata, so every localized name would be
	// empty. The error message lists the available languages.
	ErrLanguage = errors.New("language not in database")

	// ErrOptions reports an invalid Options value: an unknown Profile or a
	// LatScale above MaxLatScale. It is returned before the source database
	// is opened and before any output file is created.
	ErrOptions = errors.New("invalid options")

	// ErrStringTooLong reports a source string (a name, code or time zone) of
	// 65,536 bytes or more, which the .geoc format cannot store: string
	// lengths are 16-bit. No output is written.
	ErrStringTooLong = errors.New("string too long")
)
