#!/usr/bin/env bash
# Picks the next release for a build of main, and tells the image job what
# the registries already have. CI calls it from the jobs `version` and
# `image`; see CONTRIBUTING.md, "Releases". Run it from the root of a full
# clone (fetch-depth 0, tags included).
#
#   next-version.sh            prints vX.Y.Z, or nothing when there is
#                              nothing to release
#   next-version.sh state X.Y.Z
#                              prints, as key=value lines for $GITHUB_OUTPUT,
#                              ghcr=true|false and dockerhub=true|false
#                              (whether each registry has the image X.Y.Z),
#                              revision= (the commit it was built from) and
#                              digest= (its digest on ghcr.io, or Docker Hub)
#
# Exit status 2 means the script could not decide: a registry gave no clear
# answer, or the tip of main could not be read.
#
# Nothing is released when HEAD is not the tip of main on the remote (a
# re-run of an old run would release an older commit as the newest
# version), or when HEAD already carries a release tag that has a GitHub
# release. A release tag on HEAD without a GitHub release is printed again,
# so that the run finishes it.
#
# The bump is, from high to low priority:
#   BUMP=patch|minor|major   chosen when the workflow is started by hand;
#                            empty (or auto) follows the commits
#   a line `Release: major` or `Release: minor` in the body of a commit
#   since the highest release in HEAD's history (the highest one counts;
#   `Release: patch` is allowed and changes nothing)
#   patch, when anything that goes into the image changed since that release
# Without any of these there is nothing to release. A `Release:` line with
# another value is ignored with a warning.
#
# A version whose image already exists on ghcr.io or Docker Hub is skipped,
# on to the next patch number, with a line "Skipping vX.Y.Z: …" on stderr: a
# release that pushed its image but failed before it was tagged must not be
# picked again, as Docker Hub refuses to overwrite it and ghcr.io would
# overwrite what users already pulled. When that image was built from HEAD
# (its org.opencontainers.image.revision), it is HEAD's own release that
# failed after the push: the script picks that version again, and the image
# job completes it without building.
#
# For the tests: KNOWN_IMAGE_TAGS replaces the registry lookups (a
# space-separated list of tags, each optionally with the commit it was built
# from: "1.2.2 1.3.0=<sha>"), KNOWN_RELEASES the GitHub releases ("v1.2.2"),
# and MAIN_TIP the tip of main on the remote.
set -euo pipefail

IMAGE=${IMAGE:-feyst/qti3-validator}
# Bounded and retried: a registry that hangs or answers 429 or 5xx for a
# while must neither stall the run nor fail it at the first try.
CURL=(curl -sS --connect-timeout 10 --max-time 30 --retry 4 --retry-connrefused)
ACCEPT='application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json'
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

fail() { echo "$*" >&2; exit 2; }

# registry_lookup NAME TOKEN_URL MANIFEST_URL sets found=true|false and, when
# found, revision and digest; it fails the script when the registry gives no
# clear answer, as assuming "absent" could overwrite a published release.
registry_lookup() {
  local name=$1 token_url=$2 url=$3 token code
  token=$("${CURL[@]}" "$token_url" | jq -r '.token // empty' 2>/dev/null) || token=
  [ -n "$token" ] || fail "$name gave no token to read $IMAGE"
  # HEAD first: on Docker Hub only a GET counts against the pull limit.
  code=$("${CURL[@]}" -o /dev/null -w '%{http_code}' -I \
    -H "Authorization: Bearer $token" -H "Accept: $ACCEPT" "$url") || code=000
  case $code in
    404) found=false; return ;;
    200) ;;
    *) fail "$name answered $code for $url" ;;
  esac
  code=$("${CURL[@]}" -o "$TMP/manifest" -D "$TMP/headers" -w '%{http_code}' \
    -H "Authorization: Bearer $token" -H "Accept: $ACCEPT" "$url") || code=000
  [ "$code" = 200 ] || fail "$name answered $code for $url"
  found=true
  revision=$(jq -r '.annotations["org.opencontainers.image.revision"] // empty' "$TMP/manifest" 2>/dev/null) || revision=
  digest=$(tr -d '\r' < "$TMP/headers" | sed -n 's/^[Dd]ocker-[Cc]ontent-[Dd]igest: *//p' | tail -n 1)
  # What the registry says goes to $GITHUB_OUTPUT and into a shell: only a
  # commit hash and a digest are taken.
  [[ $revision =~ ^[0-9a-f]{40}$ ]] || revision=''
  [[ $digest =~ ^sha256:[0-9a-f]{64}$ ]] || fail "$name gave no digest for $url"
}

# lookup 1.2.3 sets ghcr and dockerhub to true or false, and revision and
# digest from the first registry that has the image.
lookup() {
  local tag=$1 entry
  ghcr=false dockerhub=false revision='' digest=''
  if [ -n "${KNOWN_IMAGE_TAGS+set}" ]; then
    for entry in $KNOWN_IMAGE_TAGS; do
      if [ "${entry%%=*}" = "$tag" ]; then
        ghcr=true
        [ "$entry" = "$tag" ] || revision=${entry#*=}
        digest=sha256:known
      fi
    done
    return
  fi
  local r d
  registry_lookup ghcr.io "https://ghcr.io/token?scope=repository:$IMAGE:pull" \
    "https://ghcr.io/v2/$IMAGE/manifests/$tag"
  ghcr=$found r=$revision d=$digest
  registry_lookup "Docker Hub" "https://auth.docker.io/token?service=registry.docker.io&scope=repository:$IMAGE:pull" \
    "https://registry-1.docker.io/v2/$IMAGE/manifests/$tag"
  dockerhub=$found
  if [ "$ghcr" = true ]; then
    # Two different images under one version are of no single commit.
    if [ "$dockerhub" = true ] && [ "$digest" != "$d" ]; then
      echo "ghcr.io and Docker Hub hold different images as $tag" >&2
      r=''
    fi
    revision=$r digest=$d
  fi
}

if [ "${1:-}" = state ]; then
  [ $# -eq 2 ] || fail "usage: $0 state X.Y.Z"
  lookup "$2"
  printf 'ghcr=%s\ndockerhub=%s\nrevision=%s\ndigest=%s\n' "$ghcr" "$dockerhub" "$revision" "$digest"
  exit 0
fi

bump=${BUMP:-}
case $bump in
  '' | auto) bump= ;;
  patch | minor | major) ;;
  *) fail "Unknown bump '$bump'; use auto, patch, minor or major" ;;
esac

# The remote, not refs/remotes/origin/main: actions/checkout points that at
# the commit of the run, also when main has moved on since.
head=$(git rev-parse HEAD)
tip=${MAIN_TIP-$(git ls-remote origin refs/heads/main | cut -f1)}
[ -n "$tip" ] || fail "Could not read the tip of main from origin"
if [ "$tip" != "$head" ]; then
  echo "HEAD $head is not the tip of main ($tip): start a new run to release" >&2
  exit 0
fi

# Only plain vX.Y.Z tags count; a prerelease such as v1.1.0-rc.1 is neither a
# base to compare with nor one to count on from.
releases() { grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' || true; }

has_release() {
  if [ -n "${KNOWN_RELEASES+set}" ]; then
    case " $KNOWN_RELEASES " in *" $1 "*) return 0 ;; *) return 1 ;; esac
  fi
  if gh api "repos/${GITHUB_REPOSITORY:-feyst/qti3-validator}/releases/tags/$1" > /dev/null 2> "$TMP/release.err"; then
    return 0
  fi
  grep -q 'HTTP 404' "$TMP/release.err" || fail "Could not ask GitHub for the release $1: $(cat "$TMP/release.err")"
  return 1
}

released=$(git tag --points-at HEAD | releases | sort -V | tail -n 1)
if [ -n "$released" ]; then
  if has_release "$released"; then
    echo "HEAD is already released as $released" >&2
  else
    echo "HEAD is tagged $released without a GitHub release: finishing it" >&2
    echo "$released"
  fi
  exit 0
fi

# The next number counts on from the highest release of all, so it never
# repeats one, also when two tags share a commit or a tag sits outside main.
last=$(git tag --list 'v*' | releases | sort -V | tail -n 1)
if [ -z "$last" ]; then
  last=v0.0.0 bump=minor
fi
# Whether anything changed, and which Release lines count, is asked of the
# highest release in HEAD's history.
base=$(git tag --merged HEAD --list 'v*' | releases | sort -V | tail -n 1)
range=${base:+$base..}HEAD

if [ -z "$bump" ]; then
  # Whole lines of the bodies, not only the trailer block git parses as
  # trailers: a squash merge lists the squashed commits one after another,
  # and GitHub may append Co-authored-by after a blank line. A bullet in
  # front is allowed, and up to three spaces; a line indented further is a
  # code block, quoting rather than asking.
  bodies=$(git log --format=%b "$range")
  lines=$(grep -iE '^ {0,3}([-*] +)?release:' <<< "$bodies" || true)
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    value=$(sed -nE 's/^ {0,3}([-*] +)?[Rr][Ee][Ll][Ee][Aa][Ss][Ee]:[[:space:]]*([A-Za-z]+)[[:space:]]*(\(.*\))?[[:space:]]*$/\2/p' <<< "$line" |
      tr '[:upper:]' '[:lower:]')
    case $value in
      major) bump=major ;;
      minor) [ "$bump" = major ] || bump=minor ;;
      patch) ;;
      *) echo "warning: ignored the line '$line'; a bump is 'Release: major', 'Release: minor' or 'Release: patch' on a line of its own" >&2 ;;
    esac
  done <<< "$lines"
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
while lookup "$major.$minor.$patch"; [ "$ghcr" = true ] || [ "$dockerhub" = true ]; do
  if [ "$revision" = "$head" ]; then
    echo "The image $major.$minor.$patch was built from HEAD: finishing its release" >&2
    break
  fi
  echo "Skipping v$major.$minor.$patch: its image already exists" >&2
  patch=$((patch + 1))
done
echo "v$major.$minor.$patch"
