package conformance

import (
	"encoding/binary"
	"net/netip"
)

// geo1Dataset is the source of a GEO1 (v0.1.x) file, which internal/build
// no longer writes: it is encoded here, field by field, as docs/FORMAT.md
// describes the legacy layout.
type geo1Dataset struct {
	file     string
	about    string
	profile  uint8
	latScale uint8
	strings  []string
	records  [][12]uint32 // the u32 fields of a city record
	v4       []geo1Start
	v6       []geo1Start
	queries  []query
}

// geo1Start is a range start and its record id.
type geo1Start struct {
	start netip.Addr
	id    uint32
}

func s1(addr string, id uint32) geo1Start { return geo1Start{netip.MustParseAddr(addr), id} }

// i32 returns the u32 bit pattern of a signed coordinate.
func i32(v int32) uint32 { return uint32(v) }

func geo1CityDataset() geo1Dataset {
	return geo1Dataset{
		file:     "geo1-city.geoc",
		about:    "GEO1 (v0.1.x) city file, transcoded by the reader at open: string id 0 reads as empty even when string 0 is not, a string id past the table reads as empty, record 0 is never decoded, a u32 accuracy above 65535 keeps its low 16 bits, unmerged adjacent starts with the same record, an explicit gap start at 0.0.0.0",
		profile:  2,
		latScale: 4,
		strings: []string{
			0:  "Ghost", // id 0 always reads as ""
			1:  "QT",
			2:  "Testland",
			3:  "XC",
			4:  "North Testshire",
			5:  "T1",
			6:  "Oldtown",
			7:  "Testland/Capitalia",
			8:  "Unused string",
			9:  "QM",
			10: "Mockovïa",
			11: "Ünïcode City 🧪",
		},
		records: [][12]uint32{
			// countryISO, countryName, continent, countryGeo, regionISO,
			// regionName, city, cityGeo, lat, lon, accuracy, timeZone
			0: {1, 2, 3, 9000001, 5, 4, 6, 9100001, 1, 2, 3, 7}, // never read
			1: {1, 2, 3, 9000001, 5, 4, 6, 9100001, i32(123456), i32(-654321), 25, 7},
			2: {9, 10, 3, 9000002, 0, 0, 11, 9100002, i32(-900000), i32(1800000), 1000, 999}, // time zone id past the table
			3: {},
			4: {1, 0, 3, 9000001, 5, 4, 0, 0, 0, 0, 70000, 7}, // name id 0; accuracy over u16
		},
		v4: []geo1Start{
			s1("0.0.0.0", 0),
			s1("1.0.0.0", 1),
			s1("1.0.1.0", 1), // same record as the previous start
			s1("1.0.2.0", 0),
			s1("2.0.0.0", 2),
			s1("2.0.0.255", 3),
			s1("3.0.0.0", 4),
			s1("4.0.0.0", 0),
			s1("255.255.255.0", 1),
		},
		v6: []geo1Start{
			s1("2001:db8::", 1),
			s1("2001:db8::8000:0:0:0", 2),
			s1("2001:db8:0:1::", 0),
			s1("2001:db8:0:1::1", 3),
			s1("2001:db8:0:1::2", 0),
			s1("ffff:ffff:ffff:ffff:ffff:ffff:ffff:ff00", 4),
		},
		queries: []query{
			q("::ffff:1.0.0.1", "IPv4-mapped alias of 1.0.0.1"),
			q("2002:200:0::", "6to4 alias of 2.0.0.0"),
			q("2001:0:300:1::", "Teredo alias of 3.0.0.1"),
			q("::300:1", "IPv4-compatible alias of 3.0.0.1"),
			q("2001:db8::7fff:ffff:ffff:ffff", "last address before the start with a non-zero low half"),
		},
	}
}

// encodeGEO1 returns the GEO1 encoding of d.
func encodeGEO1(d *geo1Dataset) []byte {
	le := binary.LittleEndian
	b := []byte{'G', 'E', 'O', '1', 1, d.profile, d.latScale, 0}
	b = le.AppendUint32(b, uint32(len(d.strings)))
	for _, s := range d.strings {
		b = le.AppendUint16(b, uint16(len(s)))
		b = append(b, s...)
	}
	nf := geo1Fields(d.profile)
	b = le.AppendUint32(b, uint32(len(d.records)))
	b = le.AppendUint16(b, uint16(nf*4))
	for _, r := range d.records {
		for _, f := range r[:nf] {
			b = le.AppendUint32(b, f)
		}
	}
	b = le.AppendUint32(b, uint32(len(d.v4)))
	for _, s := range d.v4 {
		a := s.start.As4()
		b = le.AppendUint32(b, binary.BigEndian.Uint32(a[:]))
	}
	for _, s := range d.v4 {
		b = le.AppendUint32(b, s.id)
	}
	b = le.AppendUint32(b, uint32(len(d.v6)))
	for _, s := range d.v6 {
		a := s.start.As16()
		b = append(b, a[:]...)
	}
	for _, s := range d.v6 {
		b = le.AppendUint32(b, s.id)
	}
	return b
}

// geo1Fields returns the number of u32 fields of a GEO1 record.
func geo1Fields(profile uint8) int {
	switch profile {
	case 0:
		return 4
	case 1:
		return 6
	default:
		return 12
	}
}
