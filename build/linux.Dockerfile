# syntax=docker/dockerfile:1.7
#
# Builds a deepcove binary dynamically linked against glibc, for Linux
# servers and workstations. Targeting glibc means we can use the
# duckdb-go-bindings precompiled libduckdb_static.a directly — no
# DuckDB source rebuild needed (which is what made the previous
# musl-static path expensive).
#
# Targeted glibc baseline: bookworm ships glibc 2.36. The Go binary
# dynamically links against glibc symbols available at build time;
# glibc's forward-compatibility means the result runs on any system
# with glibc >= 2.36 (Debian 12+, Ubuntu 22.04+, RHEL 9+, modern Fedora
# and Arch — every actively supported distro in 2026).
#
# Alpine is NOT supported. We verified: `apk add gcompat libstdc++` is
# not enough — the DuckDB code uses glibc-specific symbols like
# __res_init (resolver internals) that gcompat doesn't shim. The
# binary requires actual glibc. The from-source musl path lives in git
# history if anyone ever needs to revive it.
#
# Build:
#   docker buildx build --platform linux/amd64 -f build/linux.Dockerfile \
#     --output=type=local,dest=./dist/linux-amd64 .
#
# Output:
#   - dist/linux-<arch>/deepcove

# The UI is embedded in the binary, so it is built here rather than on the host.
FROM node:24-bookworm AS ui-builder
ARG VERSION=dev
WORKDIR /ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui/ ./
RUN DEEPCOVE_VERSION=${VERSION} npm run build

FROM golang:1.27-bookworm AS go-builder
ARG VERSION=dev

WORKDIR /src

# Module cache layer — separate from the (changing) source so iteration
# rebuilds don't re-download deps.
COPY go.mod go.sum ./
RUN go mod download

# Source.
COPY . .
COPY --from=ui-builder /ui/build ./ui/build

# CGO required by go-duckdb. No build tags needed — the bindings'
# default cgo_static.go path pulls in their precompiled libduckdb_static.a
# for the target GOOS/GOARCH and links it. No -extldflags=-static here:
# we want glibc + libstdc++ resolved at runtime.
ENV CGO_ENABLED=1
RUN go build \
    -ldflags "-s -w -X github.com/waltzofpearls/deepcove/internal/cli.Version=${VERSION}" \
    -o /out/deepcove \
    ./cmd/deepcove

FROM scratch AS export
COPY --from=go-builder /out/deepcove /deepcove
