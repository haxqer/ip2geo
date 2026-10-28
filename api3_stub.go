package ip2geo

// Phase 3 reader API. The signatures and doc comments are the contract; the
// bodies are placeholders until the reader implements revision 2.1, after
// which this file is deleted and the methods live with the reader.

import (
	"errors"
	"iter"
	"net/netip"
)

// ErrLanguage means a language passed to Language or WithLanguage is not one
// of the database's languages (see Reader.Languages).
var ErrLanguage = errors.New("language not in database")

// Language makes Open and OpenBytes return a Reader whose names (in Lookup,
// LookupDetails and Ranges) are in lang, one of the database's languages.
// A name missing in lang is returned in the primary language
// (Metadata.Language). The empty string selects the primary language. Open
// fails with an error wrapping ErrLanguage when the database does not have
// lang.
func Language(lang string) Option {
	return func(o *options) {}
}

// Languages returns the languages of the database's names, the primary one
// first. It is nil for a GEO1 database, which does not record its language.
// The caller may modify the returned slice.
func (r *Reader) Languages() []string { return nil }

// Fields returns the optional field sets the database holds.
func (r *Reader) Fields() FieldSet { return 0 }

// WithLanguage returns a Reader for the same database whose names are in
// lang, with the fallback described at Language. It shares the database's
// memory with r and is cheap: call it once per language, not per lookup.
// It fails with an error wrapping ErrLanguage when the database does not
// have lang.
func (r *Reader) WithLanguage(lang string) (*Reader, error) {
	return nil, errors.New("ip2geo: WithLanguage: not implemented")
}

// LookupDetails is Lookup with every optional field: the Record plus the
// field sets the database holds (see Fields), and for a ProfileASN database
// the ASN. It does not allocate. ok is as for Lookup.
func (r *Reader) LookupDetails(ip netip.Addr) (d Details, ok bool) { return Details{}, false }

// ASN returns the autonomous system of ip. ok is false when ip is not
// covered, and always for a database that is not ProfileASN.
func (r *Reader) ASN(ip netip.Addr) (ASN, bool) { return ASN{}, false }

// LookupRange returns the range of addresses around ip that share its
// answer: the stored boundary range that contains ip. ok reports whether
// that range is covered (as Lookup's ok); an uncovered gap is returned with
// ok == false. For an IPv4 address, and for the IPv6 forms Lookup maps to
// IPv4, the range is the IPv4 one, in IPv4 form. The zero netip.Addr
// returns the zero Range and false.
func (r *Reader) LookupRange(ip netip.Addr) (Range, bool) { return Range{}, false }

// Ranges iterates over the covered ranges in ascending order, the IPv4 ones
// (in IPv4 form) before the IPv6 ones, each with its details as
// LookupDetails would return them. Gaps are skipped; adjacent ranges may
// share a record when the source split them. IPv6 aliases of IPv4 space are
// not repeated.
func (r *Reader) Ranges() iter.Seq2[Range, Details] {
	return func(yield func(Range, Details) bool) {}
}
