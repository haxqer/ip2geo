# Sample databases and data releases

ip2geo publishes ready-to-use sample databases every month, so you can try
the library without signing up for or buying any data. This page covers what
they contain, how they are built and verified, how long they are kept, and
how to build the same kind of files from your own MaxMind account in your own
CI. For that last case, the rule is short: **never publish MaxMind-derived
files.**

## What is published

Each month [`data.yml`](../.github/workflows/data.yml) publishes a GitHub
Release tagged `data-YYYY-MM` (for example `data-2026-11`), built from that
month's DB-IP Lite downloads:

| Source | Licence |
|---|---|
| [DB-IP IP-to-City Lite](https://db-ip.com/db/download/ip-to-city-lite) | CC BY 4.0 |
| [DB-IP IP-to-Country Lite](https://db-ip.com/db/download/ip-to-country-lite) | CC BY 4.0 |
| [DB-IP IP-to-ASN Lite](https://db-ip.com/db/download/ip-to-asn-lite) | CC BY 4.0 |

Every monthly release has these eight assets:

| Asset | Contents |
|---|---|
| `dbip-country-lite.geoc.gz` | `.geoc`, country profile, built from City Lite |
| `dbip-region-lite.geoc.gz` | `.geoc`, region profile, built from City Lite |
| `dbip-city-lite.geoc.gz` | `.geoc`, city profile, built from City Lite |
| `dbip-city-lite.mmdb.gz` | DB-IP's City Lite download, byte for byte (renamed to drop the month) |
| `dbip-country-lite.mmdb.gz` | DB-IP's Country Lite download, byte for byte |
| `dbip-asn-lite.mmdb.gz` | DB-IP's ASN Lite download, byte for byte |
| `ATTRIBUTION.txt` | The CC BY 4.0 attribution and a description of the changes |
| `checksums.txt` | SHA-256 of the seven files above |

The `.mmdb.gz` sources are attached because DB-IP serves only the current
month. Keeping them makes each month's build reproducible, and CI checks the
README size table against them (`scripts/size-table.sh fetch`).

The `.geoc` files are GEO2 files from `data-2026-11` on, and need ip2geo v0.2.0
or later. `data-2026-10` and older are GEO1, which every version still reads.

### Download URLs

```sh
# A fixed month: reproducible. Use this in builds and tests.
https://github.com/haxqer/ip2geo/releases/download/data-2026-11/<asset>

# Always the newest month (see "data-latest" below).
https://github.com/haxqer/ip2geo/releases/download/data-latest/<asset>
```

### Checksums

`checksums.txt` uses the `sha256sum` format. Download it from the same
release as the files and check them:

```sh
base=https://github.com/haxqer/ip2geo/releases/download/data-latest
curl -fL -O "$base/dbip-city-lite.geoc.gz" -O "$base/checksums.txt"
sha256sum -c --ignore-missing checksums.txt   # macOS: shasum -a 256 -c --ignore-missing checksums.txt
```

The checksums cover the compressed `.gz` files, so check them before you run
`gunzip`.

### Licence and attribution (DB-IP Lite, CC BY 4.0)

The DB-IP Lite databases are © DB-IP.com and licensed under
[Creative Commons Attribution 4.0 International](https://creativecommons.org/licenses/by/4.0/).
CC BY 4.0 lets anyone copy, redistribute and adapt the data, commercially
too, under these conditions:

- **Attribute DB-IP.** DB-IP asks for the line *IP Geolocation by DB-IP*,
  linked to <https://db-ip.com>. Put it wherever the data is used or shown,
  for example in your about page, your documentation, or next to the
  results.
- **Link the licence**, <https://creativecommons.org/licenses/by/4.0/>.
- **Say that the data was changed.** The `.geoc` files are converted, not
  copies: `ATTRIBUTION.txt` describes the change (the format conversion and
  the profile, with no change to the geolocation data). Keep that notice when
  you pass the files on.
- **Add no further restrictions.** You may not add terms or technical
  measures that stop others from doing what the licence allows.

Every release ships `ATTRIBUTION.txt`. If you redistribute any of the files,
for example in a container image or a package, include it with them. The
ip2geo code is MIT-licensed. That licence does not cover the data.

## How a release is built and verified

`data.yml` runs at 06:00 UTC on the 3rd of each month. DB-IP publishes the
month's Lite files on the 1st, which leaves two days of slack. A maintainer
can also start it by hand (`workflow_dispatch`) with an optional `YYYY-MM`.
It publishes only from `main`, and runs never overlap. All the logic is in
[`scripts/data-release.sh`](../scripts/data-release.sh), so every step can
also be run locally.

| Step | Command | What it does |
|---|---|---|
| Resolve month | (in data.yml) | Uses the input or the current UTC month, and checks the `YYYY-MM` format |
| Check for an existing release | `data-release.sh exists YYYY-MM` | Exits 0 if the release exists, which skips build and publish; exits 1 if it does not; exits 2 on an API error, which fails the run |
| Build | `data-release.sh build YYYY-MM OUTDIR` | Downloads the three DB-IP Lite `.mmdb.gz` files (a 404 fails clearly), builds the CLI from the checkout, builds the three `.geoc` profiles from City Lite, looks up 8.8.8.8 and 2001:4860:4860::8888 in each as a smoke test, compresses them with `gzip -9 -n` (reproducible bytes), and writes `ATTRIBUTION.txt`, `checksums.txt` and the release notes |
| Verify against the source | `data-release.sh verify YYYY-MM OUTDIR` | Runs the builder-vs-source integration tests (`TestBuildMatchesSource`, `TestRegisteredCountryOnly`) on the month's City Lite |
| Check with the latest release | `data-release.sh compat YYYY-MM OUTDIR` | Installs the newest released CLI (`go install …@latest`) and checks that it opens the new files and resolves both smoke addresses, so a release built from `main` can never publish files that released versions can't read |
| Publish | `data-release.sh publish YYYY-MM OUTDIR` | Checks `checksums.txt` again, then creates the release at the commit that built it, with `--latest=false`. gh uploads into a draft and publishes it only after every asset is up |
| Update data-latest | `data-release.sh latest` | Points `data-latest` at the newest monthly release (see below) |
| Prune old monthly releases | `data-release.sh prune` | Applies the retention policy (see below) |

The last two steps run on every run that gets past publish, including when
the month already existed. Both are idempotent, so rerunning the workflow
finishes a step that failed halfway.

To run the steps locally (`exists`, `publish`, `latest` and `prune` need `gh`
signed in, or `GH_TOKEN`):

```sh
scripts/data-release.sh build   2026-11 out/
scripts/data-release.sh verify  2026-11 out/
scripts/data-release.sh compat  2026-11 out/
GH_REPO=haxqer/ip2geo scripts/data-release.sh latest --dry-run
GH_REPO=haxqer/ip2geo scripts/data-release.sh prune  --dry-run
```

`--dry-run` (or `DATA_DRY_RUN=1`) makes only read-only API calls and prints
every change it would make.

## Retention policy

`data.yml` keeps the **newest 12 published `data-YYYY-MM` releases**. That's
one year, about 1.2 GB of assets (about 100 MB a month). `prune` deletes older monthly releases **and
their tags**. Tags are deleted too because a tag without a release serves no
files and would clutter `git fetch --tags`.

- The default is `DEFAULT_KEEP` in `scripts/data-release.sh` (12), the only
  place it is written. To change it without editing code, set a repository
  variable `DATA_KEEP` (Settings → Secrets and variables → Actions →
  Variables). Values must be at least 1.
- The month pinned in `scripts/size-table.month` is also kept, whatever its
  age, because ci.yml's README size-table check downloads its sources from
  that release.
- Only tags that match exactly `^data-[0-9]{4}-(0[1-9]|1[0-2])$` are ever
  deleted. `data-latest`, `v*` tags and any other tag are never touched, and
  each tag is checked against the pattern again right before it is deleted.
- Drafts don't count towards the 12 and are not deleted. A monthly tag
  without a published release is deleted only if it is older than the oldest
  kept month and no draft uses it.
- `prune` lists everything before it changes anything. Any API error other
  than "already gone" stops it with exit status 2 (fail closed). It is
  idempotent: a second run finds nothing to delete, and a run that stopped
  halfway is completed by the next one.

If you need an older month, keep your own copy. The sources and checksums are
in each release, so a copy is easy to verify.

## data-latest

`data-latest` is a real GitHub Release whose assets are byte-for-byte copies
of the newest `data-YYYY-MM` release. Its tag points at that release's
commit. After each monthly run,

```
https://github.com/haxqer/ip2geo/releases/download/data-latest/<asset>
```

serves the current month. Release download URLs resolve through a release
with that tag name, so a bare tag would not be enough. That is why
`data-latest` is a full release. Its tag is created once and never moves:
which commit it names does not matter for downloads, and the workflow
token may not point a tag at an older commit whose workflow files differ
from `main`.

How it is updated (`scripts/data-release.sh latest [YYYY-MM]`):

1. It picks the newest published monthly release, or the month given (for
   example to roll back a bad month). A manual backfill of an older month
   therefore never moves `data-latest` backwards.
2. It downloads that release's assets and checks them against its
   `checksums.txt`.
3. The first time, it creates the release, and with it the tag, at the
   commit running the workflow, with `gh release create --latest=false`. After that it updates the existing release in place:
   every new file is uploaded first as `next-<name>`, then each old asset is
   deleted and its `next-` copy renamed, with `checksums.txt` last. Each URL
   is missing only between two quick API calls, never for a whole upload,
   and the release object, its URL and its download counters stay.
4. It updates the title and notes, with `make_latest=false` on every update,
   then checks that GitHub's **Latest release** is still a `v*` release.

It is a no-op when `data-latest` already matches (same title, asset
names and sizes, and `checksums.txt`).

Things to know:

- **Use a fixed month for anything reproducible.** `data-latest` changes
  every month. While it is being updated (a few seconds), a download can mix
  the old and new months, so check files against the `checksums.txt` you
  downloaded with them.
- **The tag does not follow the data.** `git checkout data-latest` gives the
  commit that first created the alias, not the one that built the current
  month; the release notes link the month's own release and tag.
- **It is not GitHub's Latest release.** `/releases/latest` and the
  repository badge stay on the newest `v*` code release. If that ever goes
  wrong, `latest` fails the workflow and prints the fix
  (`gh release edit <v-tag> --latest`).
- **Other tooling ignores it.** GoReleaser ignores `data-*` tags
  (`.goreleaser.yaml`), and release.yml names its tag explicitly.
  bench.yml's previous-release lookup only matches `v*`.
  `scripts/size-table.sh` reads the month pinned in
  `scripts/size-table.month`. `data-latest` is not a semver tag, so the Go
  module proxy ignores it as well.

## Building from your own MaxMind account (in your own CI)

ip2geo reads MaxMind GeoLite2 and GeoIP2 `.mmdb` files directly
(`mmdb.Open`) and converts them to `.geoc` files (`mmdb.Build`, or `ip2geo
build`). Licensing is the catch:

> **Hard rule: never redistribute MaxMind-derived files.** A `.geoc` built
> from GeoLite2 or GeoIP2 is derived from MaxMind data and stays under
> MaxMind's terms: the [GeoLite EULA](https://www.maxmind.com/en/geolite2/eula),
> which forbids disclosing the data to third parties without MaxMind's prior
> written consent, or your GeoIP2 commercial agreement, which is stricter
> still. Keep the outputs in private storage:
> a private repository's workflow artifacts, a private bucket, an internal
> registry, or your own servers' disks. **Never put them in a GitHub Release,
> a public repository, a public container image, a package, or a public
> repository's Actions artifacts.** Artifacts of a public repository can be
> downloaded by anyone signed in to GitHub. This repository publishes DB-IP
> data only, for exactly this reason.

GeoLite users also have to keep the data current. MaxMind's GeoLite page
says to delete GeoLite databases within 30 days of a new release, and limits
GeoLite accounts to 30 downloads a day. Read the EULA for the full terms,
including attribution.

### Download URLs

MaxMind documents permalinks of the form

```
https://download.maxmind.com/geoip/databases/<EDITION>/download?suffix=<SUFFIX>
```

The documented example is `GeoIP2-City-CSV` with `suffix=zip`. Binary
`.mmdb` editions download as gzip archives, so for those the suffix is
`tar.gz`. Requests use HTTP Basic authentication, with your **account ID** as
the user name and your **licence key** as the password. They redirect to
Cloudflare R2 (`mm-prod-geoip-databases.a2649acb697e2c09b632799562c076f2.r2.cloudflarestorage.com`),
so your client must follow redirects (`curl -L`), and that host must not be
blocked. Edition IDs are names such as `GeoLite2-City`, `GeoLite2-Country`,
`GeoLite2-ASN` and `GeoIP2-City`. The account portal's *Download Databases*
page lists the exact permalinks for your account.

Not confirmed: MaxMind's public docs don't spell out the checksum permalink.
The portal offers a SHA256 link next to each download, and the example below
assumes it is `suffix=tar.gz.sha256`. Copy the real link from your portal
and adjust the example if it differs. See MaxMind's
[Updating databases](https://dev.maxmind.com/geoip/updating-databases/)
page for the current scheme. The officially supported alternative is
MaxMind's [`geoipupdate`](https://github.com/maxmind/geoipupdate) tool.

### Example workflow

The workflow below is an example for **your own private repository**. It is
not part of this repository. It downloads GeoLite2-City, builds `.geoc` files
with a pinned ip2geo CLI, and keeps them as a short-lived workflow artifact.
Its first step fails the job if the repository is public. Before using it:

1. Create two repository secrets (Settings → Secrets and variables → Actions):
   `MAXMIND_ACCOUNT_ID` and `MAXMIND_LICENSE_KEY`. Never print them or put
   them in a URL query string.
2. Pin the ip2geo version (`IP2GEO_VERSION`) and the action SHAs yourself.

```yaml
name: GeoIP databases (private)

on:
  schedule:
    - cron: "0 7 * * 3" # weekly; stay within the 30-downloads-a-day limit
  workflow_dispatch:

permissions:
  contents: read

jobs:
  build:
    runs-on: ubuntu-latest
    timeout-minutes: 30
    env:
      EDITION: GeoLite2-City # or GeoIP2-City with a commercial account
      IP2GEO_VERSION: v0.3.4 # pin; bump on purpose
    steps:
      # MaxMind-derived files must never become public: refuse to run in a
      # public repository, where workflow artifacts are downloadable.
      - name: Refuse to run in a public repository
        env:
          GH_TOKEN: ${{ github.token }}
        run: |
          private="$(gh api "repos/${GITHUB_REPOSITORY}" --jq .private)"
          if [ "$private" != "true" ]; then
            echo "::error::this workflow handles MaxMind data; run it only in a private repository"
            exit 1
          fi

      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version: stable

      - name: Download ${{ env.EDITION }}
        env:
          MAXMIND_ACCOUNT_ID: ${{ secrets.MAXMIND_ACCOUNT_ID }}
          MAXMIND_LICENSE_KEY: ${{ secrets.MAXMIND_LICENSE_KEY }}
        run: |
          set -euo pipefail
          base="https://download.maxmind.com/geoip/databases/${EDITION}/download"
          auth="${MAXMIND_ACCOUNT_ID}:${MAXMIND_LICENSE_KEY}"
          mkdir -p "$RUNNER_TEMP/mm" && cd "$RUNNER_TEMP/mm"
          # -L: the permalink redirects to Cloudflare R2. The credentials go
          # in Basic auth (-u), never in the URL.
          curl -fsSL --retry 3 -u "$auth" -o db.tar.gz "${base}?suffix=tar.gz"
          # Checksum permalink: copy it from your account portal if it differs.
          curl -fsSL --retry 3 -u "$auth" -o db.tar.gz.sha256 "${base}?suffix=tar.gz.sha256"
          echo "$(cut -d' ' -f1 db.tar.gz.sha256)  db.tar.gz" | sha256sum -c -
          tar -xzf db.tar.gz
          mv ./*/"${EDITION}.mmdb" "${EDITION}.mmdb"

      - name: Build .geoc files
        run: |
          set -euo pipefail
          GOBIN="$RUNNER_TEMP/bin" go install "github.com/haxqer/ip2geo/cmd/ip2geo@${IP2GEO_VERSION}"
          cd "$RUNNER_TEMP/mm"
          for profile in country region city; do
            "$RUNNER_TEMP/bin/ip2geo" build -src "${EDITION}.mmdb" -out "${profile}.geoc" -profile "$profile"
            "$RUNNER_TEMP/bin/ip2geo" lookup -db "${profile}.geoc" 8.8.8.8 2001:4860:4860::8888
          done
          sha256sum ./*.geoc > checksums.txt

      # A private artifact, kept briefly. For long-term storage, push to a
      # private bucket or internal registry instead, never to a release.
      - uses: actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # v7.0.2
        with:
          name: geoc-${{ env.EDITION }}
          path: |
            ${{ runner.temp }}/mm/*.geoc
            ${{ runner.temp }}/mm/checksums.txt
          retention-days: 7
          if-no-files-found: error
```

Your applications then fetch the artifact, or your private copy, with their
own credentials, and open it with `ip2geo.Open`. If you would rather not
store converted files at all, ship the `.mmdb` the same private way and call
`mmdb.Open` at startup. That costs about 2 s for a City database.
