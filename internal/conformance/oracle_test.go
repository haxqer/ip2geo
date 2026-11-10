package conformance

import (
	"bytes"
	"math/rand/v2"
	"net/netip"
	"slices"
	"testing"

	"github.com/haxqer/ip2geo"
)

// The tests here check the Go reader against the synthetic sources, by a
// model written from docs/FORMAT.md alone: the expected JSON documents are
// the reader's answers, so this is what makes them more than a snapshot.

// segment is a run of addresses [first, last] of one family with record
// id rec, 0 for a gap.
type segment struct {
	first, last netip.Addr
	rec         uint32
}

// model is the boundary list of a database: segments covering each family
// from its first to its last address.
type model struct{ v4, v6 []segment }

// newModel merges ranges as docs/FORMAT.md describes the boundaries:
// adjacent ranges with the same record merge, gaps get record 0, and the
// segments cover each family completely.
func newModel(v4, v6 []segment, merge bool) model {
	fill := func(rs []segment, lo, hi netip.Addr) []segment {
		slices.SortFunc(rs, func(a, b segment) int { return a.first.Compare(b.first) })
		var out []segment
		next := lo
		for _, r := range rs {
			if r.first != next {
				out = append(out, segment{next, r.first.Prev(), 0})
			}
			if n := len(out); merge && n > 0 && out[n-1].rec == r.rec && out[n-1].last.Next() == r.first {
				out[n-1].last = r.last
			} else {
				out = append(out, r)
			}
			next = r.last.Next()
		}
		if next.IsValid() && (len(out) == 0 || out[len(out)-1].last != hi) {
			out = append(out, segment{next, hi, 0})
		}
		return out
	}
	return model{
		v4: fill(v4, netip.MustParseAddr("0.0.0.0"), netip.MustParseAddr("255.255.255.255")),
		v6: fill(v6, netip.IPv6Unspecified(), netip.MustParseAddr("ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff")),
	}
}

// aliasPrefixes are the IPv6 prefixes docs/FORMAT.md maps to IPv4, with the
// bit offset of the embedded IPv4 address.
var aliasPrefixes = []struct {
	p   netip.Prefix
	off int
}{
	{netip.MustParsePrefix("::/96"), 96},
	{netip.MustParsePrefix("::ffff:0:0/96"), 96},
	{netip.MustParsePrefix("2001::/32"), 32},
	{netip.MustParsePrefix("2002::/16"), 16},
}

// toV4 returns the IPv4 address an address is looked up as, and whether it
// is one.
func toV4(a netip.Addr) (netip.Addr, bool) {
	if a.Is4() {
		return a, true
	}
	b := a.As16()
	for _, al := range aliasPrefixes {
		if al.p.Contains(a) {
			o := al.off / 8
			return netip.AddrFrom4([4]byte(b[o : o+4])), true
		}
	}
	return a, false
}

// find returns the segment holding a, which must be of the family of segs.
func find(segs []segment, a netip.Addr) segment {
	i, _ := slices.BinarySearchFunc(segs, a, func(s segment, a netip.Addr) int {
		switch {
		case s.last.Less(a):
			return -1
		case a.Less(s.first):
			return 1
		}
		return 0
	})
	return segs[i]
}

// lookup returns the record id and the range LookupRange should return.
func (m *model) lookup(a netip.Addr) (uint32, ip2geo.Range) {
	if v4, ok := toV4(a); ok {
		s := find(m.v4, v4)
		return s.rec, ip2geo.Range{First: s.first, Last: s.last}
	}
	s := find(m.v6, a)
	first, last := s.first, s.last
	for _, al := range aliasPrefixes {
		lo, hi := al.p.Addr(), lastAddr(al.p)
		if hi.Less(a) && !hi.Less(first) {
			first = hi.Next()
		}
		if a.Less(lo) && !last.Less(lo) {
			last = lo.Prev()
		}
	}
	return s.rec, ip2geo.Range{First: first, Last: last}
}

// covered returns the covered ranges as Ranges should yield them: IPv4
// first, IPv6 with the alias prefixes cut out.
func (m *model) covered() []segment {
	var out []segment
	for _, s := range m.v4 {
		if s.rec != 0 {
			out = append(out, s)
		}
	}
	for _, s := range m.v6 {
		if s.rec == 0 {
			continue
		}
		parts := []segment{s}
		for _, al := range aliasPrefixes {
			lo, hi := al.p.Addr(), lastAddr(al.p)
			var next []segment
			for _, p := range parts {
				if p.last.Less(lo) || hi.Less(p.first) {
					next = append(next, p)
					continue
				}
				if p.first.Less(lo) {
					next = append(next, segment{p.first, lo.Prev(), p.rec})
				}
				if hi.Less(p.last) {
					next = append(next, segment{hi.Next(), p.last, p.rec})
				}
			}
			parts = next
		}
		out = append(out, parts...)
	}
	return out
}

// probes returns the addresses to check a database with: its queries, the
// ends of every segment and the addresses next to them, and random
// addresses of both families and of the alias prefixes.
func probes(m *model, qs []query) []netip.Addr {
	var out []netip.Addr
	for _, q := range qs {
		out = append(out, q.addr)
	}
	for _, segs := range [][]segment{m.v4, m.v6} {
		for _, s := range segs {
			out = append(out, s.first, s.last)
			if p := s.first.Prev(); p.IsValid() {
				out = append(out, p)
			}
			if n := s.last.Next(); n.IsValid() {
				out = append(out, n)
			}
		}
	}
	rnd := rand.New(rand.NewPCG(1, 2))
	for range 3000 {
		var b4 [4]byte
		var b16 [16]byte
		for i := range b4 {
			b4[i] = byte(rnd.Uint32())
		}
		for i := range b16 {
			b16[i] = byte(rnd.Uint32())
		}
		out = append(out, netip.AddrFrom4(b4), netip.AddrFrom16(b16))
		for _, al := range aliasPrefixes {
			b := b16
			p := al.p.Addr().As16()
			copy(b[:al.p.Bits()/8], p[:])
			out = append(out, netip.AddrFrom16(b))
		}
	}
	return out
}

// checkModel checks Lookup, LookupRange and Ranges of db against m, and
// that addresses with the same record id get the same Details.
func checkModel(t *testing.T, name string, db []byte, m *model, qs []query) {
	t.Helper()
	r, err := ip2geo.OpenBytes(db)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	byRec := map[uint32]ip2geo.Details{}
	for _, a := range probes(m, qs) {
		rec, want := m.lookup(a)
		_, ok := r.Lookup(a)
		if ok != (rec != 0) {
			t.Errorf("%s: Lookup(%s) found %v, model has record %d", name, a, ok, rec)
		}
		got, rok := r.LookupRange(a)
		if got != want || rok != ok {
			t.Errorf("%s: LookupRange(%s) = %s-%s %v, model %s-%s %v", name, a, got.First, got.Last, rok, want.First, want.Last, rec != 0)
		}
		d, _ := r.LookupDetails(a)
		if prev, seen := byRec[rec]; seen && prev != d {
			t.Errorf("%s: LookupDetails(%s) = %+v, another address of record %d has %+v", name, a, d, rec, prev)
		}
		byRec[rec] = d
	}
	var ranges []segment
	for rg := range r.Ranges() {
		ranges = append(ranges, segment{rg.First, rg.Last, 0})
	}
	cov := m.covered()
	for i := range cov {
		cov[i].rec = 0
	}
	if !slices.Equal(ranges, cov) {
		t.Errorf("%s: Ranges yields %v, model %v", name, ranges, cov)
	}
}

func TestReaderMatchesSources(t *testing.T) {
	for _, d := range datasets() {
		db, ids, err := buildDataset(&d)
		if err != nil {
			t.Fatalf("%s: %v", d.file, err)
		}
		var v4, v6 []segment
		for _, n := range d.nets {
			s := segment{n.first.Unmap(), n.last.Unmap(), ids[n.rec]}
			if s.first.Is4() {
				v4 = append(v4, s)
			} else {
				v6 = append(v6, s)
			}
		}
		m := newModel(v4, v6, true)
		checkModel(t, d.file, db, &m, datasetQueries(&d))

		// Spot-check the decoded fields against the source records.
		r, _ := ip2geo.OpenBytes(db)
		for _, n := range d.nets {
			src := d.records[n.rec]
			if _, aliased := toV4(n.first); aliased && !n.first.Is4() && !n.first.Is4In6() {
				continue // an IPv6 network starting in an alias prefix: its first address is looked up as IPv4
			}
			dt, ok := r.LookupDetails(n.first)
			if !ok {
				t.Errorf("%s: %s not found", d.file, n.first)
				continue
			}
			switch d.opts.Profile {
			case ip2geo.ProfileASN:
				if dt.ASN != src.ASN {
					t.Errorf("%s: %s ASN %+v, source %+v", d.file, n.first, dt.ASN, src.ASN)
				}
			default:
				if dt.CountryISO != src.Country.ISOCode || dt.CountryGeoNameID != src.Country.GeoNameID {
					t.Errorf("%s: %s country %q %d, source %q %d", d.file, n.first,
						dt.CountryISO, dt.CountryGeoNameID, src.Country.ISOCode, src.Country.GeoNameID)
				}
				if d.opts.Profile == ip2geo.ProfileCity && dt.CityGeoNameID != src.City.GeoNameID {
					t.Errorf("%s: %s city %d, source %d", d.file, n.first, dt.CityGeoNameID, src.City.GeoNameID)
				}
			}
		}
	}
}

func TestGEO1ReaderMatchesSource(t *testing.T) {
	g := geo1CityDataset()
	seg := func(starts []geo1Start, hi netip.Addr) []segment {
		var out []segment
		for i, s := range starts {
			last := hi
			if i+1 < len(starts) {
				last = starts[i+1].start.Prev()
			}
			if s.id != 0 {
				out = append(out, segment{s.start, last, s.id})
			}
		}
		return out
	}
	m := newModel(seg(g.v4, netip.MustParseAddr("255.255.255.255")),
		seg(g.v6, netip.MustParseAddr("ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff")), false)
	checkModel(t, g.file, encodeGEO1(&g), &m, geo1Queries(&g))
}

// TestGenerateDeterministic checks that two generations in one process are
// identical; the root package's conformance test compares a generation
// with the committed files.
func TestGenerateDeterministic(t *testing.T) {
	a, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatalf("%d files, then %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Name != b[i].Name || !bytes.Equal(a[i].Data, b[i].Data) {
			t.Errorf("%s differs between two generations", a[i].Name)
		}
	}
}
