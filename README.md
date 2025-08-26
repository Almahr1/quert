# Go Web Crawler

A high-performance (hopefully), semi-below-garbage-grade web crawler built in Go designed specifically for collecting and processing text data for Large Language Model training pipelines (among other things).

[![Go Version](https://img.shields.io/badge/go-1.21+-blue.svg)](https://golang.org)
[![License](https://img.shields.io/badge/license-GPL%20v3-blue.svg)](LICENSE)

## Notice
- **Project Status**:
  The project is still in early development, much of the features spoken about here are 
  conceptual and haven't been implemented yet. Bear with us.
- **RATE LIMITTING**:
  For the sake of not getting blocked by almost every website
  please don't use the crawler until I get rate limitting implemented completely.


## Development Transparency

This project was built by humans, augmented by AI. I believe in using the best tools for the job:

*   **Human-Led:** The core architecture, design decisions, complex logic, and final implementation were meticulously planned and executed by a human developer.
*   **AI-Assisted:** To boost productivity, I utilized AI (primarily LLMs) for generating repetitive boilerplate code, comprehensive test cases, and initial drafts of documentation. All AI-generated content was rigorously reviewed, tested, and adapted to fit the project's standards.

This approach allowed me to focus my creative energy on solving hard problems rather than repetitive tasks.

## Features

**Core Crawling Capabilities**
- High-performance concurrent crawling: 10K-50K pages/hour on a single machine
- Intelligent URL management with priority and politeness queues
- Full robots.txt compliance and adaptive rate limiting
- Circuit breaker protection with automatic failure detection

**Content Processing**
- LLM-optimized text extraction with advanced boilerplate removal
- Multi-level deduplication: URL, content hash, and semantic similarity
- Perplexity-based quality filtering and multi-dimensional scoring
- Smart semantic segmentation for optimal training data chunks

**Production Ready**
- Comprehensive Prometheus metrics and structured logging
- Graceful shutdown, error recovery, and resource management
- Multiple storage backends (PostgreSQL, BadgerDB, file)
- Docker support with containerized deployment

## Table of Contents

- [Quick Start](#quick-start)
- [Installation](#installation)
- [Configuration](#configuration)
- [Usage](#usage)
- [Architecture](#architecture)
- [Development](#development)
- [Performance](#performance)
- [Contributing](#contributing)
- [License](#license)

## Quick Start

### Prerequisites

- **Go 1.21+** - [Download and install Go](https://golang.org/dl/)
- **Git** - For cloning the repository
- **Docker** (optional) - For containerized development

### Installation

```bash
# Clone the repository
git clone https://github.com/Almahr1/quert.git
cd crawler

# Install dependencies
make deps

# Install development tools
make install-tools

# Build the crawler
make build

# Run with default configuration
make run
```

### Quick Test Run

```bash
# Run a quick crawl of a few pages
./bin/crawler -url "https://example.com" -max-pages 100 -output ./data/

# Check the results
ls -la ./data/
```

## Installation

### From Source

```bash
# Install directly from source
go install github.com/Almahr1/quert/cmd/crawler@latest

# Or clone and build
git clone https://github.com/Almahr1/quert.git
cd crawler
make build
make install
```

### Using Docker

```bash
# Build Docker image
make docker-build

# Run with Docker
docker run --rm -v $(pwd)/config:/app/config:ro -v $(pwd)/data:/app/data crawler:latest

# Or use docker-compose for full stack
make docker-compose-up
```

### Pre-built Binaries

Download the latest release from the [releases page](https://github.com/Almahr1/quert/releases). **(PLACEHOLDER)**

## Configuration

The crawler uses a flexible configuration system supporting YAML files, JSON, and environment variables.

### Basic Configuration

This is the provided `config.yaml` file:

```yaml
Will Be Updated Soon...
```

### Environment Variables

All configuration options can be overridden with environment variables:

```bash

```

### Advanced Configuration

For production deployments, see the [complete configuration reference](docs/configuration.md). **(PLACEHOLDER)**

## Usage

### Command Line Interface

```bash
# Basic usage
crawler -config config.yaml

# Override specific settings
crawler -config config.yaml -max-pages 1000 -workers 5

# Crawl specific URLs
crawler -url "https://example.com" -url "https://another-site.com"

# Read URLs from file
crawler -url-file urls.txt

# Enable debug logging
crawler -config config.yaml -log-level debug

# Run with profiling enabled
crawler -config config.yaml -enable-profiling -profile-port 6060
```

### Programmatic Usage

```go
package main

import (
    "context"
    "log"
    
    "github.com/Almahr1/quert/pkg/crawler"
    "github.com/Almahr1/quert/pkg/config"
)

func main() {
    // Load configuration
    cfg, err := config.Load("config.yaml")
    if err != nil {
        log.Fatal("Failed to load config:", err)
    }
    
    // Create crawler instance
    c, err := crawler.New(cfg)
    if err != nil {
        log.Fatal("Failed to create crawler:", err)
    }
    
    // Add seed URLs
    c.AddURL("https://example.com")
    c.AddURL("https://another-site.com")
    
    // Start crawling
    ctx := context.Background()
    if err := c.Run(ctx); err != nil {
        log.Fatal("Crawling failed:", err)
    }
    
    // Get results
    stats := c.GetStats()
    log.Printf("Crawled %d pages, extracted %d documents", 
        stats.PagesCrawled, stats.DocumentsExtracted)
}
```

## Architecture

### High-Level Overview

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   URL Frontier  │────│ Crawling Engine  │────│Content Pipeline │
│                 │    │                  │    │                 │
│ • Priority Queue│    │ • Worker Pool    │    │ • Text Extract  │
│ • Politeness    │    │ • Rate Limiter   │    │ • Quality Score │
│ • Deduplication │    │ • Circuit Breaker│    │ • Deduplication │
└─────────────────┘    └──────────────────┘    └─────────────────┘
                                │
                       ┌─────────────────┐
                       │  Storage Layer  │
                       │                 │
                       │ • Batch Writer  │
                       │ • Compression   │
                       │ • Multiple DBs  │
                       └─────────────────┘
```

### Core Components

- **URL Frontier**: Manages URL discovery, prioritization, and politeness
- **Crawling Engine**: Handles HTTP requests with rate limiting and error handling
- **Content Pipeline**: Extracts and processes text for LLM training
- **Storage Layer**: Persists processed data with compression and batching

## Documentation

- **[ARCHITECTURE.md](ARCHITECTURE.md)**: Comprehensive architectural overview and component design
- **[TECHNICAL_DECISIONS.md](TECHNICAL_DECISIONS.md)**: Detailed technical implementation decisions and rationale

## Development

### Development Setup

```bash
# Clone repository
git clone https://github.com/Almahr1/quert.git
cd crawler

# Install dependencies and tools
make deps
make install-tools

# Start development with hot reload
make dev
```

### Development Workflow

```bash
# Run tests
make test

# Run with race detector
make test-race

# Check code quality
make fmt lint vet

# Generate coverage report
make coverage

# Run benchmarks
make benchmark

# Quick development cycle
make quick  # fmt + lint + test + build
```

### Project Structure

```
crawler/
├── cmd/
│   └── crawler/           # CLI application
├── internal/
│   ├── config/           # Configuration management
│   ├── crawler/          # Core crawling logic
│   ├── frontier/         # URL frontier management
│   ├── extractor/        # Content extraction
│   ├── storage/          # Data persistence
│   ├── dedup/            # Deduplication
│   ├── metrics/          # Monitoring
│   └── quality/          # Content quality assessment
├── pkg/                  # Public API
├── test/                 # Test utilities and data
├── docs/                 # Documentation
├── config/               # Configuration files
├── scripts/              # Development scripts
└── deployments/          # Deployment configurations
```

### Testing

```bash
# Run all tests
make test

# Run tests with coverage
make coverage

# Run integration tests
go test -tags=integration ./test/integration/...

# Run specific test
go test -v ./internal/crawler -run TestCrawlerBasic

# Benchmark specific package
go test -bench=. ./internal/frontier
```

## Performance **(PLACEHOLDER) All Theoretical, Will Updated After Working Product Is Made**

### Benchmarks

- **Throughput**: 10,000-50,000 pages/hour (single machine)
- **Memory Usage**: <4GB for typical workloads
- **CPU Usage**: 2-8 cores recommended
- **Storage**: ~1KB per processed document

### Optimization Tips

- Adjust worker count based on CPU cores and target sites
- Use appropriate batch sizes for your storage backend
- Configure rate limits based on target server capacity
- Enable compression for storage efficiency

### Monitoring

The crawler exposes Prometheus metrics on `:8080/metrics`:

- `crawler_pages_crawled_total` - Total pages crawled
- `crawler_requests_duration_seconds` - Request duration histogram
- `crawler_queue_depth` - Current queue depth
- `crawler_errors_total` - Error count by type

## Configuration Reference

### Complete Configuration Options

See [docs/configuration.md](docs/configuration.md) for all available configuration options.

### Environment Variables

All configuration can be overridden via environment variables using the pattern `SECTION_KEY`:

```bash
CRAWLER_MAX_PAGES=10000
RATE_LIMIT_REQUESTS_PER_SECOND=2.0
STORAGE_TYPE=postgres
CONTENT_MIN_TEXT_LENGTH=50
```

## API Reference

### REST API Endpoints

When running with API mode enabled:

- `GET /health` - Health check endpoint
- `GET /metrics` - Prometheus metrics
- `POST /crawl` - Submit URLs for crawling
- `GET /status` - Get crawling status
- `GET /stats` - Get crawling statistics

See [docs/api.md](docs/api.md) for complete API documentation.

## Deployment

### Docker Deployment

```yaml
# docker-compose.yml
version: '3.8'
services:
  crawler:
    image: crawler:latest
    volumes:
      - ./config:/app/config:ro
      - ./data:/app/data
    environment:
      - CRAWLER_MAX_PAGES=50000
    ports:
      - "8080:8080"
  
  postgres:
    image: postgres:15
    environment:
      POSTGRES_DB: crawler
      POSTGRES_USER: crawler
      POSTGRES_PASSWORD: password
    volumes:
      - postgres_data:/var/lib/postgresql/data

volumes:
  postgres_data:
```

## Contributing

We welcome contributions! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

### Quick Contribution Guide

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Make your changes
4. Run tests (`make test`)
5. Commit your changes (`git commit -m 'Add amazing feature'`)
6. Push to the branch (`git push origin feature/amazing-feature`)
7. Open a Pull Request

### Development Standards

- **Code Coverage**: Maintain >80% test coverage
- **Code Style**: Use `gofmt` and `golangci-lint`
- **Documentation**: Update docs for new features
- **Performance**: Benchmark performance-critical changes

## License

This project is licensed under the GNU General Public License v3.0 - see the [LICENSE](LICENSE) file for details.

## Acknowledgments

- [Colly](https://github.com/gocolly/colly) - Web scraping framework
- [Viper](https://github.com/spf13/viper) - Configuration management
- [Prometheus](https://github.com/prometheus/client_golang) - Metrics collection
- [zerolog](https://github.com/rs/zerolog) - Structured logging

## Roadmap

- **Roadmap**: [Roadmap](https://github.com/Almahr1/quert/blob/main/roadmap.md) 

---

Built with care by developers, for developers
