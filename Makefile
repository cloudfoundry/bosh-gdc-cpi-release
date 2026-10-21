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

GOLANGCI_LINT_VERSION ?= v2.4.0

LDFLAGS       ?= -s -w
GOFLAGS       ?= -trimpath -buildvcs=false
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-dev")

GO_VERSION    ?= 1.25.8
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

.PHONY: all ensure-go build build-linux build-darwin release-binaries test unittests test-race integration-test pre-check format check check-license tidy release vuln-scan coverage clean help

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

# Run unit tests across all packages
test: ensure-go unittests

unittests: ensure-go
	@echo "[INFO] Running unit tests..."
	@go test -timeout=5m ./bosh-cpi/pkg/...

test-race: ensure-go
	@echo "[INFO] Running unit tests with race detector..."
	@go test -race -timeout=5m ./bosh-cpi/pkg/...

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

# Check code correctness (go vet and golangci-lint)
check: ensure-go
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

# Build BOSH release tarball
release: release-binaries
	@if ! command -v bosh >/dev/null 2>&1; then \
		echo "[ERROR] 'bosh' CLI is required to create a release. Please install bosh CLI."; \
		exit 1; \
	fi
	@mkdir -p $(BIN_DIR)
	@echo "[INFO] Creating BOSH CPI release tarball..."
	@bosh create-release --force --tarball=$(BIN_DIR)/bosh_gdc_cpi_release_$(VERSION).tgz --version $(VERSION)
	@echo "[INFO] Release tarball created: $(BIN_DIR)/bosh_gdc_cpi_release_$(VERSION).tgz"

# Run vulnerability scan on compiled CPI binary
vuln-scan: ensure-go build-linux
	@echo "[INFO] Running govulncheck on $(BIN_DIR)/cpi..."
	@if command -v govulncheck >/dev/null 2>&1; then \
		govulncheck -mode=binary $(BIN_DIR)/cpi; \
	else \
		go run golang.org/x/vuln/cmd/govulncheck@latest -mode=binary $(BIN_DIR)/cpi; \
	fi

# Generate code coverage report
coverage: ensure-go
	@mkdir -p $(REPO_ROOT)/coverage
	@echo "[INFO] Generating test coverage report..."
	@go test -coverprofile=$(REPO_ROOT)/coverage/coverage.out ./bosh-cpi/pkg/...
	@go tool cover -html=$(REPO_ROOT)/coverage/coverage.out -o $(REPO_ROOT)/coverage/coverage.html
	@echo "[INFO] Coverage report available at: $(REPO_ROOT)/coverage/coverage.html"

# Clean build artifacts and temporary files
clean:
	@rm -rf $(BIN_DIR) $(SRC_DIR) $(TOOLS_DIR) $(REPO_ROOT)/coverage $(REPO_ROOT)/.dev_builds $(REPO_ROOT)/dev_releases $(REPO_ROOT)/tmp

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
	@echo "  make test              - Run unit tests across all packages"
	@echo "  make integration-test  - Run integration tests (use TEST_ARGS for flags/filters)"
	@echo "  make pre-check         - Run code formatting, go vet, and go mod tidy"
	@echo "  make format            - Format code (goimports, shfmt, whitespace cleanup)"
	@echo "  make check             - Run go vet (and golangci-lint if installed)"
	@echo "  make tidy              - Run go mod tidy"
	@echo "  make release           - Build CPI binaries and create BOSH release tarball"
	@echo "  make vuln-scan         - Run govulncheck vulnerability scan on compiled binary"
	@echo "  make coverage          - Generate HTML test coverage report"
	@echo "  make clean             - Clean built binaries and temporary artifacts"
