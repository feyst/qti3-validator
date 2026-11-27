#!/usr/bin/env bash
# Tests next-version.sh against throwaway repositories, and its registry
# lookups against a stub curl. Run with `make release-test`; CI runs it in
# the job `version` of pull requests. Needs bash, git and jq.
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
# a clean environment plus ENV, where main's tip is HEAD unless ENV sets
# MAIN_TIP and every tag has its GitHub release unless ENV sets
# KNOWN_RELEASES; sets out, err and status.
run() {
  local envs=() args=()
  envs+=("MAIN_TIP=$(git -C "$dir" rev-parse HEAD)" "KNOWN_RELEASES=$(git -C "$dir" tag | xargs)")
  while [ $# -gt 0 ] && [ "$1" != -- ]; do envs+=("$1"); shift; done
  [ $# -gt 0 ] && shift
  args=("$@")
  status=0
  out=$(cd "$dir" && env -u BUMP -u IMAGE -u KNOWN_IMAGE_TAGS "${envs[@]}" "$script" ${args[@]+"${args[@]}"} 2> "$tmp/err") || status=$?
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
is() { if "$@"; then echo true; else echo false; fi; }

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

repo same-commit
commit cmd/main.go "Fix"
KNOWN="1.2.2=$(git -C "$dir" rev-parse HEAD)" expect "an image of HEAD is picked again to finish its release" v1.2.2
check "finishing is named on stderr" "$(grep -q 'built from HEAD' <<< "$err" && echo true || echo false)"

repo other-commit
commit cmd/main.go "Fix"
KNOWN="1.2.2=0123456789abcdef0123456789abcdef01234567" expect "an image of another commit is skipped" v1.2.3

repo minor
commit cmd/main.go "$(printf 'Add a flag\n\nRelease: minor')"
commit cmd/main.go "Fix"
expect "a minor Release line since the release" v1.3.0

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

repo crlf
commit cmd/main.go "$(printf 'Add validate\r\n\r\nRelease: minor\r\n')"
expect "a Release line with CRLF line ends counts" v1.3.0

repo bullet
commit cmd/main.go "$(printf 'Add validate\n\n- Release: minor')"
expect "a Release line in a bullet counts" v1.3.0

repo remark
commit cmd/main.go "$(printf 'Add validate\n\nRelease: minor (the validate command)')"
expect "a remark in parentheses after the value is allowed" v1.3.0

repo code-block
commit cmd/main.go "$(printf 'Docs of the bump\n\nFor example:\n\n    Release: major\n')"
expect "an indented example is not a Release line" v1.2.2

repo release-patch
commit docs/readme.md "$(printf 'Docs\n\nRelease: patch')"
expect "Release: patch alone releases nothing extra" ""

n=0
for case in "feature" "major rework of the parser" "4.8.0" "minor." ""; do
  n=$((n + 1))
  repo "unknown-$n"
  commit cmd/main.go "$(printf 'Change\n\nRelease: %s' "$case")"
  expect "an unknown Release value ('$case') is ignored" v1.2.2
  check "  with a warning" "$(grep -q '^warning: ignored the line' <<< "$err" && echo true || echo false)"
done

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

repo describe-glob
git -C "$dir" tag v1.2.1.4
commit cmd/main.go "Fix"
expect "a tag that is not vX.Y.Z is no base" v1.2.2

repo released
commit cmd/main.go "Fix"
git -C "$dir" tag v1.2.2
expect "a commit with a release is not released again" "" BUMP=minor
expect "a commit tagged without a release is picked again to finish it" v1.2.2 KNOWN_RELEASES=v1.2.1

repo not-tip
commit cmd/main.go "Fix"
expect "an old commit of main is not released" "" MAIN_TIP=0123456789abcdef0123456789abcdef01234567
check "  and says why" "$(grep -q 'not the tip of main' <<< "$err" && echo true || echo false)"
expect_fail "an unknown tip of main fails" "tip of main" MAIN_TIP=

dir=$tmp/first
git init -q -b main "$dir"
git -C "$dir" -c commit.gpgsign=false -c user.email=t@e -c user.name=t commit -q --allow-empty -m first
expect "the first release" v0.1.0
KNOWN="0.1.0" expect "the first release skips an existing image too" v0.1.1

echo "# registries (stub curl)"

# A curl that answers from STUB_GHCR and STUB_HUB, lists of tag:code (a tag
# not listed answers 404), STUB_GHCR_REV and STUB_HUB_REV, lists of
# tag:commit, STUB_GHCR_DIGEST and STUB_HUB_DIGEST, the digests' hex, and
# STUB_GHCR_TOKEN and STUB_HUB_TOKEN, the token bodies.
mkdir -p "$tmp/bin"
cat > "$tmp/bin/curl" <<'EOF'
#!/usr/bin/env bash
url=${!#} out=/dev/stdout headers=/dev/null head=false
while [ $# -gt 1 ]; do
  case $1 in -o) out=$2 ;; -D) headers=$2 ;; -I) head=true ;; esac
  shift
done
lookup() { local key=$1 list=$2 fallback=$3 pair; for pair in $list; do [ "${pair%%:*}" = "$key" ] && { echo "${pair#*:}"; return; }; done; echo "$fallback"; }
token='{"token":"t"}'
manifest() {
  local tag=${url##*/} code rev
  code=$(lookup "$tag" "$1" 404) rev=$(lookup "$tag" "$2" "")
  if [ "$head" = false ]; then
    printf '{"annotations":{"org.opencontainers.image.revision":"%s"}}' "$rev" > "$out"
    printf 'HTTP/2 %s\r\ndocker-content-digest: sha256:%s\r\n\r\n' "$code" "$3" > "$headers"
  fi
  printf '%s' "$code"
}
case $url in
  https://ghcr.io/token*) printf '%s' "${STUB_GHCR_TOKEN-$token}" ;;
  https://auth.docker.io/token*) printf '%s' "${STUB_HUB_TOKEN-$token}" ;;
  https://ghcr.io/v2/*) manifest "${STUB_GHCR:-}" "${STUB_GHCR_REV:-}" "${STUB_GHCR_DIGEST:-1111111111111111111111111111111111111111111111111111111111111111}" ;;
  https://registry-1.docker.io/v2/*) manifest "${STUB_HUB:-}" "${STUB_HUB_REV:-}" "${STUB_HUB_DIGEST:-2222222222222222222222222222222222222222222222222222222222222222}" ;;
  *) exit 6 ;;
esac
EOF
chmod +x "$tmp/bin/curl"
stub="PATH=$tmp/bin:$PATH"
dir=$tmp/patch
headsha=$(git -C "$dir" rev-parse HEAD)

run "$stub" STUB_GHCR="1.2.2:200" STUB_GHCR_REV="1.2.2:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" -- state 1.2.2
check "state: on ghcr.io" "$(is [ "$out" = "$(printf 'ghcr=true\ndockerhub=false\nrevision=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\ndigest=sha256:1111111111111111111111111111111111111111111111111111111111111111')" ])"
run "$stub" STUB_HUB="1.2.2:200" STUB_HUB_REV="1.2.2:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" -- state 1.2.2
check "state: on Docker Hub only, with its revision and digest" "$(is [ "$out" = "$(printf 'ghcr=false\ndockerhub=true\nrevision=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\ndigest=sha256:2222222222222222222222222222222222222222222222222222222222222222')" ])"
run "$stub" STUB_GHCR="1.2.2:200" STUB_HUB="1.2.2:200" STUB_GHCR_REV="1.2.2:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" STUB_HUB_REV="1.2.2:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" STUB_HUB_DIGEST=1111111111111111111111111111111111111111111111111111111111111111 -- state 1.2.2
check "state: the same image on both" "$(is [ "$out" = "$(printf 'ghcr=true\ndockerhub=true\nrevision=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\ndigest=sha256:1111111111111111111111111111111111111111111111111111111111111111')" ])"
run "$stub" STUB_GHCR="1.2.2:200" STUB_HUB="1.2.2:200" STUB_GHCR_REV="1.2.2:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" STUB_HUB_REV="1.2.2:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" -- state 1.2.2
check "state: different images on the two have no revision" "$(is [ "$out" = "$(printf 'ghcr=true\ndockerhub=true\nrevision=\ndigest=sha256:1111111111111111111111111111111111111111111111111111111111111111')" ])"
run "$stub" STUB_GHCR="1.2.2:200" STUB_GHCR_REV="1.2.2:not-a-commit" -- state 1.2.2
check "state: a revision that is no commit hash is dropped" "$(is [ "$out" = "$(printf 'ghcr=true\ndockerhub=false\nrevision=\ndigest=sha256:1111111111111111111111111111111111111111111111111111111111111111')" ])"
run "$stub" STUB_GHCR="1.2.2:200" STUB_GHCR_DIGEST="x; rm -rf /" -- state 1.2.2
check "state: a digest that is no digest fails" "$([ "$status" = 2 ] && grep -q 'gave no digest' <<< "$err" && echo true || echo false)"
run "$stub" -- state 1.2.2
check "state: on neither" "$(is [ "$out" = "$(printf 'ghcr=false\ndockerhub=false\nrevision=\ndigest=')" ])"
run "$stub" STUB_GHCR="1.2.2:429" -- state 1.2.2
check "state: a 429 from ghcr.io is no answer" "$([ "$status" = 2 ] && grep -q 'ghcr.io answered 429' <<< "$err" && echo true || echo false)"
run "$stub" STUB_HUB="1.2.2:503" -- state 1.2.2
check "state: a 503 from Docker Hub is no answer" "$([ "$status" = 2 ] && grep -q 'Docker Hub answered 503' <<< "$err" && echo true || echo false)"
run "$stub" STUB_GHCR_TOKEN='{"errors":[]}' -- state 1.2.2
check "state: JSON without a token is no answer" "$([ "$status" = 2 ] && grep -q 'ghcr.io gave no token' <<< "$err" && echo true || echo false)"
run "$stub" STUB_HUB_TOKEN='<html>' -- state 1.2.2
check "state: a token body that is no JSON is no answer" "$([ "$status" = 2 ] && grep -q 'Docker Hub gave no token' <<< "$err" && echo true || echo false)"
run "$stub" STUB_GHCR="1.2.2:200" STUB_HUB="1.2.3:200"
check "next: skips images on either registry" "$([ "$status" = 0 ] && [ "$out" = v1.2.4 ] && echo true || echo false)"
run "$stub" STUB_HUB="1.2.2:500"
check "next: fails when a registry gives no answer" "$([ "$status" = 2 ] && [ -z "$out" ] && echo true || echo false)"
run "$stub" STUB_GHCR="1.2.2:200" STUB_GHCR_REV="1.2.2:$headsha"
check "next: picks an image of HEAD on ghcr.io again" "$([ "$status" = 0 ] && [ "$out" = v1.2.2 ] && echo true || echo false)"
run "$stub" STUB_GHCR="1.2.2:200" STUB_HUB="1.2.2:200" STUB_GHCR_REV="1.2.2:$headsha" STUB_HUB_REV="1.2.2:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
check "next: skips a version whose registries hold different images" "$([ "$status" = 0 ] && [ "$out" = v1.2.3 ] && echo true || echo false)"
run "$stub" STUB_HUB="1.2.2:200" STUB_HUB_REV="1.2.2:$headsha"
check "next: picks an image of HEAD on Docker Hub only again" "$([ "$status" = 0 ] && [ "$out" = v1.2.2 ] && echo true || echo false)"

if [ "$failures" -gt 0 ]; then
  echo "$failures failed"
  exit 1
fi
