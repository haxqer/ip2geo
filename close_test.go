package ip2geo

import (
	"fmt"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

func TestLookupAfterClose(t *testing.T) {
	r := openTest(t)
	ip := netip.MustParseAddr("10.0.0.5")
	want, _ := r.Lookup(ip)
	for i := 0; i < 2; i++ {
		if err := r.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i+1, err)
		}
	}
	if got, ok := r.Lookup(ip); !ok || got != want {
		t.Errorf("Lookup after Close = %+v, %v; want %+v, true", got, ok, want)
	}
	if r.Profile() != ProfileCity {
		t.Errorf("Profile after Close = %v", r.Profile())
	}
}

// TestCloseDuringLookups models hot reload: lookups load the current Reader
// from an atomic.Pointer while a reloader swaps in another one and Closes the
// old one. Under -race this fails if Close writes anything Lookup reads;
// without it, every lookup must still return the right record. Readers come
// from a small pool, so Closed readers are published again, and each reload
// waits for lookups to progress so that every Close overlaps them.
func TestCloseDuringLookups(t *testing.T) {
	const (
		lookupers = 8
		reloads   = 2000
		poolSize  = 8
	)
	probes := []struct {
		ip  netip.Addr
		iso string
	}{
		{netip.MustParseAddr("10.0.0.5"), "US"},
		{netip.MustParseAddr("2001:db8::1"), "DE"},
		{netip.MustParseAddr("2002:a00:5::1"), "US"},
		{netip.MustParseAddr("192.0.2.1"), ""},
	}
	pool := make([]*Reader, poolSize)
	for i := range pool {
		pool[i] = openTest(t)
	}

	var cur atomic.Pointer[Reader]
	cur.Store(openTest(t))
	var (
		stop    atomic.Bool
		lookups atomic.Int64
		wg      sync.WaitGroup
	)
	errs := make(chan string, lookupers)
	for g := 0; g < lookupers; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					errs <- fmt.Sprint("panic: ", p)
				}
			}()
			for n := 0; !stop.Load(); n++ {
				p := probes[n%len(probes)]
				rec, ok := cur.Load().Lookup(p.ip)
				if ok != (p.iso != "") || rec.CountryISO != p.iso {
					errs <- fmt.Sprintf("%v: got %q ok=%v, want %q", p.ip, rec.CountryISO, ok, p.iso)
					return
				}
				lookups.Add(1)
			}
		}()
	}

	for i := 0; i < reloads && len(errs) == 0; i++ {
		if err := cur.Swap(pool[i%poolSize]).Close(); err != nil {
			t.Error(err)
			break
		}
		for seen := lookups.Load(); lookups.Load() < seen+lookupers && len(errs) == 0; {
			runtime.Gosched()
		}
	}
	stop.Store(true)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	t.Logf("%d reloads, %d lookups", reloads, lookups.Load())
}
