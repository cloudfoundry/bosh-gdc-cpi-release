# SPDX-FileCopyrightText: 2026 Google LLC
#
# SPDX-License-Identifier: Apache-2.0

REPO_ROOT     := $(shell dirname $(realpath $(lastword $(MAKEFILE_LIST))))
BIN_DIR       := $(REPO_ROOT)/bin
SRC_DIR       := $(REPO_ROOT)/src/bosh-gdc-cpi
TOOLS_DIR     := $(REPO_ROOT)/.tools
TOOLS_BIN_DIR := $(TOOLS_DIR)/bin
GO_DIR        := $(TOOLS_DIR)/go
export PATH   := $(abspath $(GO_DIR)/bin):$(abspath $(TOOLS_BIN_DIR)):$(PATH)

RELEASE_NAME  := bosh-gdc-cpi
RELEASE_ROOT_TGZ_FIND := find $(REPO_ROOT) -maxdepth 1 \( -name 'bosh-gdc-cpi-*.tgz' -o -name 'bosh_gdc_cpi_release_*.tgz' -o -name 'bosh_gdch_cpi_release_*.tgz' \)

GOLANGCI_LINT_VERSION ?= v2.4.0

LDFLAGS       ?= -s -w
GOFLAGS       ?= -trimpath -buildvcs=false
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-dev")

GO_VERSION    ?= 1.26.10
export GOTOOLCHAIN ?= local

# Verify Go version is at least 1.24 (required by go.mod 1.25.0)
GO_VERSION_OK := $(shell \
	if command -v go >/dev/null 2>&1; then \
		v=$$(go version 2>/dev/null | awk '{print $$3}' | sed 's/go//'); \
		major=$$(echo "$$v" | cut -d. -f1); \
		minor=$$(echo "$$v" | cut -d. -f2); \
		if [ "$$major" -gt 1 ] 2>/dev/null || ([ "$$major" -eq 1 ] 2>/dev/null && [ "$$minor" -ge 24 ] 2>/dev/null); then \
			echo "ok"; \
		fi; \
	fi)

ifeq ($(GO_VERSION_OK),)
export GOROOT := $(abspath $(GO_DIR))
endif

.PHONY: all ensure-go build build-linux build-darwin release-binaries test unittests test-race py-test integration-test pre-check format check check-license tidy release dev-release release-name-check release-clean release-hygiene vuln-scan coverage clean help

# Default target
all: ensure-go pre-check test build

# Ensure Go >= 1.24 is available; bootstrap hermetic Go toolchain into .tools/go if system Go is outdated or missing
ensure-go:
	@if [ -z "$(GO_VERSION_OK)" ] && [ ! -x "$(GO_DIR)/bin/go" ]; then \
		echo "[INFO] Go >= 1.24 not found in environment (found $$(command -v go >/dev/null 2>&1 && go version 2>/dev/null || echo 'none')). Downloading Go $(GO_VERSION)..."; \
		mkdir -p $(TOOLS_DIR); \
		curl -fsSL https://dl.google.com/go/go$(GO_VERSION).linux-amd64.tar.gz | tar -xz -C $(TOOLS_DIR); \
	fi

# Build CPI binaries for Linux and Darwin in bin/
build: ensure-go build-linux build-darwin

build-linux: ensure-go
	@mkdir -p $(BIN_DIR)
	@echo "[INFO] Compiling CPI binary for Linux (amd64)..."
	@CGO_ENABLED=0 GOOS=linux go build $(GOFLAGS) -ldflags '$(LDFLAGS) -X main.Version=$(VERSION)' -o $(BIN_DIR)/cpi $(REPO_ROOT)/bosh-cpi/cmd

build-darwin: ensure-go
	@mkdir -p $(BIN_DIR)
	@echo "[INFO] Compiling CPI binary for Darwin (amd64)..."
	@CGO_ENABLED=0 GOOS=darwin go build $(GOFLAGS) -ldflags '$(LDFLAGS) -X main.Version=$(VERSION)' -o $(BIN_DIR)/cpi_darwin $(REPO_ROOT)/bosh-cpi/cmd

# Prepare CPI binaries in src/bosh-gdc-cpi for BOSH packaging
release-binaries: ensure-go
	@mkdir -p $(SRC_DIR)
	@echo "[INFO] Preparing CPI binaries for BOSH packaging in $(SRC_DIR)..."
	@CGO_ENABLED=0 GOOS=linux go build $(GOFLAGS) -ldflags '$(LDFLAGS) -X main.Version=$(VERSION)' -o $(SRC_DIR)/cpi_binary_linux $(REPO_ROOT)/bosh-cpi/cmd
	@CGO_ENABLED=0 GOOS=darwin go build $(GOFLAGS) -ldflags '$(LDFLAGS) -X main.Version=$(VERSION)' -o $(SRC_DIR)/cpi_binary_darwin $(REPO_ROOT)/bosh-cpi/cmd

# Run unit tests across all packages (Go and Python release helpers)
test: ensure-go unittests py-test

unittests: ensure-go
	@echo "[INFO] Running unit tests..."
	@go test -timeout=5m ./bosh-cpi/pkg/...

test-race: ensure-go
	@echo "[INFO] Running unit tests with race detector..."
	@go test -race -timeout=5m ./bosh-cpi/pkg/...

py-test:
	@echo "[INFO] Running Python release script unit tests..."
	@command -v python3 >/dev/null 2>&1 || { echo "[ERROR] python3 is required for py-test"; exit 1; }
	@set -e; for t in $(REPO_ROOT)/scripts/*_test.py; do \
		echo "  == $$t"; \
		python3 "$$t"; \
	done

# Run integration tests against target environment
integration-test: ensure-go
	@echo "[INFO] Running integration tests..."
	@go test -v -timeout=3600s ./bosh-cpi/integration $(TEST_ARGS)

# Code formatting
format: ensure-go
	@if command -v goimports >/dev/null 2>&1; then \
		echo "[INFO] Running goimports..."; \
		goimports -w $(REPO_ROOT)/bosh-cpi; \
	elif command -v go >/dev/null 2>&1; then \
		echo "[INFO] Running go fmt..."; \
		go fmt ./bosh-cpi/...; \
	fi
	@if command -v shfmt >/dev/null 2>&1; then \
		echo "[INFO] Running shfmt..."; \
		find $(REPO_ROOT) -name "*.sh" -not -path "*/vendor/*" -not -path "*/.git/*" -not -path "*/.tools/*" -not -path "*/bin/*" | xargs -r shfmt -i 2 -w; \
	fi
	@echo "[INFO] Removing trailing whitespaces..."
	@find $(REPO_ROOT) -type f \( -name "*.go" -o -name "*.sh" -o -name "*.md" -o -name "*.py" -o -name "*.html" -o -name "*.css" \) \
		-not -path "*/vendor/*" -not -path "*/.git/*" -not -path "*/bin/*" -not -path "*/.tools/*" \
		-exec sed -i 's/[[:space:]]*$$//' {} +

# Pre-check validations (format, license checks, go mod tidy)
pre-check: ensure-go format check tidy

# Check code correctness (go vet, golangci-lint, py-test, and release hygiene)
check: ensure-go py-test release-hygiene
	@echo "[INFO] Running go vet..."
	@go vet ./bosh-cpi/...
	@if ! command -v golangci-lint >/dev/null 2>&1; then \
		echo "[INFO] Installing golangci-lint $(GOLANGCI_LINT_VERSION)..."; \
		mkdir -p $(TOOLS_BIN_DIR); \
		GOBIN=$(abspath $(TOOLS_BIN_DIR)) CGO_ENABLED=1 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION); \
	fi
	@echo "[INFO] Running golangci-lint..."
	@golangci-lint run --config=$(REPO_ROOT)/.golangci.yaml ./bosh-cpi/...

# Check and add license headers if addlicense is installed
check-license:
	@if command -v addlicense >/dev/null 2>&1; then \
		echo "[INFO] Running addlicense..."; \
		addlicense -v -ignore '**/vendor/**' -ignore '**/deps/**' -ignore '**/.tools/**' $(REPO_ROOT); \
	fi

# Clean and tidy Go module dependencies
tidy: ensure-go
	@if [ -f $(REPO_ROOT)/go.mod ]; then \
		echo "[INFO] Running go mod tidy..."; \
		go mod tidy; \
	fi

# Assert Makefile's RELEASE_NAME matches config/final.yml's name
release-name-check:
	@final="$$(bosh int $(REPO_ROOT)/config/final.yml --path /name 2>/dev/null || bosh int $(REPO_ROOT)/config/final.yml --path /final_name)"; \
	if [ "$$final" != "$(RELEASE_NAME)" ]; then \
		echo "[ERROR] Makefile RELEASE_NAME ($(RELEASE_NAME)) does not match config/final.yml name ($$final)" >&2; \
		exit 1; \
	fi

# Build a dev BOSH release tarball under dev_releases/$(RELEASE_NAME)/
dev-release: release-name-check
	@$(REPO_ROOT)/scripts/create-release dev

# Build a versioned BOSH release tarball under releases/$(RELEASE_NAME)/
release: release-name-check
	@if [ -z "$(VERSION)" ] || [ "$(VERSION)" = "dev" ]; then \
		echo "[ERROR] VERSION= required for a versioned release (e.g., make release VERSION=0.1.0)" >&2; \
		echo "For a dev tarball use: make dev-release" >&2; \
		exit 1; \
	fi
	@$(REPO_ROOT)/scripts/create-release $(VERSION)

# Remove bin/, dev_releases/, and built tarballs/reports under releases/
# (the committed final-release manifests in releases/ and .final_builds/ are kept: bosh.io needs them)
release-clean:
	@rm -rf $(BIN_DIR) $(REPO_ROOT)/dev_releases
	@find $(REPO_ROOT)/releases -type f \( -name '*.tgz' -o -name '*.tgz.sha256' -o -name 'vulnerability-report-*.md' \) -delete 2>/dev/null || true
	@echo "[INFO] Release artifacts cleaned"

# Assert no loose CPI release tarballs exist at the repo root
release-hygiene:
	@found="$$($(RELEASE_ROOT_TGZ_FIND))"; \
	if [ -n "$$found" ]; then \
		echo "[ERROR] Loose release tarballs found at repo root:" >&2; \
		echo "$$found" >&2; \
		exit 1; \
	fi

# Run vulnerability scan on compiled CPI binary and render the per-module report
# (fails when a finding has a published fix; set VULN_FAIL_ON_FIXABLE=false to report only)
VULN_FAIL_ON_FIXABLE ?= true
vuln-scan: ensure-go build-linux
	@echo "[INFO] Running govulncheck on $(BIN_DIR)/cpi..."
	@if ! command -v govulncheck >/dev/null 2>&1; then \
		echo "[INFO] Installing govulncheck..."; \
		mkdir -p $(TOOLS_BIN_DIR); \
		GOBIN=$(abspath $(TOOLS_BIN_DIR)) go install golang.org/x/vuln/cmd/govulncheck@latest; \
	fi
	@govulncheck -mode=binary -json $(BIN_DIR)/cpi > $(BIN_DIR)/govulncheck.json || true
	@python3 $(REPO_ROOT)/scripts/_vuln_report.py $(BIN_DIR)/govulncheck.json \
		--version "$(VERSION)" --go-version "$$(go version $(BIN_DIR)/cpi | awk '{print $$2}')" \
		--output $(BIN_DIR)/vulnerability-report.md $(if $(filter true,$(VULN_FAIL_ON_FIXABLE)),--fail-on-fixable,)
	@cat $(BIN_DIR)/vulnerability-report.md

# Generate code coverage report
coverage: ensure-go
	@mkdir -p $(REPO_ROOT)/coverage
	@echo "[INFO] Generating test coverage report..."
	@go test -coverprofile=$(REPO_ROOT)/coverage/coverage.out ./bosh-cpi/pkg/...
	@go tool cover -html=$(REPO_ROOT)/coverage/coverage.out -o $(REPO_ROOT)/coverage/coverage.html
	@echo "[INFO] Coverage report available at: $(REPO_ROOT)/coverage/coverage.html"

# Clean build artifacts and temporary files
clean: release-clean
	@rm -rf $(SRC_DIR) $(TOOLS_DIR) $(REPO_ROOT)/coverage $(REPO_ROOT)/.dev_builds $(REPO_ROOT)/tmp

# Display available targets
help:
	@echo "GDC BOSH CPI Build System"
	@echo "========================="
	@echo "Available make targets:"
	@echo "  make all               - Run pre-check, unit tests, and build binaries"
	@echo "  make build             - Compile CPI binaries for Linux and Darwin in bin/"
	@echo "  make build-linux       - Compile Linux CPI binary (bin/cpi)"
	@echo "  make build-darwin      - Compile Darwin CPI binary (bin/cpi_darwin)"
	@echo "  make release-binaries  - Compile CPI binaries into src/bosh-gdc-cpi for BOSH release"
	@echo "  make test              - Run Go and Python unit tests"
	@echo "  make py-test           - Run Python SemVer and release artifact unit tests"
	@echo "  make integration-test  - Run integration tests (use TEST_ARGS for flags/filters)"
	@echo "  make pre-check         - Run code formatting, go vet, and go mod tidy"
	@echo "  make format            - Format code (goimports, shfmt, whitespace cleanup)"
	@echo "  make check             - Run go vet, golangci-lint, py-test, and release-hygiene"
	@echo "  make tidy              - Run go mod tidy"
	@echo "  make dev-release       - Build a dev BOSH release tarball under dev_releases/"
	@echo "  make release           - Build a versioned BOSH release tarball (VERSION=X.Y.Z)"
	@echo "  make release-clean     - Clean bin/, dev_releases/, and built tarballs under releases/ (keeps final-release manifests)"
	@echo "  make release-hygiene   - Verify no loose release tarballs exist at repo root"
	@echo "  make vuln-scan         - Run govulncheck vulnerability scan on compiled binary"
	@echo "  make coverage          - Generate HTML test coverage report"
	@echo "  make clean             - Clean built binaries and temporary artifacts"
