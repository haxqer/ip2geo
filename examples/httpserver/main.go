// Command httpserver is a small HTTP service that answers IP geolocation
// lookups from a .geoc database and replaces the database while it runs,
// without dropping or failing a request. It imports only the standard
// library and github.com/haxqer/ip2geo.
//
//	httpserver -db city.geoc -addr localhost:8080 -poll 1m
//	curl 'localhost:8080/lookup?ip=1.0.0.1'
//	curl 'localhost:8080/info'
//
// # Reloading
//
// The current *ip2geo.Reader is kept in an atomic.Pointer. A request loads it
// once and uses that Reader to the end. A watcher goroutine checks the file
// every -poll interval: when the path names another file (it was replaced by
// a rename, as the ip2geo command, mmdb.Build and csvdb.Build do) or the
// file's size or modification time changed, it opens the new file and, only
// if that succeeds, swaps the pointer and closes the old Reader. SIGHUP (on
// Unix) makes it reopen the file at once, changed or not. A file that fails to
// open is logged and skipped until it changes again, and the previous
// database keeps serving.
//
// # Retiring the old Reader
//
// Code that swaps a shared resource usually has to know when the requests
// still using the old one are done before freeing it: it counts references,
// guards each generation with a sync.RWMutex, or waits out a grace period.
// None of that is needed here, because ip2geo.Reader.Close frees nothing.
// It returns at once, may run concurrently with lookups and more than once,
// and leaves the Reader working. The memory mapping is released by the
// garbage collector (runtime.AddCleanup) once no Reader of that database is
// reachable, and a request still running on the old Reader keeps it
// reachable. The garbage collector is the reference count: a lookup in
// flight on the old Reader cannot touch an unmapped file, and the old file's
// memory goes back to the system at a garbage collection after the last such
// request ends. Close is called anyway, as for any io.Closer.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/haxqer/ip2geo"
)

func main() {
	dbPath := flag.String("db", "", "path of the .geoc database (required)")
	addr := flag.String("addr", "localhost:8080", "address to listen on")
	poll := flag.Duration("poll", time.Minute, "how often to check the database file for a new version; 0 checks only on SIGHUP")
	flag.Parse()
	if *dbPath == "" || flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}

	s, err := newServer(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("serving %s: %s", *dbPath, describe(s.db.Load()))

	ctx, stop := signal.NotifyContext(context.Background(), stopSignals...)
	defer stop()
	hup := make(chan os.Signal, 1)
	if len(reloadSignals) > 0 { // Notify with no signals would relay them all
		signal.Notify(hup, reloadSignals...)
	}
	go s.watch(ctx, *poll, hup)

	srv := &http.Server{Addr: *addr, Handler: s.handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("listening on %s", *addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// server answers lookups from the database at path.
type server struct {
	path string
	db   atomic.Pointer[ip2geo.Reader]
	logf func(format string, args ...any)

	mu   sync.Mutex  // serializes reloads
	seen os.FileInfo // the file last opened, or last found unusable
}

// newServer opens the database at path.
func newServer(path string) (*server, error) {
	s := &server{path: path, logf: log.Printf}
	if _, err := s.reload(true); err != nil {
		return nil, err
	}
	return s, nil
}

// openOptions are the options the database is opened with. Windows refuses
// to replace a file that has a mapped view, so there the database is read
// into memory instead, and the file can be renamed over while it is served.
var openOptions = func() []ip2geo.Option {
	if runtime.GOOS == "windows" {
		return []ip2geo.Option{ip2geo.NoMmap()}
	}
	return nil
}()

// reload opens the database again if its file changed since the last
// reload, or whatever the file when force is set, and makes it the current
// one. It reports whether it did. On error the current database stays.
func (s *server) reload(force bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fi, err := os.Stat(s.path)
	if err != nil {
		return false, err
	}
	if !force && s.seen != nil && sameVersion(fi, s.seen) {
		return false, nil
	}
	s.seen = fi // a file that fails to open is not retried until it changes
	db, err := ip2geo.Open(s.path, openOptions...)
	if err != nil {
		return false, err
	}
	if old := s.db.Swap(db); old != nil {
		// Safe while requests still use old: see the package comment.
		old.Close()
	}
	return true, nil
}

// sameVersion reports whether a and b, from os.Stat of the same path, are
// the same file with the same contents, as far as its metadata tells.
func sameVersion(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// watch reloads the database every interval (never if it is 0) when its file
// changed, and at once on a value from hup, until ctx is done.
func (s *server) watch(ctx context.Context, interval time.Duration, hup <-chan os.Signal) {
	var tick <-chan time.Time
	if interval > 0 {
		t := time.NewTicker(interval)
		defer t.Stop()
		tick = t.C
	}
	for {
		force := false
		select {
		case <-ctx.Done():
			return
		case <-tick:
		case <-hup:
			force = true
		}
		switch ok, err := s.reload(force); {
		case err != nil:
			s.logf("reload %s: %v; still serving the previous database", s.path, err)
		case ok:
			s.logf("reloaded %s: %s", s.path, describe(s.db.Load()))
		}
	}
}

func describe(db *ip2geo.Reader) string {
	md := db.Metadata()
	return fmt.Sprintf("%s profile, %s, built %s", md.Profile, md.SourceType, md.BuildTime.Format(time.RFC3339))
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /lookup", s.lookup)
	mux.HandleFunc("GET /info", s.info)
	return mux
}

// lookupResult is the JSON answer of /lookup. Fields the database's profile
// does not have are left out.
type lookupResult struct {
	IP          string    `json:"ip"`
	Found       bool      `json:"found"`
	First       string    `json:"first"` // the range of addresses with the same answer
	Last        string    `json:"last"`
	CountryISO  string    `json:"country_iso,omitempty"`
	CountryName string    `json:"country_name,omitempty"`
	Continent   string    `json:"continent,omitempty"`
	RegionISO   string    `json:"region_iso,omitempty"`
	RegionName  string    `json:"region_name,omitempty"`
	City        string    `json:"city,omitempty"`
	Location    *location `json:"location,omitempty"`
	ASN         *asn      `json:"asn,omitempty"`
}

type location struct {
	Latitude       float64 `json:"latitude"`
	Longitude      float64 `json:"longitude"`
	AccuracyRadius uint16  `json:"accuracy_radius,omitempty"`
	TimeZone       string  `json:"time_zone,omitempty"`
}

type asn struct {
	Number       uint32 `json:"number"`
	Organization string `json:"organization"`
}

// lookup answers GET /lookup?ip=ADDR, or for the client's own address when
// ip is missing.
func (s *server) lookup(w http.ResponseWriter, r *http.Request) {
	var ip netip.Addr
	if q := r.URL.Query().Get("ip"); q != "" {
		a, err := netip.ParseAddr(q)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("bad ip %q", q)})
			return
		}
		ip = a.WithZone("")
	} else if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		ip = ap.Addr().WithZone("")
	} else {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no ip given"})
		return
	}

	db := s.db.Load() // one Reader for the whole request, even across a reload
	d, ok := db.LookupDetails(ip)
	rng, _ := db.LookupRange(ip)
	res := lookupResult{
		IP: ip.String(), Found: ok, First: rng.First.String(), Last: rng.Last.String(),
		CountryISO: d.CountryISO, CountryName: d.CountryName, Continent: d.Continent,
		RegionISO: d.RegionISO, RegionName: d.RegionName, City: d.City,
	}
	switch {
	case !ok:
	case db.Profile() == ip2geo.ProfileCity:
		res.Location = &location{d.Latitude, d.Longitude, d.AccuracyRadius, d.TimeZone}
	case db.Profile() == ip2geo.ProfileASN:
		res.ASN = &asn{d.ASN.Number, d.ASN.Organization}
	}
	writeJSON(w, http.StatusOK, res)
}

// info answers GET /info with the current database's metadata.
func (s *server) info(w http.ResponseWriter, _ *http.Request) {
	md := s.db.Load().Metadata()
	writeJSON(w, http.StatusOK, map[string]any{
		"profile":     md.Profile.String(),
		"source_type": md.SourceType,
		"description": md.Description,
		"build_time":  md.BuildTime,
		"ipv4_ranges": md.IPv4Ranges,
		"ipv6_ranges": md.IPv6Ranges,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
