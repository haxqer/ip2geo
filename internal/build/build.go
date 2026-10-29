// Package build is the source-agnostic core of the ip2geo database
// builders: it turns a stream of address ranges and their data records into
// a GEO2 file (docs/FORMAT.md). A frontend reads one kind of source (package
// mmdb reads MaxMind DB files) and feeds this package; it never touches the
// output format itself.
//
// A build runs in four steps:
//
//  1. Options.Normalize validates the options and fills in the defaults. It
//     does no I/O, so a frontend calls it before it opens the source.
//  2. New starts a build for a source, described by a Source (its name for
//     error messages, its type and build epoch for the metadata).
//  3. For every source data record, the frontend fills a Record and calls
//     Builder.Record, which returns the record's id; then it calls
//     Builder.AddPrefix or Builder.AddRange for every network or range of
//     addresses that has that data. A frontend whose source shares data
//     records between networks (as MaxMind DB files do) calls Record once
//     per source record and reuses the id, which skips decoding and
//     interning the repeated ones; Stats.DistinctRecords counts the calls
//     to Record.
//  4. Builder.Finish returns the encoded file and its Stats.
//
// Ranges should arrive in ascending address order, IPv4 and IPv6 in any
// interleaving: the merge then runs in one pass and holds only the merged
// boundaries. Out-of-order input is accepted and gives the same file, at
// the cost of holding every range until Finish. Ranges must not overlap.
//
// Record ids are deterministic: records are numbered, and strings interned,
// in the order Record first sees them, so the same input in the same order
// gives a byte-identical file.
//
// Options.Fields and a second language add the XFIELDS, XRECORDS and LANGS
// sections of format revision 2.1; source records that differ only in
// their extension fields or in an extra language's names become distinct
// records. Without either, the file is a revision 2.0 file, byte for byte
// what the builder wrote before revision 2.1. A ProfileASN build keeps
// Record.ASN and nothing else.
//
// Errors are returned without a package prefix; a frontend wraps them in
// its own ("mmdb: ...").
package build

import (
	"encoding/binary"
	"fmt"
	"math"
	"net/netip"
	"runtime/debug"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/internal/format"
)

const (
	// DefaultLatScale is the number of latitude/longitude decimals that
	// ProfileCity keeps when Options.LatScale is 0.
	DefaultLatScale = 4
	// MaxLatScale is the largest accepted Options.LatScale.
	MaxLatScale = format.MaxLatScale
)

// Options controls a build; the frontends document the fields to their
// users.
type Options struct {
	Profile ip2geo.Profile
	// Fields are the extension field sets. ip2geo.FieldsAll means every
	// set the profile allows; any other set must be allowed by the profile
	// (format.CheckFields).
	Fields ip2geo.FieldSet
	// Languages are the languages of the names, the primary one first:
	// distinct and non-empty, one for ProfileASN. Record names are indexed
	// in this order. Empty means ["en"].
	Languages   []string
	LatScale    uint8
	BuildTime   time.Time
	Description string
}

// allowedFields returns the field sets profile p allows.
func allowedFields(p ip2geo.Profile) ip2geo.FieldSet {
	var fs ip2geo.FieldSet
	for f := ip2geo.FieldSet(1); f&ip2geo.FieldsAll != 0; f <<= 1 {
		if format.CheckFields(uint8(p), uint32(f)) {
			fs |= f
		}
	}
	return fs
}

// Normalize validates o and fills in the defaults. It does no I/O, and
// calling it again on its result changes nothing. Errors wrap ErrOptions.
func (o *Options) Normalize() error {
	switch o.Profile {
	case ip2geo.ProfileCountry, ip2geo.ProfileRegion, ip2geo.ProfileCity, ip2geo.ProfileASN:
	default:
		return fmt.Errorf("unknown Profile %d (want ProfileCountry, ProfileRegion, ProfileCity or ProfileASN): %w",
			o.Profile, ErrOptions)
	}
	if o.Fields == ip2geo.FieldsAll {
		o.Fields = allowedFields(o.Profile)
	} else if !format.CheckFields(uint8(o.Profile), uint32(o.Fields)) {
		bad := o.Fields &^ allowedFields(o.Profile)
		return fmt.Errorf("option Fields %#x: field sets %#x are not allowed with profile %s: %w",
			uint32(o.Fields), uint32(bad), o.Profile, ErrOptions)
	}
	seen := make(map[string]bool, len(o.Languages))
	for _, l := range o.Languages {
		switch {
		case l == "":
			return fmt.Errorf("option Languages %q: empty language: %w", o.Languages, ErrOptions)
		case seen[l]:
			return fmt.Errorf("option Languages %q: %q is listed twice: %w", o.Languages, l, ErrOptions)
		}
		seen[l] = true
	}
	if o.Profile == ip2geo.ProfileASN && len(o.Languages) > 1 {
		return fmt.Errorf("option Languages %q: profile asn has no names, so at most one language: %w", o.Languages, ErrOptions)
	}
	if o.LatScale > MaxLatScale {
		return fmt.Errorf("LatScale %d is above %d (coordinates would overflow int32): %w",
			o.LatScale, MaxLatScale, ErrOptions)
	}
	if o.BuildTime.IsZero() {
		o.BuildTime = time.Now()
	}
	if o.BuildTime.Unix() < 0 {
		return fmt.Errorf("BuildTime %s is before 1970: %w", o.BuildTime.UTC().Format(time.RFC3339), ErrOptions)
	}
	o.BuildTime = o.BuildTime.UTC().Truncate(time.Second)
	if len(o.Description) > math.MaxUint16 {
		return fmt.Errorf("option Description is %d bytes, more than %d: %w", len(o.Description), math.MaxUint16, ErrOptions)
	}
	if !utf8.ValidString(o.Description) {
		return fmt.Errorf("option Description %.40q is not valid UTF-8: %w", o.Description, ErrOptions)
	}
	if len(o.Languages) == 0 {
		o.Languages = []string{"en"}
	}
	if o.Profile != ip2geo.ProfileCity {
		o.LatScale = 0 // no coordinates are stored, so the header says so
	} else if o.LatScale == 0 {
		o.LatScale = DefaultLatScale
	}
	return nil
}

// Source describes the source database of a build.
type Source struct {
	// Name identifies the source in error messages, e.g. its file path.
	Name string
	// Type is recorded in the metadata, e.g. "GeoIP2-City".
	Type string
	// BuildEpoch is the source's build time in Unix seconds, 0 if unknown.
	BuildEpoch uint64
}

// Stats summarizes a build; package mmdb documents the fields. A frontend
// converts it to its own Stats type, which has the same fields.
type Stats struct {
	Profile         ip2geo.Profile
	Networks        int
	DistinctRecords int
	Records         int
	Strings         int
	V4Ranges        int
	V6Ranges        int
	Bytes           int64
}

// String renders the stats for logging.
func (s Stats) String() string {
	return fmt.Sprintf("profile=%s networks=%d distinct=%d records=%d strings=%d v4ranges=%d v6ranges=%d size=%.2fMB",
		s.Profile, s.Networks, s.DistinctRecords, s.Records, s.Strings, s.V4Ranges, s.V6Ranges,
		float64(s.Bytes)/1024/1024)
}

// modulePath is the path of the module the builder is part of.
const modulePath = "github.com/haxqer/ip2geo"

// BuilderName is the builder string recorded in META: "ip2geo/" and the
// version of this module in the running binary (a release tag, a
// pseudo-version, or "(devel)"), or just "ip2geo" when the binary carries
// no build information.
var BuilderName = sync.OnceValue(func() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "ip2geo"
	}
	mod := &bi.Main
	if mod.Path != modulePath {
		mod = nil
		for _, d := range bi.Deps {
			if d.Path == modulePath {
				mod = d
				break
			}
		}
	}
	if mod == nil {
		return "ip2geo"
	}
	v := mod.Version
	if mod.Replace != nil {
		v = mod.Replace.Version
	}
	if v == "" {
		v = "(devel)"
	}
	return "ip2geo/" + v
})

// Builder accumulates the records and ranges of one build.
type Builder struct {
	opts  Options
	src   Source
	scale float64

	strs *interner
	meta format.Meta
	// Record id 0 is reserved for gaps (addresses without data) and is never
	// handed out: a record whose kept fields are all empty gets its own
	// non-zero id, so the reader reports its networks as found.
	keys map[recKey]uint32
	recs blockList[recKey]
	// x holds the extension entries, nil when the build has none; langs
	// are the provisional string ids of the LANGS section.
	x     *xTable
	langs []uint32

	v4       merger4
	v6       merger6
	networks int
	distinct int
}

// New starts a build of src with opts, which it normalizes.
func New(opts Options, src Source) (*Builder, error) {
	if err := opts.Normalize(); err != nil {
		return nil, err
	}
	b := &Builder{
		opts:  opts,
		src:   src,
		scale: math.Pow10(int(opts.LatScale)),
		strs:  newInterner(),
		keys:  map[recKey]uint32{},
	}
	// The META strings are ordinary strings of the table. Interned first,
	// they come right after the strings of 16-bit fields (or among them,
	// when a 16-bit field uses the same string).
	b.meta = format.Meta{
		BuildTime:   uint64(opts.BuildTime.Unix()),
		SourceTime:  src.BuildEpoch,
		SourceType:  b.strs.id(src.Type),
		Language:    b.strs.id(opts.Languages[0]),
		Description: b.strs.id(opts.Description),
		Builder:     b.strs.id(BuilderName()),
	}
	if len(opts.Languages) > 1 {
		for _, l := range opts.Languages {
			b.langs = append(b.langs, b.strs.id(l))
		}
	}
	b.x = newXTable(uint8(opts.Profile), uint32(opts.Fields), len(opts.Languages))
	b.recs.push(recKey{})
	return b, nil
}

// Options returns the normalized options of the build.
func (b *Builder) Options() *Options { return &b.opts }

// Record interns r and returns its record id, which is never 0. Records
// that are equal in every field the build keeps get the same id. r is not
// retained.
func (b *Builder) Record(r *Record) uint32 {
	b.distinct++
	k := r.key(b.opts.Profile, b.scale, b.strs)
	if b.x != nil {
		k.ext = b.x.add(r, b.strs)
	}
	id, ok := b.keys[k]
	if !ok {
		id = uint32(b.recs.n)
		b.keys[k] = id
		b.recs.push(k)
	}
	return id
}

// AddPrefix adds a network whose data is record id. An IPv4-mapped IPv6
// prefix (::ffff:0:0/96 and below) is an IPv4 network.
func (b *Builder) AddPrefix(pfx netip.Prefix, id uint32) {
	b.networks++
	addr := pfx.Addr()
	if addr.Is4() || addr.Is4In6() {
		a := addr.As4()
		start := binary.BigEndian.Uint32(a[:])
		bits := pfx.Bits()
		if addr.Is4In6() {
			bits -= 96
		}
		last := start | uint32((uint64(1)<<(32-bits))-1)
		b.v4.add(range4{start: start, last: last, rec: id})
	} else {
		s16 := addr.As16()
		b.v6.add(range6{start: s16, last: lastV6(s16, pfx.Bits()), rec: id})
	}
}

// AddRange adds the addresses first to last, inclusive, whose data is
// record id. Both must be of the same family (an IPv4-mapped IPv6 address
// counts as IPv4) and first must not be above last.
func (b *Builder) AddRange(first, last netip.Addr, id uint32) error {
	first, last = first.Unmap(), last.Unmap()
	switch {
	case !first.IsValid() || !last.IsValid() || first.Is4() != last.Is4():
		return fmt.Errorf("range %s-%s: addresses of different families", first, last)
	case last.Less(first):
		return fmt.Errorf("range %s-%s: first address above the last", first, last)
	}
	b.networks++
	if first.Is4() {
		f, l := first.As4(), last.As4()
		b.v4.add(range4{start: binary.BigEndian.Uint32(f[:]), last: binary.BigEndian.Uint32(l[:]), rec: id})
	} else {
		b.v6.add(range6{start: first.As16(), last: last.As16(), rec: id})
	}
	return nil
}

// buildResult is what encode writes. The string ids in recs and meta are
// the final ones, indices into strs.
type buildResult struct {
	strs []string
	recs blockList[recKey]
	v4b  blockList[boundary4]
	v6b  blockList[boundary6]
	meta format.Meta

	// x is the extension table, nil without one; its entries still hold
	// provisional string ids, which remap renumbers. langs are the final
	// string ids of LANGS, nil with one language.
	x     *xTable
	remap []uint32
	langs []uint32
}

// Finish checks the string table, renumbers the strings so that those of
// 16-bit fields come first, and returns the encoded database, in one buffer
// of exactly its size, with the build's Stats. The Builder must not be used
// afterwards.
func (b *Builder) Finish() ([]byte, Stats, error) {
	list, remap, nU16 := b.strs.finish()
	b.strs = nil
	if err := checkStrings(b.src.Name, list, nU16); err != nil {
		return nil, Stats{}, err
	}
	for _, blk := range b.recs.blocks {
		for i := range blk {
			k := &blk[i]
			k.countryISO, k.countryName, k.continent = remap[k.countryISO], remap[k.countryName], remap[k.continent]
			k.regionISO, k.regionName = remap[k.regionISO], remap[k.regionName]
			k.cityName, k.tz = remap[k.cityName], remap[k.tz]
		}
	}
	b.keys = nil
	meta := b.meta
	meta.SourceType, meta.Language = remap[meta.SourceType], remap[meta.Language]
	meta.Description, meta.Builder = remap[meta.Description], remap[meta.Builder]
	meta.Networks = uint64(b.networks)
	meta.DistinctRecords = uint32(b.distinct)

	for i, id := range b.langs {
		b.langs[i] = remap[id]
	}
	if b.x != nil {
		b.x.ids = nil // the entries are all encode needs
	}

	br := &buildResult{strs: list, recs: b.recs, v4b: b.v4.finish(), v6b: b.v6.finish(), meta: meta,
		x: b.x, remap: remap, langs: b.langs}
	buf, err := encode(&b.opts, br)
	if err != nil {
		return nil, Stats{}, err
	}
	return buf, Stats{
		Profile: b.opts.Profile, Networks: b.networks, DistinctRecords: b.distinct, Records: br.recs.n,
		Strings: len(br.strs), V4Ranges: br.v4b.n, V6Ranges: br.v6b.n, Bytes: int64(len(buf)),
	}, nil
}
