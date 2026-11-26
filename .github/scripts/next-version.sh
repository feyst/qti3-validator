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
# Exit status 2 means the script could not decide, or must not: a registry
# gave no clear answer, a bump or a Release line is not one it knows, or the
# image of the version it would skip was built from this very commit.
#
# Nothing is released when HEAD is not the tip of origin/main (a re-run of an
# old run would release an older commit as the newest version), or when HEAD
# already carries a release tag.
#
# The bump is, from high to low priority:
#   BUMP=patch|minor|major   chosen when the workflow is started by hand;
#                            empty (or auto) follows the commits
#   a line `Release: major` or `Release: minor` in the body of a commit
#   since the nearest release in HEAD's history (the highest one counts;
#   `Release: patch` is allowed and changes nothing)
#   patch, when anything that goes into the image changed since that release
# Without any of these there is nothing to release.
#
# A version whose image already exists on ghcr.io or Docker Hub is skipped,
# on to the next patch number, with a line "Skipping vX.Y.Z: …" on stderr: a
# release that pushed its image but failed before it was tagged must not be
# picked again, as Docker Hub refuses to overwrite it and ghcr.io would
# overwrite what users already pulled. When that image was built from HEAD
# (its org.opencontainers.image.revision on ghcr.io), the release of HEAD
# failed after the push: skipping would push the same commit as yet another
# version, so the script stops instead.
#
# KNOWN_IMAGE_TAGS replaces the registry lookups for the tests: a
# space-separated list of tags, each optionally with the commit it was built
# from, such as "1.2.2 1.3.0=<sha>".
set -euo pipefail

IMAGE=${IMAGE:-feyst/qti3-validator}
# Bounded and retried: a registry that hangs or answers 429 or 5xx for a
# while must neither stall the run nor fail it at the first try.
CURL=(curl -sS --connect-timeout 10 --max-time 30 --retry 4 --retry-connrefused)
ACCEPT='application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json'
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

# image_exists 1.2.3 returns 0 when either registry has the tag, 1 when
# neither has it, and ends the script with status 2 when a registry gives
# no clear answer: assuming "absent" then could overwrite a published
# release on ghcr.io. It sets revision to the commit the ghcr.io image was
# built from, empty when unknown.
image_exists() {
  local tag=$1 code token entry
  revision=
  if [ -n "${KNOWN_IMAGE_TAGS+set}" ]; then
    for entry in $KNOWN_IMAGE_TAGS; do
      if [ "${entry%%=*}" = "$tag" ]; then
        [ "$entry" = "$tag" ] || revision=${entry#*=}
        return 0
      fi
    done
    return 1
  fi
  token=$("${CURL[@]}" "https://ghcr.io/token?scope=repository:$IMAGE:pull" | jq -r '.token // empty') || token=
  if [ -z "$token" ]; then
    echo "ghcr.io gave no token to read $IMAGE" >&2
    exit 2
  fi
  code=$("${CURL[@]}" -o "$TMP/manifest" -w '%{http_code}' \
    -H "Authorization: Bearer $token" -H "Accept: $ACCEPT" \
    "https://ghcr.io/v2/$IMAGE/manifests/$tag") || code=000
  case $code in
    200)
      revision=$(jq -r '.annotations["org.opencontainers.image.revision"] // empty' "$TMP/manifest" 2>/dev/null) || revision=
      return 0 ;;
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

head=$(git rev-parse HEAD)
if tip=$(git rev-parse -q --verify refs/remotes/origin/main); then
  if [ "$tip" != "$head" ]; then
    echo "HEAD $head is not the tip of main ($tip): start a new run to release" >&2
    exit 0
  fi
fi

# Only plain vX.Y.Z tags count; a prerelease such as v1.1.0-rc.1 is neither a
# base to compare with nor one to count on from.
releases() { grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' || true; }

released=$(git tag --points-at HEAD | releases | sort -V | tail -n 1)
if [ -n "$released" ]; then
  echo "HEAD is already released as $released" >&2
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
  # front is allowed; after the value only a remark in parentheses.
  bodies=$(git log --format=%b "$range")
  lines=$(grep -iE '^[[:space:]]*([-*][[:space:]]+)?release:' <<< "$bodies" || true)
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    value=$(sed -nE 's/^[[:space:]]*([-*][[:space:]]+)?[Rr][Ee][Ll][Ee][Aa][Ss][Ee]:[[:space:]]*([A-Za-z]+)[[:space:]]*(\(.*\))?[[:space:]]*$/\2/p' <<< "$line" |
      tr '[:upper:]' '[:lower:]')
    case $value in
      major) bump=major ;;
      minor) [ "$bump" = major ] || bump=minor ;;
      patch) ;;
      *)
        echo "Release line not understood: '$line'. Write 'Release: major', 'Release: minor' or 'Release: patch' on a line of its own, or start the workflow by hand with a bump to release past it." >&2
        exit 2 ;;
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
while image_exists "$major.$minor.$patch"; do
  if [ "$revision" = "$head" ]; then
    echo "The image $major.$minor.$patch was built from this commit, so its release failed after the push. Finish it instead of releasing the commit again: Re-run failed jobs of that run, or tag v$major.$minor.$patch by hand (CONTRIBUTING.md, Releases)." >&2
    exit 2
  fi
  echo "Skipping v$major.$minor.$patch: its image already exists" >&2
  patch=$((patch + 1))
done
echo "v$major.$minor.$patch"
