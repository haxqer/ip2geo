package mmdb_test

// A minimal MaxMind DB writer for hermetic tests: it hand-encodes a format 2.0
// database (IPv6 search tree, 24-bit records) from a list of non-overlapping
// networks, so the builder can be tested without a licensed database or a
// writer dependency. See https://maxmind.github.io/MaxMind-DB/ for the format.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// mmdbFixture describes a tiny MaxMind DB for writeMMDB.
type mmdbFixture struct {
	dbType    string
	languages []string // omitted from the metadata when nil
	networks  []mmdbNet
	// aliasV4 points ::ffff:0:0/96 at the IPv4 subtree, as MaxMind's own
	// writer does; maxminddb's Networks skips the alias by default.
	aliasV4 bool
}

// mmdbNet is one network and its data record. Supported value types are
// string, float64 (double), uint16, uint32, uint64, bool, []any, []string and
// map[string]any.
type mmdbNet struct {
	prefix string // IPv4 or IPv6 CIDR; IPv4 is stored at ::a.b.c.d/(96+bits)
	data   map[string]any
}

// writeMMDB encodes fx into a file in t.TempDir and returns its path.
func writeMMDB(t testing.TB, fx mmdbFixture) string {
	t.Helper()
	b, err := encodeMMDB(fx)
	if err != nil {
		t.Fatalf("encode mmdb fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "fixture.mmdb")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// trieNode is a search-tree node. A child is a node (kid), a data record
// (data, 1 + offset into the data section) or empty (both zero).
type trieNode struct {
	kid  [2]*trieNode
	data [2]int
}

func bitAt(key [16]byte, i int) int { return int(key[i/8]>>(7-uint(i%8))) & 1 }

// insert stores ref for the prefix of the given length. Overlapping prefixes
// are rejected: the fixture must describe disjoint networks.
func (n *trieNode) insert(key [16]byte, bits, ref int) error {
	if bits == 0 {
		return fmt.Errorf("a /0 network cannot be encoded")
	}
	for i := 0; i < bits-1; i++ {
		b := bitAt(key, i)
		if n.data[b] != 0 {
			return fmt.Errorf("network overlaps a shorter one at bit %d", i)
		}
		if n.kid[b] == nil {
			n.kid[b] = &trieNode{}
		}
		n = n.kid[b]
	}
	b := bitAt(key, bits-1)
	if n.kid[b] != nil || n.data[b] != 0 {
		return fmt.Errorf("network overlaps another one")
	}
	n.data[b] = ref
	return nil
}

func encodeMMDB(fx mmdbFixture) ([]byte, error) {
	root := &trieNode{}
	var data enc
	for _, nw := range fx.networks {
		pfx, err := netip.ParsePrefix(nw.prefix)
		if err != nil {
			return nil, err
		}
		if pfx != pfx.Masked() {
			return nil, fmt.Errorf("%s: host bits set", pfx)
		}
		key, bits := pfx.Addr().As16(), pfx.Bits()
		if pfx.Addr().Is4() {
			a4 := pfx.Addr().As4()
			key = [16]byte{12: a4[0], 13: a4[1], 14: a4[2], 15: a4[3]}
			bits += 96
		}
		ref := 1 + data.Len()
		if err := data.val(nw.data); err != nil {
			return nil, fmt.Errorf("%s: %w", pfx, err)
		}
		if err := root.insert(key, bits, ref); err != nil {
			return nil, fmt.Errorf("%s: %w", pfx, err)
		}
	}

	if fx.aliasV4 {
		v4 := root
		for i := 0; i < 96 && v4 != nil; i++ {
			v4 = v4.kid[0]
		}
		if v4 == nil {
			return nil, fmt.Errorf("aliasV4 needs IPv4 networks narrower than /0")
		}
		n := root
		alias := netip.MustParseAddr("::ffff:0:0").As16()
		for i := 0; i < 95; i++ {
			b := bitAt(alias, i)
			if n.kid[b] == nil {
				n.kid[b] = &trieNode{}
			}
			n = n.kid[b]
		}
		n.kid[1] = v4
	}

	// Number the nodes breadth-first from the root (node 0). A shared subtree
	// (the IPv4 alias) is numbered once.
	index := map[*trieNode]int{root: 0}
	order := []*trieNode{root}
	for i := 0; i < len(order); i++ {
		for _, k := range order[i].kid {
			if k == nil {
				continue
			}
			if _, seen := index[k]; !seen {
				index[k] = len(order)
				order = append(order, k)
			}
		}
	}
	nodeCount := len(order)

	var tree bytes.Buffer
	for _, n := range order {
		for b := 0; b < 2; b++ {
			v := nodeCount // empty
			switch {
			case n.kid[b] != nil:
				v = index[n.kid[b]]
			case n.data[b] != 0:
				v = nodeCount + 16 + n.data[b] - 1
			}
			if v >= 1<<24 {
				return nil, fmt.Errorf("record value %d does not fit 24 bits", v)
			}
			tree.Write([]byte{byte(v >> 16), byte(v >> 8), byte(v)})
		}
	}

	md := map[string]any{
		"binary_format_major_version": uint16(2),
		"binary_format_minor_version": uint16(0),
		"build_epoch":                 uint64(1700000000),
		"database_type":               fx.dbType,
		"description":                 map[string]any{"en": "ip2geo test fixture"},
		"ip_version":                  uint16(6),
		"node_count":                  uint32(nodeCount),
		"record_size":                 uint16(24),
	}
	if fx.languages != nil {
		md["languages"] = fx.languages
	}
	var meta enc
	if err := meta.val(md); err != nil {
		return nil, err
	}

	var f bytes.Buffer
	f.Write(tree.Bytes())
	f.Write(make([]byte, 16)) // data section separator
	f.Write(data.Bytes())
	f.WriteString("\xab\xcd\xefMaxMind.com")
	f.Write(meta.Bytes())
	return f.Bytes(), nil
}

// enc writes MaxMind DB data-section values.
type enc struct{ bytes.Buffer }

// MaxMind DB type numbers.
const (
	tString  = 2
	tDouble  = 3
	tUint16  = 5
	tUint32  = 6
	tMap     = 7
	tUint64  = 9
	tArray   = 11
	tBoolean = 14
)

// ctrl writes a control byte (plus the extended type byte and size bytes).
func (e *enc) ctrl(typ, size int) {
	var sz int
	var ext []byte
	switch {
	case size < 29:
		sz = size
	case size < 29+256:
		sz, ext = 29, []byte{byte(size - 29)}
	case size < 285+65536:
		v := size - 285
		sz, ext = 30, []byte{byte(v >> 8), byte(v)}
	default:
		v := size - 65821
		sz, ext = 31, []byte{byte(v >> 16), byte(v >> 8), byte(v)}
	}
	if typ <= 7 {
		e.WriteByte(byte(typ<<5 | sz))
	} else {
		e.WriteByte(byte(sz))
		e.WriteByte(byte(typ - 7))
	}
	e.Write(ext)
}

func (e *enc) uint(typ int, v uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	i := 0
	for i < 8 && b[i] == 0 {
		i++
	}
	e.ctrl(typ, 8-i)
	e.Write(b[i:])
}

func (e *enc) val(v any) error {
	switch x := v.(type) {
	case string:
		e.ctrl(tString, len(x))
		e.WriteString(x)
	case float64:
		e.ctrl(tDouble, 8)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], math.Float64bits(x))
		e.Write(b[:])
	case uint16:
		e.uint(tUint16, uint64(x))
	case uint32:
		e.uint(tUint32, uint64(x))
	case uint64:
		e.uint(tUint64, x)
	case bool:
		size := 0
		if x {
			size = 1
		}
		e.ctrl(tBoolean, size)
	case []string:
		e.ctrl(tArray, len(x))
		for _, s := range x {
			e.ctrl(tString, len(s))
			e.WriteString(s)
		}
	case []any:
		e.ctrl(tArray, len(x))
		for _, s := range x {
			if err := e.val(s); err != nil {
				return err
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		e.ctrl(tMap, len(x))
		for _, k := range keys {
			e.ctrl(tString, len(k))
			e.WriteString(k)
			if err := e.val(x[k]); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
	default:
		return fmt.Errorf("unsupported value type %T", v)
	}
	return nil
}
