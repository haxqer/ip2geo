//! The error type of [`Reader`](crate::Reader) construction.

use std::fmt;
use std::io;

/// Why a database could not be opened, or a language selected.
///
/// The variants mirror the sentinel errors of the Go reader (`ErrBadMagic`,
/// `ErrUnsupportedVersion`, `ErrCorrupt`, `ErrTooLarge`, `ErrLanguage`);
/// each carries a message saying what exactly was wrong.
#[derive(Debug)]
#[non_exhaustive]
pub enum Error {
    /// Reading the file failed.
    Io(io::Error),
    /// The input starts with neither the GEO2 nor the GEO1 magic.
    BadMagic(String),
    /// The format version is not one this crate reads, or the file has a
    /// field set this crate does not know (a later revision's).
    UnsupportedVersion(String),
    /// The input is truncated, fails a checksum, or breaks a structural rule
    /// of its format (docs/FORMAT.md, "Reader requirements").
    Corrupt(String),
    /// The input is larger than 2 GiB, or is a GEO1 database whose strings do
    /// not fit the GEO2 layout it is converted to.
    TooLarge(String),
    /// A language passed to [`OpenOptions::language`](crate::OpenOptions::language)
    /// or [`Reader::with_language`](crate::Reader::with_language) is not one
    /// of the database's languages.
    Language(String),
}

impl Error {
    pub(crate) fn corrupt(msg: impl Into<String>) -> Self {
        Error::Corrupt(msg.into())
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::Io(e) => write!(f, "ip2geo: {e}"),
            Error::BadMagic(m) => write!(f, "ip2geo: {m}: not an ip2geo database"),
            Error::UnsupportedVersion(m) => write!(f, "ip2geo: {m}: unsupported format version"),
            Error::Corrupt(m) => write!(f, "ip2geo: {m}: corrupt database"),
            Error::TooLarge(m) => write!(f, "ip2geo: {m}: database too large"),
            Error::Language(m) => write!(f, "ip2geo: {m}: language not in database"),
        }
    }
}

impl std::error::Error for Error {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            Error::Io(e) => Some(e),
            _ => None,
        }
    }
}

impl From<io::Error> for Error {
    fn from(e: io::Error) -> Self {
        Error::Io(e)
    }
}

/// `Result<T, ip2geo::Error>`.
pub type Result<T> = std::result::Result<T, Error>;
