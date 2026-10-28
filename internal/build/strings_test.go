package build

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// TestInternerFinish: strings of 16-bit fields come first, then the
// others, each group in first-seen order; a string used both ways is a
// 16-bit one; remap maps every provisional id to the file id.
func TestInternerFinish(t *testing.T) {
	in := newInterner()
	meta := in.id("GeoIP2-City")
	city := in.id("Paris")
	tz := in.id16("Europe/Paris")
	both := in.id("Tokyo")
	in.id16("Tokyo")
	iso := in.id16("FR")
	if in.id("") != 0 || in.id16("") != 0 || in.id("Paris") != city {
		t.Fatal("id is not stable")
	}

	strs, remap, nU16 := in.finish()
	if want := []string{"", "Europe/Paris", "Tokyo", "FR", "GeoIP2-City", "Paris"}; !slices.Equal(strs, want) || nU16 != 4 {
		t.Fatalf("finish = %q, %d; want %q, 4", strs, nU16, want)
	}
	for prov, want := range map[uint32]string{0: "", meta: "GeoIP2-City", city: "Paris", tz: "Europe/Paris", both: "Tokyo", iso: "FR"} {
		if got := strs[remap[prov]]; got != want {
			t.Errorf("remap[%d] -> %q, want %q", prov, got, want)
		}
	}
}

func TestCheckStringLimits(t *testing.T) {
	for _, tt := range []struct {
		nStrings, nU16 int
		blob           uint64
		want           string // substring of the error; "" means no error
	}{
		{1, 1, 0, ""},
		{maxStrings, maxU16Strings, maxStrBlob, ""},
		{maxStrings, maxU16Strings + 1, 0, "65536 distinct strings in country"},
		{maxStrings + 1, 1, 0, "16777217 distinct strings"},
		{2, 1, maxStrBlob + 1, "4294967296 bytes of strings"},
	} {
		err := checkStringCounts("src.mmdb", tt.nStrings, tt.nU16, tt.blob)
		if tt.want == "" {
			if err != nil {
				t.Errorf("%d strings, %d of 16-bit fields, %d bytes: %v", tt.nStrings, tt.nU16, tt.blob, err)
			}
			continue
		}
		if !errors.Is(err, ErrTooManyStrings) || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%d strings, %d of 16-bit fields, %d bytes: err = %v, want ErrTooManyStrings with %q",
				tt.nStrings, tt.nU16, tt.blob, err, tt.want)
		}
	}

	long := strings.Repeat("x", 1<<16)
	if err := checkStrings("src.mmdb", []string{"", long}, 1); !errors.Is(err, ErrStringTooLong) {
		t.Errorf("a 65536-byte string: err = %v, want ErrStringTooLong", err)
	}
	if err := checkStrings("src.mmdb", []string{"", long[1:]}, 1); err != nil {
		t.Errorf("a 65535-byte string: %v", err)
	}
}
