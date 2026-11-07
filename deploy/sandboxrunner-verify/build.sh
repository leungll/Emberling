#!/bin/sh
# Builds the verification image, pushes it to the given repository and prints the
# digest-pinned reference the sandbox runner takes as SANDBOXRUNNER_VERIFY_IMAGE.
#
#   sh deploy/sandboxrunner-verify/build.sh <image repository> [tag]
#
# for example `sh deploy/sandboxrunner-verify/build.sh localhost:5000/emberling-sandbox-verify`
# against a local registry started with `docker run -d -p 5000:5000 registry:2`. A pushed
# image is required because only a registry assigns the manifest digest a `name@sha256:...`
# reference resolves to; the runner refuses a tag, so a moved tag can never change what a
# verification runs in. Run it from the repository root: the build context is the whole
# repository, so the Dockerfile can copy the vulnerability snapshot and the example
# repository's module files.
set -eu

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  echo "usage: $0 <image repository> [tag]" >&2
  exit 2
fi
repository=$1
tag=${2:-$(git rev-parse --short HEAD 2>/dev/null || echo local)}
reference="$repository:$tag"
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)

docker build --file "$here/Dockerfile" --tag "$reference" "$root"
docker push "$reference"
digest=$(docker image inspect --format '{{index .RepoDigests 0}}' "$reference")
case "$digest" in
  *@sha256:*) ;;
  *) echo "build.sh: no repository digest recorded for $reference after the push" >&2; exit 1 ;;
esac
echo "SANDBOXRUNNER_VERIFY_IMAGE=$digest"
