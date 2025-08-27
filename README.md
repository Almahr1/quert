# Quert

A concurrent web crawler implemented in Go for text data collection. The system implements worker pool-based crawling with rate limiting, robots.txt compliance, and content extraction capabilities.

## Requirements

- Go 1.23.0 or later
- Standard library dependencies only (see go.mod for complete list)

## Architecture

The crawler consists of several core components:

- **Configuration Management**: Viper-based configuration system supporting YAML, JSON, and environment variables
- **HTTP Client**: Custom HTTP client with connection pooling, timeout handling, and retry logic
- **URL Processing**: URL normalization, validation, and multi-level deduplication system
- **Robots.txt Handler**: Compliant robots.txt parser with caching and per-host request coordination
- **Crawler Engine**: Worker pool implementation with job distribution and rate limiting
- **Content Extraction**: HTML, plain text, and XML content extraction using goquery

## Installation

```bash
git clone https://github.com/Almahr1/quert.git
cd quert
go mod download
```

## Usage

### Basic Example

```go
crawlerConfig := &config.CrawlerConfig{
    MaxPages:          100,
    MaxDepth:          2,
    ConcurrentWorkers: 10,
    RequestTimeout:    15 * time.Second,
    UserAgent:         "Quert/1.0",
    SeedURLs:          []string{"https://example.com"},
}

httpConfig := &config.HTTPConfig{
    MaxIdleConnections:        50,
    MaxIdleConnectionsPerHost: 5,
    IdleConnectionTimeout:     30 * time.Second,
    Timeout:                   15 * time.Second,
}

engine := crawler.NewCrawlerEngine(crawlerConfig, httpConfig, nil, logger)
ctx := context.Background()

if err := engine.Start(ctx); err != nil {
    log.Fatal(err)
}
```

### Running Examples

Three example applications are provided:

```bash
# Basic crawling demonstration
make run-basic-crawl

# Domain-focused crawling with statistics
make run-domain-crawler

# Link collection from multiple sources
make run-link-collector
```

## Configuration

The system accepts configuration through YAML files, environment variables, or programmatic setup. Key configuration sections include:

- `crawler`: Worker count, depth limits, URL patterns, user agent
- `http`: Connection pooling, timeouts, compression settings
- `rate_limit`: Request rates, burst limits, per-host constraints
- `content`: Quality thresholds, text length limits, extraction settings

Example configuration file structure is provided in `config.yaml`.

## Content Extraction

The extraction system supports multiple content types through a factory pattern:

- **HTML**: Main content extraction, link parsing, metadata extraction
- **Plain Text**: Basic text processing with quality scoring
- **XML**: XML/RSS/Atom feed processing

Content quality is assessed using configurable metrics including text length, word count, sentence structure, and language detection.

## Rate Limiting

The crawler implements both global and per-host rate limiting:

- Global rate limiting controls overall request volume
- Per-host rate limiting ensures compliance with individual site constraints
- Adaptive rate adjustment responds to server response characteristics
- Robots.txt crawl-delay directives are automatically respected

## Testing

```bash
# Run all tests
make test

# Run component-specific tests
make test-config
make test-http
make test-url
make test-extractor
make test-crawler
make test-robots

# Run tests with race detection
make test-race
```

## Development

```bash
# Format code
make fmt

# Run linter
make lint

# Build binary
make build

# Quick development workflow
make quick
```

## Implementation Status

**Completed Components:**
- Configuration management with validation
- HTTP client with middleware support
- URL processing and deduplication
- Robots.txt parsing with caching
- Worker pool crawler implementation
- Content extraction for HTML, text, and XML formats

**Storage Layer:** Directory structure exists but implementation is pending.

## License

GNU General Public License v3.0

## Dependencies

Core dependencies:
- `github.com/PuerkitoBio/goquery` - HTML parsing and DOM manipulation
- `github.com/spf13/viper` - Configuration management
- `github.com/temoto/robotstxt` - Robots.txt parsing
- `go.uber.org/zap` - Structured logging
- `golang.org/x/time` - Rate limiting primitives

Development dependencies:
- `github.com/stretchr/testify` - Testing framework

## Development Transparency

This project was built by humans, augmented by AI. I believe in using the best tools for the job:

*   **Human-Led:** The core architecture, design decisions, complex logic, and final implementation were meticulously planned and executed by a human developer.
*   **AI-Assisted:** To boost productivity, I utilized AI (primarily LLMs) for generating repetitive boilerplate code, comprehensive test cases, and initial drafts of documentation. All AI-generated content was rigorously reviewed, tested, and adapted to fit the project's standards.

This approach allowed me to focus my creative energy on solving hard problems rather than repetitive tasks.
