package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const conformance = "../../testdata/conformance/"

// install puts the conformance database name at dir/db.geoc the way a
// database should be replaced under a running server: written beside it and
// renamed over it. It returns the path.
func install(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(conformance + name)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "db.geoc")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func startServer(t *testing.T, name string) (*server, *httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := newServer(install(t, dir, name))
	if err != nil {
		t.Fatal(err)
	}
	s.logf = t.Logf
	ts := httptest.NewServer(s.handler())
	t.Cleanup(ts.Close)
	return s, ts, dir
}

func get(c *http.Client, url string, v any) (int, error) {
	resp, err := c.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return resp.StatusCode, err
	}
	return resp.StatusCode, nil
}

func profileOf(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	var info struct{ Profile string }
	if _, err := get(ts.Client(), ts.URL+"/info", &info); err != nil {
		t.Fatal(err)
	}
	return info.Profile
}

func TestLookup(t *testing.T) {
	_, ts, _ := startServer(t, "city.geoc")
	tests := []struct {
		query  string
		status int
		want   lookupResult
	}{
		{"?ip=1.0.0.1", 200, lookupResult{
			IP: "1.0.0.1", Found: true, First: "1.0.0.0", Last: "1.0.0.255",
			CountryISO: "QT", CountryName: "Testland", Continent: "XC", RegionISO: "T1", RegionName: "North Testshire",
			City: "Testville", Location: &location{12.3456, -65.4321, 20, "Testland/Capitalia"},
		}},
		{"?ip=::ffff:1.0.2.1", 200, lookupResult{
			IP: "::ffff:1.0.2.1", Found: true, First: "1.0.2.0", Last: "1.0.2.255",
			CountryISO: "QM", CountryName: "Mockovïa", Continent: "YC",
			City: "Southpole Station", Location: &location{-90, 180, 1000, "Mockovia/Pole"},
		}},
		{"?ip=0.0.0.4", 200, lookupResult{IP: "0.0.0.4", First: "0.0.0.4", Last: "0.255.255.255"}},
		{"?ip=1.2.3", 400, lookupResult{}},
	}
	for _, tc := range tests {
		var got lookupResult
		status, err := get(ts.Client(), ts.URL+"/lookup"+tc.query, &got)
		if err != nil || status != tc.status {
			t.Errorf("%q: status %d, %v; want %d", tc.query, status, err, tc.status)
			continue
		}
		if tc.status != 200 {
			continue
		}
		gj, _ := json.Marshal(got)
		wj, _ := json.Marshal(tc.want)
		if string(gj) != string(wj) {
			t.Errorf("%q:\n got %s\nwant %s", tc.query, gj, wj)
		}
	}

	// Without ip, the client's own address.
	var got lookupResult
	if status, err := get(ts.Client(), ts.URL+"/lookup", &got); err != nil || status != 200 || got.IP != "127.0.0.1" {
		t.Errorf("lookup without ip: status %d, %+v, %v; want the client's address", status, got, err)
	}

	_, asnTS, _ := startServer(t, "asn.geoc")
	got = lookupResult{}
	if _, err := get(asnTS.Client(), asnTS.URL+"/lookup?ip=5.1.1.1", &got); err != nil {
		t.Fatal(err)
	}
	if !got.Found || got.ASN == nil || *got.ASN != (asn{0, ""}) || got.Location != nil {
		t.Errorf("ASN 0 lookup: %+v", got)
	}
}

// TestReloadDuringLookups swaps the database file back and forth between
// two profiles while clients look up addresses with the same country in
// both. No request may fail or see a wrong answer, and every retired Reader
// must become unreachable, which is when ip2geo unmaps its file. Run it with
// -race.
func TestReloadDuringLookups(t *testing.T) {
	const (
		clients = 8
		reloads = 20
	)
	s, ts, dir := startServer(t, "city.geoc")
	probes := []struct{ ip, iso string }{{"1.0.0.1", "QT"}, {"1.0.2.1", "QM"}, {"::ffff:1.0.0.9", "QT"}}

	var (
		stop    atomic.Bool
		lookups atomic.Int64
		wg      sync.WaitGroup
	)
	defer stop.Store(true) // on t.Fatal
	errs := make(chan error, clients)
	for g := 0; g < clients; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; !stop.Load(); n++ {
				p := probes[n%len(probes)]
				var got lookupResult
				status, err := get(ts.Client(), ts.URL+"/lookup?ip="+p.ip, &got)
				if err == nil && (status != 200 || !got.Found || got.CountryISO != p.iso) {
					err = fmt.Errorf("%s: status %d, %+v; want %s", p.ip, status, got, p.iso)
				}
				if err != nil {
					errs <- err
					return
				}
				lookups.Add(1)
			}
		}()
	}

	released := make(chan struct{}, reloads)
	files := []string{"country.geoc", "city.geoc"}
	for i := 0; i < reloads && len(errs) == 0; i++ {
		// Ask to hear when the Reader about to be retired is unreachable.
		runtime.AddCleanup(s.db.Load(), func(ch chan struct{}) { ch <- struct{}{} }, released)
		install(t, dir, files[i%2])
		if ok, err := s.reload(false); !ok || err != nil {
			t.Fatalf("reload %d: %v, %v", i, ok, err)
		}
		// Let every client finish a request or so on the new Reader, so
		// that the next swap overlaps requests in flight.
		deadline := time.Now().Add(10 * time.Second)
		for seen := lookups.Load(); lookups.Load() < seen+clients && len(errs) == 0; {
			if time.Now().After(deadline) {
				t.Fatal("lookups stalled")
			}
			time.Sleep(time.Millisecond)
		}
	}
	stop.Store(true)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got, want := profileOf(t, ts), "city"; got != want {
		t.Errorf("serving the %s profile after the last reload, want %s", got, want)
	}
	t.Logf("%d reloads, %d lookups", reloads, lookups.Load())

	deadline := time.Now().Add(10 * time.Second)
	for n := 0; n < reloads; {
		runtime.GC()
		select {
		case <-released:
			n++
		case <-time.After(10 * time.Millisecond):
			if time.Now().After(deadline) {
				t.Fatalf("%d of %d retired Readers are still reachable", reloads-n, reloads)
			}
		}
	}
}

// TestReloadFailure replaces the database with a file that does not open:
// the server keeps answering from the old one, does not retry the bad file
// until it changes, and takes the next good one.
func TestReloadFailure(t *testing.T) {
	s, ts, dir := startServer(t, "city.geoc")
	path := install(t, dir, "README.md")
	if ok, err := s.reload(false); ok || err == nil {
		t.Fatalf("reload of a bad file: %v, %v", ok, err)
	}
	if ok, err := s.reload(false); ok || err != nil {
		t.Fatalf("second reload of the same bad file: %v, %v; want no attempt", ok, err)
	}
	var got lookupResult
	if _, err := get(ts.Client(), ts.URL+"/lookup?ip=1.0.0.1", &got); err != nil || got.City != "Testville" {
		t.Fatalf("lookup after a failed reload: %+v, %v", got, err)
	}

	install(t, dir, "country.geoc")
	if ok, err := s.reload(false); !ok || err != nil {
		t.Fatalf("reload of a good file: %v, %v", ok, err)
	}
	if got := profileOf(t, ts); got != "country" {
		t.Errorf("profile %s after reload, want country", got)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.reload(false); ok || err == nil {
		t.Fatalf("reload of a missing file: %v, %v", ok, err)
	}
	if got := profileOf(t, ts); got != "country" {
		t.Errorf("profile %s after a missing file, want country", got)
	}
}

// TestWatch runs the watcher: it picks up a replaced file by polling, and
// reopens an unchanged one on SIGHUP.
func TestWatch(t *testing.T) {
	s, ts, dir := startServer(t, "city.geoc")
	ctx, cancel := context.WithCancel(context.Background())
	hup := make(chan os.Signal)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.watch(ctx, 5*time.Millisecond, hup)
	}()
	defer func() {
		cancel()
		<-done
	}()

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	install(t, dir, "asn.geoc")
	waitFor("the poll to load asn.geoc", func() bool { return profileOf(t, ts) == "asn" })

	before := s.db.Load()
	hup <- os.Interrupt // watch takes any value from hup as SIGHUP
	waitFor("SIGHUP to reopen the file", func() bool { return s.db.Load() != before })
	if got := profileOf(t, ts); got != "asn" {
		t.Errorf("profile %s after SIGHUP, want asn", got)
	}
}
