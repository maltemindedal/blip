# Makefile for blip
# 
# This Makefile provides convenient targets for development, testing, 
# security scanning, and building the application.

# Use PowerShell on Windows
ifeq ($(OS),Windows_NT)
SHELL := pwsh.exe
.SHELLFLAGS := -NoProfile -Command
endif

.PHONY: help build build-raw clean test test-unit test-integration test-coverage test-coverage-unit test-coverage-integration lint lint-fix security-scan deps-check deps-update deps-graph license-check bench race docs run dev fmt vet all ci-local install-tools docker-build docker-run build-linux build-linux-arm64 build-darwin build-darwin-arm64 build-windows build-windows-arm64 build-all

# Default target
.DEFAULT_GOAL := help

# Variables
BINARY_NAME=blip
BUILD_DIR=./bin
MAIN_PATH=./cmd/server
GO_FILES=$(shell find . -name '*.go' -not -path './vendor/*' 2>/dev/null || dir /s /b *.go 2>nul | findstr /v "\\vendor\\")
COVERAGE_FILE=coverage.out
COVERAGE_HTML=coverage.html

# Platform-specific binary name
ifeq ($(OS),Windows_NT)
BINARY=$(BINARY_NAME).exe
else
BINARY=$(BINARY_NAME)
endif

# Platform-specific directories
LINUX_DIR=$(BUILD_DIR)/linux
DARWIN_DIR=$(BUILD_DIR)/MacOS
WINDOWS_DIR=$(BUILD_DIR)/windows

# Build information
ifeq ($(OS),Windows_NT)
VERSION ?= dev
COMMIT ?= unknown
BUILD_TIME ?= $(shell (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ'))
else
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
endif

# Build flags
STAMP=-X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuildTime=$(BUILD_TIME)
LDFLAGS=-ldflags "$(STAMP)"
# Release builds additionally strip the symbol table and DWARF data.
RELEASE_LDFLAGS=-ldflags "$(STAMP) -s -w"

## help: Show this help message
help:
	@echo "Available targets:"
	@sed -n 's/^##//p' $(MAKEFILE_LIST) | column -t -s ':' | sed -e 's/^/ /'

## build: Build the application binary for current platform (with fmt and vet checks)
build: fmt vet
ifeq ($(OS),Windows_NT)
	Write-Host "Building $(BINARY) for current platform..."
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY) $(MAIN_PATH)
	Write-Host "Binary built: $(BUILD_DIR)/$(BINARY)"
else
	@echo "Building $(BINARY) for current platform..."
	@go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY) $(MAIN_PATH)
	@echo "Binary built: $(BUILD_DIR)/$(BINARY)"
endif

## build-raw: Build the application binary without running static checks
build-raw:
ifeq ($(OS),Windows_NT)
	Write-Host "Building $(BINARY) for current platform (skipping checks)..."
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY) $(MAIN_PATH)
	Write-Host "Binary built: $(BUILD_DIR)/$(BINARY)"
else
	@echo "Building $(BINARY) for current platform (skipping checks)..."
	@go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY) $(MAIN_PATH)
	@echo "Binary built: $(BUILD_DIR)/$(BINARY)"
endif

## clean: Remove build artifacts and temporary files
clean:
ifeq ($(OS),Windows_NT)
	Write-Host "Cleaning up..."
	if (Test-Path $(BUILD_DIR)) { Remove-Item -Recurse -Force $(BUILD_DIR) }
	go clean -cache -testcache -modcache
	Write-Host "Clean completed"
else
	@echo "Cleaning up..."
	@rm -rf $(BUILD_DIR)
	@go clean -cache -testcache -modcache
	@echo "Clean completed"
endif

## test: Run all tests
test:
	@echo "Running tests..."
	go test -v -race ./...

## test-unit: Run unit tests only
test-unit:
	@echo "Running unit tests..."
	go test -v -race ./test/unit/...

## test-integration: Run integration tests only
test-integration:
	@echo "Running integration tests..."
	go test -v -race ./test/integration/...

## test-coverage: Run tests with coverage report
test-coverage:
	@echo "Running tests with coverage..."
	go test -race -coverpkg=./cmd/...,./internal/... -coverprofile=$(COVERAGE_FILE) ./...
	go tool cover -html=$(COVERAGE_FILE) -o $(COVERAGE_HTML)
	@echo "Coverage report generated: $(COVERAGE_HTML)"
	go tool cover -func=$(COVERAGE_FILE)

## test-coverage-unit: Run unit tests with coverage report
test-coverage-unit:
	@echo "Running unit tests with coverage..."
	go test -v -race -coverpkg=./cmd/...,./internal/... -coverprofile=unit-$(COVERAGE_FILE) ./test/unit/...
	go tool cover -html=unit-$(COVERAGE_FILE) -o unit-$(COVERAGE_HTML)
	@echo "Unit test coverage report generated: unit-$(COVERAGE_HTML)"
	go tool cover -func=unit-$(COVERAGE_FILE)

## test-coverage-integration: Run integration tests with coverage report
test-coverage-integration:
	@echo "Running integration tests with coverage..."
	go test -v -race -coverpkg=./cmd/...,./internal/... -coverprofile=integration-$(COVERAGE_FILE) ./test/integration/...
	go tool cover -html=integration-$(COVERAGE_FILE) -o integration-$(COVERAGE_HTML)
	@echo "Integration test coverage report generated: integration-$(COVERAGE_HTML)"
	go tool cover -func=integration-$(COVERAGE_FILE)

## lint: Run golangci-lint
lint:
	@echo "Running golangci-lint..."
	golangci-lint run --config .golangci.yml

## lint-fix: Run golangci-lint with auto-fix
lint-fix:
	@echo "Running golangci-lint with auto-fix..."
	golangci-lint run --config .golangci.yml --fix

## security-scan: Run security vulnerability scans
security-scan:
	@echo "Running security scans..."
	@echo "1. Running govulncheck..."
	govulncheck ./...
	@echo "2. Running gosec..."
	gosec ./...

## deps-check: Check for outdated dependencies
deps-check:
	@echo "Checking dependencies..."
	@echo "Current dependencies:"
	go list -u -m all
	@echo ""
	@echo "Checking for vulnerabilities in dependencies..."
	govulncheck ./...

## deps-update: Update dependencies
deps-update:
	@echo "Updating dependencies..."
	go get -u ./...
	go mod tidy
	@echo "Dependencies updated"

## run: Build and run the application
run: build
ifeq ($(OS),Windows_NT)
	Write-Host "Running $(BINARY)..."
	$(BUILD_DIR)/$(BINARY)
else
	@echo "Running $(BINARY)..."
	$(BUILD_DIR)/$(BINARY)
endif

## dev: Run the application in development mode (with auto-restart)
dev:
	@echo "Starting development server..."
	air

## fmt: Format Go code
fmt:
	@echo Formatting code...
	@go fmt ./...

## vet: Run go vet
vet:
	@echo Running go vet...
	@go vet ./...

## all: Run all checks and build
all: clean fmt vet lint test build
	@echo "All checks completed successfully!"

## ci-local: Run the same checks as CI pipeline locally
ci-local: clean fmt vet lint test-coverage security-scan deps-check build
	@echo "Local CI pipeline completed successfully!"

## install-tools: Install development tools
install-tools:
	@echo "Installing development tools..."
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2
	go install golang.org/x/vuln/cmd/govulncheck@v1.8.0
	go install github.com/securego/gosec/v2/cmd/gosec@v2.29.0
	go install golang.org/x/tools/cmd/goimports@v0.50.0
	go install github.com/air-verse/air@v1.67.4
	@echo "Development tools installed"

## docker-build: Build Docker image
docker-build:
	@echo "Building Docker image..."
	docker build -t $(BINARY_NAME):$(VERSION) .
	docker tag $(BINARY_NAME):$(VERSION) $(BINARY_NAME):latest
	@echo "Docker image built: $(BINARY_NAME):$(VERSION)"

## docker-run: Run application in Docker container
docker-run: docker-build
	@echo "Running Docker container..."
	docker run --rm -p 8080:8080 $(BINARY_NAME):latest

# Generate mod graph for dependency analysis
## deps-graph: Generate dependency graph
deps-graph:
	@echo "Generating dependency graph..."
	go mod graph | dot -T png -o deps-graph.png
	@echo "Dependency graph saved as deps-graph.png"

# Check for license compatibility
## license-check: Check licenses of dependencies
license-check:
	@echo "Checking dependency licenses..."
	@which go-licenses > /dev/null || go install github.com/google/go-licenses@latest
	go-licenses report ./... --template licenses.tpl > licenses.txt || true
	@echo "License report saved to licenses.txt"

# Performance benchmarks
## bench: Run performance benchmarks
bench:
	@echo "Running benchmarks..."
	go test -bench=. -benchmem ./...

# Check for potential race conditions
## race: Run tests with race detection
race:
	@echo "Running race detection..."
	go test -race ./...

# Generate documentation
## docs: Generate documentation
docs:
	@echo "Generating documentation..."
	@which godoc > /dev/null || go install golang.org/x/tools/cmd/godoc@latest
	@echo "Documentation server will be available at http://localhost:6060"
	godoc -http=:6060

# Cross-platform build targets
## build-linux: Build for Linux (amd64)
build-linux:
ifeq ($(OS),Windows_NT)
	if (-not (Test-Path $(LINUX_DIR))) { New-Item -ItemType Directory -Force -Path $(LINUX_DIR) }
	Write-Host "Building for Linux (amd64)..."
	$$env:CGO_ENABLED='0'; $$env:GOOS='linux'; $$env:GOARCH='amd64'; go build $(LDFLAGS) -o $(LINUX_DIR)/$(BINARY_NAME)-amd64 $(MAIN_PATH)
	Write-Host "Linux binary built: $(LINUX_DIR)/$(BINARY_NAME)-amd64"
else
	@mkdir -p $(LINUX_DIR)
	@echo "Building for Linux (amd64)..."
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(LINUX_DIR)/$(BINARY_NAME)-amd64 $(MAIN_PATH)
	@echo "Linux binary built: $(LINUX_DIR)/$(BINARY_NAME)-amd64"
endif

## build-linux-arm64: Build for Linux (arm64)
build-linux-arm64:
ifeq ($(OS),Windows_NT)
	if (-not (Test-Path $(LINUX_DIR))) { New-Item -ItemType Directory -Force -Path $(LINUX_DIR) }
	Write-Host "Building for Linux (arm64)..."
	$$env:CGO_ENABLED='0'; $$env:GOOS='linux'; $$env:GOARCH='arm64'; go build $(LDFLAGS) -o $(LINUX_DIR)/$(BINARY_NAME)-arm64 $(MAIN_PATH)
	Write-Host "Linux ARM64 binary built: $(LINUX_DIR)/$(BINARY_NAME)-arm64"
else
	@mkdir -p $(LINUX_DIR)
	@echo "Building for Linux (arm64)..."
	@CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(LINUX_DIR)/$(BINARY_NAME)-arm64 $(MAIN_PATH)
	@echo "Linux ARM64 binary built: $(LINUX_DIR)/$(BINARY_NAME)-arm64"
endif

## build-darwin: Build for macOS (Intel)
build-darwin:
ifeq ($(OS),Windows_NT)
	if (-not (Test-Path $(DARWIN_DIR))) { New-Item -ItemType Directory -Force -Path $(DARWIN_DIR) }
	Write-Host "Building for macOS (Intel)..."
	$$env:CGO_ENABLED='0'; $$env:GOOS='darwin'; $$env:GOARCH='amd64'; go build $(LDFLAGS) -o $(DARWIN_DIR)/$(BINARY_NAME)-amd64 $(MAIN_PATH)
	Write-Host "macOS Intel binary built: $(DARWIN_DIR)/$(BINARY_NAME)-amd64"
else
	@mkdir -p $(DARWIN_DIR)
	@echo "Building for macOS (Intel)..."
	@CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(DARWIN_DIR)/$(BINARY_NAME)-amd64 $(MAIN_PATH)
	@echo "macOS Intel binary built: $(DARWIN_DIR)/$(BINARY_NAME)-amd64"
endif

## build-darwin-arm64: Build for macOS (Apple Silicon)
build-darwin-arm64:
ifeq ($(OS),Windows_NT)
	if (-not (Test-Path $(DARWIN_DIR))) { New-Item -ItemType Directory -Force -Path $(DARWIN_DIR) }
	Write-Host "Building for macOS (Apple Silicon)..."
	$$env:CGO_ENABLED='0'; $$env:GOOS='darwin'; $$env:GOARCH='arm64'; go build $(LDFLAGS) -o $(DARWIN_DIR)/$(BINARY_NAME)-arm64 $(MAIN_PATH)
	Write-Host "macOS ARM64 binary built: $(DARWIN_DIR)/$(BINARY_NAME)-arm64"
else
	@mkdir -p $(DARWIN_DIR)
	@echo "Building for macOS (Apple Silicon)..."
	@CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(DARWIN_DIR)/$(BINARY_NAME)-arm64 $(MAIN_PATH)
	@echo "macOS ARM64 binary built: $(DARWIN_DIR)/$(BINARY_NAME)-arm64"
endif

## build-windows: Build for Windows (amd64)
build-windows:
ifeq ($(OS),Windows_NT)
	if (-not (Test-Path $(WINDOWS_DIR))) { New-Item -ItemType Directory -Force -Path $(WINDOWS_DIR) }
	Write-Host "Building for Windows (amd64)..."
	$$env:CGO_ENABLED='0'; $$env:GOOS='windows'; $$env:GOARCH='amd64'; go build $(LDFLAGS) -o $(WINDOWS_DIR)/$(BINARY_NAME)-amd64.exe $(MAIN_PATH)
	Write-Host "Windows binary built: $(WINDOWS_DIR)/$(BINARY_NAME)-amd64.exe"
else
	@mkdir -p $(WINDOWS_DIR)
	@echo "Building for Windows (amd64)..."
	@CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(WINDOWS_DIR)/$(BINARY_NAME)-amd64.exe $(MAIN_PATH)
	@echo "Windows binary built: $(WINDOWS_DIR)/$(BINARY_NAME)-amd64.exe"
endif

## build-windows-arm64: Build for Windows (ARM64)
build-windows-arm64:
ifeq ($(OS),Windows_NT)
	if (-not (Test-Path $(WINDOWS_DIR))) { New-Item -ItemType Directory -Force -Path $(WINDOWS_DIR) }
	Write-Host "Building for Windows (ARM64)..."
	$$env:CGO_ENABLED='0'; $$env:GOOS='windows'; $$env:GOARCH='arm64'; go build $(LDFLAGS) -o $(WINDOWS_DIR)/$(BINARY_NAME)-arm64.exe $(MAIN_PATH)
	Write-Host "Windows ARM64 binary built: $(WINDOWS_DIR)/$(BINARY_NAME)-arm64.exe"
else
	@mkdir -p $(WINDOWS_DIR)
	@echo "Building for Windows (ARM64)..."
	@CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build $(LDFLAGS) -o $(WINDOWS_DIR)/$(BINARY_NAME)-arm64.exe $(MAIN_PATH)
	@echo "Windows ARM64 binary built: $(WINDOWS_DIR)/$(BINARY_NAME)-arm64.exe"
endif

## build-all: Build for all supported platforms
build-all: build-linux build-linux-arm64 build-darwin build-darwin-arm64 build-windows build-windows-arm64
	@echo All platform binaries built successfully!

## build-current: Build for current platform
build-current:
	@echo Building for current platform...
	@CGO_ENABLED=0 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)$(shell go env GOEXE) $(MAIN_PATH)
	@echo Binary built for current platform: $(BUILD_DIR)/$(BINARY_NAME)$(shell go env GOEXE)

# Create release build
## release: Create optimized release build for all platforms
release: clean fmt vet lint test
	@echo Creating release builds...
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(RELEASE_LDFLAGS) -trimpath -o $(LINUX_DIR)/$(BINARY_NAME)-amd64 $(MAIN_PATH)
	@CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(RELEASE_LDFLAGS) -trimpath -o $(LINUX_DIR)/$(BINARY_NAME)-arm64 $(MAIN_PATH)
	@CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build $(RELEASE_LDFLAGS) -trimpath -o $(DARWIN_DIR)/$(BINARY_NAME)-amd64 $(MAIN_PATH)
	@CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(RELEASE_LDFLAGS) -trimpath -o $(DARWIN_DIR)/$(BINARY_NAME)-arm64 $(MAIN_PATH)
	@CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(RELEASE_LDFLAGS) -trimpath -o $(WINDOWS_DIR)/$(BINARY_NAME)-amd64.exe $(MAIN_PATH)
	@CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build $(RELEASE_LDFLAGS) -trimpath -o $(WINDOWS_DIR)/$(BINARY_NAME)-arm64.exe $(MAIN_PATH)
	@echo Release builds created in $(BUILD_DIR)/
	@echo Creating checksums...
	@cd $(LINUX_DIR) && (sha256sum * > checksums.txt 2>/dev/null || shasum -a 256 * > checksums.txt)
	@cd $(DARWIN_DIR) && (sha256sum * > checksums.txt 2>/dev/null || shasum -a 256 * > checksums.txt)
	@cd $(WINDOWS_DIR) && (sha256sum * > checksums.txt 2>/dev/null || shasum -a 256 * > checksums.txt)
	@echo Checksums saved to each platform directory

## list-platforms: List all supported GOOS/GOARCH combinations
list-platforms:
	@echo Supported platforms:
	@go tool dist list