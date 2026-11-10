# csvdb sample sources

Tiny CSV files in the layouts of DB-IP's Lite CSV downloads, read by the
package examples (`example_test.go`). The data is made up, like that of
`testdata/conformance` at the repository root: user-assigned ISO codes
(`QT`, `QM`, `XS`), invented names, documentation address ranges and AS
numbers. None of it comes from DB-IP.

| file | layout (`csvdb.Format`) |
|---|---|
| `dbip-city-lite-sample.csv` | `DBIPCity`: ip_start, ip_end, continent, country, stateprov, city, latitude, longitude |
| `dbip-asn-lite-sample.csv` | `DBIPASN`: ip_start, ip_end, as_number, as_organization |
