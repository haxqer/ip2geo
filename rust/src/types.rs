//! The values the reader returns.

use std::fmt;
use std::net::IpAddr;
use std::ops::{BitAnd, BitOr, BitOrAssign};
use std::time::SystemTime;

/// How much geo detail a database keeps (the header profile byte).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
#[repr(u8)]
pub enum Profile {
    /// Country: ISO code, name, continent, GeoNames id.
    Country = 0,
    /// Country plus the top-level subdivision (region or state).
    Region = 1,
    /// Country, region, city, coordinates, accuracy radius and time zone.
    City = 2,
    /// Autonomous system number and organization (format revision 2.1).
    Asn = 3,
}

impl Profile {
    pub(crate) fn from_u8(p: u8) -> Option<Profile> {
        match p {
            0 => Some(Profile::Country),
            1 => Some(Profile::Region),
            2 => Some(Profile::City),
            3 => Some(Profile::Asn),
            _ => None,
        }
    }

    /// The profile name as the Go reader prints it: `"country"`,
    /// `"region"`, `"city"` or `"asn"`.
    ///
    /// ```
    /// assert_eq!(ip2geo::Profile::City.name(), "city");
    /// ```
    pub fn name(self) -> &'static str {
        match self {
            Profile::Country => "country",
            Profile::Region => "region",
            Profile::City => "city",
            Profile::Asn => "asn",
        }
    }
}

impl fmt::Display for Profile {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.name())
    }
}

/// Optional field sets stored beside a geo profile's [`Record`] fields (the
/// XFIELDS bits of a revision 2.1 file). Each set is a bit; combine them
/// with `|`.
///
/// ```
/// use ip2geo::FieldSet;
/// let f = FieldSet::EU | FieldSet::POSTAL;
/// assert!(f.contains(FieldSet::EU));
/// assert!(!f.contains(FieldSet::METRO));
/// assert_eq!(f.bits(), 0b1_0001);
/// ```
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash)]
pub struct FieldSet(u32);

impl FieldSet {
    /// No field set.
    pub const NONE: FieldSet = FieldSet(0);
    /// `country.is_in_european_union`.
    pub const EU: FieldSet = FieldSet(1 << 0);
    /// The continent name and GeoNames id.
    pub const CONTINENT: FieldSet = FieldSet(1 << 1);
    /// `registered_country` and `represented_country`.
    pub const REGISTERED: FieldSet = FieldSet(1 << 2);
    /// The first subdivision's GeoNames id and the second subdivision.
    pub const SUBDIVISIONS: FieldSet = FieldSet(1 << 3);
    /// The postal code.
    pub const POSTAL: FieldSet = FieldSet(1 << 4);
    /// `location.metro_code`.
    pub const METRO: FieldSet = FieldSet(1 << 5);
    /// `is_anycast`, `is_anonymous_proxy`, `is_satellite_provider`.
    pub const TRAITS: FieldSet = FieldSet(1 << 6);
    /// Whether the coordinates and the accuracy radius are present.
    pub const LOCATION: FieldSet = FieldSet(1 << 7);
    /// Every field set.
    pub const ALL: FieldSet = FieldSet((1 << 8) - 1);

    /// The raw bits: bit 0 EU, 1 continent, 2 registered, 3 subdivisions,
    /// 4 postal, 5 metro, 6 traits, 7 location.
    pub const fn bits(self) -> u32 {
        self.0
    }

    /// The set with these bits, keeping unknown ones.
    pub const fn from_bits_retain(bits: u32) -> FieldSet {
        FieldSet(bits)
    }

    /// Whether every set in `other` is in `self`.
    pub const fn contains(self, other: FieldSet) -> bool {
        self.0 & other.0 == other.0
    }

    /// Whether no set is present.
    pub const fn is_empty(self) -> bool {
        self.0 == 0
    }
}

impl BitOr for FieldSet {
    type Output = FieldSet;
    fn bitor(self, rhs: FieldSet) -> FieldSet {
        FieldSet(self.0 | rhs.0)
    }
}

impl BitOrAssign for FieldSet {
    fn bitor_assign(&mut self, rhs: FieldSet) {
        self.0 |= rhs.0;
    }
}

impl BitAnd for FieldSet {
    type Output = FieldSet;
    fn bitand(self, rhs: FieldSet) -> FieldSet {
        FieldSet(self.0 & rhs.0)
    }
}

/// The decoded result of a lookup. Fields absent from the database's
/// profile, or missing in its source, are empty or zero.
///
/// The strings borrow from the [`Reader`](crate::Reader) (its string table,
/// copied out of the file at open), so a lookup allocates nothing.
#[derive(Clone, Copy, Debug, Default, PartialEq)]
#[non_exhaustive]
pub struct Record<'a> {
    /// ISO 3166-1 alpha-2 code, e.g. `"GB"`.
    pub country_iso: &'a str,
    /// Country name, in the reader's language.
    pub country_name: &'a str,
    /// Two-letter continent code, e.g. `"EU"`.
    pub continent: &'a str,
    /// GeoNames id of the country.
    pub country_geoname_id: u32,
    /// Top subdivision ISO code, e.g. `"ENG"`.
    pub region_iso: &'a str,
    /// Top subdivision name, in the reader's language.
    pub region_name: &'a str,
    /// City name, in the reader's language.
    pub city: &'a str,
    /// GeoNames id of the city.
    pub city_geoname_id: u32,
    /// Latitude in degrees: the stored integer divided by `10^lat_scale`.
    pub latitude: f64,
    /// Longitude in degrees: the stored integer divided by `10^lat_scale`.
    pub longitude: f64,
    /// Accuracy radius in km.
    pub accuracy_radius: u16,
    /// IANA time zone, e.g. `"Europe/London"`.
    pub time_zone: &'a str,
}

/// A country other than the located one: the country a network is
/// registered to, or the one a represented population belongs to.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Country<'a> {
    /// ISO 3166-1 alpha-2 code.
    pub iso: &'a str,
    /// Name, in the reader's language.
    pub name: &'a str,
    /// GeoNames id.
    pub geoname_id: u32,
    /// Whether the country is in the European Union.
    pub is_in_european_union: bool,
}

/// The autonomous system of a network.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Asn<'a> {
    /// Autonomous system number.
    pub number: u32,
    /// Autonomous system organization.
    pub organization: &'a str,
}

/// A [`Record`] with every optional field. Fields of a field set the
/// database does not have, and fields missing in the source, are empty,
/// zero or false.
#[derive(Clone, Copy, Debug, Default, PartialEq)]
#[non_exhaustive]
pub struct Details<'a> {
    /// The record [`Reader::lookup`](crate::Reader::lookup) returns.
    pub record: Record<'a>,
    /// Continent name ([`FieldSet::CONTINENT`]).
    pub continent_name: &'a str,
    /// Continent GeoNames id ([`FieldSet::CONTINENT`]).
    pub continent_geoname_id: u32,
    /// Whether the country is in the EU ([`FieldSet::EU`]).
    pub is_in_european_union: bool,
    /// GeoNames id of the first subdivision ([`FieldSet::SUBDIVISIONS`]).
    pub region_geoname_id: u32,
    /// ISO code of the second subdivision ([`FieldSet::SUBDIVISIONS`]).
    pub region2_iso: &'a str,
    /// Name of the second subdivision ([`FieldSet::SUBDIVISIONS`]).
    pub region2_name: &'a str,
    /// GeoNames id of the second subdivision ([`FieldSet::SUBDIVISIONS`]).
    pub region2_geoname_id: u32,
    /// Postal code ([`FieldSet::POSTAL`]).
    pub postal_code: &'a str,
    /// Metro code ([`FieldSet::METRO`]).
    pub metro_code: u16,
    /// Whether the source has the coordinates ([`FieldSet::LOCATION`]).
    pub has_coordinates: bool,
    /// Whether the source has the accuracy radius ([`FieldSet::LOCATION`]).
    pub has_accuracy_radius: bool,
    /// The country the network is registered to ([`FieldSet::REGISTERED`]).
    pub registered_country: Country<'a>,
    /// The country a represented population belongs to
    /// ([`FieldSet::REGISTERED`]).
    pub represented_country: Country<'a>,
    /// The kind of represented country, e.g. `"military"`
    /// ([`FieldSet::REGISTERED`]).
    pub represented_country_type: &'a str,
    /// [`FieldSet::TRAITS`].
    pub is_anycast: bool,
    /// [`FieldSet::TRAITS`].
    pub is_anonymous_proxy: bool,
    /// [`FieldSet::TRAITS`].
    pub is_satellite_provider: bool,
    /// The autonomous system, in a [`Profile::Asn`] database only.
    pub asn: Asn<'a>,
}

/// An inclusive address range. `first` and `last` are in the same family;
/// a range need not be one CIDR prefix.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct IpRange {
    /// The first address of the range.
    pub first: IpAddr,
    /// The last address of the range.
    pub last: IpAddr,
}

/// Describes an open database: its header, the META section of a GEO2 file
/// and the sizes of its tables. A GEO1 file has no META section, so for one
/// the fields from `build_time` to `distinct_records` are `None`, empty or
/// zero.
#[derive(Clone, Debug, PartialEq)]
#[non_exhaustive]
pub struct Metadata {
    /// The file format version: 2 for GEO2, 1 for GEO1.
    pub format_version: u8,
    /// The profile the database was built with.
    pub profile: Profile,
    /// The decimal scale of the stored coordinates, 0 to 7.
    pub lat_scale: u8,
    /// When the file was built; `None` when unknown.
    pub build_time: Option<SystemTime>,
    /// When the source database was built; `None` when unknown.
    pub source_time: Option<SystemTime>,
    /// The source database, e.g. `"GeoIP2-City"`.
    pub source_type: String,
    /// The primary language of the names, e.g. `"en"`.
    pub language: String,
    /// The database's optional field sets.
    pub fields: FieldSet,
    /// Free text; may be empty.
    pub description: String,
    /// The program that built the file, e.g. `"ip2geo/v0.2.0"`.
    pub builder: String,
    /// The number of networks read from the source.
    pub networks: u64,
    /// The number of distinct source records the builder saw.
    pub distinct_records: u64,
    /// The number of records, record 0 ("no data") included.
    pub records: usize,
    /// The number of strings in the file's string table.
    pub strings: usize,
    /// The number of IPv4 range starts (boundaries).
    pub ipv4_ranges: usize,
    /// The number of IPv6 range starts (boundaries).
    pub ipv6_ranges: usize,
}
