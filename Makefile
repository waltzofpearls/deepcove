.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

VERSION_PKG := github.com/waltzofpearls/deepcove/internal/cli
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo v0.0.0-dev)

# Native (host) build flags. CGO is required by go-duckdb. `-s -w` strips
# DWARF + symbol table to shrink the binary; injects VERSION.
LDFLAGS_NATIVE := -s -w -X $(VERSION_PKG).Version=$(VERSION)

.PHONY: ui-build
ui-build: ## Build the SvelteKit UI into ui/build/
	cd ui && npm run build

# Fallback stub when the real UI hasn't been built (e.g. cross-compile
# in CI without Node). Only fires if ui/build/index.html doesn't exist.
ui/build/index.html:
	@mkdir -p ui/build
	@echo '<h1>deepcove is running</h1><p>SvelteKit not yet built. Run: make ui-build</p>' > ui/build/index.html

.PHONY: build
build: ui-build ## Build the deepcove binary for the host (builds UI first)
	CGO_ENABLED=1 go build -ldflags '$(LDFLAGS_NATIVE)' -o deepcove ./cmd/deepcove

# Darwin builds use the host toolchain — macOS does not support fully
# static binaries (libSystem.dylib is mandatory), so the artifact is
# dynamically linked against Apple's system frameworks. That's fine:
# every Mac at the target OS version has them.

.PHONY: build-darwin
build-darwin: build-darwin-arm64 build-darwin-amd64 ## Build darwin binaries for arm64 and amd64

.PHONY: build-darwin-arm64
build-darwin-arm64: ui/build/index.html ## Build a darwin/arm64 binary into dist/
	@mkdir -p dist/darwin-arm64
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
		go build -ldflags '$(LDFLAGS_NATIVE)' \
		-o dist/darwin-arm64/deepcove ./cmd/deepcove

# Cross-compiles to darwin/amd64 from Apple Silicon. Xcode's clang
# ships with the x86_64 SDK stubs by default (universal binary support),
# so passing `-arch x86_64` to CC/CXX is enough — no separate toolchain
# install needed. The bindings module pulls in its precompiled
# duckdb-go-bindings/darwin-amd64 .a files automatically based on
# GOOS/GOARCH; no source rebuild required (Apple's libc has no
# musl/glibc split).
.PHONY: build-darwin-amd64
build-darwin-amd64: ui/build/index.html ## Build a darwin/amd64 binary (cross-compiled from arm64 host)
	@mkdir -p dist/darwin-amd64
	CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 \
		CC="clang -arch x86_64" \
		CXX="clang++ -arch x86_64" \
		go build -ldflags '$(LDFLAGS_NATIVE)' \
		-o dist/darwin-amd64/deepcove ./cmd/deepcove

# Cross-compiles to windows/amd64 via mingw-w64. Prerequisite on macOS:
# `brew install mingw-w64` (preinstalled on macos-latest GitHub runners).
# duckdb-go-bindings/windows-amd64 ships precompiled .a files, so no
# source rebuild needed.
#
# We intentionally don't ship a windows/arm64 binary: upstream
# duckdb-go-bindings doesn't support it, and Windows ARM64 ships with
# Prism (x86_64 emulation, analogous to Rosetta) so amd64 binaries run
# natively on Snapdragon X / Surface Pro X without user action.
.PHONY: build-windows
build-windows: build-windows-amd64 ## Build windows binary for amd64

.PHONY: build-windows-amd64
build-windows-amd64: ui/build/index.html ## Build a windows/amd64 binary (cross-compiled via mingw-w64)
	@mkdir -p dist/windows-amd64
	CGO_ENABLED=1 GOOS=windows GOARCH=amd64 \
		CC=x86_64-w64-mingw32-gcc \
		CXX=x86_64-w64-mingw32-g++ \
		go build -ldflags '$(LDFLAGS_NATIVE)' \
		-o dist/windows-amd64/deepcove.exe ./cmd/deepcove

# Linux builds run inside golang:1.26-bookworm (Debian 12, glibc 2.36)
# via build/linux.Dockerfile. We use the duckdb-go-bindings precompiled
# libduckdb_static.a directly — no DuckDB source rebuild needed. Output
# binary dynamically links against glibc and runs on any glibc 2.36+
# system (every actively supported distro in 2026). Alpine is NOT
# supported — gcompat doesn't shim the glibc resolver internals DuckDB
# relies on (see build/linux.Dockerfile for details).

.PHONY: build-linux
build-linux: build-linux-amd64 build-linux-arm64 ## Build linux binaries for amd64 and arm64

.PHONY: build-linux-amd64
build-linux-amd64: ## Build a linux/amd64 binary (glibc, dynamic)
	docker buildx build --platform linux/amd64 \
		-f build/linux.Dockerfile \
		--build-arg VERSION=$(VERSION) \
		--output=type=local,dest=./dist/linux-amd64 .

.PHONY: build-linux-arm64
build-linux-arm64: ## Build a linux/arm64 binary (glibc, dynamic)
	docker buildx build --platform linux/arm64 \
		-f build/linux.Dockerfile \
		--build-arg VERSION=$(VERSION) \
		--output=type=local,dest=./dist/linux-arm64 .

.PHONY: bootstrap
bootstrap: ## Install ui/node_modules from a Linux container (run before first `make dev`, or after package.json changes)
	rm -rf ui/node_modules
	docker run --rm -v "$$(pwd)/ui:/app" -w /app node:24.16-alpine3.23 npm install

.PHONY: dev
dev: ## Run the dev stack (Go server with air + Vite dev server) in docker compose
	docker compose up --build

# GHCR_NAMESPACE / IMAGE_TAG let CI (and curious humans) override the image ref. The
# default tags as `:latest` for the local cache so the agent registry's
# OfficialImage reference resolves without a registry pull.
GHCR_NAMESPACE ?= ghcr.io/waltzofpearls
IMAGE_TAG      ?= latest

.PHONY: image-claude
image-claude: ## Build the deepcove-claude session image locally (tagged for the GHCR namespace so Docker resolves it from cache)
	docker build -f deepcove-claude.Dockerfile \
		-t $(GHCR_NAMESPACE)/deepcove-claude:$(IMAGE_TAG) .

.PHONY: test
test: test-go test-js ## Run all unit tests (Go + JS)

.PHONY: test-go
test-go: ui/build/index.html ## Run Go unit tests (CGO; uses a UI stub for the //go:embed)
	CGO_ENABLED=1 go test ./... -v -cover -count=1

.PHONY: test-js
test-js: ## Run frontend (vitest) unit tests
	cd ui && npm run test

.PHONY: lint
lint: lint-go lint-js ## Run all linters (Go + JS)

.PHONY: lint-go
lint-go: ui/build/index.html ## Run Go linters (go vet + golangci-lint; UI stub for the //go:embed)
	CGO_ENABLED=1 go vet ./...
	CGO_ENABLED=1 golangci-lint run ./...

.PHONY: lint-js
lint-js: ## Run the frontend linter (svelte-check, incl. .ts type-checking)
	cd ui && npm run check

.PHONY: clean
clean: ## Remove built binaries
	rm -f deepcove
	rm -rf dist
