# New PHONY without lint targets
.PHONY: all build clean install check help fmt vet deps update

all: build

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod

# Build the project
build:
	$(GOBUILD) -v ./...
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

# (lint removed) - golangci-lint targets removed from this Makefile

# Format code
fmt:
	$(GOCMD) fmt ./...

# Vet code
vet:
	$(GOCMD) vet ./...

# Run all checks (without tests)
check: fmt vet

# Install the package
install:
	$(GOCMD) install ./...

# Show help
help:
	@echo "Available targets:"
	@echo "  build         - Build the project"
	@echo "  deps          - Install dependencies"
	@echo "  update        - Update dependencies"
	@echo "  clean         - Clean build artifacts"
	@echo "  fmt           - Format code"
	@echo "  vet           - Vet code"
	@echo "  check         - Run all checks (fmt, vet)"
	@echo "  install       - Install the package"
	@echo "  help          - Show this help"