package ip2geo

import (
	"net/netip"
	"runtime"
	"testing"
	"time"
)

// copyProbes cover IPv4, IPv4 in IPv6 forms, IPv6 with and without
// exceptions, and uncovered addresses.
var copyProbes = []netip.Addr{
	netip.MustParseAddr("10.0.0.5"),
	netip.MustParseAddr("::ffff:10.0.0.255"),
	netip.MustParseAddr("2002:a00:1::"),
	netip.MustParseAddr("192.0.2.1"),
	netip.MustParseAddr("2001:db8::1"),
	netip.MustParseAddr("2001:db8::8000:0:0:0"),
	netip.MustParseAddr("8.8.8.8"),
	netip.MustParseAddr("2001:db9::"),
}

// checkLikeRef checks every lookup entry point of r against ref, a heap
// Reader of the same database.
func checkLikeRef(t *testing.T, name string, r, ref *Reader) {
	t.Helper()
	for _, ip := range copyProbes {
		g1, ok1 := r.Lookup(ip)
		w1, wok1 := ref.Lookup(ip)
		sameAnswer(t, name, "Lookup", ip, g1, ok1, w1, wok1)
		g2, ok2 := r.LookupDetails(ip)
		w2, wok2 := ref.LookupDetails(ip)
		sameAnswer(t, name, "LookupDetails", ip, g2, ok2, w2, wok2)
		g3, ok3 := r.ASN(ip)
		w3, wok3 := ref.ASN(ip)
		sameAnswer(t, name, "ASN", ip, g3, ok3, w3, wok3)
		g4, ok4 := r.LookupRange(ip)
		w4, wok4 := ref.LookupRange(ip)
		sameAnswer(t, name, "LookupRange", ip, g4, ok4, w4, wok4)
	}
	type entry struct {
		rg Range
		d  Details
	}
	var want []entry
	for rg, d := range ref.Ranges() {
		want = append(want, entry{rg, d})
	}
	k := 0
	for rg, d := range r.Ranges() {
		if k >= len(want) || (entry{rg, d}) != want[k] {
			t.Fatalf("%s: Ranges[%d] = %v %+v, want %d ranges like the reference", name, k, rg, d, len(want))
		}
		k++
	}
	if k != len(want) {
		t.Fatalf("%s: Ranges: %d ranges, want %d", name, k, len(want))
	}
}

func sameAnswer[T comparable](t *testing.T, name, fn string, ip netip.Addr, got T, ok bool, want T, wok bool) {
	t.Helper()
	if got != want || ok != wok {
		t.Fatalf("%s: %s(%s) = %+v, %v; want %+v, %v", name, fn, ip, got, ok, want, wok)
	}
}

// TestCopyKeepsMapping copies mapped Readers by value and drops the
// pointers Open returned. The copies, and a WithLanguage view of a copy,
// must keep the mapping while the collector runs, and once every copy and
// view is unreachable the mapping must be released.
func TestCopyKeepsMapping(t *testing.T) {
	if !mmapSupported {
		t.Skip("no memory mapping on " + runtime.GOOS)
	}
	cityDB := laFile(excTestDB()).encode()
	asnDB := toGEO2(asnTestDB(), g2opts{meta: testMeta}).encode()
	cityRef, asnRef := openGEO2(t, cityDB), openGEO2(t, asnDB)
	deRef, err := cityRef.WithLanguage("de")
	if err != nil {
		t.Fatal(err)
	}
	cityPath, asnPath := writeMapped(t, cityDB), writeMapped(t, asnDB)
	// openCopy returns a copy of the Reader Open returns, and drops that.
	openCopy := func(p string) Reader {
		r, err := Open(p)
		if err != nil {
			t.Fatal(err)
		}
		if r.m == nil {
			t.Fatal("the Reader is not mapped")
		}
		return *r
	}
	before := drainCleanups()

	view := func() *Reader {
		city, asn := openCopy(cityPath), openCopy(asnPath)
		view, err := city.WithLanguage("de")
		if err != nil {
			t.Fatal(err)
		}
		if n := drainCleanups(); n != before {
			t.Fatalf("%d mappings released while copies of their Readers are reachable", n-before)
		}
		for range 20 {
			runtime.GC()
			checkLikeRef(t, "city copy", &city, cityRef)
			checkLikeRef(t, "ASN copy", &asn, asnRef)
			checkLikeRef(t, "view of a copy", view, deRef)
		}
		return view
	}()

	// Only the view of the city copy is left: the ASN mapping goes, the
	// city one stays.
	if n := drainCleanups(); n != before+1 {
		t.Fatalf("%d mappings released with only a view reachable, want 1", n-before)
	}
	for range 20 {
		runtime.GC()
		checkLikeRef(t, "view of a dropped copy", view, deRef)
	}
	runtime.KeepAlive(view) // view is unreachable from here on

	for deadline := time.Now().Add(5 * time.Second); unmapped.Load() < before+2; {
		if time.Now().After(deadline) {
			t.Fatal("the mapping was not released after every copy and view became unreachable")
		}
		runtime.GC()
		time.Sleep(time.Millisecond)
	}
	if n := drainCleanups(); n != before+2 {
		t.Errorf("%d mappings released, want 2", n-before)
	}
}

// TestCopyLookupsUnderGC looks up through a value copy of a mapped Reader
// while the collector runs on another goroutine and the pointer Open
// returned is gone.
func TestCopyLookupsUnderGC(t *testing.T) {
	if !mmapSupported {
		t.Skip("no memory mapping on " + runtime.GOOS)
	}
	db := laFile(excTestDB()).encode()
	ref := openGEO2(t, db)
	p := writeMapped(t, db)
	c := func() Reader {
		r, err := Open(p)
		if err != nil {
			t.Fatal(err)
		}
		return *r
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 50 {
			runtime.GC()
			time.Sleep(time.Millisecond)
		}
	}()
	for {
		select {
		case <-done:
			checkLikeRef(t, "copy", &c, ref)
			return
		default:
			checkLikeRef(t, "copy", &c, ref)
		}
	}
}
