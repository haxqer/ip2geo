package mmdb_test

import (
	"encoding/binary"
	"math/rand"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/haxqer/ip2geo"
	"github.com/haxqer/ip2geo/mmdb"
	maxminddb "github.com/oschwald/maxminddb-golang/v2"
)

// TestBuildMatchesSource builds a country-profile database from a real MaxMind
// City .mmdb (path in IP2GEO_TEST_MMDB) and verifies that ip2geo returns the
// same country as the source database for many random IPs. Skipped if the env
// var is unset, so CI stays hermetic.
func TestBuildMatchesSource(t *testing.T) {
	src := os.Getenv("IP2GEO_TEST_MMDB")
	if src == "" {
		t.Skip("set IP2GEO_TEST_MMDB to a GeoIP2-City .mmdb to run")
	}

	out := filepath.Join(t.TempDir(), "country.geoc")
	if _, err := mmdb.Build(src, out, mmdb.Options{Profile: ip2geo.ProfileCountry}); err != nil {
		t.Fatalf("build: %v", err)
	}

	db, err := ip2geo.Open(out)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	mm, err := maxminddb.Open(src)
	if err != nil {
		t.Fatalf("open mmdb: %v", err)
	}
	defer mm.Close()

	r := rand.New(rand.NewSource(1))
	mismatch := 0
	const N = 200000
	for i := 0; i < N; i++ {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], r.Uint32())
		ip := netip.AddrFrom4(b)

		var want struct {
			Country struct {
				ISOCode string `maxminddb:"iso_code"`
			} `maxminddb:"country"`
		}
		_ = mm.Lookup(ip).Decode(&want)

		rec, ok := db.Lookup(ip)
		got := ""
		if ok {
			got = rec.CountryISO
		}
		if got != want.Country.ISOCode {
			if mismatch < 10 {
				t.Errorf("%s: got %q want %q", ip, got, want.Country.ISOCode)
			}
			mismatch++
		}
	}
	if mismatch > 0 {
		t.Fatalf("%d/%d mismatches", mismatch, N)
	}
	t.Logf("verified %d random IPs, 0 mismatches", N)
}

// TestOpenInMemory exercises the one-call drop-in path: build the reader in
// memory straight from the .mmdb, no intermediate file.
func TestOpenInMemory(t *testing.T) {
	src := os.Getenv("IP2GEO_TEST_MMDB")
	if src == "" {
		t.Skip("set IP2GEO_TEST_MMDB to a GeoIP2-City .mmdb to run")
	}
	db, err := mmdb.Open(src, mmdb.Options{Profile: ip2geo.ProfileCity})
	if err != nil {
		t.Fatalf("mmdb.Open: %v", err)
	}
	defer db.Close()

	rec, ok := db.Lookup(netip.MustParseAddr("81.2.69.142"))
	if !ok || rec.CountryISO != "GB" || rec.City != "Wimbledon" {
		t.Fatalf("81.2.69.142 -> %+v ok=%v", rec, ok)
	}
	if _, ok := db.Lookup(netip.MustParseAddr("2001:4860:4860::8888")); !ok {
		t.Fatal("expected IPv6 data for 2001:4860:4860::8888")
	}
}
