#!/usr/bin/env bash
# Picks the next release tag for a build of main. CI calls it from the jobs
# `version` and `image`; see CONTRIBUTING.md, "Releases". Run it from the
# root of a full clone (fetch-depth 0, tags included).
#
#   next-version.sh            prints vX.Y.Z, or nothing when there is
#                              nothing to release
#   next-version.sh exists X.Y.Z
#                              exits 0 when the image X.Y.Z is on ghcr.io or
#                              Docker Hub, 1 when it is on neither
#
# Exit status 2 means the script could not decide: a registry gave no clear
# answer, or a bump or a Release line is not one it knows.
#
# The bump is, from high to low priority:
#   BUMP=patch|minor|major   chosen when the workflow is started by hand;
#                            empty (or auto) follows the commits
#   a line `Release: major` or `Release: minor` in the body of a commit
#   since the nearest release in main's history (the highest one counts;
#   `Release: patch` is allowed and changes nothing)
#   patch, when anything that goes into the image changed since that release
# Without any of these there is nothing to release.
#
# A version whose image already exists on ghcr.io or Docker Hub is skipped,
# on to the next patch number, with a line "Skipping vX.Y.Z: …" on stderr: a
# release that pushed its image but failed before it was tagged must not be
# picked again, as Docker Hub refuses to overwrite it and ghcr.io would
# overwrite what users already pulled.
#
# KNOWN_IMAGE_TAGS, a space-separated list such as "1.2.2 1.3.0", replaces
# the registry lookups; the tests use it.
set -euo pipefail

IMAGE=${IMAGE:-feyst/qti3-validator}
# Bounded and retried: a registry that hangs or answers 429 or 5xx for a
# while must neither stall the run nor fail it at the first try.
CURL=(curl -sS --connect-timeout 10 --max-time 30 --retry 4 --retry-connrefused)
ACCEPT='application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json'

# image_exists 1.2.3 returns 0 when either registry has the tag, 1 when
# neither has it, and ends the script with status 2 when a registry gives
# no clear answer: assuming "absent" then could overwrite a published
# release on ghcr.io.
image_exists() {
  local tag=$1 code token
  if [ -n "${KNOWN_IMAGE_TAGS+set}" ]; then
    case " $KNOWN_IMAGE_TAGS " in *" $tag "*) return 0 ;; *) return 1 ;; esac
  fi
  token=$("${CURL[@]}" "https://ghcr.io/token?scope=repository:$IMAGE:pull" | jq -r '.token // empty') || token=
  if [ -z "$token" ]; then
    echo "ghcr.io gave no token to read $IMAGE" >&2
    exit 2
  fi
  code=$("${CURL[@]}" -o /dev/null -w '%{http_code}' -I \
    -H "Authorization: Bearer $token" -H "Accept: $ACCEPT" \
    "https://ghcr.io/v2/$IMAGE/manifests/$tag") || code=000
  case $code in
    200) return 0 ;;
    404) ;;
    *) echo "ghcr.io answered $code for $IMAGE:$tag" >&2; exit 2 ;;
  esac
  code=$("${CURL[@]}" -o /dev/null -w '%{http_code}' \
    "https://hub.docker.com/v2/namespaces/${IMAGE%%/*}/repositories/${IMAGE#*/}/tags/$tag") || code=000
  case $code in
    200) return 0 ;;
    404) return 1 ;;
    *) echo "Docker Hub answered $code for $IMAGE:$tag" >&2; exit 2 ;;
  esac
}

if [ "${1:-}" = exists ]; then
  [ $# -eq 2 ] || { echo "usage: $0 exists X.Y.Z" >&2; exit 2; }
  image_exists "$2"
  exit
fi

bump=${BUMP:-}
case $bump in
  '' | auto) bump= ;;
  patch | minor | major) ;;
  *) echo "Unknown bump '$bump'; use auto, patch, minor or major" >&2; exit 2 ;;
esac

# Only plain vX.Y.Z tags count; a prerelease such as v1.1.0-rc.1 is neither a
# base to compare with nor one to count on from.
releases() { grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' || true; }

# The next number counts on from the highest release of all, so it never
# repeats one, also when two tags share a commit or a tag sits outside main.
last=$(git tag --list 'v*' | releases | sort -V | tail -n 1)
if [ -z "$last" ]; then
  last=v0.0.0 bump=minor
fi
# Whether anything changed, and which Release lines count, is asked of the
# nearest release in main's history.
base=$(git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' --exclude '*-*' 2>/dev/null || true)
range=${base:+$base..}HEAD

if [ -z "$bump" ]; then
  # Whole lines of the bodies, not only the trailer block git parses as
  # trailers: a squash merge lists the squashed commits one after another,
  # and GitHub may append Co-authored-by after a blank line. A bullet in
  # front is allowed, words after the value are not read.
  lines=$(git log --format=%b "$range" |
    grep -iE '^[[:space:]]*([-*][[:space:]]+)?release:' || true)
  values=$(sed -E 's/^[[:space:]]*([-*][[:space:]]+)?[Rr][Ee][Ll][Ee][Aa][Ss][Ee]:[[:space:]]*//; s/[^A-Za-z].*$//' <<< "$lines" |
    tr '[:upper:]' '[:lower:]')
  while read -r value; do
    case $value in
      '' ) [ -z "$lines" ] || { echo "A Release line has no value; use major, minor or patch:" >&2; echo "$lines" >&2; exit 2; } ;;
      major) bump=major ;;
      minor) [ "$bump" = major ] || bump=minor ;;
      patch) ;;
      *) echo "Unknown Release value '$value'; use major, minor or patch. Start the workflow by hand with a bump to release past it." >&2; exit 2 ;;
    esac
  done <<< "$values"
fi
if [ -z "$bump" ]; then
  # Tests, their data and their helpers are not in the image.
  if [ -n "$base" ] && git diff --quiet "$base" HEAD -- \
      go.mod go.sum Dockerfile cmd internal rules third_party \
      ':(exclude)*_test.go' ':(exclude)internal/testutil' \
      ':(exclude,glob)internal/**/testdata/**' ':(exclude,glob)cmd/**/testdata/**'; then
    echo "Nothing in the image changed since $base" >&2
    exit 0
  fi
  bump='patch'
fi

IFS=. read -r major minor patch <<< "${last#v}"
major=$((10#$major)) minor=$((10#$minor)) patch=$((10#$patch))
case $bump in
  major) major=$((major + 1)) minor=0 patch=0 ;;
  minor) minor=$((minor + 1)) patch=0 ;;
  patch) patch=$((patch + 1)) ;;
esac
while image_exists "$major.$minor.$patch"; do
  echo "Skipping v$major.$minor.$patch: its image already exists" >&2
  patch=$((patch + 1))
done
echo "v$major.$minor.$patch"
