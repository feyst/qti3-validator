#!/usr/bin/env bash
# Tests next-version.sh against throwaway repositories, and its registry
# lookups against a stub curl. Run with `make release-test`; CI runs it in
# the job `version` of pull requests.
set -euo pipefail

script=$(cd "$(dirname "$0")" && pwd)/next-version.sh
failures=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# repo NAME: a new repository with one release v1.2.1 of a file in cmd/.
repo() {
  dir=$tmp/$1
  git init -q -b main "$dir"
  git -C "$dir" config user.email test@example.com
  git -C "$dir" config user.name test
  git -C "$dir" config commit.gpgsign false
  git -C "$dir" config tag.gpgsign false
  mkdir -p "$dir/cmd" "$dir/docs"
  echo 1 > "$dir/cmd/main.go"
  git -C "$dir" add -A
  git -C "$dir" commit -qm "first"
  git -C "$dir" tag v1.2.1
}

# commit FILE MESSAGE: changes FILE in the current repository and commits.
commit() {
  echo "$RANDOM" >> "$dir/$1"
  git -C "$dir" add -A
  git -C "$dir" commit -qm "$2"
}

# run [ENV...] [-- ARGS...]: runs the script in the current repository with
# a clean environment plus ENV; sets out, err and status.
run() {
  local envs=() args=()
  while [ $# -gt 0 ] && [ "$1" != -- ]; do envs+=("$1"); shift; done
  [ $# -gt 0 ] && shift
  args=("$@")
  status=0
  out=$(cd "$dir" && env -u BUMP -u IMAGE -u KNOWN_IMAGE_TAGS "${envs[@]}" "$script" "${args[@]}" 2> "$tmp/err") || status=$?
  err=$(cat "$tmp/err")
}

check() {
  local name=$1 ok=$2
  if [ "$ok" = true ]; then
    echo "ok    $name"
  else
    echo "FAIL  $name: status $status, out '$out', err '$err'"
    failures=$((failures + 1))
  fi
}

# expect NAME WANT [ENV...]: the script prints WANT and exits 0.
expect() {
  local name=$1 want=$2
  shift 2
  run KNOWN_IMAGE_TAGS="${KNOWN:-}" "$@"
  check "$name" "$([ "$status" = 0 ] && [ "$out" = "$want" ] && echo true || echo false)"
}

# expect_fail NAME PATTERN [ENV...]: the script exits 2, PATTERN on stderr.
expect_fail() {
  local name=$1 pattern=$2
  shift 2
  run KNOWN_IMAGE_TAGS="${KNOWN:-}" "$@"
  check "$name" "$([ "$status" = 2 ] && [ -z "$out" ] && grep -q "$pattern" <<< "$err" && echo true || echo false)"
}

echo "# versions"

repo nothing
commit docs/readme.md "Docs only"
expect "nothing in the image changed" ""

repo test-only
commit cmd/main_test.go "Test only"
expect "tests are not in the image" ""

repo patch
commit cmd/main.go "Fix"
expect "a code change is a patch" v1.2.2

repo skip
commit cmd/main.go "Fix"
KNOWN="1.2.2 1.2.3" expect "versions with an image are skipped" v1.2.4
check "a skipped version is named on stderr" \
  "$([ "$(grep -c '^Skipping v1\.2\.[23]: ' <<< "$err")" = 2 ] && echo true || echo false)"

repo minor
commit cmd/main.go "$(printf 'Add a flag\n\nRelease: minor')"
commit cmd/main.go "Fix"
expect "a minor trailer since the release" v1.3.0

repo major
commit cmd/main.go "$(printf 'Feature\n\nRelease: minor')"
commit docs/readme.md "$(printf 'Breaking\n\nRelease: Major')"
expect "the highest Release line wins, case does not matter" v2.0.0

repo major-skip
commit cmd/main.go "$(printf 'Breaking\n\nRelease: major')"
KNOWN="2.0.0" expect "a skipped major goes on to its patch" v2.0.1

repo trailer-docs
commit docs/readme.md "$(printf 'Docs\n\nRelease: minor')"
expect "a Release line releases also without an image change" v1.3.0

repo old-trailer
commit cmd/main.go "$(printf 'Feature\n\nRelease: minor')"
git -C "$dir" tag v1.3.0
commit cmd/main.go "Fix"
expect "a Release line before the last release does not count" v1.3.1

repo not-a-trailer
commit cmd/main.go "Mention Release: major in the subject"
expect "only the body counts, not the subject" v1.2.2

repo merge
git -C "$dir" switch -q -c feature
commit cmd/main.go "$(printf 'Add a flag\n\nRelease: minor')"
git -C "$dir" switch -q main
commit docs/readme.md "Docs"
git -C "$dir" merge -q --no-ff -m "Merge pull request #1 from feature" feature
expect "a Release line in a merged branch counts" v1.3.0

repo squash-bullets
commit cmd/main.go "$(printf 'Add validate (#3)\n\n* Add a flag\n\nRelease: minor\n\n* Fix review comments\n\nCo-authored-by: A <a@example.com>')"
expect "a Release line inside a squash message counts" v1.3.0

repo squash-coauthor
commit cmd/main.go "$(printf 'Add validate\n\nRelease: minor\n\nCo-authored-by: A <a@example.com>')"
expect "a Release line before Co-authored-by counts" v1.3.0

repo no-blank
commit cmd/main.go "$(printf 'Add validate\n\nBody text\nRelease: minor')"
expect "a Release line without a blank line before it counts" v1.3.0

repo bullet
commit cmd/main.go "$(printf 'Add validate\n\n- Release: minor')"
expect "a Release line in a bullet counts" v1.3.0

repo words-after
commit cmd/main.go "$(printf 'Add validate\n\nRelease: minor (new flag)')"
expect "words after the value are not read" v1.3.0

repo release-patch
commit docs/readme.md "$(printf 'Docs\n\nRelease: patch')"
expect "Release: patch alone releases nothing extra" ""

repo unknown-value
commit cmd/main.go "$(printf 'Add validate\n\nRelease: feature')"
expect_fail "an unknown Release value fails" "Release line not understood: 'Release: feature'"
expect "a manual bump releases past an unknown Release value" v1.3.0 BUMP=minor

repo empty-value
commit cmd/main.go "$(printf 'Add validate\n\nRelease:')"
expect_fail "a Release line without a value fails" "Release line not understood"

repo manual
commit docs/readme.md "Docs"
expect "a manual bump releases also without a change" v1.3.0 BUMP=minor

repo manual-over-trailer
commit cmd/main.go "$(printf 'Breaking\n\nRelease: major')"
expect "a manual bump wins over a Release line" v1.2.2 BUMP=patch
expect "auto follows the Release lines" v2.0.0 BUMP=auto

repo bad-bump
expect_fail "an unknown bump fails" "Unknown bump 'huge'" BUMP=huge

repo highest
git -C "$dir" tag v1.10.0
git -C "$dir" tag v1.9.0-rc.1
commit cmd/main.go "Fix"
expect "counts on from the highest plain release" v1.10.1

repo prose
commit cmd/main.go "$(printf 'Rework\n\nRelease: major rework of the parser')"
expect_fail "prose after a Release value fails" "Release line not understood"

repo remark
commit cmd/main.go "$(printf 'Add validate\n\nRelease: minor (the validate command)')"
expect "a remark in parentheses after the value is allowed" v1.3.0

repo released
commit cmd/main.go "Fix"
git -C "$dir" tag v1.2.2
expect "a commit with a release tag is not released again" "" BUMP=minor

repo not-tip
commit cmd/main.go "Fix"
old=$(git -C "$dir" rev-parse HEAD)
commit cmd/main.go "Newer"
git -C "$dir" update-ref refs/remotes/origin/main HEAD
git -C "$dir" checkout -q --detach "$old"
expect "an old commit of main is not released" ""
check "an old commit says why" "$(grep -q 'not the tip of main' <<< "$err" && echo true || echo false)"
git -C "$dir" checkout -q main
expect "the tip of main is released" v1.2.2

repo same-commit
commit cmd/main.go "Fix"
KNOWN="1.2.2=$(git -C "$dir" rev-parse HEAD)" expect_fail "stops when the image to skip was built from HEAD" "built from this commit"

repo describe-glob
git -C "$dir" tag v1.2.1.4
commit cmd/main.go "Fix"
expect "a tag that is not vX.Y.Z is no base" v1.2.2

dir=$tmp/first
git init -q -b main "$dir"
git -C "$dir" -c commit.gpgsign=false -c user.email=t@e -c user.name=t commit -q --allow-empty -m first
expect "the first release" v0.1.0
KNOWN="0.1.0" expect "the first release skips an existing image too" v0.1.1

echo "# registries (stub curl)"

# A curl that answers from STUB_TOKEN, STUB_GHCR and STUB_HUB, each a list
# of tag:code pairs; tags not listed answer 404.
mkdir -p "$tmp/bin"
cat > "$tmp/bin/curl" <<'EOF'
#!/usr/bin/env bash
# STUB_REV lists tag:commit pairs: the revision annotation of a ghcr.io tag.
url=${!#} out=/dev/stdout
while [ $# -gt 1 ]; do [ "$1" = -o ] && out=$2; shift; done
lookup() { local tag=$1 list=$2 fallback=$3 pair; for pair in $list; do [ "${pair%%:*}" = "$tag" ] && { echo "${pair#*:}"; return; }; done; echo "$fallback"; }
default='{"token":"t"}'
case $url in
  https://ghcr.io/token*) printf '%s' "${STUB_TOKEN-$default}" ;;
  https://ghcr.io/v2/*)
    tag=${url##*/}
    printf '{"annotations":{"org.opencontainers.image.revision":"%s"}}' "$(lookup "$tag" "${STUB_REV:-}" "")" > "$out"
    printf '%s' "$(lookup "$tag" "${STUB_GHCR:-}" 404)" ;;
  https://hub.docker.com/*) printf '%s' "$(lookup "${url##*/}" "${STUB_HUB:-}" 404)" ;;
  *) exit 6 ;;
esac
EOF
chmod +x "$tmp/bin/curl"
stub="PATH=$tmp/bin:$PATH"
dir=$tmp/patch

run "$stub" STUB_GHCR="1.2.2:200" -- exists 1.2.2
check "exists: on ghcr.io" "$([ "$status" = 0 ] && echo true || echo false)"
run "$stub" STUB_HUB="1.2.2:200" -- exists 1.2.2
check "exists: on Docker Hub only" "$([ "$status" = 0 ] && echo true || echo false)"
run "$stub" -- exists 1.2.2
check "exists: on neither" "$([ "$status" = 1 ] && echo true || echo false)"
run "$stub" STUB_GHCR="1.2.2:429" -- exists 1.2.2
check "exists: a 429 from ghcr.io is no answer" "$([ "$status" = 2 ] && grep -q 'ghcr.io answered 429' <<< "$err" && echo true || echo false)"
run "$stub" STUB_HUB="1.2.2:503" -- exists 1.2.2
check "exists: a 503 from Docker Hub is no answer" "$([ "$status" = 2 ] && grep -q 'Docker Hub answered 503' <<< "$err" && echo true || echo false)"
run "$stub" STUB_TOKEN='{"errors":[]}' -- exists 1.2.2
check "exists: JSON without a token is no answer" "$([ "$status" = 2 ] && grep -q 'no token' <<< "$err" && echo true || echo false)"
run "$stub" STUB_TOKEN='<html>' -- exists 1.2.2
check "exists: a body that is no JSON is no answer" "$([ "$status" = 2 ] && grep -q 'no token' <<< "$err" && echo true || echo false)"
run "$stub" STUB_GHCR="1.2.2:200" STUB_HUB="1.2.3:200"
check "next: skips images on either registry" "$([ "$status" = 0 ] && [ "$out" = v1.2.4 ] && echo true || echo false)"
run "$stub" STUB_HUB="1.2.2:500"
check "next: fails when a registry gives no answer" "$([ "$status" = 2 ] && [ -z "$out" ] && echo true || echo false)"
run "$stub" STUB_GHCR="1.2.2:200" STUB_REV="1.2.2:$(git -C "$dir" rev-parse HEAD)"
check "next: stops when the ghcr.io image to skip was built from HEAD" "$([ "$status" = 2 ] && [ -z "$out" ] && grep -q 'built from this commit' <<< "$err" && echo true || echo false)"
run "$stub" STUB_GHCR="1.2.2:200" STUB_REV="1.2.2:0123456789abcdef0123456789abcdef01234567"
check "next: skips an image built from another commit" "$([ "$status" = 0 ] && [ "$out" = v1.2.3 ] && echo true || echo false)"

if [ "$failures" -gt 0 ]; then
  echo "$failures failed"
  exit 1
fi
