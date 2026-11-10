module github.com/haxqer/ip2geo/mmdb

go 1.25.0

require (
	github.com/haxqer/ip2geo v0.0.0-00010101000000-000000000000
	github.com/oschwald/maxminddb-golang/v2 v2.4.1
)

require golang.org/x/sys v0.46.0 // indirect

replace github.com/haxqer/ip2geo => ../
