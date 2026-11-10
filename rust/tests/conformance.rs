//! The language-neutral conformance suite of the repository,
//! `testdata/conformance` (its README specifies the JSON documents).
//!
//! Every file in the directory must be understood: `README.md`, a
//! `NAME.geoc` database, or its `NAME.json` document with exactly the keys
//! of schema version 1. Anything else fails the test, so a new kind of
//! file or a schema change cannot be skipped silently.

use std::collections::{BTreeMap, BTreeSet};
use std::fs;
use std::net::IpAddr;
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use ip2geo::{Asn, Country, Details, IpRange, OpenOptions, Reader, Record};
use serde_json::{Map, Value};

const SCHEMA_VERSION: u64 = 1;

/// Databases the suite is known to hold; the test also fails if one goes
/// missing, so the suite cannot shrink unnoticed.
const KNOWN: &[&str] = &[
    "asn",
    "city",
    "city-langs",
    "country",
    "geo1-city",
    "region",
];

fn dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../testdata/conformance")
}

// ---- strict JSON access: a document that does not match the schema
// panics at once (it is not understood); a wrong answer is collected. ----

fn obj<'v>(v: &'v Value, path: &str, keys: &[&str], optional: &[&str]) -> &'v Map<String, Value> {
    let m = v
        .as_object()
        .unwrap_or_else(|| panic!("{path}: not an object: {v}"));
    for k in keys {
        assert!(m.contains_key(*k), "{path}: missing key {k:?}");
    }
    for k in m.keys() {
        assert!(
            keys.contains(&k.as_str()) || optional.contains(&k.as_str()),
            "{path}: unknown key {k:?} (schema changed? update this test)"
        );
    }
    m
}

fn get<'v>(m: &'v Map<String, Value>, path: &str, k: &str) -> &'v Value {
    m.get(k)
        .unwrap_or_else(|| panic!("{path}: missing key {k:?}"))
}

fn st<'v>(m: &'v Map<String, Value>, path: &str, k: &str) -> &'v str {
    get(m, path, k)
        .as_str()
        .unwrap_or_else(|| panic!("{path}.{k}: not a string"))
}

fn uint(m: &Map<String, Value>, path: &str, k: &str) -> u64 {
    get(m, path, k)
        .as_u64()
        .unwrap_or_else(|| panic!("{path}.{k}: not an unsigned integer"))
}

fn int(m: &Map<String, Value>, path: &str, k: &str) -> i64 {
    get(m, path, k)
        .as_i64()
        .unwrap_or_else(|| panic!("{path}.{k}: not an integer"))
}

fn boolean(m: &Map<String, Value>, path: &str, k: &str) -> bool {
    get(m, path, k)
        .as_bool()
        .unwrap_or_else(|| panic!("{path}.{k}: not a boolean"))
}

fn float(m: &Map<String, Value>, path: &str, k: &str) -> f64 {
    let v = get(m, path, k);
    assert!(v.is_number(), "{path}.{k}: not a number");
    v.as_f64()
        .unwrap_or_else(|| panic!("{path}.{k}: not representable as f64"))
}

fn addr(m: &Map<String, Value>, path: &str, k: &str) -> IpAddr {
    let s = st(m, path, k);
    s.parse()
        .unwrap_or_else(|e| panic!("{path}.{k}: {s:?} is not an address: {e}"))
}

/// Collects mismatches between the reader and a document.
#[derive(Default)]
struct Check {
    errors: Vec<String>,
    compared: usize,
}

impl Check {
    fn eq<T: PartialEq + std::fmt::Debug>(&mut self, path: &str, got: T, want: T) {
        self.compared += 1;
        if got != want {
            self.errors
                .push(format!("{path}: got {got:?}, want {want:?}"));
        }
    }

    fn fail(&mut self, msg: String) {
        self.errors.push(msg);
    }
}

const RECORD_KEYS: &[&str] = &[
    "country_iso",
    "country_name",
    "continent",
    "country_geoname_id",
    "region_iso",
    "region_name",
    "city",
    "city_geoname_id",
    "latitude",
    "latitude_int",
    "longitude",
    "longitude_int",
    "accuracy_radius",
    "time_zone",
];

const DETAILS_KEYS: &[&str] = &[
    "continent_name",
    "continent_geoname_id",
    "is_in_european_union",
    "region_geoname_id",
    "region2_iso",
    "region2_name",
    "region2_geoname_id",
    "postal_code",
    "metro_code",
    "has_coordinates",
    "has_accuracy_radius",
    "registered_country",
    "represented_country",
    "represented_country_type",
    "is_anycast",
    "is_anonymous_proxy",
    "is_satellite_provider",
    "asn",
];

const COUNTRY_KEYS: &[&str] = &["iso", "name", "geoname_id", "is_in_european_union"];
const ASN_KEYS: &[&str] = &["number", "organization"];

fn u32_of(m: &Map<String, Value>, path: &str, k: &str) -> u32 {
    u32::try_from(uint(m, path, k)).unwrap_or_else(|_| panic!("{path}.{k}: not a u32"))
}

fn u16_of(m: &Map<String, Value>, path: &str, k: &str) -> u16 {
    u16::try_from(uint(m, path, k)).unwrap_or_else(|_| panic!("{path}.{k}: not a u16"))
}

/// Checks a record against a document record. Coordinates are compared bit
/// for bit; the document's own `latitude_int / 10^scale` must give its
/// `latitude` (README, "Coordinates").
fn check_record(c: &mut Check, path: &str, got: &Record<'_>, want: &Value, scale: i32) {
    let m = obj(want, path, RECORD_KEYS, &[]);
    c.eq(
        &format!("{path}.country_iso"),
        got.country_iso,
        st(m, path, "country_iso"),
    );
    c.eq(
        &format!("{path}.country_name"),
        got.country_name,
        st(m, path, "country_name"),
    );
    c.eq(
        &format!("{path}.continent"),
        got.continent,
        st(m, path, "continent"),
    );
    c.eq(
        &format!("{path}.country_geoname_id"),
        got.country_geoname_id,
        u32_of(m, path, "country_geoname_id"),
    );
    c.eq(
        &format!("{path}.region_iso"),
        got.region_iso,
        st(m, path, "region_iso"),
    );
    c.eq(
        &format!("{path}.region_name"),
        got.region_name,
        st(m, path, "region_name"),
    );
    c.eq(&format!("{path}.city"), got.city, st(m, path, "city"));
    c.eq(
        &format!("{path}.city_geoname_id"),
        got.city_geoname_id,
        u32_of(m, path, "city_geoname_id"),
    );
    let div = 10f64.powi(scale);
    for (k, ki, g) in [
        ("latitude", "latitude_int", got.latitude),
        ("longitude", "longitude_int", got.longitude),
    ] {
        let want_f = float(m, path, k);
        let want_i =
            i32::try_from(int(m, path, ki)).unwrap_or_else(|_| panic!("{path}.{ki}: not an i32"));
        // The document is self-consistent (the scale table is exact).
        let from_int = f64::from(want_i) / div;
        assert_eq!(
            from_int.to_bits(),
            want_f.to_bits(),
            "{path}: {k} {want_f} is not {ki} {want_i} / 10^{scale}"
        );
        c.eq(&format!("{path}.{k} (bits)"), g.to_bits(), want_f.to_bits());
    }
    c.eq(
        &format!("{path}.accuracy_radius"),
        got.accuracy_radius,
        u16_of(m, path, "accuracy_radius"),
    );
    c.eq(
        &format!("{path}.time_zone"),
        got.time_zone,
        st(m, path, "time_zone"),
    );
}

fn check_country(c: &mut Check, path: &str, got: &Country<'_>, want: &Value) {
    let m = obj(want, path, COUNTRY_KEYS, &[]);
    c.eq(&format!("{path}.iso"), got.iso, st(m, path, "iso"));
    c.eq(&format!("{path}.name"), got.name, st(m, path, "name"));
    c.eq(
        &format!("{path}.geoname_id"),
        got.geoname_id,
        u32_of(m, path, "geoname_id"),
    );
    c.eq(
        &format!("{path}.is_in_european_union"),
        got.is_in_european_union,
        boolean(m, path, "is_in_european_union"),
    );
}

fn check_asn_value(c: &mut Check, path: &str, got: &Asn<'_>, want: &Value) {
    let m = obj(want, path, ASN_KEYS, &[]);
    c.eq(
        &format!("{path}.number"),
        got.number,
        u32_of(m, path, "number"),
    );
    c.eq(
        &format!("{path}.organization"),
        got.organization,
        st(m, path, "organization"),
    );
}

/// Checks the non-record part of a Details against a document's details.
fn check_extras(c: &mut Check, path: &str, d: &Details<'_>, want: &Value) {
    let m = obj(want, path, DETAILS_KEYS, &[]);
    let p = |k: &str| format!("{path}.{k}");
    c.eq(
        &p("continent_name"),
        d.continent_name,
        st(m, path, "continent_name"),
    );
    c.eq(
        &p("continent_geoname_id"),
        d.continent_geoname_id,
        u32_of(m, path, "continent_geoname_id"),
    );
    c.eq(
        &p("is_in_european_union"),
        d.is_in_european_union,
        boolean(m, path, "is_in_european_union"),
    );
    c.eq(
        &p("region_geoname_id"),
        d.region_geoname_id,
        u32_of(m, path, "region_geoname_id"),
    );
    c.eq(&p("region2_iso"), d.region2_iso, st(m, path, "region2_iso"));
    c.eq(
        &p("region2_name"),
        d.region2_name,
        st(m, path, "region2_name"),
    );
    c.eq(
        &p("region2_geoname_id"),
        d.region2_geoname_id,
        u32_of(m, path, "region2_geoname_id"),
    );
    c.eq(&p("postal_code"), d.postal_code, st(m, path, "postal_code"));
    c.eq(
        &p("metro_code"),
        d.metro_code,
        u16_of(m, path, "metro_code"),
    );
    c.eq(
        &p("has_coordinates"),
        d.has_coordinates,
        boolean(m, path, "has_coordinates"),
    );
    c.eq(
        &p("has_accuracy_radius"),
        d.has_accuracy_radius,
        boolean(m, path, "has_accuracy_radius"),
    );
    check_country(
        c,
        &p("registered_country"),
        &d.registered_country,
        get(m, path, "registered_country"),
    );
    check_country(
        c,
        &p("represented_country"),
        &d.represented_country,
        get(m, path, "represented_country"),
    );
    c.eq(
        &p("represented_country_type"),
        d.represented_country_type,
        st(m, path, "represented_country_type"),
    );
    c.eq(
        &p("is_anycast"),
        d.is_anycast,
        boolean(m, path, "is_anycast"),
    );
    c.eq(
        &p("is_anonymous_proxy"),
        d.is_anonymous_proxy,
        boolean(m, path, "is_anonymous_proxy"),
    );
    c.eq(
        &p("is_satellite_provider"),
        d.is_satellite_provider,
        boolean(m, path, "is_satellite_provider"),
    );
    check_asn_value(c, &p("asn"), &d.asn, get(m, path, "asn"));
}

/// Checks Lookup and LookupDetails of `ip` on `r` against a query's (or a
/// localized entry's) `lookup` and `details`.
fn check_lookups(
    c: &mut Check,
    path: &str,
    r: &Reader,
    ip: IpAddr,
    lookup: &Value,
    details: &Value,
    scale: i32,
) {
    let lp = format!("{path}.lookup");
    let lm = obj(lookup, &lp, &["found", "record"], &[]);
    let found = boolean(lm, &lp, "found");
    let rec_v = get(lm, &lp, "record");
    assert_eq!(found, !rec_v.is_null(), "{lp}: found and record disagree");

    let dp = format!("{path}.details");
    let dm = obj(details, &dp, &["found", "details"], &[]);
    let dfound = boolean(dm, &dp, "found");
    let det_v = get(dm, &dp, "details");
    assert_eq!(dfound, !det_v.is_null(), "{dp}: found and details disagree");
    assert_eq!(
        found, dfound,
        "{path}: lookup and details found flags disagree"
    );

    match r.lookup(ip) {
        Some(rec) if found => check_record(c, &format!("{lp}.record"), &rec, rec_v, scale),
        None if !found => c.eq(&format!("{lp}.found"), false, false),
        got => c.fail(format!("{lp}: got {got:?}, want found={found}")),
    }
    match r.lookup_details(ip) {
        Some(d) if dfound => {
            // The record part of Details equals the lookup record.
            check_record(c, &format!("{dp}.record"), &d.record, rec_v, scale);
            check_extras(c, &format!("{dp}.details"), &d, det_v);
        }
        None if !dfound => c.eq(&format!("{dp}.found"), false, false),
        got => c.fail(format!("{dp}: got {got:?}, want found={dfound}")),
    }
}

fn unix(t: Option<SystemTime>) -> u64 {
    t.map_or(0, |t| {
        t.duration_since(UNIX_EPOCH)
            .expect("time before 1970")
            .as_secs()
    })
}

const METADATA_KEYS: &[&str] = &[
    "format_version",
    "profile",
    "profile_name",
    "lat_scale",
    "build_time",
    "source_time",
    "source_type",
    "language",
    "fields",
    "description",
    "builder",
    "networks",
    "distinct_records",
    "records",
    "strings",
    "ipv4_ranges",
    "ipv6_ranges",
];

fn check_metadata(c: &mut Check, r: &Reader, want: &Value) {
    let path = "metadata";
    let w = obj(want, path, METADATA_KEYS, &[]);
    let m = r.metadata();
    let p = |k: &str| format!("{path}.{k}");
    c.eq(
        &p("format_version"),
        u64::from(m.format_version),
        uint(w, path, "format_version"),
    );
    c.eq(&p("profile"), m.profile as u64, uint(w, path, "profile"));
    c.eq(
        &p("profile_name"),
        m.profile.name(),
        st(w, path, "profile_name"),
    );
    c.eq(
        &p("lat_scale"),
        u64::from(m.lat_scale),
        uint(w, path, "lat_scale"),
    );
    c.eq(
        &p("build_time"),
        unix(m.build_time),
        uint(w, path, "build_time"),
    );
    c.eq(
        &p("source_time"),
        unix(m.source_time),
        uint(w, path, "source_time"),
    );
    c.eq(
        &p("source_type"),
        m.source_type.as_str(),
        st(w, path, "source_type"),
    );
    c.eq(&p("language"), m.language.as_str(), st(w, path, "language"));
    c.eq(
        &p("fields"),
        u64::from(m.fields.bits()),
        uint(w, path, "fields"),
    );
    c.eq(
        &p("description"),
        m.description.as_str(),
        st(w, path, "description"),
    );
    c.eq(&p("builder"), m.builder.as_str(), st(w, path, "builder"));
    c.eq(&p("networks"), m.networks, uint(w, path, "networks"));
    c.eq(
        &p("distinct_records"),
        m.distinct_records,
        uint(w, path, "distinct_records"),
    );
    c.eq(&p("records"), m.records as u64, uint(w, path, "records"));
    c.eq(&p("strings"), m.strings as u64, uint(w, path, "strings"));
    c.eq(
        &p("ipv4_ranges"),
        m.ipv4_ranges as u64,
        uint(w, path, "ipv4_ranges"),
    );
    c.eq(
        &p("ipv6_ranges"),
        m.ipv6_ranges as u64,
        uint(w, path, "ipv6_ranges"),
    );
}

const TOP_KEYS: &[&str] = &[
    "schema_version",
    "file",
    "about",
    "metadata",
    "languages",
    "fields",
    "queries",
    "ranges",
];

/// Checks one reader against one document.
fn check_doc(c: &mut Check, r: &Reader, doc: &Value) {
    let top = obj(doc, "$", TOP_KEYS, &[]);
    check_metadata(c, r, get(top, "$", "metadata"));
    let scale = i32::try_from(uint(
        get(top, "$", "metadata").as_object().expect("metadata"),
        "metadata",
        "lat_scale",
    ))
    .expect("lat_scale");

    // languages: null for GEO1 (Reader::languages is then empty).
    let langs: Vec<String> = match get(top, "$", "languages") {
        Value::Null => Vec::new(),
        Value::Array(a) => {
            assert!(
                !a.is_empty(),
                "languages: an empty array, want null or entries"
            );
            a.iter()
                .map(|v| v.as_str().expect("languages: not a string").to_string())
                .collect()
        }
        v => panic!("languages: not null or an array: {v}"),
    };
    c.eq("languages", r.languages().to_vec(), langs.clone());
    c.eq(
        "language()",
        r.language(),
        langs.first().map_or("", String::as_str),
    );
    c.eq(
        "fields",
        u64::from(r.fields().bits()),
        uint(top, "$", "fields"),
    );

    let views: BTreeMap<&str, Reader> = langs
        .iter()
        .skip(1)
        .map(|l| (l.as_str(), r.with_language(l).expect("with_language")))
        .collect();

    let queries = get(top, "$", "queries")
        .as_array()
        .expect("queries: not an array");
    assert!(!queries.is_empty(), "queries: empty");
    for (qi, q) in queries.iter().enumerate() {
        let path = format!("queries[{qi}]");
        let qm = obj(
            q,
            &path,
            &["ip", "lookup", "details", "asn", "range"],
            &["note", "localized"],
        );
        if let Some(n) = qm.get("note") {
            assert!(n.is_string(), "{path}.note: not a string");
        }
        let ip = addr(qm, &path, "ip");
        let path = format!("{path} ({ip})");
        check_lookups(
            c,
            &path,
            r,
            ip,
            get(qm, &path, "lookup"),
            get(qm, &path, "details"),
            scale,
        );

        // localized: present exactly when the file has two or more languages.
        match qm.get("localized") {
            None => assert!(
                langs.len() < 2,
                "{path}: no localized answers for {langs:?}"
            ),
            Some(l) => {
                assert!(langs.len() >= 2, "{path}: localized answers for {langs:?}");
                let keys: Vec<&str> = views.keys().copied().collect();
                let lm = obj(l, &format!("{path}.localized"), &keys, &[]);
                for (lang, v) in lm {
                    let lp = format!("{path}.localized[{lang}]");
                    let e = obj(v, &lp, &["lookup", "details"], &[]);
                    check_lookups(
                        c,
                        &lp,
                        &views[lang.as_str()],
                        ip,
                        get(e, &lp, "lookup"),
                        get(e, &lp, "details"),
                        scale,
                    );
                }
            }
        }

        let ap = format!("{path}.asn");
        let am = obj(get(qm, &path, "asn"), &ap, &["found", "asn"], &[]);
        let afound = boolean(am, &ap, "found");
        let av = get(am, &ap, "asn");
        assert_eq!(afound, !av.is_null(), "{ap}: found and asn disagree");
        match r.asn(ip) {
            Some(a) if afound => check_asn_value(c, &format!("{ap}.asn"), &a, av),
            None if !afound => c.eq(&format!("{ap}.found"), false, false),
            got => c.fail(format!("{ap}: got {got:?}, want found={afound}")),
        }

        let rp = format!("{path}.range");
        let rm = obj(
            get(qm, &path, "range"),
            &rp,
            &["found", "first", "last"],
            &[],
        );
        let want = (
            IpRange {
                first: addr(rm, &rp, "first"),
                last: addr(rm, &rp, "last"),
            },
            boolean(rm, &rp, "found"),
        );
        c.eq(&rp, r.lookup_range(ip), want);
    }

    let want_ranges = get(top, "$", "ranges")
        .as_array()
        .expect("ranges: not an array");
    let mut n = 0;
    for (i, (rg, d)) in r.ranges().enumerate() {
        let path = format!("ranges[{i}]");
        let Some(w) = want_ranges.get(i) else {
            c.fail(format!(
                "{path}: {}-{} past the {} expected",
                rg.first,
                rg.last,
                want_ranges.len()
            ));
            break;
        };
        let m = obj(w, &path, &["first", "last", "record", "details"], &[]);
        let want = IpRange {
            first: addr(m, &path, "first"),
            last: addr(m, &path, "last"),
        };
        c.eq(&format!("{path} range"), rg, want);
        check_record(
            c,
            &format!("{path}.record"),
            &d.record,
            get(m, &path, "record"),
            scale,
        );
        check_extras(c, &format!("{path}.details"), &d, get(m, &path, "details"));
        n += 1;
    }
    c.eq("ranges count", n, want_ranges.len());
}

/// The suite's files, classified; panics on any file it does not understand.
fn suite() -> Vec<(String, Value)> {
    let mut dbs = BTreeSet::new();
    let mut docs = BTreeMap::new();
    for e in fs::read_dir(dir()).expect("read testdata/conformance") {
        let e = e.expect("dir entry");
        let name = e.file_name().into_string().expect("non-UTF-8 file name");
        assert!(
            e.file_type().expect("file type").is_file(),
            "{name}: not a regular file"
        );
        if name == "README.md" {
            continue;
        }
        if let Some(stem) = name.strip_suffix(".geoc") {
            dbs.insert(stem.to_string());
        } else if let Some(stem) = name.strip_suffix(".json") {
            let text = fs::read_to_string(e.path()).expect("read JSON");
            let v: Value = serde_json::from_str(&text).unwrap_or_else(|e| panic!("{name}: {e}"));
            docs.insert(stem.to_string(), v);
        } else {
            panic!("testdata/conformance/{name}: a file this test does not understand");
        }
    }
    for k in KNOWN {
        assert!(dbs.contains(*k), "{k}.geoc is missing from the suite");
        assert!(docs.contains_key(*k), "{k}.json is missing from the suite");
    }
    assert_eq!(
        dbs.iter().collect::<Vec<_>>(),
        docs.keys().collect::<Vec<_>>(),
        "every NAME.geoc needs a NAME.json and vice versa"
    );
    docs.into_iter()
        .map(|(stem, v)| {
            let top = obj(&v, &format!("{stem}.json"), TOP_KEYS, &[]);
            let sv = uint(top, "$", "schema_version");
            assert_eq!(
                sv, SCHEMA_VERSION,
                "{stem}.json: schema_version {sv}, this test knows {SCHEMA_VERSION}"
            );
            let file = st(top, "$", "file");
            assert_eq!(file, format!("{stem}.geoc"), "{stem}.json: file key");
            assert!(
                top["about"].is_string(),
                "{stem}.json: about is not a string"
            );
            (file.to_string(), v)
        })
        .collect()
}

fn run(open: impl Fn(&Path) -> Reader, how: &str) {
    let mut total = 0;
    let mut failures = Vec::new();
    for (file, doc) in suite() {
        let r = open(&dir().join(&file));
        let mut c = Check::default();
        check_doc(&mut c, &r, &doc);
        total += c.compared;
        assert!(c.compared > 0, "{file}: nothing compared");
        failures.extend(c.errors.into_iter().map(|e| format!("{file} ({how}): {e}")));
    }
    if !failures.is_empty() {
        let shown: Vec<_> = failures.iter().take(50).collect();
        panic!(
            "{} of {total} comparisons failed:\n{}",
            failures.len(),
            shown
                .iter()
                .map(|s| s.as_str())
                .collect::<Vec<_>>()
                .join("\n")
        );
    }
}

#[test]
fn conformance_open() {
    run(|p| Reader::open(p).expect("open"), "open, mmap");
}

#[test]
fn conformance_no_mmap() {
    run(
        |p| OpenOptions::new().mmap(false).open(p).expect("open"),
        "open, no mmap",
    );
}

#[test]
fn conformance_from_bytes() {
    run(
        |p| Reader::from_bytes(fs::read(p).expect("read")).expect("from_bytes"),
        "from_bytes",
    );
}

#[test]
fn conformance_static_bytes() {
    run(
        |p| {
            let b: &'static [u8] = Box::leak(fs::read(p).expect("read").into_boxed_slice());
            Reader::from_bytes(b).expect("from_bytes")
        },
        "from_bytes(&'static)",
    );
}

#[test]
fn language_option_matches_with_language() {
    for (file, doc) in suite() {
        let langs = doc["languages"].as_array().cloned().unwrap_or_default();
        let path = dir().join(&file);
        let r = Reader::open(&path).expect("open");
        for l in langs.iter().filter_map(Value::as_str) {
            let a = OpenOptions::new()
                .language(l)
                .open(&path)
                .expect("open with language");
            let b = r.with_language(l).expect("with_language");
            assert_eq!(a.language(), l);
            assert_eq!(
                a.ranges().collect::<Vec<_>>(),
                b.ranges().collect::<Vec<_>>(),
                "{file} {l}"
            );
        }
        let err = OpenOptions::new()
            .language("xx-unknown")
            .open(&path)
            .unwrap_err();
        assert!(matches!(err, ip2geo::Error::Language(_)), "{file}: {err}");
        // "" is the primary language, also for GEO1, which records none.
        assert!(r.with_language("").is_ok());
    }
}
