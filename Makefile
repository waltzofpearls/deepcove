.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

VERSION_PKG := github.com/waltzofpearls/deepcove/internal/cli
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo v0.0.0-dev)

# Native (host) build flags. CGO is required by go-duckdb. `-s -w` strips
# DWARF + symbol table to shrink the binary; injects VERSION.
LDFLAGS_NATIVE := -s -w -X $(VERSION_PKG).Version=$(VERSION)

CODE_SIGN_IDENTITY ?=
APPLE_ID ?=
APPLE_TEAM_ID ?=
APPLE_PASSWORD ?=
APPLE_KEYCHAIN_PROFILE ?=

# A native build boots guests of the host's architecture. Cross-compile targets override this.
GUEST_ARCH ?= $(shell go env GOARCH)

# Recursive, and defined above the rules that use it: prerequisites expand as make reads each rule.
GUEST_INIT_EMBED = internal/guestasset/deepcove-guest-init-$(GUEST_ARCH)

# Placeholder so the package compiles in a tree that has not built the init yet. Empty on purpose:
# guestasset.Available() reads that as "not built", and the runtime reports that it needs setup.
$(GUEST_INIT_EMBED):
	@mkdir -p $(dir $@)
	@: > $@

.PHONY: ui-build
ui-build: ## Build the SvelteKit UI into ui/build/
	cd ui && npm run build

# A placeholder so the //go:embed target exists. Only lint and test use it; every build target builds
# the real UI.
ui/build/index.html:
	@mkdir -p ui/build
	@echo '<h1>deepcove is running</h1><p>SvelteKit not yet built. Run: make ui-build</p>' > ui/build/index.html

# The versions live in Go and the release build reads them as JSON, so the file is regenerated whenever it
# is older than its source. Nobody has to remember `go generate`.
VERSIONS_JSON := internal/vmruntime/versions.json

$(VERSIONS_JSON): internal/vmruntime/versions.go internal/vmruntime/gen/main.go
	go generate ./internal/vmruntime

.PHONY: build
# guest-init first, so the embedded init always matches this binary. That closes the trap where a
# rebuilt init was never picked up and sessions silently booted stale code.
build: guest-init ui-build $(VERSIONS_JSON) ## Build the deepcove binary for the host (builds the guest init + UI first)
	CGO_ENABLED=1 go build -ldflags '$(LDFLAGS_NATIVE)' -o deepcove ./cmd/deepcove
	@$(MAKE) --no-print-directory sign-darwin BIN=deepcove

# Running a VM needs the com.apple.security.virtualization entitlement, so the binary that hosts
# sessions has to be codesigned or the Apple VF runtime fails at session create time. A signature with
# no certificate is enough to run it here. Does nothing when not on macOS.
#
# CODE_SIGN_IDENTITY switches this to a distribution signature: your certificate plus the hardened
# runtime, which notarization requires and which blocks debugger attach.
.PHONY: sign-darwin
sign-darwin: ## Sign a darwin binary, e.g. make sign-darwin ARCH=arm64 [CODE_SIGN_IDENTITY=...]
	@[ "$$(go env GOOS)" = "darwin" ] || exit 0; \
	bin="$(BIN)"; [ -n "$(ARCH)" ] && bin="dist/darwin-$(ARCH)/deepcove"; \
	test -n "$$bin" || { echo "set ARCH=<arm64|amd64> or BIN=<path>"; exit 1; }; \
	if [ -n "$(CODE_SIGN_IDENTITY)" ]; then \
		codesign -f --options runtime --timestamp \
			--entitlements build/virtualization.entitlements -s "$(CODE_SIGN_IDENTITY)" "$$bin"; \
		codesign --verify --strict "$$bin"; \
		echo "signed $$bin with $(CODE_SIGN_IDENTITY)"; \
	else \
		codesign -f --entitlements build/virtualization.entitlements -s - "$$bin"; \
		echo "signed $$bin for virtualization"; \
	fi

# Darwin builds use the host toolchain — macOS does not support fully
# static binaries (libSystem.dylib is mandatory), so the artifact is
# dynamically linked against Apple's system frameworks. That's fine:
# every Mac at the target OS version has them.

.PHONY: build-darwin
build-darwin: build-darwin-arm64 build-darwin-amd64 ## Build darwin binaries for arm64 and amd64

.PHONY: build-darwin-arm64
build-darwin-arm64: ui-build ## Build a darwin/arm64 binary into dist/
	@mkdir -p dist/darwin-arm64
	@$(MAKE) --no-print-directory guest-init GUEST_ARCH=arm64
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
		go build -ldflags '$(LDFLAGS_NATIVE)' \
		-o dist/darwin-arm64/deepcove ./cmd/deepcove
	@$(MAKE) --no-print-directory sign-darwin ARCH=arm64

# Cross-compiles to darwin/amd64 from Apple Silicon. Xcode's clang
# ships with the x86_64 SDK stubs by default (universal binary support),
# so passing `-arch x86_64` to CC/CXX is enough — no separate toolchain
# install needed. The bindings module pulls in its precompiled
# duckdb-go-bindings/darwin-amd64 .a files automatically based on
# GOOS/GOARCH; no source rebuild required (Apple's libc has no
# musl/glibc split).
.PHONY: build-darwin-amd64
build-darwin-amd64: ui-build ## Build a darwin/amd64 binary (cross-compiled from arm64 host)
	@mkdir -p dist/darwin-amd64
	@$(MAKE) --no-print-directory guest-init GUEST_ARCH=amd64
	CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 \
		CC="clang -arch x86_64" \
		CXX="clang++ -arch x86_64" \
		go build -ldflags '$(LDFLAGS_NATIVE)' \
		-o dist/darwin-amd64/deepcove ./cmd/deepcove
	@$(MAKE) --no-print-directory sign-darwin ARCH=amd64

# Cross-compiles to windows/amd64 via mingw-w64. Prerequisite on macOS:
# `brew install mingw-w64` (preinstalled on macos-latest GitHub runners).
# duckdb-go-bindings/windows-amd64 ships precompiled .a files, so no
# source rebuild needed.
#
# No windows/arm64 binary yet — and this is NOT a DuckDB limitation.
# DuckDB core HAS windows-arm64 (the libduckdb-windows-arm64 build; beta
# in 1.4.3 LTS). The gap is the Go wrapper: duckdb-go-bindings hasn't
# packaged a windows-arm64 prebuilt static lib (we're on v0.1.21), so a
# build-windows-arm64 target would fail to link today. A future bindings
# bump should add it — track upstream:
#   https://github.com/duckdb/duckdb-go-bindings/issues/18
# Meanwhile Windows ARM64 ships with Prism (x86_64 emulation, analogous to
# Rosetta), so the amd64 binary runs natively on Snapdragon X / Surface
# Pro X without user action.
.PHONY: build-windows
build-windows: build-windows-amd64 ## Build windows binary for amd64

.PHONY: build-windows-amd64
build-windows-amd64: ui-build ## Build a windows/amd64 binary (cross-compiled via mingw-w64)
	@mkdir -p dist/windows-amd64
	@$(MAKE) --no-print-directory guest-init GUEST_ARCH=amd64
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
build-linux-amd64: ## Build a linux/amd64 binary (glibc, dynamic; the Dockerfile builds the UI)
	@$(MAKE) --no-print-directory guest-init GUEST_ARCH=amd64
	docker buildx build --platform linux/amd64 \
		-f build/linux.Dockerfile \
		--build-arg VERSION=$(VERSION) \
		--output=type=local,dest=./dist/linux-amd64 .

.PHONY: build-linux-arm64
build-linux-arm64: ## Build a linux/arm64 binary (glibc, dynamic; the Dockerfile builds the UI)
	@$(MAKE) --no-print-directory guest-init GUEST_ARCH=arm64
	docker buildx build --platform linux/arm64 \
		-f build/linux.Dockerfile \
		--build-arg VERSION=$(VERSION) \
		--output=type=local,dest=./dist/linux-arm64 .

.PHONY: notarize-darwin
notarize-darwin: ## Notarize one darwin binary, e.g. make notarize-darwin ARCH=arm64
	@test -n "$(ARCH)" || { echo "set ARCH=<arm64|amd64>"; exit 1; }
	@test -n "$(APPLE_KEYCHAIN_PROFILE)$(APPLE_ID)" \
		|| { echo "set APPLE_KEYCHAIN_PROFILE, or APPLE_ID with APPLE_TEAM_ID and APPLE_PASSWORD"; exit 1; }
	@test -n "$(APPLE_KEYCHAIN_PROFILE)" || test -n "$(APPLE_TEAM_ID)" || { echo "set APPLE_TEAM_ID"; exit 1; }
	@test -n "$(APPLE_KEYCHAIN_PROFILE)" || test -n "$(APPLE_PASSWORD)" || { echo "set APPLE_PASSWORD"; exit 1; }
	rm -rf dist/notarize dist/notarize.zip
	mkdir -p dist/notarize
	cp dist/darwin-$(ARCH)/deepcove dist/notarize/deepcove-darwin-$(ARCH)
	ditto -c -k dist/notarize dist/notarize.zip
	# Apple records the result against each binary's signature, not against this zip. The zip is only a
	# way to send them, and the files in dist/darwin-*/ are the ones that come out notarized.
	# Rebuilding or re-signing them afterwards breaks the match.
	@if [ -n "$(APPLE_KEYCHAIN_PROFILE)" ]; then \
		xcrun notarytool submit dist/notarize.zip \
			--keychain-profile "$(APPLE_KEYCHAIN_PROFILE)" --wait --timeout 30m; \
	else \
		xcrun notarytool submit dist/notarize.zip \
			--apple-id "$(APPLE_ID)" --team-id "$(APPLE_TEAM_ID)" --password "$(APPLE_PASSWORD)" \
			--wait --timeout 30m; \
	fi
	rm -rf dist/notarize dist/notarize.zip

.PHONY: release-darwin
release-darwin: ## Build, sign and notarize the darwin binaries for distribution
	@test -n "$(CODE_SIGN_IDENTITY)" || { echo "set CODE_SIGN_IDENTITY"; exit 1; }
	@$(MAKE) --no-print-directory build-darwin
	@for arch in arm64 amd64; do \
		$(MAKE) --no-print-directory notarize-darwin ARCH=$$arch; \
	done

RELEASE_DIR := dist/release

.PHONY: package
package: ## Archive one platform's binary, e.g. make package OS=darwin ARCH=arm64
	@test -n "$(OS)$(ARCH)" || { echo "set OS=<darwin|linux|windows> and ARCH=<arm64|amd64>"; exit 1; }
	@mkdir -p $(RELEASE_DIR) && rm -rf dist/stage && mkdir -p dist/stage
	@case "$(OS)" in \
		windows) cp dist/$(OS)-$(ARCH)/deepcove.exe dist/stage/ ;; \
		*)       cp dist/$(OS)-$(ARCH)/deepcove dist/stage/ ;; \
	esac
	@cp LICENSE README.md dist/stage/
	@case "$(OS)" in \
		darwin)  ditto -c -k --norsrc --noextattr dist/stage $(RELEASE_DIR)/deepcove_$(VERSION)_$(OS)-$(ARCH).zip ;; \
		windows) (cd dist/stage && zip -q ../../$(RELEASE_DIR)/deepcove_$(VERSION)_$(OS)-$(ARCH).zip deepcove.exe LICENSE README.md) ;; \
		*)       tar -czf $(RELEASE_DIR)/deepcove_$(VERSION)_$(OS)-$(ARCH).tar.gz -C dist/stage . ;; \
	esac
	@rm -rf dist/stage
	@ls -1 $(RELEASE_DIR)/deepcove_$(VERSION)_$(OS)-$(ARCH).*

.PHONY: package-all
package-all: ## Archive every platform already built in dist/
	@for dir in dist/darwin-arm64 dist/darwin-amd64 dist/linux-amd64 dist/linux-arm64 dist/windows-amd64; do \
		[ -d $$dir ] || continue; \
		p=$$(basename $$dir); \
		$(MAKE) --no-print-directory package OS=$${p%-*} ARCH=$${p#*-}; \
	done

.PHONY: checksums
checksums: ## Write SHA256SUMS over everything in $(RELEASE_DIR)
	@cd $(RELEASE_DIR) && rm -f SHA256SUMS && files=$$(ls) && \
		if command -v sha256sum >/dev/null; then sha256sum $$files; else shasum -a 256 $$files; fi > SHA256SUMS
	@cat $(RELEASE_DIR)/SHA256SUMS

.PHONY: build-all
build-all: build-darwin build-windows build-linux ## Build release binaries for every supported OS/arch into dist/

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
test-go: ui/build/index.html $(GUEST_INIT_EMBED) $(VERSIONS_JSON) ## Run Go unit tests (CGO; uses a UI stub for the //go:embed)
	CGO_ENABLED=1 go test ./... -v -cover -count=1

.PHONY: test-js
test-js: ## Run frontend (vitest) unit tests
	cd ui && npm run test

.PHONY: lint
lint: lint-go lint-js ## Run all linters (Go + JS)

.PHONY: lint-go
lint-go: ui/build/index.html $(GUEST_INIT_EMBED) $(VERSIONS_JSON) ## Run Go linters (go vet + golangci-lint; UI stub for the //go:embed)
	CGO_ENABLED=1 go vet ./...
	CGO_ENABLED=1 golangci-lint run ./...

.PHONY: lint-js
lint-js: ## Run the frontend linters (prettier + svelte-check, incl. .ts type-checking)
	cd ui && npm run format:check
	cd ui && npm run check

.PHONY: format
format: ## Reformat the frontend (gofmt covers Go, and golangci-lint already enforces it)
	cd ui && npm run format

# --- Apple VF runtime (macOS only) -----------------------------------------
.PHONY: guest-init
guest-init: ## Build the Linux guest init binary into the package that embeds it
	@mkdir -p $(dir $(GUEST_INIT_EMBED))
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GUEST_ARCH) \
		go build -ldflags '-s -w' -o $(GUEST_INIT_EMBED) ./cmd/deepcove-guest-init

# DATA_DIR is where a node keeps what it downloaded, matching what the binary picks when DATA_DIR is
# unset: os.UserConfigDir, which is Application Support on macOS and XDG_CONFIG_HOME or ~/.config
# everywhere else. Export your own to clean a node that runs with one.
UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
DATA_DIR ?= $(HOME)/Library/Application Support/deepcove
else
XDG_CONFIG_HOME ?= $(HOME)/.config
DATA_DIR ?= $(XDG_CONFIG_HOME)/deepcove
endif

# The release narrows VM_ARTIFACTS to what an earlier release does not already carry, and each artifact
# is its own image stage, so this asks for one at a time. INSTALL is off so a build never changes what
# a running node boots; 1, true and yes all turn it on.
VM_ARCH ?= $(shell go env GOARCH)
VM_ARTIFACTS ?= guest-kernel cloud-hypervisor virtiofsd passt
INSTALL ?= 0

.PHONY: vm-artifacts
vm-artifacts: $(VERSIONS_JSON) ## Build the VM artifact release files into $(RELEASE_DIR) (INSTALL=1 installs them too)
	@mkdir -p $(RELEASE_DIR)
	@for artifact in $(VM_ARTIFACTS); do \
		docker buildx build -f build/vm-artifacts.Dockerfile --target $$artifact \
			--platform linux/$(VM_ARCH) --output type=local,dest=$(RELEASE_DIR) . || exit 1; \
	done
ifneq ($(filter $(INSTALL),1 true yes),)
	@build/install-vm-artifacts.sh "$(DATA_DIR)" $(VM_ARCH) $(VM_ARTIFACTS)
endif

.PHONY: aperture-relay
aperture-relay: ## Build the Aperture relay for linux amd64 + arm64 into dist/relay/
	@mkdir -p dist/relay
	@for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch \
			go build -ldflags '-s -w' -o dist/relay/aperture-relay-linux-$$arch ./cmd/aperture-relay || exit 1; \
		echo "built dist/relay/aperture-relay-linux-$$arch"; \
	done

.PHONY: relay-bundle
relay-bundle: aperture-relay ## Package the relay with its installer into dist/relay/aperture-relay.tar.gz
	@cp cmd/aperture-relay/deploy/install.sh cmd/aperture-relay/deploy/aperture-relay.service dist/relay/
	@chmod +x dist/relay/install.sh
	@tar -czf dist/relay/aperture-relay.tar.gz -C dist/relay \
		aperture-relay-linux-amd64 aperture-relay-linux-arm64 install.sh aperture-relay.service
	@echo "built dist/relay/aperture-relay.tar.gz"

# HOST is anything ssh accepts. UPSTREAM and ALLOW are only needed the first time.
#   make relay-deploy HOST=user@approved-vm UPSTREAM=aperture.example.ts.net:80 ALLOW=100.75.35.244/32
.PHONY: relay-deploy
relay-deploy: relay-bundle ## Copy the relay bundle to HOST over ssh and install it
	@test -n "$(HOST)" || { echo "set HOST=<ssh target>"; exit 1; }
	scp dist/relay/aperture-relay.tar.gz $(HOST):/tmp/aperture-relay.tar.gz
	ssh $(HOST) 'rm -rf /tmp/aperture-relay && mkdir -p /tmp/aperture-relay \
		&& tar -xzf /tmp/aperture-relay.tar.gz -C /tmp/aperture-relay \
		&& sudo /tmp/aperture-relay/install.sh \
			$(if $(UPSTREAM),--upstream $(UPSTREAM)) $(if $(ALLOW),--allow $(ALLOW)) $(if $(LISTEN),--listen $(LISTEN)) \
		&& rm -rf /tmp/aperture-relay /tmp/aperture-relay.tar.gz'

.PHONY: applevf-smoke
applevf-smoke: ## Build + codesign the VF boot smoke harness (needs an Apple signing identity)
	@mkdir -p dist/darwin-$(shell go env GOARCH)
	CGO_ENABLED=1 go build -o dist/darwin-$(shell go env GOARCH)/applevf-smoke ./cmd/applevf-smoke
	codesign -f --entitlements build/virtualization.entitlements -s "$${CODE_SIGN_IDENTITY:--}" \
		dist/darwin-$(shell go env GOARCH)/applevf-smoke
	@echo "Signed dist/darwin-$(shell go env GOARCH)/applevf-smoke (identity: $${CODE_SIGN_IDENTITY:-none})"

.PHONY: clean
clean: ## Remove built binaries
	rm -f deepcove
	rm -rf dist

.PHONY: clean-vm-artifacts
clean-vm-artifacts: ## Remove the VM artifacts this node downloaded, so the next setup fetches them again
	rm -rf "$(DATA_DIR)/vm-artifacts" "$(DATA_DIR)/vm-guest"
	@echo "removed the VM artifacts under $(DATA_DIR)"
