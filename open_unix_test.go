//go:build darwin || linux

package ip2geo

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestOpenDevZero: a special file of unbounded length is refused on its
// header instead of being read until memory runs out.
func TestOpenDevZero(t *testing.T) {
	if r, err := Open("/dev/zero"); r != nil || !errors.Is(err, ErrBadMagic) {
		t.Fatalf("Open(/dev/zero) = %v, %v; want nil, ErrBadMagic", r, err)
	}
}

// TestOpenFIFO: Stat reports size 0 for a pipe, so Open must read to EOF.
func TestOpenFIFO(t *testing.T) {
	p := filepath.Join(t.TempDir(), "db.fifo")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	db := encodeCityDB()
	done := make(chan error, 1)
	go func() {
		w, err := os.OpenFile(p, os.O_WRONLY, 0)
		if err != nil {
			done <- err
			return
		}
		_, err = w.Write(db)
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		done <- err
	}()
	r, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if rec, ok := r.Lookup(netip.MustParseAddr("10.0.0.5")); !ok || rec.City != "Los Angeles" {
		t.Errorf("Lookup = %+v, %v", rec, ok)
	}
}
