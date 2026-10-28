package csvdb

import (
	"errors"

	"github.com/haxqer/ip2geo/internal/build"
)

// Errors returned by Open and Build, wrapped in a message that starts with
// "csvdb: " and names the offending value. Test for them with errors.Is.
var (
	// ErrDatabaseType reports a profile the source layout has no data for:
	// ProfileRegion or ProfileCity with a country-only layout (DBIPCountry,
	// an IP2Location DB1 file), a geo profile with an ASN layout, or
	// ProfileASN with a geo layout.
	ErrDatabaseType = build.ErrDatabaseType

	// ErrOptions reports an invalid Options value: an unknown Format or
	// Profile, a LatScale above MaxLatScale, a BuildTime before 1970, a
	// Description that is not valid UTF-8 or is longer than 65,535 bytes,
	// or Fields that the profile does not allow or the layout cannot fill.
	// It is returned before the source is opened and before any output file
	// is created, except for Fields that an IP2Location file's layout
	// cannot fill, which are known only once its first row is read (still
	// before any output file is created).
	ErrOptions = build.ErrOptions

	// ErrStringTooLong reports a source string (a name or code) of 65,536
	// bytes or more. No output is written.
	ErrStringTooLong = build.ErrStringTooLong

	// ErrTooManyStrings reports a source whose strings do not fit the string
	// ids of the .geoc format (see mmdb.ErrTooManyStrings). No output is
	// written.
	ErrTooManyStrings = build.ErrTooManyStrings

	// ErrSyntax reports a malformed source file: a line with the wrong
	// number of fields or a bad quote, an address or number that does not
	// parse, a range whose first address is above its last, a coordinate
	// out of range, rows that are not in ascending address order or whose
	// ranges overlap, or a file without rows. The message names the file
	// and the line, as "csvdb: file:line: ...". No output is written.
	ErrSyntax = errors.New("malformed CSV source")
)
