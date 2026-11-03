# syntax=docker/dockerfile:1
#
# The verification image the sandbox runner's docker verifier runs patch verifications in.
# It holds everything a verification may read and nothing it may change: the Go toolchain,
# govulncheck, the vulnerability database snapshot the runner also embeds, and an offline
# module cache with exactly the two module versions the example repository's mirror serves.
# The runner starts it with no network, a read-only root, an unprivileged user and memory
# and process limits, with GOPROXY=off and -mod=readonly, so a patch can only build against
# what is baked in here. Build it with build.sh from the repository root; the runner takes
# the pushed image by digest only.
#
# The base image is the one the docker test backend runs (its digest is also pinned in
# the runner source); keep the two in step when moving to a newer Go.
ARG GO_IMAGE=golang:1.27-alpine@sha256:738d1cf061836894ff6bb8c33881080ac66de8cf0586615012a0c8f592649cfa
FROM ${GO_IMAGE}

# govulncheck is pinned by module version; the go command verifies its checksum against the
# checksum database while building.
ARG GOVULNCHECK_VERSION=v1.1.4
RUN CGO_ENABLED=0 GOFLAGS=-mod=mod GOTOOLCHAIN=local GOBIN=/usr/local/bin \
    go install golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}

# The snapshot is the same directory the runner embeds, so the dependency comparison and
# govulncheck judge against one database. Its digest is printed for the build record.
COPY backend/internal/fixture/sandboxrunner/testdata/vulndb /vulndb
RUN find /vulndb -type f | sort | xargs sha256sum

# The offline module cache: the example repository's go.sum already pins both versions, and
# the download verifies each against it. Nothing else is ever added to this cache.
COPY backend/internal/fixture/vulnrepo/repo/go.mod backend/internal/fixture/vulnrepo/repo/go.sum /tmp/mirror/
RUN cd /tmp/mirror \
    && GOMODCACHE=/modcache GOFLAGS=-mod=mod GOTOOLCHAIN=local \
       go mod download github.com/golang-jwt/jwt@v3.2.2+incompatible github.com/golang-jwt/jwt/v5@v5.3.1 \
    && rm -rf /tmp/mirror /root/.cache \
    && chmod -R a+rX /modcache /vulndb \
    && find /modcache/cache/download -name '*.zip' | sort | xargs sha256sum

ENV GOPROXY=off GOFLAGS=-mod=readonly GOSUMDB=off GOTOOLCHAIN=local GOTELEMETRY=off CGO_ENABLED=0 \
    GOMODCACHE=/modcache GOVULNDB=file:///vulndb
USER 65534:65534
WORKDIR /work
