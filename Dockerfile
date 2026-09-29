# syntax=docker/dockerfile:1

ARG GO_VERSION=1.27.1

FROM golang:${GO_VERSION}-alpine AS build
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

ARG VERSION=0.1.0
RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w -X github.com/kennisnet/qti3-validator/internal/adapter/httpapi.Version=${VERSION}" \
    -o /out/qti-validator \
    ./cmd/qti-validator \
 && mkdir /out/tmp /out/validators

# Compile the schemas once at build time, so a schema the validator cannot
# compile fails the build rather than the container start.
RUN /out/qti-validator -check

FROM scratch

# Package uploads are spooled to /tmp instead of being held in memory.
COPY --from=build --chmod=1777 /out/tmp /tmp
COPY --from=build /out/qti-validator /qti-validator
# Empty by default: mount a directory with .xsd and .sch files here to add
# validators.
COPY --from=build /out/validators /validators

USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["/qti-validator"]
