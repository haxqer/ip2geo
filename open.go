package ip2geo

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"sync/atomic"

	"github.com/haxqer/ip2geo/internal/format"
)

// An Option changes how Open or OpenBytes loads a database.
type Option func(*options)

type options struct {
	noMmap       bool
	skipChecksum bool
	noCopy       bool
}

// NoMmap makes Open read the file into the Go heap instead of memory-mapping
// it. OpenBytes ignores it.
func NoMmap() Option { return func(o *options) { o.noMmap = true } }

// SkipChecksum skips the CRC-32C verification of a GEO2 file's sections,
// which reads every byte of the file at open. Every other check still runs,
// so a damaged file cannot make a lookup fail, but it may answer wrongly.
// GEO1 files have no checksums.
func SkipChecksum() Option { return func(o *options) { o.skipChecksum = true } }

// NoCopy makes OpenBytes use the caller's slice in place for a GEO2 database
// instead of copying it: the Reader then refers to b, which must not be
// modified afterwards for as long as the Reader is in use. Strings are
// still copied, so a returned Record never refers to b. A GEO1 database is
// always converted into new memory. Open ignores NoCopy.
func NoCopy() Option { return func(o *options) { o.noCopy = true } }

func makeOptions(opts []Option) options {
	var o options
	for _, f := range opts {
		f(&o)
	}
	return o
}

// unmapped counts the mappings released by the Reader cleanup; tests read it.
var unmapped atomic.Int64

// Open loads a compact database file. It refuses files larger than 2 GiB
// with ErrTooLarge and checks the header before reading the rest, so a
// wrong path fails fast. The contents are then validated as by OpenBytes.
// Errors from the file system are returned as they are.
//
// A regular file is memory-mapped on Unix and Windows unless NoMmap is
// given; elsewhere, for other kinds of files, or when mapping fails, it is
// read into the heap. A GEO2 file stays mapped, and the garbage collector
// unmaps it once the Reader is unreachable (Close does not); see the
// package documentation for why the file must not change meanwhile. A GEO1
// file is converted to the GEO2 layout on the heap and unmapped before Open
// returns.
func Open(path string, opts ...Option) (*Reader, error) {
	o := makeOptions(opts)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := fi.Size()
	if size > maxFileSize || size >= math.MaxInt { // MaxInt bounds 32-bit builds
		return nil, fmt.Errorf("ip2geo: %s is %d bytes, over the %d-byte limit: %w", path, size, int64(maxFileSize), ErrTooLarge)
	}

	var hdr [format.HeaderSize]byte
	n, err := io.ReadFull(f, hdr[:])
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	if err := checkAnyHeader(hdr[:n], size, fi.Mode().IsRegular()); err != nil {
		return nil, err
	}

	if !o.noMmap && fi.Mode().IsRegular() && size >= int64(n) {
		if m, err := mmapFile(f, int(size)); err == nil {
			return openMapped(m, &o)
		}
		// Mapping is not supported here or failed: read the file instead.
	}

	// Stat's size is a hint: the file may change underfoot, and pipes and
	// special files report 0. Read to EOF, but never past maxFileSize.
	b := make([]byte, n, int(max(size, int64(n))+1))
	copy(b, hdr[:n])
	for {
		if len(b) == cap(b) {
			b = append(b, 0)[:len(b)]
		}
		m, err := f.Read(b[len(b):cap(b)])
		b = b[:len(b)+m]
		if uint64(len(b)) > maxFileSize {
			return nil, fmt.Errorf("ip2geo: %s is over the %d-byte limit: %w", path, int64(maxFileSize), ErrTooLarge)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return openBuffer(b, &o, true)
}

// checkAnyHeader checks the first bytes of a file before the rest is read:
// the magic, then the GEO1 header, or the GEO2 header and, for a regular
// file of the given size, that its directory fits.
func checkAnyHeader(h []byte, size int64, regular bool) error {
	if len(h) >= 4 && string(h[:4]) == format.Magic {
		hd, err := format.ParseHeader(h)
		if err != nil {
			return headerError(h, err)
		}
		if end := format.HeaderSize + uint64(hd.SectionCount)*format.DirEntrySize; regular && end > uint64(size) {
			return corrupt("directory of %d entries needs %d bytes, the file has %d", hd.SectionCount, end, size)
		}
		return nil
	}
	if len(h) < len(magic) || string(h[:len(magic)]) != magic {
		return badMagic(h)
	}
	return checkHeader(h)
}

func badMagic(h []byte) error {
	return fmt.Errorf("ip2geo: magic %q, want %q or %q: %w", h[:min(len(h), 4)], format.Magic, magic, ErrBadMagic)
}

// openMapped opens a database from a memory mapping, which it either hands
// to the Reader, released by a cleanup when the Reader is unreachable, or
// releases before returning.
func openMapped(m []byte, o *options) (*Reader, error) {
	if len(m) >= 4 && string(m[:4]) == format.Magic {
		g, err := parseGEO2(m, o.skipChecksum)
		if err != nil {
			_ = munmap(m)
			return nil, err
		}
		r := g.reader(m)
		r.mapped = true
		runtime.AddCleanup(r, releaseMapping, m)
		return r, nil
	}
	r, err := openGEO1(m)
	if uerr := munmap(m); err == nil && uerr != nil {
		return nil, fmt.Errorf("ip2geo: unmap: %w", uerr)
	}
	return r, err
}

func releaseMapping(m []byte) {
	_ = munmap(m)
	unmapped.Add(1)
}

// OpenBytes parses a compact database held in memory, GEO2 or GEO1. b is not
// retained unless NoCopy is given.
//
// The whole input is validated first. For GEO2 that is every rule of the
// reader requirements in docs/FORMAT.md: the header (magic, version 2, a
// known profile, a lat/lon scale of at most 7, an id width of 1 to 4, zero
// reserved bytes); a directory that fits, with sections 8-byte aligned, in
// order, not overlapping and inside the input; every required section once,
// with a valid length; the CRC-32C of every section (unless SkipChecksum is
// given); string offsets from 0 to the end of the string data, an empty
// string 0, and every string id in META and the records below the string
// count; at least one record, record 0 all zero, the minimal id width and
// every record id below the record count; a non-decreasing V4IDX from 0 to
// the IPv4 start count and strictly ascending IPv4 starts; and strictly
// ascending IPv6 starts with a strictly ascending, in-range exception table
// of non-zero low halves. Sections with unknown ids are ignored.
//
// For GEO1 it is the header (magic, version 1, a known profile, a lat/lon
// scale of at most 7, a zero reserved byte), every count against the bytes
// left, a record width equal to RecWidth(profile), at least one record,
// every record id below the record count, strictly ascending IPv4 and IPv6
// range starts, and no trailing bytes.
//
// Inputs over 2 GiB are refused. Errors wrap ErrBadMagic,
// ErrUnsupportedVersion, ErrCorrupt or ErrTooLarge.
func OpenBytes(b []byte, opts ...Option) (*Reader, error) {
	if uint64(len(b)) > maxFileSize {
		return nil, fmt.Errorf("ip2geo: input is %d bytes, over the %d-byte limit: %w", len(b), int64(maxFileSize), ErrTooLarge)
	}
	o := makeOptions(opts)
	return openBuffer(b, &o, o.noCopy)
}

// openBuffer opens a database in b. A GEO2 database keeps b if retain is
// set, else a copy of it made once b is validated.
func openBuffer(b []byte, o *options, retain bool) (*Reader, error) {
	if len(b) >= 4 && string(b[:4]) == format.Magic {
		g, err := parseGEO2(b, o.skipChecksum)
		if err != nil {
			return nil, err
		}
		if !retain {
			b = bytes.Clone(b)
		}
		return g.reader(b), nil
	}
	if len(b) < len(magic) || string(b[:len(magic)]) != magic {
		return nil, badMagic(b)
	}
	return openGEO1(b)
}
