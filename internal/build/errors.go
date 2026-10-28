package build

import "errors"

// The sentinel errors of the builders. The frontends export them under
// their own names (mmdb.ErrOptions is ErrOptions), so errors.Is works
// whichever package a caller tests against, and document when each one is
// returned.
var (
	ErrDatabaseType   = errors.New("unsupported database type")
	ErrLanguage       = errors.New("language not in database")
	ErrOptions        = errors.New("invalid options")
	ErrStringTooLong  = errors.New("string too long")
	ErrTooManyStrings = errors.New("too many strings")
)
