package ip2geo

import (
	"encoding/binary"
	"fmt"
	"math/bits"
	"net/netip"
	"runtime"
	"slices"
	"strings"

	"github.com/haxqer/ip2geo/internal/format"
)

// Languages returns the languages of the database's names, the primary one
// first. It is nil for a GEO1 database, which does not record its language.
// The caller may modify the returned slice.
func (r *Reader) Languages() []string { return slices.Clone(r.langs) }

// Fields returns the optional field sets the database holds.
func (r *Reader) Fields() FieldSet { return r.fields }

// WithLanguage returns a Reader for the same database whose names are in
// lang, with the fallback described at Language. It shares the database's
// memory with r and is cheap: call it once per language, not per lookup.
// It fails with an error wrapping ErrLanguage when the database does not
// have lang.
func (r *Reader) WithLanguage(lang string) (*Reader, error) {
	v := *r
	if err := v.setLanguage(lang); err != nil {
		return nil, err
	}
	// The view's tables may be in r's mapping, which r's cleanup releases:
	// the view refers to that Reader so that it stays reachable.
	if v.owner == nil {
		v.owner = r
	}
	return &v, nil
}

// setLanguage makes lang, one of r's languages or "" for the primary one,
// the language of r's names.
func (r *Reader) setLanguage(lang string) error {
	j := 0
	if lang != "" {
		if j = slices.Index(r.langs, lang); j < 0 {
			have := "records none"
			if len(r.langs) > 0 {
				have = "has " + strings.Join(r.langs, ", ")
			}
			return fmt.Errorf("ip2geo: language %q: the database %s: %w", lang, have, ErrLanguage)
		}
	}
	r.lang = j
	for k := range r.nameOff {
		r.nameOff[k] = -1
		if j > 0 {
			r.nameOff[k] = r.xl.NameOff(j, k)
		}
	}
	r.alt = r.profile == ProfileASN || j > 0
	return nil
}

// LookupDetails is Lookup with every optional field: the Record plus the
// field sets the database holds (see Fields), and for a ProfileASN database
// the ASN. It does not allocate. ok is as for Lookup.
func (r *Reader) LookupDetails(ip netip.Addr) (d Details, ok bool) {
	if id := r.find(ip); id != 0 {
		r.details(id, &d)
		ok = true
	}
	runtime.KeepAlive(r)
	return d, ok
}

// ASN returns the autonomous system of ip. ok is false when ip is not
// covered, and always for a database that is not ProfileASN.
func (r *Reader) ASN(ip netip.Addr) (ASN, bool) {
	if r.profile != ProfileASN {
		return ASN{}, false
	}
	var a ASN
	id := r.find(ip)
	if id != 0 {
		a = r.asn(id)
	}
	runtime.KeepAlive(r)
	return a, id != 0
}

// find returns the record id of ip, 0 when it is not covered. It is the
// search of Lookup, which spells it out to stay a single call.
func (r *Reader) find(ip netip.Addr) uint32 {
	if ip.Is4() || ip.Is4In6() {
		a := ip.As4()
		return r.search4(binary.BigEndian.Uint32(a[:]))
	}
	if !ip.IsValid() {
		return 0
	}
	a := ip.As16()
	if v4, isV4 := embeddedV4(&a); isV4 {
		return r.search4(v4)
	}
	return r.search6(binary.BigEndian.Uint64(a[:8]), binary.BigEndian.Uint64(a[8:]))
}

// decodeAlt is Lookup's decode when r.alt is set: an empty Record for an
// ASN database, else the Record with its names in r's language.
func (r *Reader) decodeAlt(id uint32) Record {
	if r.profile == ProfileASN {
		return Record{}
	}
	rec := r.decode(id)
	x := r.xentry(id)
	r.localize(x, format.NameCountry, &rec.CountryName)
	r.localize(x, format.NameRegion, &rec.RegionName)
	r.localize(x, format.NameCity, &rec.City)
	return rec
}

// xentry returns the XRECORDS entry of record id.
func (r *Reader) xentry(id uint32) []byte {
	w := r.xl.Width
	off := int(id) * w
	return r.xrecs[off : off+w : off+w]
}

// localize sets *s to the name of kind k in r's language from XRECORDS entry
// x, unless the entry has none (id 0), which leaves the primary language's
// name. r.lang must not be 0.
func (r *Reader) localize(x []byte, k int, s *string) {
	if off := r.nameOff[k]; off >= 0 {
		if id := format.Uint24(x[off:]); id != 0 {
			*s = r.strings[id]
		}
	}
}

// block returns the block of field set f in XRECORDS entry x, which must
// have it.
func (r *Reader) block(x []byte, f FieldSet) []byte {
	return x[r.xl.Field[bits.TrailingZeros32(uint32(f))]:]
}

// str24 returns the string of the u24 id at the start of b. Every u24 id in
// XRECORDS was checked against the string table at open.
func (r *Reader) str24(b []byte) string { return r.strings[format.Uint24(b)] }

// details decodes record id into d, which must be zero: the Record, the
// blocks of the field sets the database has, and the names in r's language.
func (r *Reader) details(id uint32, d *Details) {
	if r.profile == ProfileASN {
		d.ASN = r.asn(id)
		return
	}
	d.Record = r.decode(id)
	if r.xl.Width == 0 {
		return
	}
	le := binary.LittleEndian
	x := r.xentry(id)
	f := r.fields
	if f&FieldEU != 0 {
		d.IsInEuropeanUnion = r.block(x, FieldEU)[format.XEUFlags]&format.XFlagEU != 0
	}
	if f&FieldContinent != 0 {
		b := r.block(x, FieldContinent)
		d.ContinentName = r.str24(b[format.XContinentName:])
		d.ContinentGeoNameID = le.Uint32(b[format.XContinentGeo:])
	}
	if f&FieldRegistered != 0 {
		b := r.block(x, FieldRegistered)
		fl := b[format.XRegisteredFlags]
		d.RegisteredCountry = Country{
			ISO:               r.str24(b[format.XRegisteredISO:]),
			Name:              r.str24(b[format.XRegisteredName:]),
			GeoNameID:         le.Uint32(b[format.XRegisteredGeo:]),
			IsInEuropeanUnion: fl&format.XFlagRegisteredEU != 0,
		}
		d.RepresentedCountry = Country{
			ISO:               r.str24(b[format.XRepresentedISO:]),
			Name:              r.str24(b[format.XRepresentedName:]),
			GeoNameID:         le.Uint32(b[format.XRepresentedGeo:]),
			IsInEuropeanUnion: fl&format.XFlagRepresentedEU != 0,
		}
		d.RepresentedCountryType = r.str24(b[format.XRepresentedType:])
	}
	if f&FieldSubdivisions != 0 {
		b := r.block(x, FieldSubdivisions)
		d.RegionGeoNameID = le.Uint32(b[format.XRegionGeo:])
		d.Region2ISO = r.str24(b[format.XRegion2ISO:])
		d.Region2Name = r.str24(b[format.XRegion2Name:])
		d.Region2GeoNameID = le.Uint32(b[format.XRegion2Geo:])
	}
	if f&FieldPostal != 0 {
		d.PostalCode = r.str24(r.block(x, FieldPostal)[format.XPostal:])
	}
	if f&FieldMetro != 0 {
		d.MetroCode = le.Uint16(r.block(x, FieldMetro)[format.XMetro:])
	}
	if f&FieldTraits != 0 {
		fl := r.block(x, FieldTraits)[format.XTraitsFlags]
		d.IsAnycast = fl&format.XFlagAnycast != 0
		d.IsAnonymousProxy = fl&format.XFlagAnonymousProxy != 0
		d.IsSatelliteProvider = fl&format.XFlagSatellite != 0
	}
	if f&FieldLocation != 0 {
		fl := r.block(x, FieldLocation)[format.XLocFlags]
		d.HasCoordinates = fl&format.XFlagHasCoordinates != 0
		d.HasAccuracyRadius = fl&format.XFlagHasAccuracy != 0
	}
	if r.lang != 0 {
		r.localize(x, format.NameCountry, &d.CountryName)
		r.localize(x, format.NameRegion, &d.RegionName)
		r.localize(x, format.NameCity, &d.City)
		r.localize(x, format.NameContinent, &d.ContinentName)
		r.localize(x, format.NameRegion2, &d.Region2Name)
		r.localize(x, format.NameRegistered, &d.RegisteredCountry.Name)
		r.localize(x, format.NameRepresented, &d.RepresentedCountry.Name)
	}
}

// asn decodes ASN record id.
func (r *Reader) asn(id uint32) ASN {
	off := int(id) * format.RecWidthASN
	e := (*[format.RecWidthASN]byte)(r.records[off : off+format.RecWidthASN])
	return ASN{
		Number:       binary.LittleEndian.Uint32(e[format.RecASNNumber:]),
		Organization: r.strings[format.Uint24(e[format.RecASNOrg:])],
	}
}
