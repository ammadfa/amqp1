.PHONY: all test lint build clean install check help install-lint fmt vet deps update
lint: install-lint

all: build

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
GOLINT=golangci-lint

# Build the project
build:
	$(GOBUILD) -v ./...

# Run tests
test:
	$(GOTEST) -v -coverprofile=coverage.out ./...

test-race:
	$(GOTEST) -v -race ./...

# Run tests with coverage report
test-coverage: test
	$(GOCMD) tool cover -html=coverage.out -o coverage.html

# Run linter
lint: ## Run golangci-lint (installs if missing)
	@command -v $(GOLINT) >/dev/null 2>&1 || { \
		echo "golangci-lint not found; run 'make install-lint' first"; exit 1; }
	$(GOLINT) run ./...

# Install dependencies
deps:
	$(GOMOD) download
	$(GOMOD) tidy

# Update dependencies
update:
	$(GOGET) -u ./...
	$(GOMOD) tidy

# Clean build artifacts
clean:
	$(GOCMD) clean
	rm -f coverage.out
	rm -f coverage.html

# Install golangci-lint
install-lint:
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(shell go env GOPATH)/bin v2.2.2

# Format code
fmt:
	$(GOCMD) fmt ./...

# Vet code
vet:
	$(GOCMD) vet ./...

# Run all checks
check: fmt vet lint test

# Install the package
install:
	$(GOCMD) install ./...

# Show help
help:
	@echo "Available targets:"
	@echo "  build         - Build the project"
	@echo "  test          - Run tests"
	@echo "  test-coverage - Run tests with coverage report"
	@echo "  lint          - Run linter"
	@echo "  deps          - Install dependencies"
	@echo "  update        - Update dependencies"
	@echo "  clean         - Clean build artifacts"
	@echo "  install-lint  - Install golangci-lint"
	@echo "  fmt           - Format code"
	@echo "  vet           - Vet code"
	@echo "  check         - Run all checks (fmt, vet, lint, test)"
	@echo "  install       - Install the package"
	@echo "  help          - Show this help"
