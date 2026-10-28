package mmdb

import (
	"fmt"
	"math"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/internal/build"
	"github.com/oschwald/maxminddb-golang/v2"
	"github.com/oschwald/maxminddb-golang/v2/mmdbdata"
)

// extDecoder reads what a build keeps beyond the base record: the field
// sets and the names in the extra languages. srcDecoder has filled in the
// base record and the primary language's names of the country, the first
// subdivision and the city by then.
//
// It walks the record once and reads only what the build keeps: the keys it
// does not need are stepped over without decoding, a names map is read only
// for the languages it still needs, and of the subdivisions only the first
// two. It implements mmdbdata.Unmarshaler. The map and slice iterators of
// mmdbdata take a yield function; extDecoder binds one per kind of map once,
// in newExtDecoder, and passes the map being read to it in its fields, so
// decoding a record allocates no closures of its own.
type extDecoder struct {
	profile ip2geo.Profile
	fields  ip2geo.FieldSet
	langs   []string
	multi   bool // more than one language

	// The record being read, its decoder and its first error.
	out *build.Record
	dec *mmdbdata.Decoder
	err error

	// What the nested yield functions fill in.
	country   *build.Country
	countryEU bool // read is_in_european_union
	base      bool // the base record has the country's ISO code, id and primary name
	sub       int  // index of the subdivision being read
	nameSlots []string
	nameFrom  int // first language of nameSlots to read
	hasLat    bool
	hasLon    bool

	yTop, yContinent, yCountry, yRepresented, ySubdivision, yCity, yLocation, yPostal, yTraits,
	yNames func([]byte, error) bool
	ySubdivisions func(error) bool
}

func newExtDecoder(p ip2geo.Profile, fields ip2geo.FieldSet, langs []string) *extDecoder {
	d := &extDecoder{profile: p, fields: fields, langs: langs, multi: len(langs) > 1}
	d.yTop = d.yield(d.top)
	d.yContinent = d.yield(d.continent)
	d.yCountry = d.yield(d.countryKey)
	d.yRepresented = d.yield(d.represented)
	d.ySubdivision = d.yield(d.subdivision)
	d.yCity = d.yield(d.city)
	d.yLocation = d.yield(d.location)
	d.yPostal = d.yield(d.postal)
	d.yTraits = d.yield(d.traits)
	d.yNames = d.yield(d.name)
	d.ySubdivisions = d.subdivisionElem
	return d
}

// decode reads the extension fields of the data record of res into out.
func (d *extDecoder) decode(res maxminddb.Result, out *build.Record) error {
	d.out, d.err = out, nil
	err := res.Decode(d)
	d.out, d.dec = nil, nil
	return err
}

// UnmarshalMaxMindDB reads the top-level map of a data record.
func (d *extDecoder) UnmarshalMaxMindDB(dec *mmdbdata.Decoder) error {
	d.dec = dec
	return d.readMap(d.yTop)
}

func (d *extDecoder) has(f ip2geo.FieldSet) bool { return d.fields&f != 0 }

// yield returns a yield function for a map iterator that calls f with each
// key. f may read the key's value or leave it; the iterator steps over it
// either way. An error stops the iteration and is kept in d.err, prefixed
// with the key.
func (d *extDecoder) yield(f func(key []byte) error) func([]byte, error) bool {
	return func(key []byte, err error) bool {
		if err == nil {
			if err = f(key); err != nil {
				err = fmt.Errorf("%s: %w", key, err)
			}
		}
		if err != nil {
			d.err = err
			return false
		}
		return true
	}
}

// readMap iterates over the map at the decoder's position with yield.
func (d *extDecoder) readMap(yield func([]byte, error) bool) error {
	keys, _, err := d.dec.ReadMap()
	if err != nil {
		return err
	}
	keys(yield)
	return d.err
}

func (d *extDecoder) top(key []byte) error {
	r := d.out
	city := d.profile >= ip2geo.ProfileCity
	switch {
	case string(key) == "continent" && d.has(ip2geo.FieldContinent):
		return d.readMap(d.yContinent)
	case string(key) == "country" && (d.multi || d.has(ip2geo.FieldEU)):
		d.country, d.countryEU, d.base = &r.Country, d.has(ip2geo.FieldEU), true
		return d.readMap(d.yCountry)
	case string(key) == "registered_country" && d.has(ip2geo.FieldRegistered):
		d.country, d.countryEU, d.base = &r.RegisteredCountry, true, false
		return d.readMap(d.yCountry)
	case string(key) == "represented_country" && d.has(ip2geo.FieldRegistered):
		return d.readMap(d.yRepresented)
	case string(key) == "subdivisions" && d.profile >= ip2geo.ProfileRegion &&
		(d.multi || d.has(ip2geo.FieldSubdivisions)):
		elems, _, err := d.dec.ReadSlice()
		if err != nil {
			return err
		}
		d.sub = 0
		elems(d.ySubdivisions)
		return d.err
	case string(key) == "city" && city && d.multi:
		return d.readMap(d.yCity)
	case string(key) == "location" && city && d.has(ip2geo.FieldLocation|ip2geo.FieldMetro):
		d.hasLat, d.hasLon = false, false
		err := d.readMap(d.yLocation)
		if d.has(ip2geo.FieldLocation) {
			r.Location.HasCoordinates = d.hasLat && d.hasLon
		}
		return err
	case string(key) == "postal" && d.has(ip2geo.FieldPostal):
		return d.readMap(d.yPostal)
	case string(key) == "traits" && d.has(ip2geo.FieldTraits):
		return d.readMap(d.yTraits)
	}
	return nil
}

// readNames reads a names map into slots, which has one entry per language
// of the build, for the languages from index from on.
func (d *extDecoder) readNames(slots []string, from int) error {
	if from >= len(d.langs) {
		return nil
	}
	d.nameSlots, d.nameFrom = slots, from
	return d.readMap(d.yNames)
}

func (d *extDecoder) name(key []byte) (err error) {
	for j := d.nameFrom; j < len(d.langs); j++ {
		if string(key) == d.langs[j] {
			d.nameSlots[j], err = d.dec.ReadString()
			break
		}
	}
	return err
}

func (d *extDecoder) continent(key []byte) (err error) {
	c := &d.out.Continent
	switch string(key) {
	case "names":
		err = d.readNames(c.Names, 0)
	case "geoname_id":
		c.GeoNameID, err = readUint32(d.dec)
	}
	return err
}

// countryKey reads a key of d.country. With d.base, the base record has the
// ISO code, the GeoNames id and the primary name, and only the other names
// and, with d.countryEU, is_in_european_union are read.
func (d *extDecoder) countryKey(key []byte) (err error) {
	c := d.country
	switch {
	case string(key) == "names":
		from := 0
		if d.base {
			from = 1
		}
		err = d.readNames(c.Names, from)
	case string(key) == "is_in_european_union" && d.countryEU:
		c.IsInEuropeanUnion, err = d.dec.ReadBool()
	case d.base:
	case string(key) == "iso_code":
		c.ISOCode, err = d.dec.ReadString()
	case string(key) == "geoname_id":
		c.GeoNameID, err = readUint32(d.dec)
	}
	return err
}

// represented reads a key of represented_country: a country with a type.
func (d *extDecoder) represented(key []byte) (err error) {
	c := &d.out.RepresentedCountry
	switch string(key) {
	case "iso_code":
		c.ISOCode, err = d.dec.ReadString()
	case "names":
		err = d.readNames(c.Names, 0)
	case "geoname_id":
		c.GeoNameID, err = readUint32(d.dec)
	case "is_in_european_union":
		c.IsInEuropeanUnion, err = d.dec.ReadBool()
	case "type":
		d.out.RepresentedType, err = d.dec.ReadString()
	}
	return err
}

// subdivisionElem reads element d.sub of subdivisions: the first one, and
// the second one with ip2geo.FieldSubdivisions.
func (d *extDecoder) subdivisionElem(err error) bool {
	n := 1
	if d.has(ip2geo.FieldSubdivisions) {
		n = 2
	}
	if err == nil && d.sub < n {
		if err = d.readMap(d.ySubdivision); err != nil {
			err = fmt.Errorf("%d: %w", d.sub, err)
		}
	}
	if err != nil {
		d.err = err
		return false
	}
	d.sub++
	return d.sub < n
}

// subdivision reads a key of subdivision d.sub. The base record has the
// first one's ISO code and primary name.
func (d *extDecoder) subdivision(key []byte) (err error) {
	s := &d.out.Subdivisions[d.sub]
	first := d.sub == 0
	switch {
	case string(key) == "names":
		from := 0
		if first {
			from = 1
		}
		err = d.readNames(s.Names, from)
	case string(key) == "geoname_id" && d.has(ip2geo.FieldSubdivisions):
		s.GeoNameID, err = readUint32(d.dec)
	case string(key) == "iso_code" && !first:
		s.ISOCode, err = d.dec.ReadString()
	}
	return err
}

// city reads the city's names in the extra languages.
func (d *extDecoder) city(key []byte) error {
	if string(key) == "names" {
		return d.readNames(d.out.City.Names, 1)
	}
	return nil
}

// location reads which of the coordinates and the accuracy radius are
// present, and the metro code.
func (d *extDecoder) location(key []byte) (err error) {
	l := &d.out.Location
	switch string(key) {
	case "latitude":
		d.hasLat = true
	case "longitude":
		d.hasLon = true
	case "accuracy_radius":
		l.HasAccuracyRadius = d.has(ip2geo.FieldLocation)
	case "metro_code":
		if d.has(ip2geo.FieldMetro) {
			var n uint64
			n, err = readUint(d.dec, math.MaxUint16)
			l.MetroCode = uint16(n)
		}
	}
	return err
}

func (d *extDecoder) postal(key []byte) (err error) {
	if string(key) == "code" {
		d.out.PostalCode, err = d.dec.ReadString()
	}
	return err
}

func (d *extDecoder) traits(key []byte) (err error) {
	t := &d.out.Traits
	switch string(key) {
	case "is_anycast":
		t.IsAnycast, err = d.dec.ReadBool()
	case "is_anonymous_proxy":
		t.IsAnonymousProxy, err = d.dec.ReadBool()
	case "is_satellite_provider":
		t.IsSatelliteProvider, err = d.dec.ReadBool()
	}
	return err
}

// readUint32 reads an unsigned integer that fits a uint32.
func readUint32(dec *mmdbdata.Decoder) (uint32, error) {
	n, err := readUint(dec, math.MaxUint32)
	return uint32(n), err
}

// readUint reads an integer of any MaxMind DB integer type whose value is
// 0..max, as maxminddb's struct decoding accepts it for an unsigned field.
func readUint(dec *mmdbdata.Decoder, max uint64) (uint64, error) {
	kind, err := dec.PeekKind()
	if err != nil {
		return 0, err
	}
	var n uint64
	switch kind {
	case mmdbdata.KindUint16:
		var v uint16
		v, err = dec.ReadUint16()
		n = uint64(v)
	case mmdbdata.KindUint32:
		var v uint32
		v, err = dec.ReadUint32()
		n = uint64(v)
	case mmdbdata.KindUint64:
		n, err = dec.ReadUint64()
	case mmdbdata.KindUint128:
		var hi uint64
		hi, n, err = dec.ReadUint128()
		if err == nil && hi != 0 {
			return 0, fmt.Errorf("uint128 value above %d", max)
		}
	case mmdbdata.KindInt32:
		var v int32
		v, err = dec.ReadInt32()
		if err == nil && v < 0 {
			return 0, fmt.Errorf("negative value %d for an unsigned field", v)
		}
		n = uint64(v)
	default:
		return 0, fmt.Errorf("value of type %s, want an unsigned integer", kind)
	}
	if err != nil {
		return 0, err
	}
	if n > max {
		return 0, fmt.Errorf("value %d is above %d", n, max)
	}
	return n, nil
}
