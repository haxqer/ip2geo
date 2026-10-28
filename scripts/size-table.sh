#!/usr/bin/env bash
# Generate and check the README's size table (<!-- size-table:begin/end -->).
#
#   scripts/size-table.sh update SRCDIR   rebuild every row from SRCDIR and rewrite README.md
#   scripts/size-table.sh check  SRCDIR   exit 1 with a diff when README.md differs from a rebuild
#   scripts/size-table.sh fetch  OUTDIR   download the pinned month's sources from release data-MONTH
#   scripts/size-table.sh month           print the pinned month
#
# The month is pinned in scripts/size-table.month, the only place it is
# written; README.md names it through the generated block, so check fails
# when the two disagree. To move the table to a new month, edit that file
# and run update on the new month's sources.
#
# SRCDIR holds the month's three DB-IP Lite sources, City, Country and ASN,
# under DB-IP's names (dbip-city-lite-YYYY-MM.mmdb) or the data release's
# (dbip-city-lite.mmdb), each optionally gzipped. Rows are built by
# scripts/sizetable (mmdb.Build with a fixed build time), so the output
# depends only on the sources and this tree.
#
# fetch takes the sources from our own GitHub Release data-MONTH, because
# DB-IP serves only the current month: data-release.sh attaches them to every
# data release, and checksums.txt there must list them. It needs curl only,
# no token. GH_SERVER and GH_REPO (or GitHub Actions' GITHUB_SERVER_URL and
# GITHUB_REPOSITORY) select the repository, default haxqer/ip2geo on
# github.com.
#
# Exit status: 0 on success; 1 from check when README.md is stale, and from
# fetch when the release has no source assets; 2 on any error.
set -euo pipefail

readonly EDITIONS=(city country asn)

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly month_file="$repo_root/scripts/size-table.month"
readonly readme="$repo_root/README.md"

log() { printf 'size-table: %s\n' "$*" >&2; }
die() {
	printf 'size-table: error: %s\n' "$*" >&2
	exit 2
}

usage() {
	sed -n '2,7p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2
	exit 2
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$@"
	else
		shasum -a 256 "$@"
	fi
}

pinned_month() {
	local m
	[[ -s $month_file ]] || die "$month_file is missing"
	m="$(tr -d '[:space:]' <"$month_file")"
	[[ $m =~ ^[0-9]{4}-(0[1-9]|1[0-2])$ ]] || die "$month_file must hold one YYYY-MM, got '$m'"
	printf '%s\n' "$m"
}

# source_file SRCDIR EDITION MONTH WORK -> path of the edition's .mmdb,
# decompressed into WORK if SRCDIR has only the .gz.
source_file() {
	local dir=$1 ed=$2 month=$3 work=$4 base
	for base in "dbip-${ed}-lite-${month}.mmdb" "dbip-${ed}-lite.mmdb"; do
		if [[ -s "$dir/$base" ]]; then
			printf '%s\n' "$dir/$base"
			return
		fi
	done
	for base in "dbip-${ed}-lite-${month}.mmdb" "dbip-${ed}-lite.mmdb"; do
		if [[ -s "$dir/$base.gz" ]]; then
			gunzip -c "$dir/$base.gz" >"$work/$base" || die "cannot decompress $dir/$base.gz"
			printf '%s\n' "$work/$base"
			return
		fi
	done
	die "no DB-IP ${ed} Lite source for ${month} in $dir (want dbip-${ed}-lite-${month}.mmdb or dbip-${ed}-lite.mmdb, optionally .gz)"
}

# generate SRCDIR OUT: write README.md with a freshly built table to OUT.
generate() {
	local dir=$1 out=$2 month work
	month="$(pinned_month)"
	[[ -d $dir ]] || die "$dir is not a directory"
	dir="$(cd "$dir" && pwd)"
	work="$(mktemp -d "${TMPDIR:-/tmp}/size-table.XXXXXX")"
	# shellcheck disable=SC2064 # expand $work now: it is local
	trap "rm -rf '$work'" EXIT

	local city country asn
	city="$(source_file "$dir" city "$month" "$work")"
	country="$(source_file "$dir" country "$month" "$work")"
	asn="$(source_file "$dir" asn "$month" "$work")"
	log "building every row from DB-IP Lite $month"
	(cd "$repo_root" && go run ./scripts/sizetable -month "$month" \
		-city "$city" -country "$country" -asn "$asn" -readme "$readme" -out "$out")
}

cmd_update() {
	local tmp="$readme.size-table.tmp"
	generate "$1" "$tmp"
	mv "$tmp" "$readme"
	log "updated $readme"
}

cmd_check() {
	local tmp
	tmp="$(mktemp "${TMPDIR:-/tmp}/size-table-readme.XXXXXX")"
	generate "$1" "$tmp"
	if diff -u --label README.md --label 'README.md (regenerated)' "$readme" "$tmp"; then
		rm -f "$tmp"
		log "README.md size table is up to date"
		return 0
	fi
	rm -f "$tmp"
	log "README.md size table is stale: run scripts/size-table.sh update SRCDIR with the $(pinned_month) sources and commit the result"
	return 1
}

# http_status URL -> the final HTTP status of a HEAD request (redirects followed).
http_status() {
	curl -sSIL --retry 2 -o /dev/null -w '%{http_code}' "$1" || die "cannot reach $1"
}

cmd_fetch() {
	local out month server repo base
	month="$(pinned_month)"
	mkdir -p "$1"
	out="$(cd "$1" && pwd)"
	server="${GH_SERVER:-${GITHUB_SERVER_URL:-https://github.com}}"
	repo="${GH_REPO:-${GITHUB_REPOSITORY:-haxqer/ip2geo}}"
	base="${server}/${repo}/releases/download/data-${month}"

	# Releases published before data-release.sh attached the sources have
	# none: report that as "absent" (1), not as an error.
	local ed name status files=()
	for ed in "${EDITIONS[@]}"; do
		name="dbip-${ed}-lite.mmdb.gz"
		status="$(http_status "$base/$name")"
		case $status in
		200) files+=("$name") ;;
		404)
			log "release data-${month} has no $name ($base/$name is HTTP 404)"
			return 1
			;;
		*) die "unexpected HTTP $status from $base/$name" ;;
		esac
	done

	local f
	for f in checksums.txt "${files[@]}"; do
		log "downloading $base/$f"
		curl -fsSL --retry 3 --retry-delay 5 -o "$out/$f.part" "$base/$f" || die "cannot download $base/$f"
		mv "$out/$f.part" "$out/$f"
	done

	# Every source must be listed in the release's checksums.txt and match.
	local sums="$out/checksums.source.txt"
	: >"$sums"
	for f in "${files[@]}"; do
		grep -E "^[0-9a-f]{64}  $f\$" "$out/checksums.txt" >>"$sums" ||
			die "checksums.txt of data-${month} does not list $f; upload a checksums.txt that does"
	done
	(cd "$out" && sha256 -c checksums.source.txt >&2) || die "the sources do not match checksums.txt of data-${month}"
	for f in "${files[@]}"; do
		gzip -t "$out/$f" || die "$out/$f is not a valid gzip file"
	done
	log "DB-IP Lite ${month} sources in $out"
}

main() {
	[[ $# -ge 1 ]] || usage
	local cmd=$1
	shift
	case $cmd in
	update | check | fetch)
		[[ $# -eq 1 ]] || usage
		"cmd_$cmd" "$1"
		;;
	month)
		[[ $# -eq 0 ]] || usage
		pinned_month
		;;
	*) usage ;;
	esac
}

main "$@"
