# httpserver

A small HTTP lookup service built on `github.com/haxqer/ip2geo` and the
standard library only. It replaces its database while it runs, without
failing or dropping a request.

```sh
go run ./examples/httpserver -db testdata/conformance/city.geoc
curl 'localhost:8080/lookup?ip=1.0.0.1'
```

```json
{"ip":"1.0.0.1","found":true,"first":"1.0.0.0","last":"1.0.0.255","country_iso":"QT","country_name":"Testland","continent":"XC","region_iso":"T1","region_name":"North Testshire","city":"Testville","location":{"latitude":12.3456,"longitude":-65.4321,"accuracy_radius":20,"time_zone":"Testland/Capitalia"}}
```

| endpoint | answer |
|---|---|
| `GET /lookup?ip=ADDR` | the record for `ADDR` (IPv4 or IPv6), and the range of addresses with the same answer; without `ip`, for the client's address. An ASN database answers with `asn`. |
| `GET /info` | the current database's profile, source, build time and range counts |

Flags: `-db` (the `.geoc` file, required), `-addr` (default `localhost:8080`),
`-poll` (how often to check the file, default `1m`; `0` disables polling).

## Updating the database

Write the new file next to the old one and rename it over it, which is what
`ip2geo build`, `mmdb.Build` and `csvdb.Build` do. Never rewrite the served
file in place: it is memory-mapped. The server notices the new file at the
next poll (another file at the path, or a new size or modification time), or
at once on `SIGHUP`:

```sh
ip2geo build -src GeoLite2-City.mmdb -out city.geoc -profile city   # atomic replace
kill -HUP "$(pgrep httpserver)"                                     # optional: reload now
```

A file that does not open (truncated, wrong format) is logged and skipped,
and the previous database keeps serving until a good file appears.

## How the swap stays safe

Each request loads the current `*ip2geo.Reader` from an `atomic.Pointer` once
and uses it to the end. A reload opens the new file, swaps the pointer and
closes the old Reader. `Reader.Close` frees nothing and never waits: the
mapping is released by the garbage collector once no Reader of that file is
reachable, and a request still running on the old Reader keeps it reachable.
So no reference counting or grace period is needed, and an in-flight lookup
can never touch an unmapped file. For a short while after a reload both
databases are in memory.

On Windows a file with a mapped view cannot be renamed over, so there the
server reads the database into memory (`ip2geo.NoMmap`) and reloads on polls
only.

`main_test.go` swaps the file back and forth while clients send requests,
checks that none fails, and that every retired Reader becomes unreachable:

```sh
go test -race ./examples/httpserver
```
