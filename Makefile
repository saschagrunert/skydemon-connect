GO ?= go

# go.mod's toolchain line is only a minimum: a newer local Go would build a
# different exe. This runs that exact one, downloading it if needed; set
# GOTOOLCHAIN to override.
GOTOOLCHAIN ?= $(shell sed -n 's/^toolchain //p' go.mod)
export GOTOOLCHAIN := $(GOTOOLCHAIN)

# golangci-lint ships as a binary, not as a Go tool dependency (its authors
# advise against building it from source), so bump the version and the
# checksums below by hand. govulncheck is pinned in tools/go.mod, which
# Dependabot keeps current.
GOLANGCI_LINT_VERSION = 2.15.0

# Prettier, which formats the Markdown, YAML and JSON, has no binary
# releases: npx runs this version of the npm package in the node image,
# pinned by the digest of its index. Bump both by hand; for the digest, see
# `skopeo inspect --format '{{.Digest}}' docker://docker.io/library/node:<version>-slim`.
PRETTIER_VERSION = 3.10.1
NODE_VERSION = 26.13.1
NODE_DIGEST = sha256:e6bedef217677b276c591795e169840edd9096dbf13f29df8ef8d1856b8820bc
NODE_IMAGE = docker.io/library/node:$(NODE_VERSION)-slim@$(NODE_DIGEST)
CONTAINER_RUNTIME ?= podman

# SHA-256 checksums for downloaded tools, named <TOOL>_SHA256_<os>_<arch>,
# from the release's checksums.txt.
# Downloads fail when no checksum is pinned for the current platform.
GOLANGCI_LINT_SHA256_linux_amd64 = 9b7fe6a6bf6334b98decf8d86b11415ea7889afdabdd1959fdce23af2a9ebcea
GOLANGCI_LINT_SHA256_linux_arm64 = b4d8f17068b35ee6060cc2f69a16fb48740d706fbba81661c318649c114a6998
GOLANGCI_LINT_SHA256_darwin_amd64 = 767b899a28285e88ded2823d5dcc4bc5ef681173eb2d80f029a7b17ee4ece3b8
GOLANGCI_LINT_SHA256_darwin_arm64 = d1e409481a8ca846c4bc97e781eb5cb1bd5c23353a0b3977a1cbda1b1b6447d5
GOLANGCI_LINT_SHA256_windows_amd64 = cd7ea6063c95de245e17e5246e42bdc14bb221792c429dbd239ab3a6ade6ca6a
GOLANGCI_LINT_SHA256_windows_arm64 = 4fb8988abf186158e30af1b8a0fb3671cf2cb02bc91d7e6ce6996f4c1e922e9a

BUILD_DIR := build
BINARY := $(BUILD_DIR)/SkyDemonConnect.exe
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUNDLE := $(BUILD_DIR)/SkyDemonConnect-$(VERSION).zip

# The MSFS 2024 SDK whose SimConnect.dll the release zip ships. To bump,
# take the "SDK Installer (Core)" of the newest entry in
# https://sdk.flightsimulator.com/msfs2024/files/sdk.json. On a mismatch,
# make bundle prints the actual SHA-256: check that the zip came from
# sdk.flightsimulator.com, pin it, and do the same for SimConnect.dll and
# the EULA. ci caches both files under make sdk-cache-key.
SDK_VERSION = 1.8.2
SDK_URL = https://sdk.flightsimulator.com/msfs2024/files/installers/$(SDK_VERSION)/MSFS2024_SDK_Core_Installer_$(SDK_VERSION).zip
SDK_SHA256 = 1dd575e47a9c4de3efff39bd0f9ab180602059f4a1c16913f659d018a91b5a0a
SIMCONNECT_SHA256 = 8f386401a990eb5dce561cd9ecebf4c3e7565f4501ed48e1785edf2467feee41
EULA_SHA256 = 8856e6201705a2e7b0097e9ab891f1e36c7944d52af1f5bcd4ceee6a3e69386c
SDK_DIR := $(BUILD_DIR)/sdk-$(SDK_VERSION)

LDFLAGS := -s -w -H windowsgui -X main.version=$(VERSION)

# The exe's icon and version resource, which Explorer, SmartScreen, the
# firewall prompt and Task Manager show. Windows wants a numeric file version:
# v1.2.3 becomes 1.2.3.0, anything else 0.0.0.0.
SYSO := cmd/skydemon-connect/rsrc_windows_amd64.syso
FILE_VERSION := $(or $(shell echo "$(VERSION)" | sed -nE 's/^v?([0-9]+)\.([0-9]+)\.([0-9]+).*/\1.\2.\3.0/p'),0.0.0.0)

# Go's names for the host, which also name the golangci-lint downloads. Not
# OS and ARCH: Windows sets OS=Windows_NT in every environment. Every Go
# names the host alike, so the local one answers, without downloading the
# toolchain of go.mod, e.g. for make verify-format, which needs no Go.
HOST_OS := $(shell GOTOOLCHAIN=local $(GO) env GOHOSTOS 2>/dev/null)
HOST_ARCH := $(shell GOTOOLCHAIN=local $(GO) env GOHOSTARCH 2>/dev/null)

# The build's own tools run here, also when GOOS=windows is exported.
HOST_GO := GOOS=$(HOST_OS) GOARCH=$(HOST_ARCH) $(GO)

# Executables on a Windows host need .exe, and golangci-lint ships as a zip
# there.
EXE := $(if $(filter windows,$(HOST_OS)),.exe)
LINT_ARCHIVE := $(if $(filter windows,$(HOST_OS)),zip,tar.gz)
# unpack extracts file $(2) from archive $(1) into $(BUILD_DIR).
unpack = $(if $(filter windows,$(HOST_OS)),unzip -q -o -j $(1) $(2) -d $(BUILD_DIR),tar -xzf $(1) -C $(BUILD_DIR) --strip-components=1 $(2))

# The code is mostly Windows-only (SimConnect, Win32 tray, COM ports), so
# it is linted for both: the host run never loads *_windows.go files.
LINT_OSES = $(sort $(HOST_OS) windows)

# A GOLANGCI_LINT given from outside, e.g. GOLANGCI_LINT=golangci-lint for
# one on the PATH, is used as it is; the default is downloaded.
GOLANGCI_LINT ?= $(BUILD_DIR)/golangci-lint-$(GOLANGCI_LINT_VERSION)$(EXE)

COLOR := \033[36m
NOCOLOR := \033[0m

# check_sha256 checks a file against an expected SHA-256 checksum and,
# unless a third argument is given, removes it on mismatch.
# Usage: $(call check_sha256,file,checksum[,keep])
define check_sha256
	if command -v sha256sum >/dev/null 2>&1; then \
		actual="$$(sha256sum "$(1)" | cut -d ' ' -f 1)"; \
	else \
		actual="$$(shasum -a 256 "$(1)" | cut -d ' ' -f 1)"; \
	fi; \
	if [ "$$actual" != "$(2)" ]; then \
		echo "ERROR: SHA-256 mismatch for $(1): expected $(2), got $$actual" >&2; \
		$(if $(3),,rm -f "$(1)";) \
		exit 1; \
	fi
endef

# verify_checksum checks a downloaded tool against the SHA-256 checksum
# pinned for the current OS and architecture.
# Usage: $(call verify_checksum,file,TOOL)
define verify_checksum
	if [ -z "$($(2)_SHA256_$(HOST_OS)_$(HOST_ARCH))" ]; then \
		echo "ERROR: no pinned SHA-256 checksum for $(2) on $(HOST_OS)/$(HOST_ARCH);" \
			"install golangci-lint and run make lint GOLANGCI_LINT=golangci-lint" >&2; \
		rm -f "$(1)"; \
		exit 1; \
	fi; \
	$(call check_sha256,$(1),$($(2)_SHA256_$(HOST_OS)_$(HOST_ARCH)))
endef

.PHONY: all
all: build ## Build the Windows binary

.PHONY: help
help: ## Display this help
	@awk 'BEGIN { FS = ":.*##"; printf "\nUsage:\n  make $(COLOR)<target>$(NOCOLOR)\n\n" } \
		/^[a-zA-Z0-9_-]+:.*?##/ { printf "  $(COLOR)%-15s$(NOCOLOR) %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

.PHONY: build $(BINARY)
build: $(BINARY) ## Build build/SkyDemonConnect.exe (Windows, GUI subsystem)

$(BINARY): $(SYSO)
	@mkdir -p $(BUILD_DIR)
	@# The resource is removed also when the build fails: left in the
	@# source tree, it would go into every later Windows go build or go
	@# test, with this build's version.
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -buildvcs=false \
		-ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/skydemon-connect; \
		s=$$?; rm -f $(SYSO); exit $$s

.PHONY: $(SYSO)
$(SYSO): $(BUILD_DIR)/go-winres$(EXE)
	$(HOST_GO) run ./internal/icon/genpng $(BUILD_DIR)/icon.png
	$(BUILD_DIR)/go-winres$(EXE) simply --arch amd64 --manifest gui --icon $(BUILD_DIR)/icon.png \
		--product-name SkyDemonConnect --file-description SkyDemonConnect \
		--original-filename SkyDemonConnect.exe --copyright "The SkyDemonConnect Authors" \
		--product-version "$(VERSION)" --file-version $(FILE_VERSION) \
		--out cmd/skydemon-connect/rsrc

$(BUILD_DIR)/go-winres$(EXE): tools/go.mod tools/go.sum
	@mkdir -p $(BUILD_DIR)
	cd tools && $(HOST_GO) build -o ../$(BUILD_DIR)/go-winres$(EXE) github.com/tc-hib/go-winres

.PHONY: bundle
bundle: $(BUNDLE) ## Zip the exe with SimConnect.dll from the MSFS SDK (needs msiextract)

# The entries get SOURCE_DATE_EPOCH, or else the commit's time, so the same
# commit gives the same zip.
# docs/reference.md keeps its folder, so that the README's link to it works.
# The SDK files are checked again, as they may come from an earlier build or
# a cache; on a mismatch they are removed, to be extracted anew.
$(BUNDLE): $(BINARY) $(SDK_DIR)/SimConnect.dll README.md docs/reference.md LICENSE NOTICE.md
	@( $(call check_sha256,$(SDK_DIR)/SimConnect.dll,$(SIMCONNECT_SHA256),keep) ) && \
		( $(call check_sha256,$(SDK_DIR)/MSFS SDK EULA.pdf,$(EULA_SHA256),keep) ) || \
		{ rm -f $(SDK_DIR)/SimConnect.dll "$(SDK_DIR)/MSFS SDK EULA.pdf"; exit 1; }
	$(HOST_GO) run ./internal/zipfiles $@ "$${SOURCE_DATE_EPOCH:-$$(git log -1 --format=%ct)}" \
		$(BINARY) $(SDK_DIR)/SimConnect.dll "$(SDK_DIR)/MSFS SDK EULA.pdf" README.md \
		docs/reference.md=docs/reference.md LICENSE NOTICE.md

# The SDK installer is an MSI with external cabinets; msiextract unpacks
# all of it, of which two files are kept.
# The download is kept on a checksum mismatch, so that a bump needs it once.
$(SDK_DIR)/SimConnect.dll:
	@for tool in curl unzip msiextract; do \
		command -v $$tool >/dev/null || { echo "ERROR: make bundle needs $$tool" >&2; exit 1; }; \
	done
	rm -rf $(SDK_DIR)/msi $(SDK_DIR)/files
	mkdir -p $(SDK_DIR)/msi
	[ -f $(SDK_DIR)/sdk.zip ] || curl -sSfL --retry 3 -o $(SDK_DIR)/sdk.zip.part "$(SDK_URL)"
	[ -f $(SDK_DIR)/sdk.zip ] || mv $(SDK_DIR)/sdk.zip.part $(SDK_DIR)/sdk.zip
	@$(call check_sha256,$(SDK_DIR)/sdk.zip,$(SDK_SHA256),keep)
	unzip -q -j $(SDK_DIR)/sdk.zip -d $(SDK_DIR)/msi
	cd $(SDK_DIR)/msi && msiextract -C ../files *.msi >/dev/null
	cp "$(SDK_DIR)/files/MSFS 2024 SDK/Licenses/MSFS SDK EULA.pdf" $(SDK_DIR)/
	@$(call check_sha256,$(SDK_DIR)/MSFS SDK EULA.pdf,$(EULA_SHA256))
	cp "$(SDK_DIR)/files/MSFS 2024 SDK/SimConnect SDK/lib/SimConnect.dll" $(SDK_DIR)/
	@$(call check_sha256,$(SDK_DIR)/SimConnect.dll,$(SIMCONNECT_SHA256))
	rm -rf $(SDK_DIR)/msi $(SDK_DIR)/files $(SDK_DIR)/sdk.zip

# The key ci caches $(SDK_DIR) under: a new pin makes a new entry.
.PHONY: sdk-cache-key
sdk-cache-key:
	@echo msfs-sdk-$(SDK_VERSION)-$(SDK_SHA256)-$(SIMCONNECT_SHA256)-$(EULA_SHA256)

.PHONY: verify-version
# A host build with the same -X: go version -m can't check the exe itself,
# since -trimpath leaves -ldflags out of its build info.
# On Windows -version shows a message box, so this runs elsewhere only.
verify-version: ## Verify that -ldflags still sets the version (not on Windows)
	@[ "$(HOST_OS)" != windows ] || { echo "ERROR: verify-version needs a non-Windows host" >&2; exit 1; }
	$(HOST_GO) run -ldflags "-X main.version=verify" ./cmd/skydemon-connect -version | \
		grep -qx 'SkyDemonConnect verify'

# The race detector needs cgo and a C compiler; RACE= turns it off.
RACE ?= -race

.PHONY: test
# ci.yml runs this command itself, as Windows has no make: keep both alike.
test: ## Run the tests with the race detector and coverage
	@mkdir -p $(BUILD_DIR)
	$(GO) test $(RACE) -count=1 -coverprofile=$(BUILD_DIR)/coverage.out -covermode=atomic ./...

.PHONY: demo
demo: ## Run a demo flight on this machine (console, port 49002)
	$(HOST_GO) run ./cmd/skydemon-connect -demo -console

.PHONY: lint
lint: $(filter $(BUILD_DIR)/%,$(GOLANGCI_LINT)) ## Run golangci-lint for the host and for Windows
	@set -e; for goos in $(LINT_OSES); do \
		echo "GOOS=$$goos golangci-lint run"; \
		GOOS=$$goos $(GOLANGCI_LINT) run ./...; \
	done

# Windows releases are zips: unzip unpacks them, as Git Bash's or MSYS2's
# tar (GNU) can't.
$(BUILD_DIR)/golangci-lint-$(GOLANGCI_LINT_VERSION)$(EXE):
	@mkdir -p $(BUILD_DIR)
	curl -sSfL --retry 3 -o $(BUILD_DIR)/golangci-lint.$(LINT_ARCHIVE) \
		https://github.com/golangci/golangci-lint/releases/download/v$(GOLANGCI_LINT_VERSION)/golangci-lint-$(GOLANGCI_LINT_VERSION)-$(HOST_OS)-$(HOST_ARCH).$(LINT_ARCHIVE)
	$(call verify_checksum,$(BUILD_DIR)/golangci-lint.$(LINT_ARCHIVE),GOLANGCI_LINT)
	$(call unpack,$(BUILD_DIR)/golangci-lint.$(LINT_ARCHIVE),golangci-lint-$(GOLANGCI_LINT_VERSION)-$(HOST_OS)-$(HOST_ARCH)/golangci-lint$(EXE))
	mv $(BUILD_DIR)/golangci-lint$(EXE) $@
	rm $(BUILD_DIR)/golangci-lint.$(LINT_ARCHIVE)

PRETTIER = $(CONTAINER_RUNTIME) run --rm -v "$(CURDIR)":/w:z -w /w \
	-e NPM_CONFIG_UPDATE_NOTIFIER=false --entrypoint npx \
	$(NODE_IMAGE) --yes prettier@$(PRETTIER_VERSION)

.PHONY: verify-format
verify-format: ## Verify the Markdown, YAML and JSON are formatted (needs podman or CONTAINER_RUNTIME=docker)
	$(PRETTIER) --check .

.PHONY: format
format: ## Format the Markdown, YAML and JSON with Prettier (needs podman or CONTAINER_RUNTIME=docker)
	$(PRETTIER) --write .

.PHONY: govulncheck
govulncheck: $(BUILD_DIR)/govulncheck$(EXE) $(BUILD_DIR)/go-winres$(EXE) ## Check the release build and its tools for known vulnerabilities
	GOOS=windows $(BUILD_DIR)/govulncheck$(EXE) ./...
	$(BUILD_DIR)/govulncheck$(EXE) -mode=binary $(BUILD_DIR)/go-winres$(EXE)

# Built for the host: go tool would build it for GOOS=windows.
$(BUILD_DIR)/govulncheck$(EXE): tools/go.mod tools/go.sum
	@mkdir -p $(BUILD_DIR)
	cd tools && $(HOST_GO) build -o ../$(BUILD_DIR)/govulncheck$(EXE) golang.org/x/vuln/cmd/govulncheck

.PHONY: verify-tidy
verify-tidy: ## Verify go.mod and tools/go.mod are tidy and pin the same Go
	$(GO) mod tidy -diff
	cd tools && $(GO) mod tidy -diff
	@[ "$$(grep -E '^(go|toolchain) ' go.mod)" = "$$(grep -E '^(go|toolchain) ' tools/go.mod)" ] || \
		{ echo "ERROR: go.mod and tools/go.mod pin different Go versions" >&2; exit 1; }

.PHONY: clean
clean: ## Remove build output
	rm -rf $(BUILD_DIR) $(SYSO)
