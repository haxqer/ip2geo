package build

import (
	"errors"

	"github.com/haxqer/ip2geo"
)

// The sentinel errors of the builders. The frontends export them under
// their own names (mmdb.ErrOptions is ErrOptions), so errors.Is works
// whichever package a caller tests against, and document when each one is
// returned.
var (
	ErrDatabaseType = errors.New("unsupported database type")
	// ErrLanguage is ip2geo.ErrLanguage itself, not a copy with the same
	// text, so a builder error also matches errors.Is(err,
	// ip2geo.ErrLanguage).
	ErrLanguage       = ip2geo.ErrLanguage
	ErrOptions        = errors.New("invalid options")
	ErrStringTooLong  = errors.New("string too long")
	ErrTooManyStrings = errors.New("too many strings")
)
