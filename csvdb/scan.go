package csvdb

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
)

// scanner reads a CSV source one row at a time. It handles the subset of
// RFC 4180 the vendors write: comma-separated fields, each optionally in
// double quotes with "" for a quote inside, and LF or CRLF line ends. A
// quoted field cannot span lines. Blank lines are skipped.
//
// The fields of a row point into the reader's buffer and are valid until
// the next call to next.
type scanner struct {
	name string // the source, for error messages
	br   *bufio.Reader
	line int    // number of the current line, from 1
	long []byte // a line longer than the reader's buffer
	row  [][]byte
}

func newScanner(name string, r io.Reader) *scanner {
	return &scanner{name: name, br: bufio.NewReaderSize(r, 1<<20)}
}

// errorf returns an ErrSyntax error for the current line.
func (s *scanner) errorf(format string, args ...any) error {
	return fmt.Errorf("csvdb: %s:%d: %s: %w", s.name, s.line, fmt.Sprintf(format, args...), ErrSyntax)
}

// next reads and splits the next non-blank line. It returns false at the
// end of the source.
func (s *scanner) next() (bool, error) {
	for {
		line, err := s.br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			s.long = append(s.long[:0], line...)
			for errors.Is(err, bufio.ErrBufferFull) {
				line, err = s.br.ReadSlice('\n')
				s.long = append(s.long, line...)
			}
			line = s.long
		}
		if err != nil && err != io.EOF {
			return false, fmt.Errorf("csvdb: read %q: %w", s.name, err)
		}
		if len(line) == 0 && err == io.EOF {
			return false, nil
		}
		s.line++
		line = bytes.TrimSuffix(line, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(line) == 0 {
			if err == io.EOF {
				return false, nil
			}
			continue
		}
		return true, s.split(line)
	}
}

// split splits line into s.row, unquoting quoted fields in place.
func (s *scanner) split(line []byte) error {
	s.row = s.row[:0]
	for i := 0; ; {
		if i < len(line) && line[i] == '"' {
			// A quoted field: copy it down over its quotes, turning "" into ".
			start, w := i, i
			i++
			for {
				j := bytes.IndexByte(line[i:], '"')
				if j < 0 {
					return s.errorf("field %d: unterminated quote", len(s.row)+1)
				}
				w += copy(line[w:], line[i:i+j])
				i += j + 1
				if i < len(line) && line[i] == '"' {
					line[w] = '"'
					w++
					i++
					continue
				}
				break
			}
			s.row = append(s.row, line[start:w])
			switch {
			case i == len(line):
				return nil
			case line[i] != ',':
				return s.errorf("field %d: text after the closing quote", len(s.row))
			}
			i++
			continue
		}
		j := bytes.IndexByte(line[i:], ',')
		f := line[i:]
		if j >= 0 {
			f = line[i : i+j]
		}
		if bytes.IndexByte(f, '"') >= 0 {
			return s.errorf("field %d: quote in an unquoted field", len(s.row)+1)
		}
		s.row = append(s.row, f)
		if j < 0 {
			return nil
		}
		i += j + 1
	}
}

// parseUint32 parses a decimal number of at most 32 bits.
func parseUint32(b []byte) (uint32, bool) {
	if len(b) == 0 || len(b) > 10 {
		return 0, false
	}
	var v uint64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + uint64(c-'0')
	}
	return uint32(v), v <= math.MaxUint32
}

// parseUint128 parses a decimal number of at most 128 bits into its high
// and low 64 bits.
func parseUint128(b []byte) (hi, lo uint64, ok bool) {
	if len(b) == 0 || len(b) > 39 {
		return 0, 0, false
	}
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, 0, false
		}
		// (hi, lo) = (hi, lo)*10 + c, failing on a carry out of hi.
		h1, h0 := bits.Mul64(hi, 10)
		l1, l0 := bits.Mul64(lo, 10)
		var carry uint64
		lo, carry = bits.Add64(l0, uint64(c-'0'), 0)
		var c2 uint64
		hi, c2 = bits.Add64(h0, l1, carry)
		if h1 != 0 || c2 != 0 {
			return 0, 0, false
		}
	}
	return hi, lo, true
}
