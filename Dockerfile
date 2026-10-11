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
 && mkdir /out/tmp /out/validators

FROM scratch

# Package uploads are spooled to /tmp instead of being held in memory.
COPY --from=build --chmod=1777 /out/tmp /tmp
COPY --from=build /out/qti-validator /qti-validator
# Empty by default: mount a directory with .xsd and .sch files here to add
# validators.
COPY --from=build /out/validators /validators

USER 65534:65534
# A soft memory limit for the Go runtime: the garbage collector works harder
# as the heap nears it, which keeps the peak under load at about 100 MiB
# instead of 110-130 MiB, at no measurable cost in speed. Override it with -e.
ENV GOMEMLIMIT=90MiB
EXPOSE 8080
# The binary checks /health itself: the image has no shell or curl. It reads
# ADDR like the service, so a changed port is followed.
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
    CMD ["/qti-validator", "-healthcheck"]
ENTRYPOINT ["/qti-validator"]
