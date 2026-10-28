package mmdb

import (
	"fmt"
	"math"
)

// String table limits of the GEO2 format (docs/FORMAT.md).
const (
	// maxU16Strings is the number of ids a 16-bit record field can hold,
	// the empty string's id 0 included.
	maxU16Strings = 1 << 16
	// maxStrings is the number of ids a 24-bit city name can hold. Every
	// string the builder stores is used by a record or by META, so the
	// table may hold at most this many.
	maxStrings = 1 << 24
	// maxStrBlob is the largest strings section a u32 STRIDX offset can end.
	maxStrBlob = math.MaxUint32
)

// interner numbers strings in the order they are first seen and remembers
// which ones a 16-bit record field uses. The numbers are provisional: finish
// renumbers the strings so that those used by 16-bit fields come first.
type interner struct {
	m    map[string]uint32
	list []string
	u16  []bool // u16[i]: string i is used by a field stored as a 16-bit id
}

func newInterner() *interner {
	return &interner{m: map[string]uint32{"": 0}, list: []string{""}, u16: []bool{true}}
}

// id returns the provisional id of s, adding s if it is new.
func (in *interner) id(s string) uint32 {
	if s == "" {
		return 0
	}
	if v, ok := in.m[s]; ok {
		return v
	}
	v := uint32(len(in.list))
	in.m[s] = v
	in.list = append(in.list, s)
	in.u16 = append(in.u16, false)
	return v
}

// id16 is id for a string stored in a 16-bit record field.
func (in *interner) id16(s string) uint32 {
	v := in.id(s)
	in.u16[v] = true
	return v
}

// finish returns the strings in file order and remap, the file id of each
// provisional id. The strings used by 16-bit fields come first, then the
// others (city names and META strings), each group in the order first
// seen; the empty string stays id 0. nU16 is the size of the first group.
func (in *interner) finish() (strs []string, remap []uint32, nU16 int) {
	for _, used := range in.u16 {
		if used {
			nU16++
		}
	}
	strs = make([]string, len(in.list))
	remap = make([]uint32, len(in.list))
	next16, next := 0, nU16
	for i, s := range in.list {
		if in.u16[i] {
			remap[i] = uint32(next16)
			strs[next16] = s
			next16++
		} else {
			remap[i] = uint32(next)
			strs[next] = s
			next++
		}
	}
	return strs, remap, nU16
}

// checkStrings rejects a string table that the format cannot hold, before
// any output is written. nU16 is the number of strings, the empty one
// included, that 16-bit record fields use; finish puts them first.
func checkStrings(src string, strs []string, nU16 int) error {
	var blob uint64
	for _, s := range strs {
		if len(s) > math.MaxUint16 {
			return fmt.Errorf("mmdb: %q: string of %d bytes (max %d) starting %.40q: %w",
				src, len(s), math.MaxUint16, s, ErrStringTooLong)
		}
		blob += uint64(len(s))
	}
	return checkStringCounts(src, len(strs), nU16, blob)
}

// checkStringCounts checks the string count, the count of strings used by
// 16-bit fields (both including the empty string) and the strings' total
// size in bytes against the format's limits.
func checkStringCounts(src string, nStrings, nU16 int, blob uint64) error {
	switch {
	case nU16 > maxU16Strings:
		return fmt.Errorf("mmdb: %q: %d distinct strings in country, continent, region and time zone fields "+
			"(max %d; they are stored as 16-bit ids): %w", src, nU16-1, maxU16Strings-1, ErrTooManyStrings)
	case nStrings > maxStrings:
		return fmt.Errorf("mmdb: %q: %d distinct strings (max %d; city names are stored as 24-bit ids): %w",
			src, nStrings, maxStrings, ErrTooManyStrings)
	case blob > maxStrBlob:
		return fmt.Errorf("mmdb: %q: %d bytes of strings (max %d): %w", src, blob, uint64(maxStrBlob), ErrTooManyStrings)
	}
	return nil
}
