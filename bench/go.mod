module github.com/haxqer/ip2geo/bench

go 1.25.0

require (
	github.com/haxqer/ip2geo v0.0.0
	github.com/oschwald/geoip2-golang/v2 v2.0.0
)

require (
	github.com/oschwald/maxminddb-golang/v2 v2.4.1 // indirect
	golang.org/x/sys v0.46.0 // indirect
)

replace github.com/haxqer/ip2geo => ../
