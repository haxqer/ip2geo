#!/usr/bin/env bash
# Run commands in every Go module of the repository, and edit the nested
# modules' go.mod files for a release (docs/RELEASING.md).
#
#   scripts/modules.sh list              print the directory of every module
#   scripts/modules.sh run CMD [ARG...]  run CMD in every module; fail if any fails
#   scripts/modules.sh require VERSION   release step 1: mmdb and cmd/ip2geo require VERSION
#   scripts/modules.sh check VERSION     check step 1 (the release workflow runs it)
#   scripts/modules.sh pin VERSION       release step 2: cmd/ip2geo builds from the published VERSION
#   scripts/modules.sh unpin             release step 3: cmd/ip2geo builds from the checkout again
#
# The modules are the directories with a go.mod that git tracks: the root
# module github.com/haxqer/ip2geo (the reader, csvdb and the builder core,
# standard library only), mmdb and cmd/ip2geo (published, tagged mmdb/vX.Y.Z
# and cmd/ip2geo/vX.Y.Z in step with the root's vX.Y.Z), and bench and
# scripts/sizetable (never published). The nested modules build against
# this checkout through replace directives; cmd/ip2geo drops its own for the
# commit tagged cmd/ip2geo/vX.Y.Z, because go install refuses a module whose
# go.mod has one.
#
# pin fetches the root and mmdb VERSION, so their tags must be pushed first;
# it uses GOPROXY=direct, as a mirror may not have them yet.
set -euo pipefail

readonly ROOT=github.com/haxqer/ip2geo
readonly MMDB=$ROOT/mmdb

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

log() { printf 'modules: %s\n' "$*" >&2; }
die() {
	printf 'modules: error: %s\n' "$*" >&2
	exit 2
}

usage() {
	sed -n '5,10p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2
	exit 2
}

modules() {
	git -C "$repo_root" ls-files -- go.mod '*/go.mod' |
		grep -v '/testdata/' | sed -e 's|/\{0,1\}go\.mod$||' -e 's|^$|.|'
}

check_version() {
	[[ $1 =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || die "'$1' is not a version like v1.2.3"
}

# required DIR MODULE -> the version of MODULE that DIR/go.mod requires.
# go.mod is in go mod edit's canonical layout: "MODULE VERSION" on a line of
# its own in a require block, or after "require ".
required() {
	awk -v m="$2" '{ sub(/^require[ \t]+/, "") } $1 == m && $2 != "=>" { print $2; exit }' "$repo_root/$1/go.mod"
}

# replaced DIR MODULE -> the directory or module that replaces every version
# of MODULE in DIR/go.mod, if any.
replaced() {
	awk -v m="$2" '{ sub(/^replace[ \t]+/, "") } $1 == m && $2 == "=>" { print $3; exit }' "$repo_root/$1/go.mod"
}

cmd_run() {
	[[ $# -gt 0 ]] || usage
	local m failed=()
	for m in $(modules); do
		if [[ -n ${GITHUB_ACTIONS:-} ]]; then
			echo "::group::$m: $*"
		else
			log "$m: $*"
		fi
		(cd "$repo_root/$m" && "$@") || failed+=("$m")
		[[ -z ${GITHUB_ACTIONS:-} ]] || echo "::endgroup::"
	done
	if [[ ${#failed[@]} -gt 0 ]]; then
		for m in "${failed[@]}"; do
			if [[ -n ${GITHUB_ACTIONS:-} ]]; then
				echo "::error::$m: $* failed"
			else
				log "$m: $* failed"
			fi
		done
		exit 1
	fi
}

cmd_require() {
	local v=$1
	check_version "$v"
	(cd "$repo_root/mmdb" && go mod edit -require="$ROOT@$v")
	(cd "$repo_root/cmd/ip2geo" && go mod edit -require="$ROOT@$v" -require="$MMDB@$v")
	cmd_check "$v"
	log "mmdb and cmd/ip2geo now require $v; commit, then tag $v and mmdb/$v on that commit"
}

cmd_check() {
	local v=$1 dir mod want got bad=0
	check_version "$v"
	while read -r dir mod; do
		got="$(required "$dir" "$mod")"
		if [[ $got != "$v" ]]; then
			log "$dir/go.mod requires $mod ${got:-(nothing)}, want $v (run: scripts/modules.sh require $v)"
			bad=1
		fi
	done <<-EOF
		mmdb $ROOT
		cmd/ip2geo $ROOT
		cmd/ip2geo $MMDB
	EOF
	# The release binaries are built from this commit's cmd/ip2geo, which
	# must build against the tagged tree, not against published modules.
	while read -r dir mod want; do
		got="$(replaced "$dir" "$mod")"
		if [[ $got != "$want" ]]; then
			log "$dir/go.mod replaces $mod with '$got', want $want (run: scripts/modules.sh unpin)"
			bad=1
		fi
	done <<-EOF
		cmd/ip2geo $ROOT ../..
		cmd/ip2geo $MMDB ../../mmdb
	EOF
	[[ $bad == 0 ]] || exit 1
	log "mmdb and cmd/ip2geo require $v and cmd/ip2geo builds against the checkout"
}

cmd_pin() {
	local v=$1
	check_version "$v"
	cd "$repo_root/cmd/ip2geo"
	go mod edit -dropreplace="$ROOT" -dropreplace="$MMDB" -require="$ROOT@$v" -require="$MMDB@$v"
	GOWORK=off GOPROXY=direct go mod tidy
	GOWORK=off go build ./...
	log "cmd/ip2geo builds from $ROOT $v and $MMDB $v; commit, tag cmd/ip2geo/$v, then run: scripts/modules.sh unpin"
}

cmd_unpin() {
	cd "$repo_root/cmd/ip2geo"
	go mod edit -replace="$ROOT=../.." -replace="$MMDB=../../mmdb"
	GOWORK=off go mod tidy
	log "cmd/ip2geo builds against the checkout again"
}

[[ $# -gt 0 ]] || usage
case "$1" in
list)
	[[ $# -eq 1 ]] || usage
	modules
	;;
run)
	shift
	cmd_run "$@"
	;;
require | check | pin)
	[[ $# -eq 2 ]] || usage
	"cmd_$1" "$2"
	;;
unpin)
	[[ $# -eq 1 ]] || usage
	cmd_unpin
	;;
*) usage ;;
esac
