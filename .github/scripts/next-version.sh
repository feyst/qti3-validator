#!/usr/bin/env bash
# Prints the next release tag for a build of main, vX.Y.Z, or nothing when
# there is nothing to release. Run from the root of a full clone (fetch-depth
# 0, tags included). CI calls it from the job `version`; see CONTRIBUTING.md,
# "Releases".
#
# The bump is, from high to low priority:
#   BUMP=patch|minor|major   set when the workflow is started by hand
#   a trailer `Release: major` or `Release: minor` in a commit since the
#   nearest release in main's history (the highest one counts)
#   patch, when anything that goes into the image changed since that release
# Without any of these there is nothing to release.
#
# A version whose image already exists on ghcr.io or Docker Hub is skipped,
# on to the next patch number: a release that pushed its image but failed
# before it was tagged must not be picked again, as Docker Hub refuses to
# overwrite it and ghcr.io would overwrite what users already pulled.
#
# KNOWN_IMAGE_TAGS, a space-separated list such as "1.2.2 1.3.0", replaces
# the registry lookups; the tests use it.
set -euo pipefail

IMAGE=${IMAGE:-feyst/qti3-validator}

# Only plain vX.Y.Z tags count; a prerelease such as v1.1.0-rc.1 is neither a
# base to compare with nor one to count on from.
releases() { grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' || true; }

# image_exists 1.2.3 exits 0 when either registry has the tag, 1 when neither
# has it, and fails the script when a registry gives no clear answer: assuming
# "absent" then could overwrite a published release on ghcr.io.
image_exists() {
  local tag=$1 code token
  if [ -n "${KNOWN_IMAGE_TAGS+set}" ]; then
    case " $KNOWN_IMAGE_TAGS " in *" $tag "*) return 0 ;; *) return 1 ;; esac
  fi
  token=$(curl -fsS "https://ghcr.io/token?scope=repository:$IMAGE:pull" | jq -r .token)
  code=$(curl -sS -o /dev/null -w '%{http_code}' -I \
    -H "Authorization: Bearer $token" \
    -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json' \
    "https://ghcr.io/v2/$IMAGE/manifests/$tag")
  case $code in
    200) return 0 ;;
    404) ;;
    *) echo "ghcr.io answered $code for $IMAGE:$tag" >&2; exit 1 ;;
  esac
  code=$(curl -sS -o /dev/null -w '%{http_code}' \
    "https://hub.docker.com/v2/namespaces/${IMAGE%%/*}/repositories/${IMAGE#*/}/tags/$tag")
  case $code in
    200) return 0 ;;
    404) return 1 ;;
    *) echo "Docker Hub answered $code for $IMAGE:$tag" >&2; exit 1 ;;
  esac
}

# The next number counts on from the highest release of all, so it never
# repeats one, also when two tags share a commit or a tag sits outside main.
last=$(git tag --list 'v*' | releases | sort -V | tail -n 1)
if [ -z "$last" ]; then
  echo v0.1.0
  exit 0
fi
# Whether anything changed, and which trailers count, is asked of the nearest
# release in main's history.
base=$(git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' --exclude '*-*' 2>/dev/null || true)
range=${base:+$base..}HEAD

bump=${BUMP:-}
if [ -z "$bump" ]; then
  trailers=$(git log --format='%(trailers:key=Release,valueonly,separator=%x0A)' "$range" | tr '[:upper:]' '[:lower:]')
  if grep -qx '[[:space:]]*major[[:space:]]*' <<< "$trailers"; then
    bump=major
  elif grep -qx '[[:space:]]*minor[[:space:]]*' <<< "$trailers"; then
    bump=minor
  fi
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
  *) echo "Unknown bump '$bump'; use patch, minor or major" >&2; exit 1 ;;
esac
while image_exists "$major.$minor.$patch"; do
  echo "Skipping v$major.$minor.$patch: its image already exists" >&2
  patch=$((patch + 1))
done
echo "v$major.$minor.$patch"
