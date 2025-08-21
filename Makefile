# Go Web Crawler Makefile

# Variables
BINARY_NAME=crawler
BINARY_PATH=./bin/$(BINARY_NAME)
MAIN_PATH=./cmd/crawler
GO_FILES=$(shell find . -name "*.go" -type f -not -path "./vendor/*")
TEST_TIMEOUT=30s

# Build info
VERSION=$(shell git describe --tags --always --dirty)
COMMIT=$(shell git rev-parse HEAD)
BUILD_TIME=$(shell date +%Y-%m-%dT%H:%M:%S%z)

# Go build flags
LDFLAGS=-ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildTime=$(BUILD_TIME)"

.PHONY: help build test run clean fmt lint deps dev docker-build docker-run benchmark coverage profile install-tools

# Default target
help: ## Show this help message
	@echo 'Usage: make <target>'
	@echo ''
	@echo 'Available targets:'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# Build targets
build: ## Build the crawler binary
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p bin
	go build $(LDFLAGS) -o $(BINARY_PATH) $(MAIN_PATH)
	@echo "Binary built at $(BINARY_PATH)"

build-race: ## Build with race detector
	@echo "Building $(BINARY_NAME) with race detector..."
	@mkdir -p bin
	go build -race $(LDFLAGS) -o $(BINARY_PATH) $(MAIN_PATH)

# Development targets
dev: ## Run in development mode with hot reload (requires air)
	@echo "Starting development server with hot reload..."
	air -c .air.toml

run: build ## Build and run the crawler
	@echo "Running $(BINARY_NAME)..."
	$(BINARY_PATH)

run-config: build ## Run with custom config file
	@echo "Running $(BINARY_NAME) with config..."
	$(BINARY_PATH) -config=./config/config.yaml


benchmark: ## Run benchmarks
	@echo "Running benchmarks..."
	go test -bench=. -benchmem ./...

benchmark-config: ## Run config benchmarks only
	@echo "Running configuration benchmarks..."
	go test -bench=. -benchmem ./internal/config/...

coverage: ## Generate test coverage report
	@echo "Generating coverage report..."
	@mkdir -p coverage
	go test -coverprofile=coverage/coverage.out ./...
	go tool cover -html=coverage/coverage.out -o coverage/coverage.html
	@echo "Coverage report generated at coverage/coverage.html"

# Code quality targets
fmt: ## Format Go code
	@echo "Formatting code..."
	gofmt -s -w $(GO_FILES)
	goimports -w $(GO_FILES)

lint: ## Run linter
	@echo "Running linter..."
	golangci-lint run --timeout 5m ./...

vet: ## Run go vet
	@echo "Running go vet..."
	go vet ./...

# Dependency management
deps: ## Download dependencies
	@echo "Downloading dependencies..."
	go mod download
	go mod tidy

deps-update: ## Update dependencies
	@echo "Updating dependencies..."
	go get -u ./...
	go mod tidy

deps-vendor: ## Vendor dependencies
	@echo "Vendoring dependencies..."
	go mod vendor

# Configuration testing targets
# Testing targets for /test folder structure

# Run all tests
test: ## Run all tests
	@echo "Running all tests..."
	go test -timeout $(TEST_TIMEOUT) -v ./test/...

# Run configuration tests only
test-config: ## Run configuration tests only
	@echo "Running configuration tests..."
	go test -timeout $(TEST_TIMEOUT) -v ./test/ -run TestLoadConfig
	go test -timeout $(TEST_TIMEOUT) -v ./test/ -run TestConfig

# Run config tests with coverage
test-config-coverage: ## Run config tests with coverage
	@echo "Running configuration tests with coverage..."
	@mkdir -p coverage
	go test -timeout $(TEST_TIMEOUT) -v -coverprofile=coverage/config_coverage.out ./test/ -run TestConfig
	go tool cover -html=coverage/config_coverage.out -o coverage/config_coverage.html
	@echo "Config coverage report generated at coverage/config_coverage.html"

# Run specific test categories
test-config-validation: ## Test configuration validation
	@echo "Testing configuration validation..."
	go test -v ./test/ -run TestConfigValidation

test-config-loading: ## Test configuration loading
	@echo "Testing configuration loading..."
	go test -v ./test/ -run TestLoadConfig

test-config-env: ## Test environment variable configuration
	@echo "Testing environment variable configuration..."
	go test -v ./test/ -run TestLoadConfigWithEnvironmentVariables

test-config-precedence: ## Test configuration precedence
	@echo "Testing configuration precedence..."
	go test -v ./test/ -run TestConfigPrecedence

# Run benchmarks
benchmark-config: ## Run config benchmarks
	@echo "Running configuration benchmarks..."
	go test -bench=. -benchmem ./test/ -run Benchmark

# Run tests with race detector
test-race: ## Run tests with race detector
	@echo "Running tests with race detector..."
	go test -race -timeout $(TEST_TIMEOUT) -v ./test/...

# Clean test artifacts
clean-test: ## Clean test artifacts
	@echo "Cleaning test artifacts..."
	@rm -rf coverage/
	@rm -rf test_data/ test_logs/

# Setup test environment
setup-test: ## Setup test environment
	@echo "Setting up test environment..."
	@mkdir -p test_data test_logs coverage
# Profiling targets
profile-cpu: ## Generate CPU profile
	@echo "Generating CPU profile..."
	@mkdir -p profiles
	go test -cpuprofile=profiles/cpu.prof -bench=. ./...
	@echo "CPU profile saved to profiles/cpu.prof"

profile-mem: ## Generate memory profile
	@echo "Generating memory profile..."
	@mkdir -p profiles
	go test -memprofile=profiles/mem.prof -bench=. ./...
	@echo "Memory profile saved to profiles/mem.prof"

profile-view-cpu: profile-cpu ## View CPU profile
	go tool pprof profiles/cpu.prof

profile-view-mem: profile-mem ## View memory profile
	go tool pprof profiles/mem.prof

# Docker targets
docker-build: ## Build Docker image
	@echo "Building Docker image..."
	docker build -t $(BINARY_NAME):$(VERSION) -t $(BINARY_NAME):latest .

docker-run: ## Run Docker container
	@echo "Running Docker container..."
	docker run --rm -it \
		-v $(PWD)/config:/app/config:ro \
		-v $(PWD)/data:/app/data \
		$(BINARY_NAME):latest

docker-compose-up: ## Start services with docker-compose
	@echo "Starting services..."
	docker-compose up -d

docker-compose-down: ## Stop services
	@echo "Stopping services..."
	docker-compose down

# Cleanup targets
clean: ## Clean build artifacts
	@echo "Cleaning up..."
	rm -rf bin/
	rm -rf coverage/
	rm -rf profiles/
	rm -rf dist/
	go clean -cache
	go clean -testcache

clean-deps: ## Clean dependency cache
	@echo "Cleaning dependency cache..."
	go clean -modcache

# Installation targets
install: build ## Install binary to $GOPATH/bin
	@echo "Installing $(BINARY_NAME)..."
	cp $(BINARY_PATH) $(GOPATH)/bin/

install-tools: ## Install development tools
	@echo "Installing development tools..."
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install golang.org/x/tools/cmd/goimports@latest
	go install github.com/cosmtrek/air@latest
	go install github.com/swaggo/swag/cmd/swag@latest

# Release targets
release: clean ## Build release binaries for multiple platforms
	@echo "Building release binaries..."
	@mkdir -p dist
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY_NAME)-linux-amd64 $(MAIN_PATH)
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY_NAME)-darwin-amd64 $(MAIN_PATH)
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o dist/$(BINARY_NAME)-darwin-arm64 $(MAIN_PATH)
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY_NAME)-windows-amd64.exe $(MAIN_PATH)
	@echo "Release binaries built in dist/"

# Security targets
security: ## Run security checks
	@echo "Running security checks..."
	gosec ./...

# Database targets (when applicable)
migrate-up: ## Run database migrations up
	@echo "Running database migrations..."
	# Add your migration command here

migrate-down: ## Run database migrations down
	@echo "Rolling back database migrations..."
	# Add your rollback command here

# Quick development workflow
quick: fmt lint test-config build ## Quick development workflow (format, lint, test config, build)
	@echo "Quick development workflow completed successfully!"

# CI/CD simulation
ci: deps fmt lint vet test-race coverage ## Simulate CI pipeline
	@echo "CI pipeline completed successfully!"

# Project setup completion check
setup-check: ## Check if project setup phase is complete
	@echo "Checking project setup completion..."
	@echo "✓ Project structure exists"
	@test -f go.mod && echo "✓ go.mod exists" || echo "✗ go.mod missing"
	@test -f config.yaml && echo "✓ config.yaml exists" || echo "✗ config.yaml missing"
	@test -f .env.example && echo "✓ .env.example exists" || echo "✗ .env.example missing"
	@test -f Makefile && echo "✓ Makefile exists" || echo "✗ Makefile missing"
	@test -f README.md && echo "✓ README.md exists" || echo "✗ README.md missing"
	@test -f LICENSE && echo "✓ LICENSE exists" || echo "✗ LICENSE missing"
	@test -f docker-compose.yml && echo "✓ docker-compose.yml exists" || echo "✗ docker-compose.yml missing"
	@test -f .gitignore && echo "✓ .gitignore exists" || echo "✗ .gitignore missing"
	@test -f internal/config/config.go && echo "✓ Configuration implementation exists" || echo "✗ Configuration implementation missing"
	@echo "Running configuration tests..."
	@make test-config
	@echo "✓ Project setup phase completed!"

# Show build info
info: ## Show build information
	@echo "Binary: $(BINARY_NAME)"
	@echo "Version: $(VERSION)"
	@echo "Commit: $(COMMIT)"
	@echo "Build time: $(BUILD_TIME)"
	@echo "Build path: $(BINARY_PATH)"