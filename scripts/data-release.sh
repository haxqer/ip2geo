#!/usr/bin/env bash
# Build and publish the monthly DB-IP Lite sample databases (data-YYYY-MM).
#
#   scripts/data-release.sh build   YYYY-MM OUTDIR   download DB-IP City, Country and ASN Lite and build the assets
#   scripts/data-release.sh verify  YYYY-MM OUTDIR   run the builder-vs-source integration test
#   scripts/data-release.sh compat  YYYY-MM OUTDIR   check that the latest released CLI opens the .geoc assets
#   scripts/data-release.sh exists  YYYY-MM          exit 0 if release data-YYYY-MM exists, 1 if not
#   scripts/data-release.sh publish YYYY-MM OUTDIR   create GitHub Release data-YYYY-MM from OUTDIR
#   scripts/data-release.sh latest  [--dry-run] [YYYY-MM]  point release data-latest at data-YYYY-MM (default: the newest)
#   scripts/data-release.sh prune   [--dry-run]      delete monthly releases and tags beyond the newest DATA_KEEP
#
# build and verify need only Go, curl and gzip: no token, no network access
# beyond download.db-ip.com. compat also fetches the latest released module
# through the Go module proxy (RELEASED_VERSION overrides @latest). exists and publish use the GitHub CLI (gh) and
# need GH_TOKEN (data.yml passes the workflow's GITHUB_TOKEN); GH_REPO selects
# the repository, otherwise gh uses the current checkout's remote.
#
# build leaves exactly eight release assets in OUTDIR:
#   dbip-country-lite.geoc.gz  dbip-region-lite.geoc.gz  dbip-city-lite.geoc.gz
#   dbip-city-lite.mmdb.gz     dbip-country-lite.mmdb.gz dbip-asn-lite.mmdb.gz
#   ATTRIBUTION.txt            checksums.txt (sha256sum -c format)
# plus release-notes.md (the release body, not an asset). The .mmdb.gz files
# are the month's DB-IP downloads, byte for byte, renamed without the month:
# DB-IP serves only the current month, so the release is what keeps a month's
# sources available, for example to scripts/size-table.sh fetch. The
# downloads, the decompressed City .mmdb and the CLI binary stay in
# OUTDIR/.work (override with DATA_WORKDIR); a rerun reuses valid downloads.
#
# latest and prune use gh the same way. latest keeps one release,
# data-latest, whose assets are byte-for-byte copies of the newest
# data-YYYY-MM release (or of the month given) and whose tag points at that
# release's commit, so .../releases/download/data-latest/<asset> always
# serves the current month. It is never marked as GitHub's "Latest release".
# prune keeps the newest DATA_KEEP (default: DEFAULT_KEEP below) published
# data-YYYY-MM releases, plus the month scripts/size-table.month pins, and
# deletes older ones together with their tags. prune only ever deletes tags
# matching MONTHLY_TAG_RE, latest only moves data-latest; v* tags are never
# touched. Both are idempotent, stop at the first API error, and with
# --dry-run (or DATA_DRY_RUN=1) make read-only calls only and print what
# they would change.
#
# Exit status: 0 on success, 1 only from exists when the release is absent,
# 2 on any error (bad arguments, failed download or build, API failure).
#
# The data is DB-IP IP-to-City Lite, IP-to-Country Lite and IP-to-ASN Lite,
# (c) DB-IP.com, CC BY 4.0, which allows redistribution with attribution.
# Every published set must ship ATTRIBUTION.txt.
set -euo pipefail

# SOURCES are the DB-IP Lite editions downloaded and attached; city is the
# one the .geoc files are built from.
readonly SOURCES=(city country asn)
# SUMMED are the assets listed in checksums.txt, ASSETS every asset.
readonly SUMMED=(dbip-country-lite.geoc.gz dbip-region-lite.geoc.gz dbip-city-lite.geoc.gz
	dbip-city-lite.mmdb.gz dbip-country-lite.mmdb.gz dbip-asn-lite.mmdb.gz ATTRIBUTION.txt)
readonly ASSETS=("${SUMMED[@]}" checksums.txt)
readonly PROFILES=(country region city)

# The monthly releases' tags; prune touches nothing else. data-latest is the
# moving alias that latest maintains. DEFAULT_KEEP is the retention policy:
# the number of newest monthly releases prune keeps unless DATA_KEEP is set.
readonly MONTHLY_TAG_RE='^data-[0-9]{4}-(0[1-9]|1[0-2])$'
readonly LATEST_TAG=data-latest
readonly DEFAULT_KEEP=12

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

log() { printf 'data-release: %s\n' "$*" >&2; }
die() {
	printf 'data-release: error: %s\n' "$*" >&2
	exit 2
}

usage() {
	sed -n '2,10p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2
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

# download MONTH EDITION WORK: download dbip-EDITION-lite-MONTH.mmdb.gz into
# WORK, or reuse a complete earlier download.
download() {
	local month=$1 edition=$2 work=$3
	local name="dbip-${edition}-lite-${month}.mmdb.gz"
	local url="https://download.db-ip.com/free/${name}"
	local gz="$work/${name}"

	if [[ -s $gz ]] && gzip -t "$gz" 2>/dev/null; then
		log "reusing $gz"
	else
		local status
		status="$(curl -sSIL -o /dev/null -w '%{http_code}' "$url")" ||
			die "cannot reach $url"
		case $status in
		200) ;;
		404) die "DB-IP has not published ${name} (HTTP 404 from $url). DB-IP publishes early in the month; retry later or pass an earlier month." ;;
		*) die "unexpected HTTP $status from $url" ;;
		esac
		log "downloading $url"
		curl -fsSL --retry 3 --retry-delay 5 -o "$gz.part" "$url"
		gzip -t "$gz.part" || die "$url is not a valid gzip file"
		mv "$gz.part" "$gz"
	fi
	log "source: $gz ($(mib "$(filesize "$gz")"))"
}

cmd_build() {
	local month=$1 out work
	out="$(abs_dir "$2")"
	work="$(abs_dir "$(work_dir "$out")")"

	local name="dbip-city-lite-${month}.mmdb"
	local gz="$work/${name}.gz" mmdb="$work/${name}"

	# 1. Download every edition (or reuse complete earlier downloads) and
	#    attach each unchanged, under the release's month-less name.
	local edition
	for edition in "${SOURCES[@]}"; do
		download "$month" "$edition" "$work"
		cp "$work/dbip-${edition}-lite-${month}.mmdb.gz" "$out/dbip-${edition}-lite.mmdb.gz"
	done

	# 2. Decompress the City source.
	gunzip -c "$gz" >"$mmdb.part"
	mv "$mmdb.part" "$mmdb"
	log "source: $mmdb ($(mib "$(filesize "$mmdb")"))"

	# 3. Build the CLI from this checkout once and run it per profile (the
	#    same as `go run . build` in cmd/ip2geo, without three compiles).
	#    cmd/ip2geo is a module of its own that builds against this
	#    checkout's root and mmdb modules.
	local bin="$work/ip2geo"
	(cd "$repo_root/cmd/ip2geo" && CGO_ENABLED=0 go build -trimpath -o "$bin" .)

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
	(cd "$out" && sha256 "${SUMMED[@]}" >checksums.txt)
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

This release also redistributes the ${month} source files themselves,
unmodified: the files are DB-IP's downloads byte for byte, renamed only to
drop the month from the file name.

  dbip-city-lite.mmdb.gz     dbip-city-lite-${month}.mmdb.gz
                             https://db-ip.com/db/download/ip-to-city-lite
  dbip-country-lite.mmdb.gz  dbip-country-lite-${month}.mmdb.gz
                             https://db-ip.com/db/download/ip-to-country-lite
  dbip-asn-lite.mmdb.gz      dbip-asn-lite-${month}.mmdb.gz
                             https://db-ip.com/db/download/ip-to-asn-lite

Copyright (c) DB-IP.com, licensed under the same CC BY 4.0 license, with
the same attribution: IP Geolocation by DB-IP — https://db-ip.com
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
| \`dbip-city-lite.mmdb.gz\` | $(mib "$(filesize "$out/dbip-city-lite.mmdb.gz")") | source: DB-IP IP-to-City Lite ${month}, unmodified |
| \`dbip-country-lite.mmdb.gz\` | $(mib "$(filesize "$out/dbip-country-lite.mmdb.gz")") | source: DB-IP IP-to-Country Lite ${month}, unmodified |
| \`dbip-asn-lite.mmdb.gz\` | $(mib "$(filesize "$out/dbip-asn-lite.mmdb.gz")") | source: DB-IP IP-to-ASN Lite ${month}, unmodified |
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

The \`.mmdb.gz\` files are the ${month} DB-IP Lite sources, attached unmodified (only renamed without the month) because DB-IP serves only the current month; they keep this month's builds reproducible, and the README's size table is checked against them.

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
	(cd "$repo_root/mmdb" && IP2GEO_TEST_MMDB="$mmdb" \
		go test -count=1 -timeout 30m -run 'TestBuildMatchesSource|TestRegisteredCountryOnly' -v .)
}

# cmd_compat checks the assets against the newest released reader, not only
# the checkout that built them: build runs from main, which can be ahead of
# the last tag (for example mid-way through a format change), and a published
# file that no released version opens would break every existing user.
cmd_compat() {
	local month=$1 out work version bin profile geoc line ip
	out="$(abs_dir "$2")"
	work="$(abs_dir "$(work_dir "$out")")"
	version="${RELEASED_VERSION:-latest}"

	# Install outside the checkout so the local go.mod plays no part. The go
	# command takes the CLI from the cmd/ip2geo module (tag
	# cmd/ip2geo/vX.Y.Z) when it has the version, and from the root module
	# for the releases before the CLI became a module of its own (v0.3.x).
	(cd "$work" && GOBIN="$work/released" CGO_ENABLED=0 \
		go install "github.com/haxqer/ip2geo/cmd/ip2geo@${version}")
	bin="$work/released/ip2geo"

	for profile in "${PROFILES[@]}"; do
		[[ -s "$out/dbip-${profile}-lite.geoc.gz" ]] ||
			die "$out/dbip-${profile}-lite.geoc.gz not found; run '$0 build $month $2' first"
		geoc="$work/compat-${profile}.geoc"
		gunzip -c "$out/dbip-${profile}-lite.geoc.gz" >"$geoc"
		"$bin" info -db "$geoc" >/dev/null ||
			die "$profile: ip2geo@${version} cannot open the built file"
		for ip in 8.8.8.8 2001:4860:4860::8888; do
			line="$("$bin" lookup -db "$geoc" "$ip")"
			log "$profile (ip2geo@${version}): $line"
			[[ $line == "$ip: "* && $line != *"no data"* ]] ||
				die "$profile: ip2geo@${version} lookup of $ip failed: $line"
		done
		rm -f "$geoc"
	done
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

# latest and prune. Every GitHub call goes through gh_get (reads) or
# gh_change (writes); gh_change is the only place that writes, and in a dry
# run it only logs. Neither is ever called in a $(...) subshell, so die stops
# the whole script: an API error fails closed before anything else changes.

dry_run=false
gh_tmp=""

# tmp_dir: create the scratch directory gh_tmp, removed at exit, once.
tmp_dir() {
	if [[ -z $gh_tmp ]]; then
		gh_tmp="$(mktemp -d "${TMPDIR:-/tmp}/data-release.XXXXXX")"
		trap 'rm -rf "$gh_tmp"' EXIT
	fi
}

is_monthly_tag() { [[ $1 =~ $MONTHLY_TAG_RE ]]; }

# assert_monthly_tag TAG: the last check before anything deletes TAG.
assert_monthly_tag() {
	is_monthly_tag "$1" || die "refusing to touch tag '$1': it is not a data-YYYY-MM tag"
}

# gh_get PATH [JQ] -> 0 with the output in GH_OUT, 1 on HTTP 404; dies on any
# other error. --paginate concatenates every page (JQ runs on each).
gh_get() {
	local path=$1 err
	tmp_dir
	local args=(api --paginate "$path")
	[[ $# -ge 2 ]] && args+=(--jq "$2")
	if GH_OUT="$(gh "${args[@]}" </dev/null 2>"$gh_tmp/err")"; then
		return 0
	fi
	err="$(cat "$gh_tmp/err")"
	[[ $err == *"HTTP 404"* ]] && return 1
	die "GET $path failed: ${err:-gh exited non-zero}"
}

# gh_change DESCRIPTION -- COMMAND...: run a gh command that changes the
# repository, or only log it in a dry run. Returns 1 when the command fails
# with HTTP 404 (the thing is already gone) and dies on any other failure.
gh_change() {
	local what=$1 err
	shift 2
	if $dry_run; then
		log "dry-run: would $what"
		return 0
	fi
	tmp_dir
	log "$what"
	if "$@" </dev/null >/dev/null 2>"$gh_tmp/err"; then
		return 0
	fi
	err="$(cat "$gh_tmp/err")"
	[[ $err == *"HTTP 404"* ]] && return 1
	die "cannot $what: ${err:-gh exited non-zero}"
}

# list_releases REPO -> RELEASES: one "tag<TAB>draft<TAB>id" line per release,
# drafts included (the token sees them when it can push).
list_releases() {
	gh_get "repos/$1/releases?per_page=100" \
		'.[] | [.tag_name, (.draft | tostring), (.id | tostring)] | @tsv' ||
		die "cannot list the releases of $1 (HTTP 404)"
	RELEASES=$GH_OUT
}

# published_monthly -> the published data-YYYY-MM releases in RELEASES as
# "tag<TAB>id", newest month first (the names sort by date). data-latest and
# v* never match.
published_monthly() {
	local tag draft id
	while IFS=$'\t' read -r tag draft id; do
		if [[ $draft == false ]] && is_monthly_tag "$tag"; then
			printf '%s\t%s\n' "$tag" "$id"
		fi
	done <<<"$RELEASES" | sort -r
}

# release_ids TAG DRAFT -> the ids of the releases in RELEASES with TAG whose
# draft state is DRAFT (true or false), one per line.
release_ids() {
	local tag draft id
	while IFS=$'\t' read -r tag draft id; do
		if [[ $tag == "$1" && $draft == "$2" ]]; then
			printf '%s\n' "$id"
		fi
	done <<<"$RELEASES"
}

# tag_commit REPO TAG -> 0 with the commit TAG points at in TAG_COMMIT (an
# annotated tag is peeled), 1 when the tag does not exist.
tag_commit() {
	gh_get "repos/$1/git/ref/tags/$2" '.object.type + " " + .object.sha' || return 1
	local type=${GH_OUT%% *} sha=${GH_OUT#* }
	while [[ $type == tag ]]; do
		gh_get "repos/$1/git/tags/$sha" '.object.type + " " + .object.sha' ||
			die "tag object $sha of $2 is missing"
		type=${GH_OUT%% *} sha=${GH_OUT#* }
	done
	[[ $type == commit && $sha =~ ^[0-9a-f]{40}$ ]] || die "tag $2 does not point at a commit ($GH_OUT)"
	TAG_COMMIT=$sha
}

# release_assets REPO ID -> ASSETS_TSV: "id<TAB>name<TAB>size" per asset of
# the release, sorted by name.
release_assets() {
	gh_get "repos/$1/releases/$2/assets?per_page=100" \
		'.[] | [(.id | tostring), .name, (.size | tostring)] | @tsv' ||
		die "release $2 of $1 disappeared"
	ASSETS_TSV="$(sort -t $'\t' -k2,2 <<<"$GH_OUT")"
}

# asset_id NAME -> the id of asset NAME in ASSETS_TSV, or nothing.
asset_id() {
	local id name
	while IFS=$'\t' read -r id name _; do
		if [[ $name == "$1" ]]; then
			printf '%s\n' "$id"
			return 0
		fi
	done <<<"$ASSETS_TSV"
}

# check_not_latest REPO: fail if GitHub's "Latest release" (/releases/latest,
# the badge on the repository page) is a data release, not a v* release.
check_not_latest() {
	gh_get "repos/$1/releases/latest" '.tag_name' || return 0 # no Latest release at all
	if [[ $GH_OUT == data-* ]]; then
		die "GitHub marks $GH_OUT as the repository's Latest release; restore the newest v* release with: gh release edit <v-tag> --repo $1 --latest"
	fi
	log "GitHub's Latest release is still $GH_OUT"
}

cmd_latest() {
	local month=${1:-} repo src src_id src_commit src_assets latest_id names f
	repo="$(gh_repo)"
	tmp_dir
	list_releases "$repo"

	# The month to mirror: the one given, else the newest published one.
	if [[ -n $month ]]; then
		src="data-$month"
		src_id="$(published_monthly | awk -F'\t' -v t="$src" '$1 == t { print $2 }')"
		[[ -n $src_id ]] || die "no published release $src in $repo"
	else
		local newest
		newest="$(published_monthly | head -n 1)"
		[[ -n $newest ]] || die "no published data-YYYY-MM release in $repo to point $LATEST_TAG at"
		src=${newest%%$'\t'*} src_id=${newest#*$'\t'}
	fi
	assert_monthly_tag "$src"
	tag_commit "$repo" "$src" || die "release $src has no tag in $repo"
	src_commit=$TAG_COMMIT
	release_assets "$repo" "$src_id"
	src_assets=$ASSETS_TSV
	[[ -n $src_assets ]] || die "release $src has no assets"
	names="$(cut -f2 <<<"$src_assets")"
	grep -qxF checksums.txt <<<"$names" || die "release $src has no checksums.txt"
	log "mirroring $src (commit ${src_commit:0:12}, $(wc -l <<<"$names" | tr -d ' ') assets) as $LATEST_TAG"

	latest_id="$(release_ids "$LATEST_TAG" false | head -n 1)"
	local title="Test databases — DB-IP Lite (latest: ${src#data-})"

	# Already current: same commit, title, asset names and sizes, and
	# checksums.txt. A rerun of data.yml ends here.
	if [[ -n $latest_id ]] && tag_commit "$repo" "$LATEST_TAG" && [[ $TAG_COMMIT == "$src_commit" ]] &&
		gh_get "repos/$repo/releases/$latest_id" '.name' && [[ $GH_OUT == "$title" ]]; then
		release_assets "$repo" "$latest_id"
		if [[ "$(cut -f2,3 <<<"$ASSETS_TSV")" == "$(cut -f2,3 <<<"$src_assets")" ]]; then
			gh release download "$src" --repo "$repo" -p checksums.txt -O "$gh_tmp/src.sums" --clobber ||
				die "cannot download checksums.txt of $src"
			gh release download "$LATEST_TAG" --repo "$repo" -p checksums.txt -O "$gh_tmp/latest.sums" --clobber ||
				die "cannot download checksums.txt of $LATEST_TAG"
			if cmp -s "$gh_tmp/src.sums" "$gh_tmp/latest.sums"; then
				log "$LATEST_TAG already mirrors $src; nothing to do"
				check_not_latest "$repo"
				return 0
			fi
		fi
	fi

	# Download the source assets once and check them before copying.
	local src_dir="$gh_tmp/src"
	if $dry_run; then
		log "dry-run: would download the assets of $src and check them against its checksums.txt"
	else
		mkdir -p "$src_dir"
		gh release download "$src" --repo "$repo" -D "$src_dir" ||
			die "cannot download the assets of $src"
		(cd "$src_dir" && sha256 -c checksums.txt >&2) || die "the assets of $src do not match its checksums.txt"
		while IFS= read -r f; do
			[[ -s "$src_dir/$f" ]] || die "asset $f of $src was not downloaded"
		done <<<"$names"
	fi

	# Point the data-latest tag at the source's commit: force-update it (it
	# moves every month) or create it.
	if tag_commit "$repo" "$LATEST_TAG"; then
		if [[ $TAG_COMMIT != "$src_commit" ]]; then
			gh_change "move tag $LATEST_TAG from ${TAG_COMMIT:0:12} to ${src_commit:0:12}" -- \
				gh api -X PATCH "repos/$repo/git/refs/tags/$LATEST_TAG" -f sha="$src_commit" -F force=true ||
				die "tag $LATEST_TAG disappeared while moving it"
		fi
	else
		gh_change "create tag $LATEST_TAG at ${src_commit:0:12}" -- \
			gh api -X POST "repos/$repo/git/refs" -f ref="refs/tags/$LATEST_TAG" -f sha="$src_commit" ||
			die "cannot create tag $LATEST_TAG"
	fi

	# Release notes: a pointer to the month, then the month's own notes
	# (file table, usage, CC BY 4.0 attribution).
	local notes="$gh_tmp/notes.md" server="${GITHUB_SERVER_URL:-https://github.com}"
	gh_get "repos/$repo/releases/$src_id" '.body // ""' || die "release $src disappeared"
	{
		# shellcheck disable=SC2016 # the backticks are Markdown
		printf '> **Moving alias.** `%s` always serves the newest monthly data release, currently **[%s](%s/%s/releases/tag/%s)**: the same files, byte for byte, re-uploaded and re-tagged every month. Download from `%s` (or another `data-YYYY-MM`) when you need a fixed month.\n\n' \
			"$LATEST_TAG" "$src" "$server" "$repo" "$src" "$src"
		printf '%s\n' "$GH_OUT"
	} >"$notes"

	# A draft left by an interrupted create carries the tag name but serves
	# nothing; remove it so the create below does not leave a second one.
	local draft_id
	while IFS= read -r draft_id; do
		[[ -n $draft_id ]] || continue
		gh_change "delete leftover draft release $draft_id ($LATEST_TAG)" -- \
			gh api -X DELETE "repos/$repo/releases/$draft_id" || true
	done <<<"$(release_ids "$LATEST_TAG" true)"

	if [[ -z $latest_id ]]; then
		# First run: create it. gh uploads into a draft and publishes it once
		# every asset is up; --latest=false keeps GitHub's Latest badge on
		# the v* code releases.
		local files=()
		while IFS= read -r f; do files+=("$src_dir/$f"); done <<<"$names"
		gh_change "create release $LATEST_TAG with the assets of $src" -- \
			gh release create "$LATEST_TAG" --repo "$repo" --verify-tag \
			--title "$title" --notes-file "$notes" --latest=false "${files[@]}" ||
			die "cannot create release $LATEST_TAG"
	else
		swap_assets "$repo" "$latest_id" "$src_dir" "$names"
		# Title and notes. make_latest=false on every update as well: the
		# API's default for it is true.
		gh_change "update the title and notes of release $LATEST_TAG" -- \
			gh api -X PATCH "repos/$repo/releases/$latest_id" \
			-f name="$title" -F body=@"$notes" -f target_commitish="$src_commit" \
			-F draft=false -F prerelease=false -f make_latest=false ||
			die "release $LATEST_TAG disappeared while updating it"
	fi
	if $dry_run; then
		log "dry-run: $LATEST_TAG would mirror $src"
	else
		log "$LATEST_TAG now mirrors $src"
	fi
	check_not_latest "$repo"
}

# swap_assets REPO ID SRCDIR NAMES: replace the assets of release ID with the
# files NAMES (one per line) in SRCDIR. Each new file is first uploaded as
# next-NAME; only then is each old NAME deleted and next-NAME renamed to
# NAME, checksums.txt last, so a download URL is missing for two quick API
# calls instead of a whole upload. Assets the source no longer has go.
swap_assets() {
	local repo=$1 id=$2 src_dir=$3 names=$4 prefix=next- f old new aid name
	local stage="$gh_tmp/stage" staged=()
	release_assets "$repo" "$id"

	# Leftovers of an interrupted swap.
	while IFS=$'\t' read -r aid name _; do
		[[ -n $aid && $name == "$prefix"* ]] || continue
		gh_change "delete leftover asset $name of $LATEST_TAG" -- \
			gh api -X DELETE "repos/$repo/releases/assets/$aid" || true
	done <<<"$ASSETS_TSV"

	mkdir -p "$stage"
	while IFS= read -r f; do
		$dry_run || cp "$src_dir/$f" "$stage/$prefix$f"
		staged+=("$stage/$prefix$f")
	done <<<"$names"
	gh_change "upload ${#staged[@]} assets to $LATEST_TAG as ${prefix}NAME" -- \
		gh release upload "$LATEST_TAG" --repo "$repo" "${staged[@]}" ||
		die "release $LATEST_TAG disappeared while uploading"

	local before=$ASSETS_TSV
	$dry_run || release_assets "$repo" "$id"
	local order
	order="$(grep -vxF checksums.txt <<<"$names" || true)"$'\n'checksums.txt
	while IFS= read -r f; do
		[[ -n $f ]] || continue
		old="$(asset_id "$f")"
		new="$(asset_id "$prefix$f")"
		$dry_run || [[ -n $new ]] || die "uploaded asset $prefix$f is missing from $LATEST_TAG"
		if [[ -n $old ]]; then
			gh_change "delete old asset $f of $LATEST_TAG" -- \
				gh api -X DELETE "repos/$repo/releases/assets/$old" || true
		fi
		gh_change "rename $prefix$f to $f on $LATEST_TAG" -- \
			gh api -X PATCH "repos/$repo/releases/assets/${new:-NEW}" -f name="$f" ||
			die "uploaded asset $prefix$f disappeared"
	done <<<"$order"

	while IFS=$'\t' read -r aid name _; do
		[[ -n $aid && $name != "$prefix"* ]] || continue
		grep -qxF "$name" <<<"$names" && continue
		gh_change "delete asset $name of $LATEST_TAG (not in the source release)" -- \
			gh api -X DELETE "repos/$repo/releases/assets/$aid" || true
	done <<<"$before"
}

# keep_count -> DATA_KEEP, or DEFAULT_KEEP when unset or empty. It must be a
# positive integer: 0 would delete every monthly release, including the one
# data-latest mirrors.
keep_count() {
	local keep=${DATA_KEEP:-$DEFAULT_KEEP}
	[[ $keep =~ ^[1-9][0-9]{0,3}$ ]] || die "DATA_KEEP must be a positive integer, got '$keep'"
	printf '%s\n' "$keep"
}

cmd_prune() {
	local keep repo refs published tag id ref
	keep="$(keep_count)"
	repo="$(gh_repo)"

	# List everything before changing anything: an error here changes nothing.
	list_releases "$repo"
	gh_get "repos/$repo/git/matching-refs/tags/data-" '.[] | .ref' ||
		die "cannot list the data-* tags of $repo (HTTP 404)"
	refs=$GH_OUT
	published="$(published_monthly)"

	local count=0
	[[ -z $published ]] || count="$(wc -l <<<"$published" | tr -d ' ')"
	log "$repo has $count published data-YYYY-MM releases; keeping the newest $keep"

	# The month scripts/size-table.month pins is kept whatever its age: the
	# README size table check in ci.yml fetches its sources from that release.
	local pinned="" month_file="$repo_root/scripts/size-table.month"
	if [[ -s $month_file ]]; then
		pinned="data-$(tr -d '[:space:]' <"$month_file")"
		is_monthly_tag "$pinned" || die "$month_file must hold one YYYY-MM, got '${pinned#data-}'"
	fi

	# Releases beyond the newest $keep, with their tags.
	local n=0 cutoff="" deleted=0 kept=""
	while IFS=$'\t' read -r tag id; do
		[[ -n $tag ]] || continue
		n=$((n + 1))
		if ((n <= keep)); then
			kept+=" $tag"
			cutoff=$tag
			continue
		fi
		if [[ $tag == "$pinned" ]]; then
			log "keeping $tag as well: scripts/size-table.month pins it"
			continue
		fi
		assert_monthly_tag "$tag"
		# The id comes from the listing; check that it is still this tag's
		# release before deleting it.
		if gh_get "repos/$repo/releases/$id" '.tag_name'; then
			[[ $GH_OUT == "$tag" ]] || die "release $id is $GH_OUT, not $tag; refusing to delete it"
			gh_change "delete release $tag ($id)" -- gh api -X DELETE "repos/$repo/releases/$id" ||
				log "release $tag ($id) is already gone"
		else
			log "release $tag ($id) is already gone"
		fi
		delete_monthly_tag "$repo" "$tag"
		deleted=$((deleted + 1))
	done <<<"$published"
	[[ -z $kept ]] || log "keeping:$kept"

	# Monthly tags without a published release (for example left by a run
	# that deleted the release and then failed) that are older than the
	# oldest kept release. Only once $keep releases are kept, so no tag is
	# deleted while there are fewer months than that.
	if ((n >= keep)) && [[ -n $cutoff ]]; then
		while IFS= read -r ref; do
			tag=${ref#refs/tags/}
			is_monthly_tag "$tag" || continue
			[[ $tag < $cutoff && $tag != "$pinned" ]] || continue
			grep -q "^$tag"$'\t' <<<"$published" && continue
			# A draft (an interrupted publish) may still want its tag.
			if [[ -n "$(release_ids "$tag" true)" ]]; then
				log "keeping tag $tag: a draft release uses it"
				continue
			fi
			delete_monthly_tag "$repo" "$tag"
			deleted=$((deleted + 1))
		done <<<"$refs"
	fi

	if ((deleted == 0)); then
		log "nothing to prune"
	elif $dry_run; then
		log "dry-run: would prune $deleted monthly releases/tags"
	else
		log "pruned $deleted monthly releases/tags"
	fi
}

# delete_monthly_tag REPO TAG: delete refs/tags/TAG, which must be a
# data-YYYY-MM tag; already gone is fine.
delete_monthly_tag() {
	assert_monthly_tag "$2"
	gh_change "delete tag $2" -- gh api -X DELETE "repos/$1/git/refs/tags/$2" ||
		log "tag $2 is already gone"
}

# parse_dry_run ARGS... -> the arguments other than --dry-run in REST.
# DATA_DRY_RUN=1 also selects a dry run.
parse_dry_run() {
	case ${DATA_DRY_RUN:-} in
	1 | true) dry_run=true ;;
	"" | 0 | false) ;;
	*) die "DATA_DRY_RUN must be 1 or 0, got '$DATA_DRY_RUN'" ;;
	esac
	REST=()
	local a
	for a in "$@"; do
		if [[ $a == --dry-run ]]; then
			dry_run=true
		else
			REST+=("$a")
		fi
	done
}

main() {
	[[ $# -ge 1 ]] || usage
	local cmd=$1
	shift
	case $cmd in
	build | verify | compat | publish)
		[[ $# -eq 2 ]] || usage
		check_month "$1"
		"cmd_$cmd" "$1" "$2"
		;;
	exists)
		[[ $# -eq 1 ]] || usage
		check_month "$1"
		cmd_exists "$1"
		;;
	latest)
		parse_dry_run "$@"
		[[ ${#REST[@]} -le 1 ]] || usage
		if [[ ${#REST[@]} -eq 1 ]]; then
			check_month "${REST[0]}"
			cmd_latest "${REST[0]}"
		else
			cmd_latest
		fi
		;;
	prune)
		parse_dry_run "$@"
		[[ ${#REST[@]} -eq 0 ]] || usage
		cmd_prune
		;;
	*) usage ;;
	esac
}

main "$@"
