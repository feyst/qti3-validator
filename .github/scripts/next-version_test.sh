#!/usr/bin/env bash
# Tests next-version.sh against throwaway repositories. Run with
# `make release-test`; CI runs it in the job `version`.
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

# expect NAME WANT [ENV...]: runs the script in the current repository with
# the given environment and compares what it prints.
expect() {
  local name=$1 want=$2 got
  shift 2
  got=$(cd "$dir" && env KNOWN_IMAGE_TAGS="${KNOWN:-}" "$@" "$script" 2>/dev/null) || got="exit $?"
  if [ "$got" = "$want" ]; then
    echo "ok    $name"
  else
    echo "FAIL  $name: got '$got', want '$want'"
    failures=$((failures + 1))
  fi
}

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

repo minor
commit cmd/main.go "$(printf 'Add a flag\n\nRelease: minor')"
commit cmd/main.go "Fix"
expect "a minor trailer since the release" v1.3.0

repo major
commit cmd/main.go "$(printf 'Feature\n\nRelease: minor')"
commit docs/readme.md "$(printf 'Breaking\n\nRelease: Major')"
expect "the highest trailer wins, case does not matter" v2.0.0

repo major-skip
commit cmd/main.go "$(printf 'Breaking\n\nRelease: major')"
KNOWN="2.0.0" expect "a skipped major goes on to its patch" v2.0.1

repo trailer-docs
commit docs/readme.md "$(printf 'Docs\n\nRelease: minor')"
expect "a trailer releases also without an image change" v1.3.0

repo old-trailer
commit cmd/main.go "$(printf 'Feature\n\nRelease: minor')"
git -C "$dir" tag v1.3.0
commit cmd/main.go "Fix"
expect "a trailer before the last release does not count" v1.3.1

repo not-a-trailer
commit cmd/main.go "Mention Release: major in the subject"
expect "only a trailer counts, not the subject" v1.2.2

repo manual
commit docs/readme.md "Docs"
expect "a manual bump wins over everything" v1.3.0 BUMP=minor

repo bad-bump
expect "an unknown bump fails" "exit 1" BUMP=huge

repo highest
git -C "$dir" tag v1.10.0
git -C "$dir" tag v1.9.0-rc.1
commit cmd/main.go "Fix"
expect "counts on from the highest plain release" v1.10.1

dir=$tmp/first
git init -q -b main "$dir"
git -C "$dir" -c commit.gpgsign=false -c user.email=t@e -c user.name=t commit -q --allow-empty -m first
expect "the first release" v0.1.0

if [ "$failures" -gt 0 ]; then
  echo "$failures failed"
  exit 1
fi
