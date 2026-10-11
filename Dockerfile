# syntax=docker/dockerfile:1

# The build stage runs on the builder's own platform and cross-compiles the
# binary for the target, so an arm64 image builds as fast as an amd64 one,
# without emulation. The Go version is written out, not an ARG, so Dependabot
# can update it; CI reads it from here too.
FROM --platform=$BUILDPLATFORM golang:1.27.2-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
COPY third_party ./third_party
RUN go mod download

COPY . .

# Download the QTI schemas pinned in internal/adapter/schemastore/schemas/schemas.lock,
# and compile the Schematron rules embedded in them together with the
# validator's own rules in rules/qti3-additional-checks.sch.
# The QTI version is fixed by the URLs in that file (spec/qti/v3p0, binding
# v1p0) and every file is checked against its SHA-256, so a changed upstream
# file fails the build instead of silently changing validation.
RUN go run ./cmd/fetchschemas

# Compile the schemas once at build time, so a schema the validator cannot
# compile fails the build rather than the container start. This runs a binary
# for the builder's platform: the target's binary may not run here.
RUN CGO_ENABLED=0 go run ./cmd/qti-validator -check

ARG VERSION=0.1.0
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -trimpath \
    -ldflags="-s -w -X qti3-validator/internal/adapter/httpapi.Version=${VERSION}" \
    -o /out/qti-validator \
    ./cmd/qti-validator \
 && mkdir -p /out/root/validators \
 && mkdir -m 1777 /out/root/tmp

FROM scratch

# The directories of the image, copied as entries of one tree so that each
# keeps its mode: COPY --chmod=1777 drops the sticky bit, and copying a
# directory onto /tmp gives it mode 0755.
#   /tmp          mode 1777; package uploads are spooled here instead of
#                 being held in memory.
#   /validators   empty by default: mount a directory with .xsd and .sch
#                 files here to add validators.
COPY --from=build /out/root/ /
COPY --from=build /out/qti-validator /qti-validator

USER 65534:65534
# No GOMEMLIMIT here: the service derives the soft memory limit of the Go
# runtime at startup from the container's memory limit (the limit less
# 40 MiB), so the garbage collector works harder only as the process nears
# what the container has. Set GOMEMLIMIT with -e to choose it yourself.
EXPOSE 8080
# The binary checks /health itself: the image has no shell or curl. It reads
# ADDR like the service, so a changed port is followed.
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
    CMD ["/qti-validator", "-healthcheck"]
ENTRYPOINT ["/qti-validator"]
