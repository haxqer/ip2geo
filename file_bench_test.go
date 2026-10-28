package ip2geo

import (
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

// readerToGEO2 lays out the in-memory sections of r as a GEO2 fixture, with
// META from r.Metadata(). It lets a GEO1 file be re-encoded as GEO2.
func readerToGEO2(r *Reader) *g2file {
	le := binary.LittleEndian
	strs := append([]string(nil), r.strings...)
	ids := map[string]uint32{}
	for i, s := range strs {
		if _, ok := ids[s]; !ok {
			ids[s] = uint32(i)
		}
	}
	strID := func(s string) uint32 {
		if id, ok := ids[s]; ok {
			return id
		}
		ids[s] = uint32(len(strs))
		strs = append(strs, s)
		return ids[s]
	}
	m := r.Metadata()
	unix := func(t interface{ Unix() int64 }, zero bool) uint64 {
		if zero {
			return 0
		}
		return uint64(t.Unix())
	}
	var meta []byte
	meta = le.AppendUint64(meta, unix(m.BuildTime, m.BuildTime.IsZero()))
	meta = le.AppendUint64(meta, unix(m.SourceTime, m.SourceTime.IsZero()))
	meta = le.AppendUint64(meta, m.Networks)
	for _, s := range []string{m.SourceType, m.Language, m.Description, m.Builder} {
		meta = le.AppendUint32(meta, strID(s))
	}
	meta = le.AppendUint32(meta, uint32(m.DistinctRecords))
	meta = append(meta, make([]byte, 20)...)
	strIdx, blob := le.AppendUint32(nil, 0), []byte(nil)
	for _, s := range strs {
		blob = append(blob, s...)
		strIdx = le.AppendUint32(strIdx, uint32(len(blob)))
	}
	f := &g2file{magic: "GEO2", version: 2, profile: byte(r.profile), latScale: byte(m.LatScale), idWidth: r.idWidth}
	f.secs = []g2sec{
		{sMeta, meta}, {sStrIdx, strIdx}, {sStrBlob, blob}, {sRecords, r.records},
		{sV4Idx, r.v4idx}, {sV4Starts, r.v4starts}, {sV4IDs, r.v4ids},
		{sV6Starts, r.v6hi}, {sV6IDs, r.v6ids}, {sV6ExcIdx, r.v6excIdx}, {sV6ExcLo, r.v6excLo},
	}
	return f
}

// indexSizes reports the bytes of the indexes a Reader builds at open and
// of its heap string table (the string bytes plus a string header each).
func indexSizes(r *Reader) string {
	strBytes := 0
	for _, s := range r.strings {
		strBytes += len(s)
	}
	flagged := 0
	for _, e := range r.v6l2 {
		if e&v6SlotExc != 0 {
			flagged++
		}
	}
	return fmt.Sprintf("lo4 %d B, v6dir %d B, v6l2 %d B (%d slots, %d with exceptions), exception bitmap %d B (%d exceptions), strings %d B + %d headers (%d B); sections %d B",
		len(r.lo4)*4, len(r.v6dir)*4, len(r.v6l2)*4, len(r.v6l2)-1, flagged, len(r.v6exc)*8, len(r.v6excIdx)/4,
		strBytes, len(r.strings), len(r.strings)*16,
		len(r.records)+len(r.v4idx)+len(r.v4starts)+len(r.v4ids)+len(r.v6hi)+len(r.v6ids)+len(r.v6excIdx)+len(r.v6excLo))
}

// BenchmarkLookupFile measures Open and Lookup on the database file named
// by IP2GEO_TEST_GEOC, and skips when that is unset. A GEO1 file is also
// re-encoded as GEO2 in a temporary file next to it, removed afterwards, so
// that the GEO1 conversion and the mapped GEO2 file are measured side by
// side. Run it without -race:
//
//	IP2GEO_TEST_GEOC=city.geoc go test -run '^$' -bench LookupFile -benchtime=2s ./
//
// Address sets: ipv4-random is uniform over IPv4; ipv6-ranges is a random
// address in a random IPv6 range that has data, every range equally likely.
// details/ and range/ measure LookupDetails and LookupRange on the file.
func BenchmarkLookupFile(b *testing.B) {
	path := os.Getenv("IP2GEO_TEST_GEOC")
	if path == "" {
		b.Skip("IP2GEO_TEST_GEOC is not set")
	}
	r, err := Open(path)
	if err != nil {
		b.Fatal(err)
	}
	m := r.Metadata()
	b.Logf("%s: GEO%d %s, %d records, %d strings, %d IPv4 and %d IPv6 starts", path, m.FormatVersion, m.Profile, m.Records, m.Strings, m.IPv4Ranges, m.IPv6Ranges)
	b.Logf("%s", indexSizes(r))
	readers := []struct {
		name string
		r    *Reader
	}{{fmt.Sprintf("geo%d", m.FormatVersion), r}}
	opens := []struct {
		name string
		path string
		opts []Option
	}{{fmt.Sprintf("geo%d", m.FormatVersion), path, nil}}
	if m.FormatVersion == 1 {
		f, err := os.CreateTemp(filepath.Dir(path), "*.geo2.geoc")
		if err != nil {
			b.Fatal(err)
		}
		defer os.Remove(f.Name())
		g2 := readerToGEO2(r).encode()
		if _, err := f.Write(g2); err != nil {
			b.Fatal(err)
		}
		if err := f.Close(); err != nil {
			b.Fatal(err)
		}
		r2, err := Open(f.Name())
		if err != nil {
			b.Fatal(err)
		}
		b.Logf("GEO2 re-encoding: %d bytes, mapped=%v", len(g2), r2.mapped)
		readers = append(readers, struct {
			name string
			r    *Reader
		}{"geo2", r2})
		opens = append(opens, struct {
			name string
			path string
			opts []Option
		}{"geo2", f.Name(), nil}, struct {
			name string
			path string
			opts []Option
		}{"geo2-skipcrc", f.Name(), []Option{SkipChecksum()}})
	}

	rng := rand.New(rand.NewPCG(1, 2))
	const n = 1 << 20
	v4 := make([]netip.Addr, n)
	for i := range v4 {
		var a [4]byte
		binary.BigEndian.PutUint32(a[:], rng.Uint32())
		v4[i] = netip.AddrFrom4(a)
	}
	starts := refStarts6(r)
	var withData []int
	for i := range starts {
		if readID(r.v6ids, r.idWidth, i) != 0 {
			withData = append(withData, i)
		}
	}
	var v6 []netip.Addr
	if len(withData) > 0 {
		v6 = make([]netip.Addr, n)
		for k := range v6 {
			i := withData[rng.IntN(len(withData))]
			lo := starts[i]
			hi := [16]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
			if i+1 < len(starts) {
				hi = add16(starts[i+1], -1)
			}
			v6[k] = netip.AddrFrom16(randomBetween(rng, lo, hi))
		}
	}

	for _, o := range opens {
		b.Run("open/"+o.name, func(b *testing.B) {
			for b.Loop() {
				if _, err := Open(o.path, o.opts...); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	for _, rd := range readers {
		for _, set := range []struct {
			name  string
			addrs []netip.Addr
		}{{"ipv4-random", v4}, {"ipv6-ranges", v6}} {
			b.Run(rd.name+"/"+set.name, func(b *testing.B) {
				if len(set.addrs) == 0 {
					b.Skip("no IPv6 ranges with data")
				}
				benchLookups(b, rd.r, set.addrs)
			})
			// LookupDetails and LookupRange, on the file as it is.
			if rd.name != fmt.Sprintf("geo%d", m.FormatVersion) {
				continue
			}
			b.Run("details/"+set.name, func(b *testing.B) {
				if len(set.addrs) == 0 {
					b.Skip("no IPv6 ranges with data")
				}
				benchDetails(b, rd.r, set.addrs)
			})
			b.Run("range/"+set.name, func(b *testing.B) {
				if len(set.addrs) == 0 {
					b.Skip("no IPv6 ranges with data")
				}
				benchRanges(b, rd.r, set.addrs)
			})
		}
	}
}

// benchDetails is benchLookups for LookupDetails.
func benchDetails(b *testing.B, r *Reader, addrs []netip.Addr) {
	b.ReportAllocs()
	found := 0
	for i := 0; b.Loop(); i++ {
		if _, ok := r.LookupDetails(addrs[i&(len(addrs)-1)]); ok {
			found++
		}
	}
	sinkN = found
}

// benchRanges is benchLookups for LookupRange.
func benchRanges(b *testing.B, r *Reader, addrs []netip.Addr) {
	b.ReportAllocs()
	found := 0
	for i := 0; b.Loop(); i++ {
		if _, ok := r.LookupRange(addrs[i&(len(addrs)-1)]); ok {
			found++
		}
	}
	sinkN = found
}

// benchLookups calls r.Lookup directly in the loop; len(addrs) is a power
// of two.
func benchLookups(b *testing.B, r *Reader, addrs []netip.Addr) {
	b.ReportAllocs()
	found := 0
	for i := 0; b.Loop(); i++ {
		if _, ok := r.Lookup(addrs[i&(len(addrs)-1)]); ok {
			found++
		}
	}
	sinkN = found
}

// randomBetween returns a random 128-bit value in [lo, hi], big-endian.
func randomBetween(rng *rand.Rand, lo, hi [16]byte) [16]byte {
	be := binary.BigEndian
	lh, ll := be.Uint64(lo[:8]), be.Uint64(lo[8:])
	hh, hl := be.Uint64(hi[:8]), be.Uint64(hi[8:])
	// The span hi - lo, as two halves.
	dl := hl - ll
	dh := hh - lh
	if hl < ll {
		dh--
	}
	var oh, ol uint64
	switch {
	case dh == 0 && dl == ^uint64(0):
		ol = rng.Uint64()
	case dh == 0:
		ol = rng.Uint64N(dl + 1)
	default:
		oh, ol = rng.Uint64N(dh), rng.Uint64()
	}
	sl := ll + ol
	sh := lh + oh
	if sl < ll {
		sh++
	}
	return key16(sh, sl)
}
