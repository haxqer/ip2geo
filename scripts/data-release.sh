#!/usr/bin/env bash
# Build and publish the monthly DB-IP Lite sample databases (data-YYYY-MM).
#
#   scripts/data-release.sh build   YYYY-MM OUTDIR   download DB-IP City Lite and build the assets
#   scripts/data-release.sh verify  YYYY-MM OUTDIR   run the builder-vs-source integration test
#   scripts/data-release.sh exists  YYYY-MM          exit 0 if release data-YYYY-MM exists, 1 if not
#   scripts/data-release.sh publish YYYY-MM OUTDIR   create GitHub Release data-YYYY-MM from OUTDIR
#
# build and verify need only Go, curl and gzip: no token, no network access
# beyond download.db-ip.com. exists and publish use the GitHub CLI (gh) and
# need GH_TOKEN (data.yml passes the workflow's GITHUB_TOKEN); GH_REPO selects
# the repository, otherwise gh uses the current checkout's remote.
#
# build leaves exactly five release assets in OUTDIR:
#   dbip-country-lite.geoc.gz  dbip-region-lite.geoc.gz  dbip-city-lite.geoc.gz
#   ATTRIBUTION.txt            checksums.txt (sha256sum -c format)
# plus release-notes.md (the release body, not an asset). The downloaded
# source, the decompressed .mmdb and the CLI binary stay in OUTDIR/.work
# (override with DATA_WORKDIR); a rerun reuses a valid download.
#
# Exit status: 0 on success, 1 only from exists when the release is absent,
# 2 on any error (bad arguments, failed download or build, API failure).
#
# The data is DB-IP IP-to-City Lite, (c) DB-IP.com, CC BY 4.0. Every published
# set must ship ATTRIBUTION.txt.
set -euo pipefail

readonly ASSETS=(dbip-country-lite.geoc.gz dbip-region-lite.geoc.gz dbip-city-lite.geoc.gz ATTRIBUTION.txt checksums.txt)
readonly PROFILES=(country region city)

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

log() { printf 'data-release: %s\n' "$*" >&2; }
die() {
	printf 'data-release: error: %s\n' "$*" >&2
	exit 2
}

usage() {
	sed -n '2,7p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2
	exit 2
}

check_month() {
	[[ $1 =~ ^[0-9]{4}-(0[1-9]|1[0-2])$ ]] || die "month must be YYYY-MM, got '$1'"
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$@"
	else
		shasum -a 256 "$@"
	fi
}

mib() { awk -v b="$1" 'BEGIN { printf "%.2f MiB", b / 1048576 }'; }

filesize() { wc -c <"$1" | tr -d ' '; }

# work_dir OUTDIR -> absolute path of the scratch directory.
work_dir() { printf '%s\n' "${DATA_WORKDIR:-$1/.work}"; }

abs_dir() {
	mkdir -p "$1"
	(cd "$1" && pwd)
}

cmd_build() {
	local month=$1 out work
	out="$(abs_dir "$2")"
	work="$(abs_dir "$(work_dir "$out")")"

	local name="dbip-city-lite-${month}.mmdb"
	local url="https://download.db-ip.com/free/${name}.gz"
	local gz="$work/${name}.gz" mmdb="$work/${name}"

	# 1. Download (or reuse a complete earlier download).
	if [[ -s $gz ]] && gzip -t "$gz" 2>/dev/null; then
		log "reusing $gz"
	else
		local status
		status="$(curl -sSIL -o /dev/null -w '%{http_code}' "$url")" ||
			die "cannot reach $url"
		case $status in
		200) ;;
		404) die "DB-IP has not published ${name}.gz (HTTP 404 from $url). DB-IP publishes early in the month; retry later or pass an earlier month." ;;
		*) die "unexpected HTTP $status from $url" ;;
		esac
		log "downloading $url"
		curl -fsSL --retry 3 --retry-delay 5 -o "$gz.part" "$url"
		gzip -t "$gz.part" || die "$url is not a valid gzip file"
		mv "$gz.part" "$gz"
	fi
	log "source: $gz ($(mib "$(filesize "$gz")"))"

	# 2. Decompress.
	gunzip -c "$gz" >"$mmdb.part"
	mv "$mmdb.part" "$mmdb"
	log "source: $mmdb ($(mib "$(filesize "$mmdb")"))"

	# 3. Build the CLI from this checkout once and run it per profile (the
	#    same as `go run ./cmd/ip2geo build`, without three compiles).
	local bin="$work/ip2geo"
	(cd "$repo_root" && CGO_ENABLED=0 go build -trimpath -o "$bin" ./cmd/ip2geo)

	local stats="$work/build-stats.txt" profile geoc line ip
	: >"$stats"
	for profile in "${PROFILES[@]}"; do
		geoc="$work/dbip-${profile}-lite.geoc"
		# Run inside $work with relative names so the logged stats (also
		# quoted in the release notes) carry no local paths.
		(cd "$work" && "$bin" build -src "$name" -out "${geoc##*/}" -profile "$profile") | tee -a "$stats"

		# Smoke test before anything can be published: an IPv4 and an IPv6
		# address that every DB-IP edition covers must resolve.
		for ip in 8.8.8.8 2001:4860:4860::8888; do
			line="$("$bin" lookup -db "$geoc" "$ip")"
			log "$profile: $line"
			[[ $line == "$ip: "* && $line != *"no data"* ]] ||
				die "$profile: smoke lookup of $ip failed: $line"
		done

		# -n: no file name or timestamp in the gzip header, so the same
		# .geoc always compresses to the same bytes.
		gzip -9 -n -c "$geoc" >"$out/dbip-${profile}-lite.geoc.gz"
		rm -f "$geoc"
	done

	# 4. Attribution, checksums, release notes.
	local commit
	commit="$(git -C "$repo_root" rev-parse HEAD 2>/dev/null || echo unknown)"
	write_attribution "$month" "$commit" >"$out/ATTRIBUTION.txt"
	(cd "$out" && sha256 "${ASSETS[@]:0:4}" >checksums.txt)
	write_notes "$month" "$commit" "$out" "$stats" >"$out/release-notes.md"

	log "assets in $out:"
	(cd "$out" && ls -l "${ASSETS[@]}" >&2)
}

write_attribution() {
	local month=$1 commit=$2
	cat <<TXT
These databases are derived from the DB-IP.com IP-to-City Lite database
(dataset ${month}, file dbip-city-lite-${month}.mmdb.gz).

Source:    https://db-ip.com/db/download/ip-to-city-lite
Copyright: (c) DB-IP.com
License:   Creative Commons Attribution 4.0 International (CC BY 4.0)
           https://creativecommons.org/licenses/by/4.0/

IP Geolocation by DB-IP — https://db-ip.com

The data was converted from DB-IP's MaxMind-compatible .mmdb into the
compact ip2geo .geoc format (country, region and city profiles) with no
modification to the underlying geolocation data. The CC BY 4.0 license and
this attribution apply to these derived files.

Converted with github.com/haxqer/ip2geo at commit ${commit}.
TXT
}

write_notes() {
	local month=$1 commit=$2 out=$3 stats=$4
	local server="${GITHUB_SERVER_URL:-https://github.com}"
	local repo="${GITHUB_REPOSITORY:-haxqer/ip2geo}"
	local base="${server}/${repo}/releases/download/data-${month}"
	cat <<MD
Ready-to-use sample databases so you can try **ip2geo** without registering for or purchasing any data. Converted from the **DB-IP IP-to-City Lite** dataset (${month}) into the compact \`.geoc\` format, with no changes to the underlying geolocation data.

## Files

| Asset | Size | Detail |
|---|---|---|
| \`dbip-country-lite.geoc.gz\` | $(mib "$(filesize "$out/dbip-country-lite.geoc.gz")") | Country ISO code + name |
| \`dbip-region-lite.geoc.gz\` | $(mib "$(filesize "$out/dbip-region-lite.geoc.gz")") | + region / subdivision |
| \`dbip-city-lite.geoc.gz\` | $(mib "$(filesize "$out/dbip-city-lite.geoc.gz")") | + city, latitude / longitude |
| \`ATTRIBUTION.txt\` | | CC BY 4.0 attribution |
| \`checksums.txt\` | | SHA-256 of the files above (\`sha256sum -c checksums.txt\`) |

## Usage

\`\`\`sh
# download + decompress (city shown; swap in country/region as needed)
curl -LO ${base}/dbip-city-lite.geoc.gz
gunzip dbip-city-lite.geoc.gz

# look up an address with the CLI
go run github.com/haxqer/ip2geo/cmd/ip2geo@latest lookup -db dbip-city-lite.geoc 8.8.8.8
\`\`\`

\`\`\`go
// zero third-party dependencies
db, _ := ip2geo.Open("dbip-city-lite.geoc")
rec, ok := db.Lookup(netip.MustParseAddr("8.8.8.8"))
\`\`\`

## Attribution & license

IP Geolocation by **DB-IP** — https://db-ip.com
Licensed under **Creative Commons Attribution 4.0 (CC BY 4.0)**. See \`ATTRIBUTION.txt\`.

These files are provided for testing and evaluation. For production use, download a current DB-IP database or use your own MaxMind GeoIP2/GeoLite2 file with \`mmdb.Open\`.

<details><summary>Build details</summary>

Built by \`scripts/data-release.sh\` from commit ${commit}.

\`\`\`
$(cat "$stats")
\`\`\`
</details>
MD
}

cmd_verify() {
	local month=$1 out work mmdb
	out="$(abs_dir "$2")"
	work="$(work_dir "$out")"
	mmdb="$work/dbip-city-lite-${month}.mmdb"
	[[ -s $mmdb ]] || die "$mmdb not found; run '$0 build $month $2' first"
	# No -race: under the race detector the City build alone exceeds go
	# test's default timeout. See the integration job in ci.yml.
	(cd "$repo_root" && IP2GEO_TEST_MMDB="$mmdb" \
		go test -count=1 -timeout 30m -run 'TestBuildMatchesSource|TestRegisteredCountryOnly' -v ./mmdb/)
}

# gh_repo -> OWNER/NAME: $GH_REPO if set, else this checkout's GitHub remote.
gh_repo() {
	command -v gh >/dev/null 2>&1 || die "the GitHub CLI (gh) is required"
	if [[ -n ${GH_REPO:-} ]]; then
		printf '%s\n' "$GH_REPO"
	else
		(cd "$repo_root" && gh repo view --json nameWithOwner --jq .nameWithOwner)
	fi
}

# release_exists REPO TAG -> 0 if a published release has TAG, 1 if not, 2 on
# error. The tags endpoint ignores drafts, so a draft left by an interrupted
# upload does not block a rerun.
release_exists() {
	local err
	if err="$(gh api "repos/$1/releases/tags/$2" --silent 2>&1)"; then
		return 0
	fi
	if [[ $err == *"HTTP 404"* ]]; then
		return 1
	fi
	printf 'data-release: error: cannot query release %s in %s: %s\n' "$2" "$1" "$err" >&2
	return 2
}

cmd_exists() {
	local tag="data-$1" rc=0 repo
	repo="$(gh_repo)"
	release_exists "$repo" "$tag" || rc=$?
	case $rc in
	0) log "release $tag exists in $repo" ;;
	1) log "release $tag does not exist in $repo" ;;
	esac
	return "$rc"
}

cmd_publish() {
	local month=$1 out tag="data-$1" rc=0 repo
	out="$(abs_dir "$2")"
	repo="$(gh_repo)"

	# Idempotent: a rerun after a successful publish is a no-op.
	release_exists "$repo" "$tag" || rc=$?
	case $rc in
	0)
		log "release $tag already exists; nothing to do"
		return 0
		;;
	1) ;;
	*) exit "$rc" ;;
	esac

	local f
	for f in "${ASSETS[@]}" release-notes.md; do
		[[ -s "$out/$f" ]] || die "$out/$f is missing; run '$0 build $month $2' first"
	done
	(cd "$out" && sha256 -c checksums.txt >&2) || die "checksums.txt does not match the assets in $out"

	# Tag the exact commit that built the assets. --latest=false keeps the
	# "Latest release" badge (and /releases/latest) on the v* code releases.
	# gh uploads into a draft and publishes it only after every asset is up.
	local commit files=()
	commit="${DATA_TARGET_COMMIT:-$(git -C "$repo_root" rev-parse HEAD)}"
	for f in "${ASSETS[@]}"; do files+=("$out/$f"); done
	gh release create "$tag" \
		--repo "$repo" \
		--title "Test databases — DB-IP Lite (${month})" \
		--notes-file "$out/release-notes.md" \
		--target "$commit" \
		--latest=false \
		"${files[@]}"
	log "published release $tag in $repo"
}

main() {
	[[ $# -ge 2 ]] || usage
	local cmd=$1 month=$2
	check_month "$month"
	case $cmd in
	build | verify | publish)
		[[ $# -eq 3 ]] || usage
		"cmd_$cmd" "$month" "$3"
		;;
	exists)
		[[ $# -eq 2 ]] || usage
		cmd_exists "$month"
		;;
	*) usage ;;
	esac
}

main "$@"
