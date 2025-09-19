# Load environment variables from .env file if it exists
-include .env
export

# New PHONY without lint targets
.PHONY: all build clean install check help fmt vet deps update build-roadrunner extract-binary run-roadrunner run-roadrunner-daemon stop-roadrunner logs shell clean-docker rebuild test-build dev-setup check-env

all: build

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod

# Docker and RoadRunner parameters
DOCKER_IMAGE_NAME ?= roadrunner-amqp1
DOCKER_TAG ?= latest
APP_VERSION ?= 2025.3.0
BUILD_TIME := $(shell date +%FT%T%z)

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
	@echo "  build         - Build the Go project"
	@echo "  deps          - Install Go dependencies"
	@echo "  update        - Update Go dependencies"
	@echo "  clean         - Clean Go build artifacts"
	@echo "  fmt           - Format Go code"
	@echo "  vet           - Vet Go code"
	@echo "  check         - Run all Go checks (fmt, vet)"
	@echo "  install       - Install the Go package"
	@echo ""
	@echo "RoadRunner Custom Build:"
	@echo "  build-roadrunner     - Build custom RoadRunner binary with AMQP1 plugin"
	@echo "  extract-binary       - Extract RoadRunner binary from Docker image"
	@echo "  run-roadrunner       - Run RoadRunner container interactively"
	@echo "  run-roadrunner-daemon - Run RoadRunner container as daemon"
	@echo "  stop-roadrunner      - Stop and remove RoadRunner daemon"
	@echo "  logs                 - Show RoadRunner container logs"
	@echo "  shell                - Get shell access to running container"
	@echo "  clean-docker         - Remove Docker image and binaries"
	@echo "  rebuild              - Clean and rebuild RoadRunner"
	@echo "  test-build           - Build and test RoadRunner binary"
	@echo "  dev-setup            - Set up development environment"
	@echo "  check-env            - Check environment variables"
	@echo "  help                 - Show this help"

# === RoadRunner Custom Build Targets ===

.PHONY: check-env
check-env: ## Check if required environment variables are set
	@echo "Checking environment variables..."
	@if [ -z "$(GITHUB_TOKEN)" ]; then \
		echo "❌ GITHUB_TOKEN is not set. Please check your .env file."; \
		exit 1; \
	fi
	@echo "✅ GITHUB_TOKEN is set"

.PHONY: dev-setup
dev-setup: ## Set up development environment
	@echo "Setting up development environment..."
	@if [ ! -f .env ]; then \
		echo "⚠️  .env file not found. Please create it with your GITHUB_TOKEN."; \
		exit 1; \
	fi
	@echo "✅ Development environment ready"

.PHONY: build-roadrunner
build-roadrunner: check-env ## Build custom RoadRunner binary with AMQP1 plugin using Docker
	@echo "Building RoadRunner with custom AMQP1 plugin..."
	docker build \
		--build-arg GITHUB_TOKEN="$(GITHUB_TOKEN)" \
		--build-arg APP_VERSION="$(APP_VERSION)" \
		--build-arg BUILD_TIME="$(BUILD_TIME)" \
		-t $(DOCKER_IMAGE_NAME):$(DOCKER_TAG) \
		-f Dockerfile .
	@echo "✅ Build complete! Image: $(DOCKER_IMAGE_NAME):$(DOCKER_TAG)"

.PHONY: extract-binary
extract-binary: ## Extract RoadRunner binary from Docker image to local filesystem
	@echo "Extracting RoadRunner binary from Docker image..."
	@mkdir -p ./bin
	docker create --name temp-container $(DOCKER_IMAGE_NAME):$(DOCKER_TAG)
	docker cp temp-container:/usr/bin/rr ./bin/rr
	docker rm temp-container
	chmod +x ./bin/rr
	./bin/rr --version
	@echo "✅ Binary extracted to ./bin/rr"

.PHONY: run-roadrunner
run-roadrunner: ## Run the custom RoadRunner container interactively
	@echo "Starting RoadRunner container..."
	docker run --rm -it \
		-p 8080:8080 \
		-v $(PWD):/app \
		-w /app \
		$(DOCKER_IMAGE_NAME):$(DOCKER_TAG)

.PHONY: run-roadrunner-daemon
run-roadrunner-daemon: ## Run RoadRunner container in daemon mode
	@echo "Starting RoadRunner container in daemon mode..."
	docker run -d \
		--name roadrunner-amqp1 \
		-p 8080:8080 \
		-v $(PWD):/app \
		-w /app \
		$(DOCKER_IMAGE_NAME):$(DOCKER_TAG)
	@echo "✅ Container started: roadrunner-amqp1"

.PHONY: stop-roadrunner
stop-roadrunner: ## Stop and remove RoadRunner daemon container
	@echo "Stopping RoadRunner container..."
	docker stop roadrunner-amqp1 || true
	docker rm roadrunner-amqp1 || true
	@echo "✅ Container stopped and removed"

.PHONY: logs
logs: ## Show logs from RoadRunner daemon container
	docker logs -f roadrunner-amqp1

.PHONY: shell
shell: ## Get shell access to running RoadRunner container
	docker exec -it roadrunner-amqp1 /bin/bash

.PHONY: clean-docker
clean-docker: ## Remove Docker image and extracted binary
	@echo "Cleaning up Docker resources..."
	docker rmi $(DOCKER_IMAGE_NAME):$(DOCKER_TAG) || true
	rm -rf ./bin
	@echo "✅ Docker cleanup complete"

.PHONY: rebuild
rebuild: clean-docker build-roadrunner ## Clean and rebuild everything

.PHONY: test-build
test-build: ## Build and test the custom RoadRunner binary
	@echo "Building and testing RoadRunner..."
	$(MAKE) build-roadrunner
	$(MAKE) extract-binary
	./bin/rr --version
	@echo "✅ Build test successful!"